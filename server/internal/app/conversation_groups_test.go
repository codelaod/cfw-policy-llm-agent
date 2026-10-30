package app

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"

	"cfw-policy-llm-agent/server/internal/domain"
	"cfw-policy-llm-agent/server/internal/storage"
)

func groupAppCase(t *testing.T, n int) (*IntentService, storage.Conversation, domain.ConversationIntent) {
	t.Helper()
	ctx := context.Background()
	store, e := storage.Open(filepath.Join(t.TempDir(), "groups.sqlite"), true)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { store.Close() })
	rows := []domain.Resource{}
	for i := 0; i < n; i++ {
		rows = append(rows, domain.Resource{Provider: "aliyun", AccountRef: "account-app", ResourceID: fmt.Sprintf("i-app-%02d", i), ResourceType: "ecs", Region: "cn-hangzhou", Name: "public-group-node", Source: "imported", ObservedAt: "2026-10-29T01:00:00Z"})
	}
	if e = store.UpsertResources(ctx, "source-app", rows, domain.CatalogueCoverage{Complete: false, Limitations: []string{}}); e != nil {
		t.Fatal(e)
	}
	page, e := store.SearchResources(ctx, storage.ResourceFilter{SourceRef: "source-app"}, "", 100)
	if e != nil {
		t.Fatal(e)
	}
	c, e := store.CreateConversation(ctx, "公开人工业务归属", "")
	if e != nil {
		t.Fatal(e)
	}
	saved, e := store.SaveIntentRevision(ctx, storage.SaveIntentTurn{ConversationID: c.ID, ExpectedConversationRevision: c.Revision, RequestKey: "selected", RequestDigest: "selected-public", Text: "选择这些明确对象", Reply: "本次对象已保存", Intent: domain.ConversationIntent{Status: "ready", Action: "audit", Boundary: "internet", Direction: "in", Mode: "rules_only", Selection: page.Items}})
	if e != nil {
		t.Fatal(e)
	}
	return &IntentService{Store: store}, saved.Conversation, saved.Intent
}
func TestGroupNaturalTwentyObjectsPreviewConfirmAndReplayUseOneLocalService(t *testing.T) {
	s, c, intent := groupAppCase(t, 20)
	ctx := context.Background()
	missing, e := s.HandleGroup(ctx, c.ID, "missing-source", "这些对象归属客户甲")
	if e != nil || !missing.Handled || missing.Preview != nil || len(missing.Missing) != 1 || missing.Missing[0] != "authorization_source" {
		t.Fatal(missing, e)
	}
	preview, e := s.HandleGroup(ctx, c.ID, "preview-natural", "这些对象归属客户甲，授权来源工单SEC-42，由业务负责人核对")
	if e != nil || preview.Preview == nil || len(preview.Preview.Members) != 20 || preview.Version != nil {
		t.Fatal(preview, e)
	}
	before, e := s.Store.SearchResources(ctx, storage.ResourceFilter{Group: "客户甲"}, "", 100)
	if e != nil || len(before.Items) != 0 {
		t.Fatal("preview changed membership", e)
	}
	confirmed, e := s.HandleGroup(ctx, c.ID, "confirm-natural", "确认归属")
	if e != nil || confirmed.Version == nil || len(confirmed.Version.Members) != 20 {
		t.Fatal(confirmed, e)
	}
	current, e := s.Store.CurrentIntent(ctx, c.ID)
	if e != nil || !reflect.DeepEqual(current, intent) {
		t.Fatal("group operation changed accepted target/role/access", e)
	}
	jobs, e := s.Store.ListJobs(ctx, c.ID, 0, 100)
	if e != nil || len(jobs) != 0 {
		t.Fatal("group operation executed audit/model/cloud", e)
	}
	c2, e := s.Store.GetConversation(ctx, c.ID)
	if e != nil {
		t.Fatal(e)
	}
	changed, e := s.Store.SaveIntentRevision(ctx, storage.SaveIntentTurn{ConversationID: c.ID, ExpectedConversationRevision: c2.Revision, ExpectedIntentRevision: current.Revision, RequestKey: "changed-target", RequestDigest: "changed-target", Text: "后续目标", Reply: "新目标已保存", Intent: current})
	if e != nil {
		t.Fatal(e)
	}
	replay, e := s.HandleGroup(ctx, c.ID, "confirm-natural", "确认归属")
	if e != nil || !replay.Replayed || replay.Version.ID != confirmed.Version.ID {
		t.Fatal("natural replay bound new preview/goal", replay, e)
	}
	if changed.Intent.Revision == current.Revision {
		t.Fatal("test target did not change")
	}
}
func TestGroupTypedFormAndConversationSharePreviewAndConfirmation(t *testing.T) {
	s, c, in := groupAppCase(t, 2)
	ctx := context.Background()
	target, e := s.Store.ResourceGroupTarget(ctx, c.ID)
	if e != nil {
		t.Fatal(e)
	}
	typed := storage.ResourceGroupRequest{Action: "preview", GroupName: "业务组支付", ResourceRefs: target.ResourceRefs, AuthorizationSource: "授权工单PAY-1", ExpectedRevision: in.Revision}
	preview, e := s.HandleGroup(ctx, c.ID, "typed-preview", "", &typed)
	if e != nil || preview.Preview == nil {
		t.Fatal(preview, e)
	}
	fromForm, e := s.Store.ResourceGroupPreview(ctx, c.ID, preview.Preview.ID)
	if e != nil || fromForm.GroupID != preview.Preview.GroupID {
		t.Fatal("form cannot read the same preview", e)
	}
	version, e := s.Store.ConfirmResourceGroup(ctx, c.ID, fromForm.ID, in.Revision, "form-confirm")
	if e != nil || version.GroupName != "业务组支付" {
		t.Fatal(version, e)
	}
	found, e := s.Store.ResourceGroup(ctx, "source-app", "account-app", "业务组支付")
	if e != nil || found.ID != version.ID {
		t.Fatal("conversation/form used separate group storage", e)
	}
	typed.ResourceRefs = []string{"resource-unselected"}
	if _, e = s.HandleGroup(ctx, c.ID, "typed-foreign", "", &typed); !errors.Is(e, storage.ErrInputInvalid) {
		t.Fatal("typed form enlarged selection", e)
	}
}
func TestGroupUnknownTargetsOrSensitiveSourcesNeverBecomeMembership(t *testing.T) {
	s, c, in := groupAppCase(t, 2)
	ctx := context.Background()
	out, e := s.HandleGroup(ctx, c.ID, "query", "审计客户甲的机器")
	if e != nil || out.Handled {
		t.Fatal("ordinary unknown group query became assignment", out, e)
	}
	out, e = s.HandleGroup(ctx, c.ID, "no-preview", "确认归属")
	if e != nil || out.Version != nil || len(out.Missing) != 1 || out.Missing[0] != "group_preview" {
		t.Fatal(out, e)
	}
	s.SensitiveValues = []string{"private-group-access-123456"}
	typed := storage.ResourceGroupRequest{Action: "preview", GroupName: "客户甲", AuthorizationSource: s.SensitiveValues[0], ExpectedRevision: in.Revision}
	out, e = s.HandleGroup(ctx, c.ID, "private-source", "", &typed)
	if e != nil || out.Preview != nil || len(out.Missing) != 1 || out.Missing[0] != "non_sensitive_group_input" {
		t.Fatal("typed secret became saved authorization source", out, e)
	}
	page, e := s.Store.SearchResources(ctx, storage.ResourceFilter{Group: "客户甲"}, "", 100)
	if e != nil || len(page.Items) != 0 {
		t.Fatal("unknown/sensitive instruction created membership", e)
	}
}

