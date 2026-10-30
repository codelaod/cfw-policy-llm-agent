package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"cfw-policy-llm-agent/server/internal/agentquery"
	"cfw-policy-llm-agent/server/internal/domain"
)

func conversationDraftCase(t *testing.T) (*Store, Job, SnapshotRecord, ConversationBusinessContext) {
	t.Helper()
	s, snapshot, _, p := executionCase(t)
	in := snapshot.Input
	for i := range in.Assets {
		in.Assets[i].AllowedConfirmed, in.Assets[i].AllowedComplete, in.Assets[i].DenyAll = false, false, false
		in.Assets[i].Allowed = []domain.Access{}
		for j := range in.Assets[i].Services {
			service := &in.Assets[i].Services[j]
			service.RoleStatus = "candidate"
			service.AllowedConfirmed, service.AllowedComplete, service.DenyAll, service.RequiredConfirmed = false, false, false, false
			service.Allowed, service.Required = []domain.Access{}, []domain.Access{}
			service.PublicAccess = "unknown"
		}
	}
	var err error
	snapshot, err = s.SaveSnapshot(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	p.SnapshotID = snapshot.ID
	p.PreparedInput, p.Heads, err = s.FreezeInput(context.Background(), snapshot.ID)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := s.SavePlan(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	job, _, err := s.EnqueuePlan(context.Background(), plan.ID, "business-source")
	if err != nil {
		t.Fatal(err)
	}
	if _, found, err := s.Claim(context.Background()); err != nil || !found {
		t.Fatal("claim", err)
	}
	if _, err = s.SealInput(context.Background(), job.ID, p.PreparedInput); err != nil {
		t.Fatal(err)
	}
	current, err := s.ConversationBusinessContext(context.Background(), job.ConversationID)
	if err != nil {
		t.Fatal(err)
	}
	return s, job, snapshot, current
}

func TestConversationDraftIndependentSectionsAndStableReplay(t *testing.T) {
	s, job, snapshot, current := conversationDraftCase(t)
	ctx := context.Background()
	asset := current.Input.Assets[0]
	required := []domain.Access{{ScopeRef: current.Target.Scope.Ref, Protocol: "tcp", Addresses: []string{"198.51.100.0/24"}, Ports: []domain.PortRange{{From: 443, To: 443}}, Applicability: "applicable"}}
	request := ConversationBusinessRequest{Action: "edit", ExpectedTargetRef: current.Target.Ref, Patches: []ConversationBusinessPatch{{AssetID: asset.ID, ServiceID: asset.Services[0].ID, Role: &RoleChange{Role: "web", Protocol: "tcp", Ports: []domain.PortRange{{From: 443, To: 443}}}, Required: &required}}, UnknownFields: []string{"management_sources"}}
	preview, err := s.ApplyConversationBusiness(ctx, job.ConversationID, "mixed-business", request)
	if err != nil || preview.Draft == nil || preview.Preview == nil || len(preview.Preview.ConfirmableSections) != 2 {
		t.Fatal(preview, err)
	}
	before, heads, err := s.FreezeInput(ctx, snapshot.ID)
	if err != nil || heads[0].Revision != 0 || before.Assets[0].Services[0].RoleStatus == "confirmed" {
		t.Fatal("preview silently confirmed business", err)
	}
	replay, err := s.ApplyConversationBusiness(ctx, job.ConversationID, "mixed-business", request)
	if err != nil || !replay.Replayed || replay.Draft.ID != preview.Draft.ID || replay.Preview.ID != preview.Preview.ID {
		t.Fatal("same key created duplicate draft/preview", err)
	}
	confirmation, err := s.ApplyConversationBusiness(ctx, job.ConversationID, "confirm-role", ConversationBusinessRequest{Action: "confirm", ExpectedTargetRef: current.Target.Ref, ExpectedDraftRevision: preview.Draft.Revision, PreviewID: preview.Preview.ID, Sections: []string{"roles"}})
	if err != nil || confirmation.Confirmation == nil || confirmation.Draft.ID != preview.Draft.ID || confirmation.Draft.Status != "draft" || confirmation.Draft.PreviewID != "" {
		t.Fatal("partial confirmation did not retain same pending draft", confirmation, err)
	}
	roleOnly, _, err := s.FreezeInput(ctx, snapshot.ID)
	if err != nil {
		t.Fatal(err)
	}
	if roleOnly.Assets[0].Services[0].RoleStatus != "confirmed" || roleOnly.Assets[0].AllowedConfirmed || roleOnly.Assets[0].Services[0].AllowedConfirmed || roleOnly.Assets[0].Services[0].RequiredConfirmed || roleOnly.Assets[0].Services[1].RoleStatus != "candidate" {
		t.Fatal("role confirmation included access/other roles", roleOnly.Assets[0])
	}
	next, err := s.ApplyConversationBusiness(ctx, job.ConversationID, "preview-required", ConversationBusinessRequest{Action: "preview", ExpectedTargetRef: current.Target.Ref})
	if err != nil {
		t.Fatal(err)
	}
	if len(next.Preview.ConfirmableSections) != 1 || next.Preview.ConfirmableSections[0] != "access" {
		t.Fatal("confirmed roles reappeared as pending", next.Preview)
	}
	confirmed, err := s.ApplyConversationBusiness(ctx, job.ConversationID, "confirm-required", ConversationBusinessRequest{Action: "confirm", ExpectedTargetRef: current.Target.Ref, ExpectedDraftRevision: next.Draft.Revision, PreviewID: next.Preview.ID, Sections: []string{"access"}})
	if err != nil {
		t.Fatal(err)
	}
	latest, _, err := s.FreezeInput(ctx, snapshot.ID)
	if err != nil {
		t.Fatal(err)
	}
	service := latest.Assets[0].Services[0]
	if !service.RequiredConfirmed || !reflect.DeepEqual(service.Required, required) || service.AllowedConfirmed || latest.Assets[0].AllowedConfirmed || service.AllowedComplete || service.DenyAll {
		t.Fatal("necessary confirmation invented allowed set", service)
	}
	sealed, err := s.GetJobInput(ctx, job.ID)
	if err != nil || !reflect.DeepEqual(sealed.Assets, before.Assets) {
		t.Fatal("new business versions rewrote old sealed task", err)
	}
	if confirmed.Draft.Status != "confirmed" {
		t.Fatal("all declared sections still appear pending")
	}
}

func TestConversationDraftUsesFormRevisionAndInvalidatesOldPreview(t *testing.T) {
	s, job, _, current := conversationDraftCase(t)
	ctx := context.Background()
	asset := current.Input.Assets[0]
	first, err := s.ApplyConversationBusiness(ctx, job.ConversationID, "role-edit", ConversationBusinessRequest{Action: "edit", ExpectedTargetRef: current.Target.Ref, Patches: []ConversationBusinessPatch{{AssetID: asset.ID, ServiceID: asset.Services[0].ID, Role: &RoleChange{Role: "web", Protocol: "tcp", Ports: asset.Services[0].Ports}}}})
	if err != nil {
		t.Fatal(err)
	}
	form := *first.Draft
	form.Changes[0].Roles[0].Role = "api-gateway"
	updated, err := s.UpdateProfileDraft(ctx, form.ID, UpdateDraftRequest{ExpectedDraftRevision: form.Revision, Changes: form.Changes})
	if err != nil {
		t.Fatal(err)
	}
	read, err := s.ConversationBusinessContext(ctx, job.ConversationID)
	if err != nil || read.Draft.ID != form.ID || read.Draft.Revision != updated.Revision || read.Draft.Changes[0].Roles[0].Role != "api-gateway" {
		t.Fatal("conversation read a separate draft from form", err)
	}
	if _, err = s.ApplyConversationBusiness(ctx, job.ConversationID, "old-preview", ConversationBusinessRequest{Action: "confirm", ExpectedTargetRef: current.Target.Ref, ExpectedDraftRevision: first.Draft.Revision, PreviewID: first.Preview.ID, Sections: []string{"roles"}}); !errors.Is(err, ErrVersionConflict) {
		t.Fatal("old preview confirmed after form edit", err)
	}
	if err = s.db.Exec("UPDATE conversations SET policy_scope_ref='',revision=revision+1 WHERE id=?", job.ConversationID).Error; err != nil {
		t.Fatal(err)
	}
	if _, err = s.ConfirmProfileDraft(ctx, form.ID, ConfirmDraftRequest{PreviewID: first.Preview.ID, ExpectedDraftRevision: updated.Revision, ConfirmedSections: []string{"roles"}}, "foreign-target"); !errors.Is(err, ErrVersionConflict) {
		t.Fatal("generic form confirmation bypassed conversation target guard", err)
	}
	preserved, err := s.GetProfileDraft(ctx, form.ID)
	if err != nil || preserved.Revision != updated.Revision {
		t.Fatal("target change discarded old draft", err)
	}
}

func TestConversationDraftRejectsForeignIDsAndRollsBackFailedConfirmation(t *testing.T) {
	s, job, _, current := conversationDraftCase(t)
	ctx := context.Background()
	for _, patch := range []ConversationBusinessPatch{{AssetID: "foreign", Role: &RoleChange{Role: "web", Protocol: "unknown"}}, {AssetID: current.Target.AssetIDs[0], ServiceID: "foreign-service", Role: &RoleChange{Role: "web", Protocol: "unknown"}}} {
		if _, err := s.ApplyConversationBusiness(ctx, job.ConversationID, "foreign-"+patch.AssetID+patch.ServiceID, ConversationBusinessRequest{Action: "edit", ExpectedTargetRef: current.Target.Ref, Patches: []ConversationBusinessPatch{patch}}); err == nil {
			t.Fatal("unissued asset/service ID was used")
		}
	}
	var drafts int64
	s.db.Raw("SELECT count(*) FROM profile_drafts").Scan(&drafts)
	if drafts != 0 {
		t.Fatal("invalid operation left a newly created draft")
	}
	asset := current.Input.Assets[0]
	first, err := s.ApplyConversationBusiness(ctx, job.ConversationID, "valid-edit", ConversationBusinessRequest{Action: "edit", ExpectedTargetRef: current.Target.Ref, Patches: []ConversationBusinessPatch{{AssetID: asset.ID, ServiceID: asset.Services[0].ID, Role: &RoleChange{Role: "web", Protocol: "tcp", Ports: asset.Services[0].Ports}}}})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.db.Exec("CREATE TRIGGER fail_business_request BEFORE INSERT ON conversation_business_requests WHEN NEW.request_key='failed-confirm' BEGIN SELECT RAISE(ABORT,'fixture failure'); END").Error; err != nil {
		t.Fatal(err)
	}
	if _, err = s.ApplyConversationBusiness(ctx, job.ConversationID, "failed-confirm", ConversationBusinessRequest{Action: "confirm", ExpectedTargetRef: current.Target.Ref, ExpectedDraftRevision: first.Draft.Revision, PreviewID: first.Preview.ID, Sections: []string{"roles"}}); err == nil {
		t.Fatal("failing request receipt did not rollback confirmation")
	}
	var versions int64
	s.db.Raw("SELECT count(*) FROM profile_versions").Scan(&versions)
	preserved, err := s.GetProfileDraft(ctx, first.Draft.ID)
	if err != nil || preserved.Status != "previewed" || preserved.Revision != first.Draft.Revision || versions != 0 {
		t.Fatal("failed save changed versions or lost draft", versions, err)
	}
}

func TestConversationDraftExceptionNeedsAuthorizationAndPreservesLegacyHistory(t *testing.T) {
	s, job, snapshot, current := conversationDraftCase(t)
	ctx := context.Background()
	asset := current.Input.Assets[0]
	x := domain.Exception{CheckID: "CHK-07", SubjectRef: asset.ID, ServiceRef: asset.Services[2].ID, ScopeRef: current.Target.Scope.Ref, Reason: "短期维护", ExpiresAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)}
	values := []domain.Exception{x}
	first, err := s.ApplyConversationBusiness(ctx, job.ConversationID, "exception-missing-source", ConversationBusinessRequest{Action: "edit", ExpectedTargetRef: current.Target.Ref, Patches: []ConversationBusinessPatch{{AssetID: asset.ID, Exceptions: &values}}})
	if err != nil {
		t.Fatal(err)
	}
	if containsSection(first.Preview.ConfirmableSections, "access") || len(first.Preview.ValidationErrors) == 0 {
		t.Fatal("new exception with no authorization source was confirmable")
	}
	values[0].AuthorizationSource = "业务责任人本次明确批准"
	next, err := s.ApplyConversationBusiness(ctx, job.ConversationID, "exception-with-source", ConversationBusinessRequest{Action: "edit", ExpectedTargetRef: current.Target.Ref, ExpectedDraftRevision: first.Draft.Revision, Patches: []ConversationBusinessPatch{{AssetID: asset.ID, Exceptions: &values}}})
	if err != nil {
		t.Fatal(err)
	}
	if !containsSection(next.Preview.ConfirmableSections, "access") {
		t.Fatal("complete bounded exception not confirmable", next.Preview)
	}
	if _, err = s.ApplyConversationBusiness(ctx, job.ConversationID, "approve-exception", ConversationBusinessRequest{Action: "confirm", ExpectedTargetRef: current.Target.Ref, ExpectedDraftRevision: next.Draft.Revision, PreviewID: next.Preview.ID, Sections: []string{"access"}}); err != nil {
		t.Fatal(err)
	}
	frozen, _, err := s.FreezeInput(ctx, snapshot.ID)
	if err != nil || len(frozen.Baseline.Exceptions) != 1 || frozen.Baseline.Exceptions[0].AuthorizationSource == "" {
		t.Fatal("authorization source not persisted in existing intent version", err)
	}
	// Imported old declarations may retain an unknown source. They cannot be
	// altered into a new approval without adding an actual authorization source.
	legacy := snapshot.Input
	legacy.Assets[0].ID = "asset-legacy-authorization-unknown"
	legacy.Selection.AssetIDs = []string{legacy.Assets[0].ID}
	values[0].AuthorizationSource = ""
	values[0].Confirmed = true
	values[0].SubjectRef = legacy.Assets[0].ID
	legacy.Baseline.Exceptions = values
	old, err := s.SaveSnapshot(ctx, legacy)
	if err != nil {
		t.Fatal(err)
	}
	draft := mustDraft(t, s, old, legacy.Assets[0].ID)
	preview := mustPreview(t, s, draft.ID)
	if !containsSection(preview.ConfirmableSections, "access") {
		t.Fatal("unchanged legacy declaration was made unreadable")
	}
	draft.Changes[0].Access.Exceptions[0].ExpiresAt = time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339Nano)
	draft, err = s.UpdateProfileDraft(ctx, draft.ID, UpdateDraftRequest{ExpectedDraftRevision: draft.Revision, Changes: draft.Changes})
	if err != nil {
		t.Fatal(err)
	}
	if changed := mustPreview(t, s, draft.ID); containsSection(changed.ConfirmableSections, "access") {
		t.Fatal("legacy unknown source was reused to approve a new grant")
	}
}

