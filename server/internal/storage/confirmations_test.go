package storage

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"cfw-policy-llm-agent/server/internal/audit"
	"cfw-policy-llm-agent/server/internal/domain"
)

func mustDraft(t *testing.T, s *Store, snapshot SnapshotRecord, ids ...string) ProfileDraft {
	t.Helper()
	draft, err := s.CreateProfileDraft(context.Background(), CreateDraftRequest{SnapshotID: snapshot.ID, AssetIDs: ids})
	if err != nil {
		t.Fatal(err)
	}
	return draft
}
func mustPreview(t *testing.T, s *Store, id string) ConfirmationPreview {
	t.Helper()
	preview, err := s.PreviewProfileDraft(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return preview
}
func mustConfirm(t *testing.T, s *Store, draft ProfileDraft, preview ConfirmationPreview, key string, sections ...string) ConfirmationResult {
	t.Helper()
	result, err := s.ConfirmProfileDraft(context.Background(), draft.ID, ConfirmDraftRequest{PreviewID: preview.ID, ExpectedDraftRevision: draft.Revision, ConfirmedSections: sections}, key)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestRoleConfirmationDoesNotConfirmAccessAndIsIdempotent(t *testing.T) {
	s := collaborationStore(t)
	ctx := context.Background()
	snapshot := collaborationSnapshot(t, s)
	assetID := snapshot.Input.Assets[0].ID
	before, refs, err := s.FreezeInput(ctx, snapshot.ID)
	if err != nil || refs[0].Revision != 0 {
		t.Fatal(refs, err)
	}
	draft := mustDraft(t, s, snapshot, assetID)
	draft.Changes[0].Roles[0].Role = "database"
	draft, err = s.UpdateProfileDraft(ctx, draft.ID, UpdateDraftRequest{ExpectedDraftRevision: draft.Revision, Changes: draft.Changes})
	if err != nil {
		t.Fatal(err)
	}
	preview := mustPreview(t, s, draft.ID)
	if len(preview.ConfirmableSections) != 2 {
		t.Fatal("roles/access preview missing", preview)
	}
	result := mustConfirm(t, s, draft, preview, "roles-only", "roles")
	if result.Status != "confirmed" || result.Heads[0].Revision != 1 || result.Heads[0].ProfileVersionID == "" || result.Heads[0].IntentVersionID != "" {
		t.Fatal(result)
	}
	after, heads, err := s.FreezeInput(ctx, snapshot.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Assets[0].Services[0].Role != "database" || after.Assets[0].Services[0].RoleStatus != "confirmed" || after.ProfileVersion == before.ProfileVersion || after.IntentVersion != before.IntentVersion {
		t.Fatal("roles version or explicit confirmation missing")
	}
	left, right := before.Assets[0], after.Assets[0]
	for i := range left.Services {
		left.Services[i].Role = right.Services[i].Role
		left.Services[i].RoleStatus = right.Services[i].RoleStatus
	}
	if !reflect.DeepEqual(left, right) {
		t.Fatal("role confirmation changed access or immutable facts")
	}
	request := ConfirmDraftRequest{PreviewID: preview.ID, ExpectedDraftRevision: draft.Revision, ConfirmedSections: []string{"roles"}}
	replay, err := s.ConfirmProfileDraft(ctx, draft.ID, request, "roles-only")
	if err != nil || replay.ActionID != result.ActionID || replay.Heads[0].Revision != 1 {
		t.Fatal("replay must precede now-stale draft/head checks", err)
	}
	request.ConfirmedSections = []string{"access"}
	if _, err = s.ConfirmProfileDraft(ctx, draft.ID, request, "roles-only"); !errors.Is(err, ErrConflict) {
		t.Fatal("same key changed section accepted", err)
	}
	if err = s.CheckHeadVersions(ctx, snapshot.ID, refs); !errors.Is(err, ErrVersionConflict) {
		t.Fatal("changed head not detected", err)
	}
	if err = s.CheckHeadVersions(ctx, snapshot.ID, heads); err != nil {
		t.Fatal(err)
	}
	after.Assets[0].Services[0].Role = "mutated-copy"
	reloaded, _, err := s.FreezeInput(ctx, snapshot.ID)
	if err != nil || reloaded.Assets[0].Services[0].Role == "mutated-copy" {
		t.Fatal("frozen copy mutated immutable versions")
	}
}

func TestDraftPreviewInvalidationAndBatchConflictAreAtomic(t *testing.T) {
	s := collaborationStore(t)
	ctx := context.Background()
	in, err := audit.SyntheticExample("internet-in")
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(in.Assets[0])
	var extra domain.Asset
	if json.Unmarshal(encoded, &extra) != nil {
		t.Fatal("fixture")
	}
	extra.ID = "asset-demo-b"
	extra.Address = "192.0.2.11"
	in.Assets = append(in.Assets, extra)
	in.Selection.AssetIDs = append(in.Selection.AssetIDs, extra.ID)
	snapshot, err := s.SaveSnapshot(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	batch := mustDraft(t, s, snapshot, in.Assets[0].ID, extra.ID)
	oldPreview := mustPreview(t, s, batch.ID)
	other := mustDraft(t, s, snapshot, extra.ID)
	other.Changes[0].Roles[0].Role = "background"
	other, err = s.UpdateProfileDraft(ctx, other.ID, UpdateDraftRequest{ExpectedDraftRevision: other.Revision, Changes: other.Changes})
	if err != nil {
		t.Fatal(err)
	}
	mustConfirm(t, s, other, mustPreview(t, s, other.ID), "other-wins", "roles")
	if _, err = s.ConfirmProfileDraft(ctx, batch.ID, ConfirmDraftRequest{PreviewID: oldPreview.ID, ExpectedDraftRevision: batch.Revision, ConfirmedSections: []string{"roles"}}, "batch-stale"); !errors.Is(err, ErrVersionConflict) {
		t.Fatal("batch stale head accepted", err)
	}
	_, heads, err := s.FreezeInput(ctx, snapshot.ID)
	if err != nil || heads[0].Revision != 0 || heads[1].Revision != 1 {
		t.Fatal("batch partially committed", heads, err)
	}
	draft := mustDraft(t, s, snapshot, in.Assets[0].ID)
	preview := mustPreview(t, s, draft.ID)
	draft.Changes[0].Roles[0].Role = "management"
	updated, err := s.UpdateProfileDraft(ctx, draft.ID, UpdateDraftRequest{ExpectedDraftRevision: draft.Revision, Changes: draft.Changes})
	if err != nil || updated.PreviewID != "" {
		t.Fatal(updated, err)
	}
	if _, err = s.ConfirmProfileDraft(ctx, draft.ID, ConfirmDraftRequest{PreviewID: preview.ID, ExpectedDraftRevision: draft.Revision, ConfirmedSections: []string{"roles"}}, "old-preview"); !errors.Is(err, ErrVersionConflict) {
		t.Fatal("changed draft used old preview", err)
	}
	rejected, err := s.RejectProfileDraft(ctx, updated.ID, updated.Revision, "reject-key")
	if err != nil || rejected.Status != "rejected" {
		t.Fatal(rejected, err)
	}
	replay, err := s.RejectProfileDraft(ctx, updated.ID, updated.Revision, "reject-key")
	if err != nil || replay.ActionID != rejected.ActionID {
		t.Fatal("reject not idempotent", err)
	}
	_, latest, err := s.FreezeInput(ctx, snapshot.ID)
	if err != nil || latest[0].Revision != 0 || latest[1].Revision != 1 {
		t.Fatal("reject deleted or changed confirmation", latest, err)
	}
}

func TestInvalidAccessCanRemainDraftWhileRolesAreConfirmed(t *testing.T) {
	s := collaborationStore(t)
	ctx := context.Background()
	snapshot := collaborationSnapshot(t, s)
	id := snapshot.Input.Assets[0].ID
	draft := mustDraft(t, s, snapshot, id)
	draft.Changes[0].Roles[0].Role = "database"
	draft.Changes[0].Access = &AssetAccessChange{AllowedComplete: true, Allowed: []domain.Access{}, DenyAll: false}
	draft, err := s.UpdateProfileDraft(ctx, draft.ID, UpdateDraftRequest{ExpectedDraftRevision: draft.Revision, Changes: draft.Changes})
	if err != nil {
		t.Fatal("invalid business candidate must remain editable", err)
	}
	preview := mustPreview(t, s, draft.ID)
	if !reflect.DeepEqual(preview.ConfirmableSections, []string{"roles"}) || len(preview.ValidationErrors) == 0 {
		t.Fatal("invalid access became confirmable or hid valid roles", preview)
	}
	if _, err = s.ConfirmProfileDraft(ctx, draft.ID, ConfirmDraftRequest{PreviewID: preview.ID, ExpectedDraftRevision: draft.Revision, ConfirmedSections: []string{"access"}}, "bad-access"); !errors.Is(err, ErrInputInvalid) {
		t.Fatal("complete empty allow without explicit denial accepted", err)
	}
	mustConfirm(t, s, draft, preview, "just-roles", "roles")
	current, heads, err := s.FreezeInput(ctx, snapshot.ID)
	if err != nil || heads[0].IntentVersionID != "" || !reflect.DeepEqual(current.Assets[0].Allowed, snapshot.Input.Assets[0].Allowed) {
		t.Fatal("invalid access leaked through roles")
	}
}

func TestConfirmationFailureRollsBackVersionsHeadsAndKey(t *testing.T) {
	s := collaborationStore(t)
	ctx := context.Background()
	snapshot := collaborationSnapshot(t, s)
	draft := mustDraft(t, s, snapshot, snapshot.Input.Assets[0].ID)
	preview := mustPreview(t, s, draft.ID)
	if err := s.db.Exec("CREATE TRIGGER fail_confirmation_head BEFORE UPDATE ON asset_heads BEGIN SELECT RAISE(ABORT,'synthetic failure'); END").Error; err != nil {
		t.Fatal(err)
	}
	request := ConfirmDraftRequest{PreviewID: preview.ID, ExpectedDraftRevision: draft.Revision, ConfirmedSections: []string{"roles"}}
	if _, err := s.ConfirmProfileDraft(ctx, draft.ID, request, "retry-key"); err == nil {
		t.Fatal("failed confirmation reported saved")
	}
	loaded, err := s.GetProfileDraft(ctx, draft.ID)
	if err != nil || loaded.Status != "previewed" || loaded.Revision != draft.Revision {
		t.Fatal("draft partially committed", loaded, err)
	}
	var count int64
	if err = s.db.Raw("SELECT count(*) FROM profile_versions").Scan(&count).Error; err != nil || count != 0 {
		t.Fatal("uncommitted version escaped rollback", count, err)
	}
	if err = s.db.Exec("DROP TRIGGER fail_confirmation_head").Error; err != nil {
		t.Fatal(err)
	}
	if result, err := s.ConfirmProfileDraft(ctx, draft.ID, request, "retry-key"); err != nil || result.Heads[0].Revision != 1 {
		t.Fatal("rollback consumed key", err)
	}
}

func TestFrozenBusinessUsesCanonicalScopeAndRebindsAlias(t *testing.T) {
	s := collaborationStore(t)
	ctx := context.Background()
	snapshot := collaborationSnapshot(t, s)
	id := snapshot.Input.Assets[0].ID
	draft := mustDraft(t, s, snapshot, id)
	draft.Changes[0].Access.Services[0].Allowed = []domain.Access{{ScopeRef: snapshot.ScopeRef, Protocol: "tcp", Addresses: []string{"203.0.113.9"}, Ports: []domain.PortRange{{From: 443, To: 443}}, Applicability: "applicable"}}
	draft.Changes[0].Access.Services[0].RequiredConfirmed = true
	draft.Changes[0].Access.Services[0].Required = nil
	draft, err := s.UpdateProfileDraft(ctx, draft.ID, UpdateDraftRequest{ExpectedDraftRevision: draft.Revision, Changes: draft.Changes})
	if err != nil {
		t.Fatal(err)
	}
	mustConfirm(t, s, draft, mustPreview(t, s, draft.ID), "intent-confirm", "access")
	in := snapshot.Input
	in.SnapshotVersion = "new-collection"
	setInputScopeRef(&in, "local-alias")
	alias, err := s.SaveSnapshot(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	frozen, heads, err := s.FreezeInput(ctx, alias.ID)
	if err != nil || heads[0].Revision != 1 {
		t.Fatal("canonical scope did not reuse confirmed version", heads, err)
	}
	for _, access := range frozen.Assets[0].Services[0].Allowed {
		if access.ScopeRef != "local-alias" {
			t.Fatal("old alias leaked into new immutable input")
		}
	}
	out, err := audit.SyntheticExample("internet-out")
	if err != nil {
		t.Fatal(err)
	}
	setInputScopeRef(&out, snapshot.ScopeRef)
	out.Scope.PolicySetRef = snapshot.Input.Scope.PolicySetRef
	if _, err = s.SaveSnapshot(ctx, out); !errors.Is(err, ErrConflict) {
		t.Fatal("same local scope reference changed canonical identity", err)
	}
	setInputScopeRef(&out, "different-direction-scope")
	other, err := s.SaveSnapshot(ctx, out)
	if err != nil {
		t.Fatal(err)
	}
	_, otherHeads, err := s.FreezeInput(ctx, other.ID)
	if err != nil || otherHeads[0].Revision != 0 {
		t.Fatal("same raw ref crossed canonical direction", otherHeads, err)
	}
}

func setInputScopeRef(in *domain.Input, ref string) {
	in.Scope.Ref = ref
	for i := range in.Policies {
		in.Policies[i].ScopeRef = ref
	}
	for i := range in.Assets {
		in.Assets[i].ScopeRef = ref
		for j := range in.Assets[i].Allowed {
			in.Assets[i].Allowed[j].ScopeRef = ref
		}
		for j := range in.Assets[i].Services {
			for k := range in.Assets[i].Services[j].Allowed {
				in.Assets[i].Services[j].Allowed[k].ScopeRef = ref
			}
			for k := range in.Assets[i].Services[j].Required {
				in.Assets[i].Services[j].Required[k].ScopeRef = ref
			}
		}
	}
	for key, value := range in.AddressRefs {
		value.ScopeRef = ref
		in.AddressRefs[key] = value
	}
	for key, value := range in.PortRefs {
		value.ScopeRef = ref
		in.PortRefs[key] = value
	}
}

func TestNewServiceRequiresExplicitRoleConfirmationWithItsAccess(t *testing.T) {
	s := collaborationStore(t)
	ctx := context.Background()
	snapshot := collaborationSnapshot(t, s)
	draft := mustDraft(t, s, snapshot, snapshot.Input.Assets[0].ID)
	draft.Changes[0].Roles = append(draft.Changes[0].Roles, RoleChange{Role: "cache", Protocol: "tcp", Ports: []domain.PortRange{{From: 6379, To: 6379}}})
	updated, err := s.UpdateProfileDraft(ctx, draft.ID, UpdateDraftRequest{ExpectedDraftRevision: draft.Revision, Changes: draft.Changes})
	if err != nil {
		t.Fatal(err)
	}
	newID := updated.Changes[0].Roles[len(updated.Changes[0].Roles)-1].ServiceID
	if newID == "" {
		t.Fatal("server did not assign new service identity")
	}
	updated.Changes[0].Access.Services = append(updated.Changes[0].Access.Services, ServiceAccessChange{ServiceID: newID, PublicAccess: "unknown", Allowed: []domain.Access{}, Required: []domain.Access{}})
	updated.Changes[0].Access.Exceptions = []domain.Exception{{CheckID: "CHK-07", ServiceRef: newID, ExpiresAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339), AuthorizationSource: "人工测试明确授权", Reason: "仅针对新增服务的业务例外"}}
	updated, err = s.UpdateProfileDraft(ctx, draft.ID, UpdateDraftRequest{ExpectedDraftRevision: updated.Revision, Changes: updated.Changes})
	if err != nil {
		t.Fatal(err)
	}
	preview := mustPreview(t, s, updated.ID)
	if !containsSection(preview.ConfirmableSections, "access") || !reflect.DeepEqual(preview.SectionDependencies["access"], []string{"roles"}) {
		t.Fatal("same draft cannot clearly preview new role plus its access", preview)
	}
	if _, err = s.ConfirmProfileDraft(ctx, updated.ID, ConfirmDraftRequest{PreviewID: preview.ID, ExpectedDraftRevision: updated.Revision, ConfirmedSections: []string{"access"}}, "access-without-role"); !errors.Is(err, ErrInputInvalid) {
		t.Fatal("new service silently confirmed through access only", err)
	}
	result := mustConfirm(t, s, updated, preview, "both-new-service", "roles", "access")
	if result.Heads[0].ProfileVersionID == "" || result.Heads[0].IntentVersionID == "" || result.Heads[0].Revision != 1 {
		t.Fatal(result)
	}
	in, _, err := s.FreezeInput(ctx, snapshot.ID)
	if err != nil {
		t.Fatal(err)
	}
	service := selectedService(&in.Assets[0], newID)
	if service == nil || service.Role != "cache" || service.PublicAccess != "unknown" {
		t.Fatal("new confirmed service lost or guessed access")
	}
	if len(in.Baseline.Exceptions) != 1 || in.Baseline.Exceptions[0].ServiceRef != newID || !in.Baseline.Exceptions[0].Confirmed {
		t.Fatal("new-service exception did not share the explicit roles dependency", in.Baseline.Exceptions)
	}
}

func TestBusinessExceptionsOnlyApplyThroughAccessConfirmation(t *testing.T) {
	s := collaborationStore(t)
	ctx := context.Background()
	snapshot := collaborationSnapshot(t, s)
	imported := snapshot.Input
	imported.SourceKind = "imported"
	snapshot, err := s.SaveSnapshot(ctx, imported)
	if err != nil {
		t.Fatal(err)
	}
	asset := snapshot.Input.Assets[0]
	exception := domain.Exception{CheckID: "CHK-07", ServiceRef: asset.Services[1].ID, ExpiresAt: time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339), AuthorizationSource: "人工测试明确授权", Reason: "明确的短期人工例外", Confirmed: true}
	draft := mustDraft(t, s, snapshot, asset.ID)
	draft.Changes[0].Access.Exceptions = []domain.Exception{exception}
	draft, err = s.UpdateProfileDraft(ctx, draft.ID, UpdateDraftRequest{ExpectedDraftRevision: draft.Revision, Changes: draft.Changes})
	if err != nil {
		t.Fatal(err)
	}
	if draft.Changes[0].Access.Exceptions[0].Confirmed {
		t.Fatal("draft self-asserted approval")
	}
	mustConfirm(t, s, draft, mustPreview(t, s, draft.ID), "roles-not-exceptions", "roles")
	rolesOnly, heads, err := s.FreezeInput(ctx, snapshot.ID)
	if err != nil || len(rolesOnly.Baseline.Exceptions) != 0 || heads[0].IntentVersionID != "" {
		t.Fatal("roles approved exception", err)
	}
	draft = mustDraft(t, s, snapshot, asset.ID)
	draft.Changes[0].Access.Exceptions = []domain.Exception{exception}
	draft, err = s.UpdateProfileDraft(ctx, draft.ID, UpdateDraftRequest{ExpectedDraftRevision: draft.Revision, Changes: draft.Changes})
	if err != nil {
		t.Fatal(err)
	}
	confirmed := mustConfirm(t, s, draft, mustPreview(t, s, draft.ID), "access-exception", "access")
	frozen, heads, err := s.FreezeInput(ctx, snapshot.ID)
	if err != nil {
		t.Fatal(err)
	}
	if frozen.SourceKind != "imported" || frozen.Baseline.Version != snapshot.Input.Baseline.Version || frozen.IntentVersion == snapshot.Input.IntentVersion || len(frozen.Baseline.Exceptions) != 1 {
		t.Fatal("exception changed policy baseline or assurance")
	}
	x := frozen.Baseline.Exceptions[0]
	if !x.Confirmed || x.SubjectRef != asset.ID || x.ScopeRef != snapshot.ScopeRef || x.ServiceRef != asset.Services[1].ID {
		t.Fatal("exception owner not bound", x)
	}
	result, err := audit.Run(ctx, frozen)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, finding := range result.Findings {
		if finding.RuleID == "CHK-07" && finding.ServiceRef == x.ServiceRef && finding.AcceptedException {
			if finding.Verdict != "fail" || finding.Severity != "high" {
				t.Fatal("exception erased original fact")
			}
			found = true
		}
	}
	if !found {
		t.Fatal("approved exception not linked to its actual check")
	}
	var oldBody string
	if err = s.db.Raw("SELECT body FROM intent_versions WHERE id=?", confirmed.Heads[0].IntentVersionID).Scan(&oldBody).Error; err != nil {
		t.Fatal(err)
	}
	draft = mustDraft(t, s, snapshot, asset.ID)
	draft.Changes[0].Access.Exceptions = []domain.Exception{}
	draft, err = s.UpdateProfileDraft(ctx, draft.ID, UpdateDraftRequest{ExpectedDraftRevision: draft.Revision, Changes: draft.Changes})
	if err != nil {
		t.Fatal(err)
	}
	mustConfirm(t, s, draft, mustPreview(t, s, draft.ID), "clear-current-exception", "access")
	current, _, err := s.FreezeInput(ctx, snapshot.ID)
	if err != nil || len(current.Baseline.Exceptions) != 0 || len(frozen.Baseline.Exceptions) != 1 {
		t.Fatal("clear changed historical frozen input or failed current projection")
	}
	var preserved string
	if err = s.db.Raw("SELECT body FROM intent_versions WHERE id=?", heads[0].IntentVersionID).Scan(&preserved).Error; err != nil || preserved != oldBody {
		t.Fatal("immutable exception history changed")
	}
}

func TestExceptionTargetsAndFreshApprovalExpiryAreBounded(t *testing.T) {
	for _, mode := range []string{"expired", "no-timezone", "no-reason", "scope", "subject", "service", "partition-check", "nat-information", "wrong-direction"} {
		t.Run(mode, func(t *testing.T) {
			s := collaborationStore(t)
			snapshot := collaborationSnapshot(t, s)
			asset := snapshot.Input.Assets[0]
			draft := mustDraft(t, s, snapshot, asset.ID)
			x := domain.Exception{CheckID: "CHK-07", ServiceRef: asset.Services[1].ID, ExpiresAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339), AuthorizationSource: "人工测试明确授权", Reason: "明确人工理由"}
			structural := false
			switch mode {
			case "expired":
				x.ExpiresAt = time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
			case "no-timezone":
				x.ExpiresAt = "2027-01-01 00:00:00"
			case "no-reason":
				x.Reason = " "
			case "scope":
				x.ScopeRef = "outside-scope"
				structural = true
			case "subject":
				x.SubjectRef = "outside-asset"
				structural = true
			case "service":
				x.ServiceRef = "unknown-service"
				structural = true
			case "partition-check":
				x.CheckID = "CHK-04"
				structural = true
			case "nat-information":
				x.CheckID = "NAT-01"
				structural = true
			case "wrong-direction":
				x.CheckID = "OUT-01"
				structural = true
			}
			draft.Changes[0].Access.Exceptions = []domain.Exception{x}
			updated, err := s.UpdateProfileDraft(context.Background(), draft.ID, UpdateDraftRequest{ExpectedDraftRevision: draft.Revision, Changes: draft.Changes})
			if structural {
				if !errors.Is(err, ErrInputInvalid) {
					t.Fatal("out of scope exception candidate accepted", err)
				}
				return
			}
			if err != nil {
				t.Fatal("incomplete candidate must stay editable", err)
			}
			preview := mustPreview(t, s, updated.ID)
			if containsSection(preview.ConfirmableSections, "access") || !containsSection(preview.ConfirmableSections, "roles") || len(preview.ValidationErrors) == 0 {
				t.Fatal("invalid exception affected roles or became approved", preview)
			}
		})
	}
}

func TestNATExceptionUsesExactAssetSubcheckAndAlias(t *testing.T) {
	s := collaborationStore(t)
	ctx := context.Background()
	input, err := audit.SyntheticExample("nat-out")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := s.SaveSnapshot(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	asset := input.Assets[0]
	draft := mustDraft(t, s, snapshot, asset.ID)
	draft.Changes[0].Access.Exceptions = []domain.Exception{{CheckID: "NAT-02", Subcheck: "OUT-01", ServiceRef: asset.Services[0].ID, ExpiresAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339), AuthorizationSource: "人工测试明确授权", Reason: "仅此资产的目标上界例外"}}
	draft, err = s.UpdateProfileDraft(ctx, draft.ID, UpdateDraftRequest{ExpectedDraftRevision: draft.Revision, Changes: draft.Changes})
	if err != nil {
		t.Fatal(err)
	}
	mustConfirm(t, s, draft, mustPreview(t, s, draft.ID), "nat-out-01-only", "access")
	frozen, _, err := s.FreezeInput(ctx, snapshot.ID)
	if err != nil {
		t.Fatal(err)
	}
	result, err := audit.Run(ctx, frozen)
	if err != nil {
		t.Fatal(err)
	}
	seen := false
	for _, finding := range result.Findings {
		if finding.RuleID == "NAT-02" && finding.Subcheck == "OUT-01" {
			seen = finding.AcceptedException
		}
		if finding.RuleID == "NAT-02" && finding.Subcheck != "OUT-01" && finding.AcceptedException {
			t.Fatal("one subcheck exception expanded")
		}
	}
	if !seen {
		t.Fatal("exception not applied")
	}
	setInputScopeRef(&input, "new-nat-alias")
	input.SnapshotVersion = "nat-new-snapshot"
	alias, err := s.SaveSnapshot(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	again, _, err := s.FreezeInput(ctx, alias.ID)
	if err != nil || again.Baseline.Exceptions[0].ScopeRef != "new-nat-alias" {
		t.Fatal("exception alias not rebound", err)
	}
	bad := mustDraft(t, s, alias, asset.ID)
	bad.Changes[0].Access.Exceptions = []domain.Exception{{CheckID: "NAT-02", Subcheck: "CHK-04", ExpiresAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339), AuthorizationSource: "人工测试明确授权", Reason: "must not expand to partition"}}
	if _, err = s.UpdateProfileDraft(ctx, bad.ID, UpdateDraftRequest{ExpectedDraftRevision: bad.Revision, Changes: bad.Changes}); !errors.Is(err, ErrInputInvalid) {
		t.Fatal("asset exception expanded to gateway maintenance", err)
	}
}

func TestConfirmedAccessSurvivesMissingServiceCandidatesInNewSnapshot(t *testing.T) {
	s := collaborationStore(t)
	ctx := context.Background()
	snapshot := collaborationSnapshot(t, s)
	assetID := snapshot.Input.Assets[0].ID
	serviceID := snapshot.Input.Assets[0].Services[0].ID
	draft := mustDraft(t, s, snapshot, assetID)
	draft.Changes[0].Access.AllowedComplete = false
	draft.Changes[0].Access.DenyAll = false
	access := &draft.Changes[0].Access.Services[0]
	access.AllowedComplete, access.DenyAll, access.RequiredConfirmed = false, false, true
	access.Required = []domain.Access{{ScopeRef: snapshot.ScopeRef, Protocol: "tcp", Addresses: []string{"203.0.113.8"}, Ports: []domain.PortRange{{From: 443, To: 443}}, Applicability: "applicable"}}
	draft.Changes[0].Access.Exceptions = []domain.Exception{{CheckID: "CHK-07", ServiceRef: serviceID, ExpiresAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339), AuthorizationSource: "人工测试明确授权", Reason: "此服务的明确临时业务例外"}}
	draft, err := s.UpdateProfileDraft(ctx, draft.ID, UpdateDraftRequest{ExpectedDraftRevision: draft.Revision, Changes: draft.Changes})
	if err != nil {
		t.Fatal(err)
	}
	confirmed := mustConfirm(t, s, draft, mustPreview(t, s, draft.ID), "access-without-local-role", "access")
	in, err := cloneConfirmationInput(snapshot.Input)
	if err != nil {
		t.Fatal(err)
	}
	in.Assets[0].Services = []domain.Service{}
	in.SnapshotVersion = "collection-without-service-candidates"
	setInputScopeRef(&in, "service-candidates-missing")
	fresh, err := s.SaveSnapshot(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	frozen, heads, err := s.FreezeInput(ctx, fresh.ID)
	if err != nil {
		t.Fatal("confirmed business access was lost with collection candidate", err)
	}
	if heads[0].ProfileVersionID != "" || heads[0].IntentVersionID != confirmed.Heads[0].IntentVersionID || frozen.ProfileVersion != in.ProfileVersion {
		t.Fatal("access-only confirmation invented a local role version", heads)
	}
	service := selectedService(&frozen.Assets[0], serviceID)
	if service == nil || !service.RequiredConfirmed || len(service.Required) != 1 || service.Required[0].ScopeRef != fresh.ScopeRef {
		t.Fatal("missing candidate erased a confirmed business requirement", service)
	}
	if service.Role != "unknown" || service.RoleStatus != "unknown" || service.Protocol != "unknown" || len(service.Ports) != 0 {
		t.Fatal("access placeholder invented a listener or confirmed role", service)
	}
	if len(frozen.Baseline.Exceptions) != 1 || frozen.Baseline.Exceptions[0].ServiceRef != serviceID || !frozen.Baseline.Exceptions[0].Confirmed || frozen.Baseline.Exceptions[0].ScopeRef != fresh.ScopeRef {
		t.Fatal("service exception lost or escaped its current alias", frozen.Baseline.Exceptions)
	}
	if err := audit.ValidateContext(ctx, frozen); err != nil {
		t.Fatal(err)
	}
	stored, err := s.GetSnapshot(ctx, fresh.ID)
	if err != nil || len(stored.Input.Assets[0].Services) != 0 || stored.SourceKind != snapshot.SourceKind {
		t.Fatal("projection altered collection facts or source assurance", err)
	}
}

func TestExceptionExpiryRecheckedAtConfirmationAndUnchangedHistoryPreserved(t *testing.T) {
	s := collaborationStore(t)
	ctx := context.Background()
	snapshot := collaborationSnapshot(t, s)
	asset := snapshot.Input.Assets[0]
	expires := time.Now().UTC().Add(2 * time.Second)
	draft := mustDraft(t, s, snapshot, asset.ID)
	draft.Changes[0].Access.Exceptions = []domain.Exception{{CheckID: "CHK-07", ServiceRef: asset.Services[1].ID, ExpiresAt: expires.Format(time.RFC3339Nano), AuthorizationSource: "人工测试明确授权", Reason: "原已确认的短期业务例外"}}
	draft, err := s.UpdateProfileDraft(ctx, draft.ID, UpdateDraftRequest{ExpectedDraftRevision: draft.Revision, Changes: draft.Changes})
	if err != nil {
		t.Fatal(err)
	}
	first := mustConfirm(t, s, draft, mustPreview(t, s, draft.ID), "exception-before-expiry", "access")
	var historicalBody string
	if err = s.db.Raw("SELECT body FROM intent_versions WHERE id=?", first.Heads[0].IntentVersionID).Scan(&historicalBody).Error; err != nil {
		t.Fatal(err)
	}
	changed := mustDraft(t, s, snapshot, asset.ID)
	changed.Changes[0].Access.Exceptions[0].Reason = "修改后的理由必须重新获得有效授权"
	changed, err = s.UpdateProfileDraft(ctx, changed.ID, UpdateDraftRequest{ExpectedDraftRevision: changed.Revision, Changes: changed.Changes})
	if err != nil {
		t.Fatal(err)
	}
	preview := mustPreview(t, s, changed.ID)
	if !containsSection(preview.ConfirmableSections, "access") {
		t.Fatal("fixture expired before preview", preview)
	}
	<-time.After(time.Until(expires) + 10*time.Millisecond)
	if _, err = s.ConfirmProfileDraft(ctx, changed.ID, ConfirmDraftRequest{PreviewID: preview.ID, ExpectedDraftRevision: changed.Revision, ConfirmedSections: []string{"access"}}, "exception-after-expiry"); !errors.Is(err, ErrInputInvalid) {
		t.Fatal("preview granted an exception that had expired before confirmation", err)
	}
	unchanged := mustDraft(t, s, snapshot, asset.ID)
	unchangedPreview := mustPreview(t, s, unchanged.ID)
	if !containsSection(unchangedPreview.ConfirmableSections, "access") {
		t.Fatal("unchanged expired history blocked unrelated access confirmation", unchangedPreview)
	}
	mustConfirm(t, s, unchanged, unchangedPreview, "keep-expired-history", "access")
	frozen, _, err := s.FreezeInput(ctx, snapshot.ID)
	if err != nil || len(frozen.Baseline.Exceptions) != 1 || frozen.Baseline.Exceptions[0].Reason != "原已确认的短期业务例外" {
		t.Fatal(err)
	}
	frozen.EvaluationAt = expires.Add(time.Second).Format(time.RFC3339Nano)
	result, err := audit.Run(ctx, frozen)
	if err != nil {
		t.Fatal(err)
	}
	for _, finding := range result.Findings {
		if finding.RuleID == "CHK-07" && finding.ServiceRef == asset.Services[1].ID && finding.AcceptedException {
			t.Fatal("expired history was still accepted at the fixed evaluation time")
		}
	}
	var preserved string
	if err = s.db.Raw("SELECT body FROM intent_versions WHERE id=?", first.Heads[0].IntentVersionID).Scan(&preserved).Error; err != nil || preserved != historicalBody {
		t.Fatal("expiry handling changed an immutable historical version", err)
	}
}
