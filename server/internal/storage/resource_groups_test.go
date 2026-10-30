package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"cfw-policy-llm-agent/server/internal/domain"
)

func groupCase(t *testing.T, n int) (*Store, Conversation, domain.ConversationIntent, []domain.Resource) {
	t.Helper()
	ctx := context.Background()
	s := collaborationStore(t)
	resources := []domain.Resource{}
	for i := 0; i < n; i++ {
		resources = append(resources, domain.Resource{Provider: "aliyun", AccountRef: "account-group", Region: "cn-hangzhou", ResourceType: "ecs", ResourceID: fmt.Sprintf("i-group-%02d", i), Name: fmt.Sprintf("group-node-%02d", i), Tags: map[string]string{"owner": "客户甲"}, Group: "客户甲", GroupConfirmed: true, Source: "imported", ObservedAt: "2026-10-29T01:00:00Z"})
	}
	if e := s.UpsertResources(ctx, "source-group", resources, domain.CatalogueCoverage{Complete: false, ObservedAt: "2026-10-29T01:00:00Z", Limitations: []string{"PUBLIC_ARTIFICIAL_FIXTURE"}}); e != nil {
		t.Fatal(e)
	}
	page, e := s.SearchResources(ctx, ResourceFilter{SourceRef: "source-group"}, "", 100)
	if e != nil {
		t.Fatal(e)
	}
	c, e := s.CreateConversation(ctx, "归属公开人工对象", "")
	if e != nil {
		t.Fatal(e)
	}
	saved, e := s.SaveIntentRevision(ctx, SaveIntentTurn{ConversationID: c.ID, ExpectedConversationRevision: c.Revision, RequestKey: "select-group-objects", RequestDigest: "public-selection", Text: "选择这些对象", Reply: "已明确选择本次对象", Intent: domain.ConversationIntent{Status: "ready", Action: "audit", Boundary: "internet", Direction: "in", Mode: "rules_only", Selection: page.Items}})
	if e != nil {
		t.Fatal(e)
	}
	return s, saved.Conversation, saved.Intent, page.Items
}
func TestResourceGroupTwentyObjectsRequireExplicitPreviewAndConfirmation(t *testing.T) {
	s, c, intent, resources := groupCase(t, 20)
	ctx := context.Background()
	before, e := s.SearchResources(ctx, ResourceFilter{Group: "客户甲"}, "", 100)
	if e != nil || len(before.Items) != 0 || before.Coverage.Complete {
		t.Fatal("ordinary tag/body flag became confirmed membership", before, e)
	}
	if resources[0].GroupConfirmed || resources[0].Group != "" {
		t.Fatal("untrusted catalog group displayed as confirmed")
	}
	refs := []string{}
	for _, r := range resources {
		refs = append(refs, r.ID)
	}
	refs = append(refs, refs[0])
	p, e := s.PreviewResourceGroup(ctx, c.ID, "客户甲", refs, "工单 SEC-42，业务负责人明确核对", intent.Revision, "group-preview")
	if e != nil || len(p.Members) != 20 {
		t.Fatal(p, e)
	}
	expires, _ := time.Parse(time.RFC3339Nano, p.ExpiresAt)
	created, _ := time.Parse(time.RFC3339Nano, p.CreatedAt)
	if expires.Sub(created) != 5*time.Minute {
		t.Fatal("preview lifetime", p)
	}
	still, _ := s.SearchResources(ctx, ResourceFilter{Group: "客户甲"}, "", 100)
	if len(still.Items) != 0 {
		t.Fatal("preview confirmed membership")
	}
	version, e := s.ConfirmResourceGroup(ctx, c.ID, p.ID, intent.Revision, "group-confirm")
	if e != nil || version.Revision != 1 || len(version.Members) != 20 {
		t.Fatal(version, e)
	}
	found, e := s.SearchResources(ctx, ResourceFilter{Group: "客户甲"}, "", 100)
	if e != nil || len(found.Items) != 20 || !found.Coverage.Complete || found.HasMore || !containsPrepared(found.Coverage.Limitations, "CONFIRMED_GROUP_MEMBERSHIP_ONLY") {
		t.Fatal(found, e)
	}
	for _, r := range found.Items {
		if r.Group != "客户甲" || !r.GroupConfirmed || len(found.ConfirmedGroups[r.ID]) != 1 {
			t.Fatal("missing explicit confirmed mapping", r, found.ConfirmedGroups[r.ID])
		}
	}
	var jobs int64
	s.db.Raw("SELECT count(*) FROM e0_jobs").Scan(&jobs)
	if jobs != 0 {
		t.Fatal("business group confirmation executed a task")
	}
}
func TestResourceGroupsStayIndependentImmutableAndInvalidateCatalogueCursors(t *testing.T) {
	s, c, in, resources := groupCase(t, 20)
	ctx := context.Background()
	p, e := s.PreviewResourceGroup(ctx, c.ID, "客户甲", nil, "业务负责人核对", in.Revision, "first-preview")
	if e != nil {
		t.Fatal(e)
	}
	first, e := s.ConfirmResourceGroup(ctx, c.ID, p.ID, in.Revision, "first-confirm")
	if e != nil {
		t.Fatal(e)
	}
	page, e := s.SearchResources(ctx, ResourceFilter{Group: "客户甲"}, "", 7)
	if e != nil || !page.HasMore || page.Coverage.Complete {
		t.Fatal(page, e)
	}
	p2, e := s.PreviewResourceGroup(ctx, c.ID, "业务组支付", nil, "工单PAY-1", in.Revision, "pay-preview")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.ConfirmResourceGroup(ctx, c.ID, p2.ID, in.Revision, "pay-confirm"); e != nil {
		t.Fatal(e)
	}
	if _, e = s.SearchResources(ctx, ResourceFilter{Group: "客户甲"}, page.NextCursor, 7); !errors.Is(e, ErrCursor) {
		t.Fatal("confirmed change did not invalidate old catalog cursor", e)
	}
	plain, e := s.SearchResources(ctx, ResourceFilter{ResourceID: resources[0].ResourceID}, "", 100)
	if e != nil || len(plain.Items) != 1 || len(plain.ConfirmedGroups[resources[0].ID]) != 2 || plain.Items[0].GroupConfirmed {
		t.Fatal("multiple confirmed groups lost/flattened", plain, e)
	}
	p3, e := s.PreviewResourceGroup(ctx, c.ID, "客户甲", []string{resources[0].ID}, "修订工单C-2", in.Revision, "replace-preview")
	if e != nil {
		t.Fatal(e)
	}
	second, e := s.ConfirmResourceGroup(ctx, c.ID, p3.ID, in.Revision, "replace-confirm")
	if e != nil || second.Revision != 2 {
		t.Fatal(second, e)
	}
	old, e := s.ResourceGroupVersion(ctx, first.ID)
	if e != nil || len(old.Members) != 20 {
		t.Fatal("old membership version changed", old, e)
	}
	current, _ := s.SearchResources(ctx, ResourceFilter{Group: "客户甲"}, "", 100)
	if len(current.Items) != 1 {
		t.Fatal("new group head did not replace membership", current)
	}
	persisted, e := s.CurrentIntent(ctx, c.ID)
	if e != nil || !reflect.DeepEqual(persisted.Selection, in.Selection) {
		t.Fatal("group update rewrote old selected goal", e)
	}
}
func TestGroupPreviewIgnoresUnrelatedDirectoryAndRenameButChecksStableIdentity(t *testing.T) {
	s, c, in, resources := groupCase(t, 2)
	ctx := context.Background()
	p, e := s.PreviewResourceGroup(ctx, c.ID, "客户甲", nil, "业务确认A", in.Revision, "rename-preview")
	if e != nil {
		t.Fatal(e)
	}
	renamed := resources[0]
	renamed.Name = "changed-name"
	renamed.LocalAssetID = "changed-local-alias"
	renamed.IdentityContext = "other-snapshot-scope"
	renamed.ObservedAt = "2026-10-29T02:00:00Z"
	unrelated := renamed
	unrelated.ResourceID = "i-unrelated"
	if e = s.UpsertResources(ctx, "source-group", []domain.Resource{renamed, unrelated}, domain.CatalogueCoverage{Complete: false, Limitations: []string{}}); e != nil {
		t.Fatal(e)
	}
	if _, e = s.ConfirmResourceGroup(ctx, c.ID, p.ID, in.Revision, "rename-confirm"); e != nil {
		t.Fatal("non-identity catalog update invalidated preview", e)
	}
	current, _ := s.SearchResources(ctx, ResourceFilter{ResourceID: renamed.ResourceID}, "", 100)
	if current.Items[0].Name != "changed-name" || !current.Items[0].GroupConfirmed {
		t.Fatal("metadata update erased confirmed membership")
	}
}
func TestGroupPreviewChecksTargetRevisionExpiryHeadCASAndStableReplay(t *testing.T) {
	s, c, in, resources := groupCase(t, 3)
	ctx := context.Background()
	req := ResourceGroupRequest{Action: "preview", GroupName: "客户甲", AuthorizationSource: "工单B", ExpectedRevision: in.Revision}
	first, e := s.ApplyResourceGroup(ctx, c.ID, "preview-first", req)
	if e != nil {
		t.Fatal(e)
	}
	rival, e := s.PreviewResourceGroup(ctx, c.ID, "客户甲", []string{resources[0].ID}, "工单C", in.Revision, "preview-rival")
	if e != nil {
		t.Fatal(e)
	}
	confirmed, e := s.ConfirmResourceGroup(ctx, c.ID, first.Preview.ID, in.Revision, "confirm-first")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.ConfirmResourceGroup(ctx, c.ID, rival.ID, in.Revision, "confirm-rival"); !errors.Is(e, ErrVersionConflict) {
		t.Fatal("stale group head replaced confirmed version", e)
	}
	updated, e := s.SaveIntentRevision(ctx, SaveIntentTurn{ConversationID: c.ID, ExpectedConversationRevision: c.Revision, ExpectedIntentRevision: in.Revision, RequestKey: "new-group-target", RequestDigest: "new-target", Text: "修改目标", Reply: "目标新版本", Intent: in})
	if e != nil {
		t.Fatal(e)
	}
	replay, e := s.ApplyResourceGroup(ctx, c.ID, "preview-first", req)
	if e != nil || !replay.Replayed || replay.Preview.ID != first.Preview.ID {
		t.Fatal("preview replay checked newer target first", replay, e)
	}
	replayVersion, e := s.ConfirmResourceGroup(ctx, c.ID, first.Preview.ID, in.Revision, "confirm-first")
	if e != nil || replayVersion.ID != confirmed.ID {
		t.Fatal("confirmation replay checked newer target first", e)
	}
	if _, e = s.ConfirmResourceGroup(ctx, c.ID, rival.ID, updated.Intent.Revision, "stale-target"); !errors.Is(e, ErrVersionConflict) {
		t.Fatal("preview survived changed goal", e)
	}
	exp, e := s.PreviewResourceGroup(ctx, c.ID, "客户乙", nil, "工单X", updated.Intent.Revision, "exp-preview")
	if e != nil {
		t.Fatal(e)
	}
	exp.ExpiresAt = time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano)
	body, _ := json.Marshal(exp)
	if e = s.db.Exec("UPDATE resource_group_previews SET expires_at=?,body=? WHERE id=?", exp.ExpiresAt, string(body), exp.ID).Error; e != nil {
		t.Fatal(e)
	}
	if _, e = s.ConfirmResourceGroup(ctx, c.ID, exp.ID, updated.Intent.Revision, "exp-confirm"); !errors.Is(e, ErrVersionConflict) {
		t.Fatal("expired preview confirmed", e)
	}
}
func TestGroupRejectsUnselectedOrMixedSourceAndRollsBackFailedCommit(t *testing.T) {
	s, c, in, resources := groupCase(t, 2)
	ctx := context.Background()
	if _, e := s.PreviewResourceGroup(ctx, c.ID, "客户甲", []string{"resource-foreign"}, "业务核对", in.Revision, "foreign"); !errors.Is(e, ErrInputInvalid) {
		t.Fatal("unselected member accepted", e)
	}
	if _, e := s.PreviewResourceGroup(ctx, c.ID, "客户甲", nil, "", in.Revision, "missing-source"); !errors.Is(e, ErrInputInvalid) {
		t.Fatal("missing authority source accepted", e)
	}
	p, e := s.PreviewResourceGroup(ctx, c.ID, "客户甲", nil, "工单D", in.Revision, "rollback-preview")
	if e != nil {
		t.Fatal(e)
	}
	var revision int64
	s.db.Raw("SELECT revision FROM resource_catalogue_state WHERE id=1").Scan(&revision)
	if e = s.db.Exec("CREATE TRIGGER fail_group_commit BEFORE INSERT ON resource_group_requests WHEN NEW.request_key='rollback-confirm' BEGIN SELECT RAISE(ABORT,'public fixture failure'); END").Error; e != nil {
		t.Fatal(e)
	}
	if _, e = s.ConfirmResourceGroup(ctx, c.ID, p.ID, in.Revision, "rollback-confirm"); e == nil {
		t.Fatal("fixture commit did not fail")
	}
	var versions, after int64
	s.db.Raw("SELECT count(*) FROM resource_group_versions").Scan(&versions)
	s.db.Raw("SELECT revision FROM resource_catalogue_state WHERE id=1").Scan(&after)
	if versions != 0 || after != revision {
		t.Fatal("failed confirmation left partial group/cursor revision", versions, after, revision)
	}
	foreign := resources[0]
	foreign.AccountRef = "account-other"
	foreign.ResourceID = "i-other"
	foreign.SourceRef = "other-source"
	if e = s.UpsertResources(ctx, "other-source", []domain.Resource{foreign}, domain.CatalogueCoverage{Complete: false, Limitations: []string{}}); e != nil {
		t.Fatal(e)
	}
	other, e := s.SearchResources(ctx, ResourceFilter{SourceRef: "other-source"}, "", 100)
	if e != nil {
		t.Fatal(e)
	}
	c2, e := s.GetConversation(ctx, c.ID)
	if e != nil {
		t.Fatal(e)
	}
	in.Selection = append(in.Selection, other.Items[0])
	next, e := s.SaveIntentRevision(ctx, SaveIntentTurn{ConversationID: c.ID, ExpectedConversationRevision: c2.Revision, ExpectedIntentRevision: in.Revision, RequestKey: "mixed-selection", RequestDigest: "mixed", Text: "人工混合源夹具", Reply: "仅作边界验证", Intent: in})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.PreviewResourceGroup(ctx, c.ID, "混合组", nil, "工单mixed", next.Intent.Revision, "mixed-preview"); !errors.Is(e, ErrInputInvalid) {
		t.Fatal("cross-source/account group accepted", e)
	}
}
func TestResourceGroupReplaySurvivesReopenAndMissingSchemaRejectsStartup(t *testing.T) {
	s, c, in, _ := groupCase(t, 2)
	ctx := context.Background()
	p, e := s.PreviewResourceGroup(ctx, c.ID, "客户甲", nil, "业务核对", in.Revision, "persist-preview")
	if e != nil {
		t.Fatal(e)
	}
	v, e := s.ConfirmResourceGroup(ctx, c.ID, p.ID, in.Revision, "persist-confirm")
	if e != nil {
		t.Fatal(e)
	}
	var database string
	s.db.Raw("SELECT file FROM pragma_database_list WHERE name='main'").Scan(&database)
	s.Close()
	reopened, e := Open(database, false)
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.Close()
	replay, e := reopened.ConfirmResourceGroup(ctx, c.ID, p.ID, in.Revision, "persist-confirm")
	if e != nil || replay.ID != v.ID {
		t.Fatal("reopen lost immutable confirmation/replay", e)
	}
	if e = reopened.db.Exec("DROP TABLE resource_group_requests").Error; e != nil {
		t.Fatal(e)
	}
	reopened.Close()
	if bad, e := Open(database, false); e == nil {
		bad.Close()
		t.Fatal("startup silently accepted incomplete group schema")
	} else if !strings.Contains(e.Error(), "migration") {
		t.Fatal(e)
	}
	repaired, e := Open(database, true)
	if e != nil {
		t.Fatal(e)
	}
	repaired.Close()
}