func TestNewExceptionAuthorityPlaceholdersRemainUnconfirmableDrafts(t *testing.T) {
	for _, source := range []string{"未知", "不清楚", "unknown", "待确认", "-", "不知道", "未核实", "N/A", " 无来源 "} {
		t.Run(source, func(t *testing.T) {
			s, job, _, current := conversationDraftCase(t)
			asset := current.Input.Assets[0]
			values := []domain.Exception{{CheckID: "CHK-07", SubjectRef: asset.ID, ServiceRef: asset.Services[2].ID, ScopeRef: current.Target.Scope.Ref, Reason: "本次受控临时例外", ExpiresAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano), AuthorizationSource: source}}
			result, err := s.ApplyConversationBusiness(context.Background(), job.ConversationID, "placeholder-authority", ConversationBusinessRequest{Action: "edit", ExpectedTargetRef: current.Target.Ref, Patches: []ConversationBusinessPatch{{AssetID: asset.ID, Exceptions: &values}}})
			if err != nil || result.Draft == nil || result.Preview == nil {
				t.Fatal("missing authority should remain editable draft", err)
			}
			if containsSection(result.Preview.ConfirmableSections, "access") {
				t.Fatal("explicit missing-source placeholder was accepted as new authorization", source)
			}
		})
	}
}