func TestNaturalAndTypedGroupUnknownAuthorityKeepClarification(t *testing.T) {
	s, c, in := groupAppCase(t, 2)
	ctx := context.Background()
	for i, authority := range []string{"未知", "不知道", "未核实", "unknown"} {
		out, e := s.HandleGroup(ctx, c.ID, fmt.Sprintf("natural-unknown-%d", i), "这些对象归属客户甲，授权来源"+authority)
		if e != nil || !out.Handled || out.Preview != nil || out.Version != nil || !reflect.DeepEqual(out.Missing, []string{"authorization_source"}) {
			t.Fatalf("unknown natural authority was trusted: %+v %v", out, e)
		}
	}
	req := storage.ResourceGroupRequest{Action: "preview", GroupName: "客户甲", AuthorizationSource: "未知", ExpectedRevision: in.Revision}
	out, e := s.HandleGroup(ctx, c.ID, "typed-unknown-source", "", &req)
	if e != nil || out.Preview != nil || !reflect.DeepEqual(out.Missing, []string{"authorization_source"}) {
		t.Fatal("typed unknown authority was trusted", out, e)
	}
	page, e := s.Store.SearchResources(ctx, storage.ResourceFilter{Group: "客户甲"}, "", 100)
	if e != nil || len(page.Items) != 0 || page.Coverage.Complete {
		t.Fatal("unknown authority became complete trusted group", page, e)
	}
}