func TestGroupAuthorityUnknownDoesNotBecomeTrustedButTicketTitlesStayValid(t *testing.T) {
	s, c, in, _ := groupCase(t, 2)
	ctx := context.Background()
	for i, authority := range []string{"未知", "不知道", "未核实", "尚未确认", "来源：未知", "（未知）", "unknown", "N/A"} {
		if _, e := s.PreviewResourceGroup(ctx, c.ID, "客户甲", nil, authority, in.Revision, fmt.Sprintf("unknown-authority-%d", i)); !errors.Is(e, ErrInputInvalid) {
			t.Fatalf("placeholder authority %q was accepted: %v", authority, e)
		}
	}
	var previews int64
	s.db.Raw("SELECT count(*) FROM resource_group_previews").Scan(&previews)
	if previews != 0 {
		t.Fatal("unknown authority left previews")
	}
	p, e := s.PreviewResourceGroup(ctx, c.ID, "客户甲", nil, "工单AUTH-42：未知资产归属排查已获业务负责人授权", in.Revision, "known-ticket-title")
	if e != nil {
		t.Fatal("real reference title containing ordinary unknown words was rejected", e)
	}
	if _, e = s.ConfirmResourceGroup(ctx, c.ID, p.ID, in.Revision, "known-ticket-confirm"); e != nil {
		t.Fatal(e)
	}
}

func TestGroupCannotConfirmLegacyPreviewWithUnknownAuthority(t *testing.T) {
	s, c, in, _ := groupCase(t, 2)
	ctx := context.Background()
	preview, e := s.PreviewResourceGroup(ctx, c.ID, "客户甲", nil, "工单AUTH-4", in.Revision, "legacy-unknown-preview")
	if e != nil {
		t.Fatal(e)
	}
	preview.AuthorizationSource = "未知"
	body, _ := json.Marshal(preview)
	if e = s.db.Exec("UPDATE resource_group_previews SET body=? WHERE id=?", string(body), preview.ID).Error; e != nil {
		t.Fatal(e)
	}
	if _, e = s.ConfirmResourceGroup(ctx, c.ID, preview.ID, in.Revision, "legacy-unknown-confirm"); !errors.Is(e, ErrInputInvalid) {
		t.Fatal("old unknown-authority preview created a new trusted version", e)
	}
}