func TestExceptionAuthorityPresenceKeepsLiteralTicketsAndUnchangedLegacy(t *testing.T) {
	s, job, _, current := conversationDraftCase(t)
	ctx := context.Background()
	asset := current.Input.Assets[0]
	value := domain.Exception{CheckID: "CHK-07", SubjectRef: asset.ID, ServiceRef: asset.Services[2].ID, ScopeRef: current.Target.Scope.Ref, Reason: "明确短期维护", ExpiresAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano), AuthorizationSource: "业务工单 UNKNOWN-42，责任人明确核对"}
	values := []domain.Exception{value}
	result, err := s.ApplyConversationBusiness(ctx, job.ConversationID, "literal-authority-ticket", ConversationBusinessRequest{Action: "edit", ExpectedTargetRef: current.Target.Ref, Patches: []ConversationBusinessPatch{{AssetID: asset.ID, Exceptions: &values}}})
	if err != nil || !containsSection(result.Preview.ConfirmableSections, "access") {
		t.Fatal("substring matching rejected a declared ticket reference", err)
	}
	for index, source := range []string{"", "未知", "待确认"} {
		legacy := current.Input
		legacy.Assets = append([]domain.Asset{}, current.Input.Assets...)
		legacy.Assets[0].ID = fmt.Sprintf("legacy-authority-%d", index)
		legacy.Selection.AssetIDs = []string{legacy.Assets[0].ID}
		oldValue := value
		oldValue.SubjectRef = legacy.Assets[0].ID
		oldValue.AuthorizationSource = source
		oldValue.Confirmed = true
		legacy.Baseline.Exceptions = []domain.Exception{oldValue}
		snapshot, err := s.SaveSnapshot(ctx, legacy)
		if err != nil {
			t.Fatal(err)
		}
		draft := mustDraft(t, s, snapshot, legacy.Assets[0].ID)
		if preview := mustPreview(t, s, draft.ID); !containsSection(preview.ConfirmableSections, "access") {
			t.Fatalf("unchanged historical source %q became unreadable", source)
		}
	}
}

func TestConversationDraftMapsOnlyIssuedAgentRoleCandidatesAndPreservesRejection(t *testing.T) {
	s, job, content := e3AgentResultCase(t, "audit")
	ctx := context.Background()
	input, err := s.GetJobInput(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, run, err := s.ReadJobAudit(ctx, job.ID)
	if err != nil || run == nil {
		t.Fatal(err)
	}
	queries, err := agentquery.New(input, &run.Result)
	if err != nil {
		t.Fatal(err)
	}
	var initial agentquery.InitialView
	if json.Unmarshal([]byte(queries.InitialContext()), &initial) != nil {
		t.Fatal("initial view")
	}
	args, _ := json.Marshal(map[string]string{"asset_ref": initial.Assets[0]})
	toolJSON, err := queries.Invoke(ctx, "get_asset_context", string(args))
	if err != nil {
		t.Fatal(err)
	}
	var tool struct {
		Data struct {
			EvidenceRef string `json:"evidence_ref"`
			Services    []struct {
				ServiceRef string `json:"service_ref"`
			} `json:"services"`
		} `json:"data"`
	}
	if json.Unmarshal([]byte(toolJSON), &tool) != nil || len(tool.Data.Services) == 0 || tool.Data.EvidenceRef == "" {
		t.Fatal("tool data")
	}
	queries.BeginTurn()
	content.Output = &agentquery.Output{ScopeRef: initial.ScopeRef, RuleVersion: initial.RuleVersion, SourceKind: initial.SourceKind, AssessmentBasis: initial.AssessmentBasis, Explanation: "有界资料中的角色候选，需要操作者核对。", Roles: []agentquery.RoleCandidate{{AssetRef: initial.Assets[0], ServiceRef: tool.Data.Services[0].ServiceRef, Role: "database", SupportingEvidence: []string{tool.Data.EvidenceRef}, ContradictingEvidence: []string{}, Unknowns: []string{"端口不证明真实用途"}, Questions: []string{"请确认该服务用途"}}}, Recommendations: []agentquery.Recommendation{}, Questions: []string{}}
	content.Bindings, content.ToolCalls = queries.LocalBindings(), 1
	if _, err = s.SaveJobAgentResult(ctx, job.ID, content); err != nil {
		t.Fatal("candidate fixture not valid", err)
	}
	current, err := s.ConversationBusinessContext(ctx, job.ConversationID)
	if err != nil || len(current.Candidates) != 1 {
		t.Fatal(current, err)
	}
	candidate := current.Candidates[0]
	if candidate.AssetID != input.Assets[0].ID || candidate.ServiceID != input.Assets[0].Services[0].ID || len(candidate.Evidence) != 1 || candidate.Evidence[0].Field != "/assets/0" || !candidate.ConflictsWithConfirmed {
		t.Fatal("candidate aliases did not resolve to fixed local evidence", candidate)
	}
	rejected, err := s.ApplyConversationBusiness(ctx, job.ConversationID, "reject-role-candidate", ConversationBusinessRequest{Action: "reject_candidate", ExpectedTargetRef: current.Target.Ref, CandidateRef: candidate.Ref})
	if err != nil || rejected.Draft != nil || rejected.Candidates[0].Decision != "rejected" {
		t.Fatal("candidate rejection created confirmation/draft", rejected, err)
	}
	read, err := s.ConversationBusinessContext(ctx, job.ConversationID)
	if err != nil || read.Candidates[0].Decision != "rejected" {
		t.Fatal("rejection record was lost", err)
	}
	if _, err = s.ApplyConversationBusiness(ctx, job.ConversationID, "fake-candidate", ConversationBusinessRequest{Action: "accept_candidate", ExpectedTargetRef: current.Target.Ref, CandidateRef: "role_candidate_forged"}); !errors.Is(err, ErrInputInvalid) {
		t.Fatal("unissued candidate ref was accepted", err)
	}
	accepted, err := s.ApplyConversationBusiness(ctx, job.ConversationID, "accept-for-preview", ConversationBusinessRequest{Action: "accept_candidate", ExpectedTargetRef: current.Target.Ref, CandidateRef: candidate.Ref})
	if err != nil || accepted.Preview == nil || accepted.Candidates[0].Decision != "accepted_for_preview" {
		t.Fatal(accepted, err)
	}
	unchanged, _, err := s.FreezeInput(ctx, current.Target.SnapshotID)
	if err != nil || unchanged.Assets[0].Services[0].Role != "web" {
		t.Fatal("accepting conflicting candidate overwrote prior confirmation", err)
	}
}

func TestProfileDraftUsesActualSnapshotMembershipBeyondLegacyFifty(t *testing.T) {
	s := collaborationStore(t)
	snapshot := collaborationSnapshot(t, s)
	in := snapshot.Input
	base := in.Assets[0]
	in.Assets = nil
	in.Selection.AssetIDs = nil
	for i := 0; i < 51; i++ {
		asset := base
		asset.ID = fmt.Sprintf("asset-large-%d", i)
		in.Assets = append(in.Assets, asset)
		in.Selection.AssetIDs = append(in.Selection.AssetIDs, asset.ID)
	}
	large, err := s.SaveSnapshot(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	draft, err := s.CreateProfileDraft(context.Background(), CreateDraftRequest{SnapshotID: large.ID, AssetIDs: in.Selection.AssetIDs})
	if err != nil || len(draft.Changes) != 51 {
		t.Fatal("legacy asset count blocked bound finite snapshot", err)
	}
	if _, err = s.UpdateProfileDraft(context.Background(), draft.ID, UpdateDraftRequest{ExpectedDraftRevision: draft.Revision, Changes: draft.Changes}); err != nil {
		t.Fatal("legacy fifty blocked shared draft update", err)
	}
	encoded, _ := json.Marshal(draft)
	if len(encoded) > 1<<20 {
		t.Fatal("fixture unexpectedly exceeds byte protection")
	}
}

func TestConversationDraftBindingSurvivesSameObjectGoalRevision(t *testing.T) {
	s, job, _, current := conversationDraftCase(t)
	ctx := context.Background()
	asset := current.Input.Assets[0]
	edit, err := s.ApplyConversationBusiness(ctx, job.ConversationID, "binding-role", ConversationBusinessRequest{Action: "edit", ExpectedTargetRef: current.Target.Ref, Patches: []ConversationBusinessPatch{{AssetID: asset.ID, ServiceID: asset.Services[0].ID, Role: &RoleChange{Role: "web", Protocol: "tcp", Ports: asset.Services[0].Ports}}}})
	if err != nil {
		t.Fatal(err)
	}
	// A new explicit goal may reuse the same immutable objects. Its visible
	// revision changes, but the pending common draft must remain reachable.
	intent := domain.ConversationIntent{ID: "intent-business-same-objects", ConversationID: job.ConversationID, Revision: 1, Status: "ready_for_confirmation", Boundary: current.Target.Scope.Boundary, Direction: current.Target.Scope.Direction, Selection: []domain.Resource{{AccountRef: current.Target.Scope.AccountRef, LocalAssetID: asset.ID, IdentityContext: current.Target.Scope.Ref}}}
	body, _ := json.Marshal(intent)
	if err = s.db.Exec("INSERT INTO conversation_intents(id,conversation_id,revision,body,created_at) VALUES(?,?,?,?,?)", intent.ID, job.ConversationID, 1, string(body), now()).Error; err != nil {
		t.Fatal(err)
	}
	if err = s.db.Exec("INSERT INTO conversation_intent_heads(conversation_id,intent_id,revision) VALUES(?,?,1)", job.ConversationID, intent.ID).Error; err != nil {
		t.Fatal(err)
	}
	first, err := s.ConversationBusinessContext(ctx, job.ConversationID)
	if err != nil || first.Target.Ref != current.Target.Ref || first.Draft == nil || first.Draft.ID != edit.Draft.ID {
		t.Fatal("same-object goal detached original draft", first, err)
	}
	intent.Revision = 2
	body, _ = json.Marshal(intent)
	if err = s.db.Exec("INSERT INTO conversation_intents(id,conversation_id,revision,body,created_at) VALUES(?,?,?,?,?)", intent.ID, job.ConversationID, 2, string(body), now()).Error; err != nil {
		t.Fatal(err)
	}
	if err = s.db.Exec("UPDATE conversation_intent_heads SET revision=2 WHERE conversation_id=?", job.ConversationID).Error; err != nil {
		t.Fatal(err)
	}
	latest, err := s.ConversationBusinessContext(ctx, job.ConversationID)
	if err != nil || latest.Target.IntentRevision != 2 || latest.Target.Ref != current.Target.Ref || latest.Draft == nil || latest.Draft.ID != edit.Draft.ID || latest.Preview == nil || latest.Preview.ID != edit.Preview.ID {
		t.Fatal("pure intent revision invalidated object-bound business preview", latest, err)
	}
	if _, err = s.ConfirmProfileDraft(ctx, edit.Draft.ID, ConfirmDraftRequest{PreviewID: edit.Preview.ID, ExpectedDraftRevision: edit.Draft.Revision, ConfirmedSections: []string{"roles"}}, "same-objects-confirm"); err != nil {
		t.Fatal("same-object fresh preview became unconfirmable", err)
	}
}

func TestConversationDraftBindingChangesForNewImmutableData(t *testing.T) {
	s, job, snapshot, current := conversationDraftCase(t)
	ctx := context.Background()
	asset := current.Input.Assets[0]
	edit, err := s.ApplyConversationBusiness(ctx, job.ConversationID, "old-data-business", ConversationBusinessRequest{Action: "edit", ExpectedTargetRef: current.Target.Ref, Patches: []ConversationBusinessPatch{{AssetID: asset.ID, ServiceID: asset.Services[0].ID, Role: &RoleChange{Role: "web", Protocol: "tcp", Ports: asset.Services[0].Ports}}}})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.FinishWithCode(ctx, job.ID, "partial", "LOCAL_FIXTURE"); err != nil {
		t.Fatal(err)
	}
	newInput := snapshot.Input
	newInput.SnapshotVersion = "new-immutable-business-data"
	newSnapshot, err := s.SaveSnapshot(ctx, newInput)
	if err != nil {
		t.Fatal(err)
	}
	conversation, err := s.GetConversation(ctx, job.ConversationID)
	if err != nil {
		t.Fatal(err)
	}
	prepared, heads, err := s.FreezeInput(ctx, newSnapshot.ID)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := s.SavePlan(ctx, ExecutionPlan{Kind: "audit", ConversationID: conversation.ID, ConversationRevision: conversation.Revision, DataSourceID: "local-offline", PolicyScopeRef: prepared.Scope.Ref, SnapshotID: newSnapshot.ID, Mode: "rules_only", ConfigRevision: "offline-v1", PreparedInput: prepared, Heads: heads})
	if err != nil {
		t.Fatal(err)
	}
	newJob, _, err := s.EnqueuePlan(ctx, plan.ID, "new-data-job")
	if err != nil {
		t.Fatal(err)
	}
	if _, found, err := s.Claim(ctx); err != nil || !found {
		t.Fatal(err)
	}
	if _, err = s.SealInput(ctx, newJob.ID, prepared); err != nil {
		t.Fatal(err)
	}
	latest, err := s.ConversationBusinessContext(ctx, conversation.ID)
	if err != nil || latest.Target.Ref == current.Target.Ref || latest.Draft != nil {
		t.Fatal("new data silently reused old business preview", latest, err)
	}
	if _, err = s.ConfirmProfileDraft(ctx, edit.Draft.ID, ConfirmDraftRequest{PreviewID: edit.Preview.ID, ExpectedDraftRevision: edit.Draft.Revision, ConfirmedSections: []string{"roles"}}, "old-data-confirm"); !errors.Is(err, ErrVersionConflict) {
		t.Fatal("new data did not invalidate old preview", err)
	}
	old, err := s.GetProfileDraft(ctx, edit.Draft.ID)
	if err != nil || old.Revision != edit.Draft.Revision || old.Status != "previewed" {
		t.Fatal("new data discarded prior draft", err)
	}
}
