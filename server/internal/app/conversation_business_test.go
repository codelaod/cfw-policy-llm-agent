package app

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"cfw-policy-llm-agent/server/internal/domain"
	"cfw-policy-llm-agent/server/internal/storage"
)

func businessRouterCase(t *testing.T) (*IntentService, storage.Conversation, storage.SnapshotRecord, storage.Job) {
	t.Helper()
	return businessRouterInputCase(t, nil)
}

func businessRouterInputCase(t *testing.T, changeInput func(*domain.Input)) (*IntentService, storage.Conversation, storage.SnapshotRecord, storage.Job) {
	t.Helper()
	store, plans, snapshot, conversation, request := planCase(t)
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
	if changeInput != nil {
		changeInput(&in)
	}
	var err error
	snapshot, err = store.SaveSnapshot(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	request.DataPolicy.SnapshotID = snapshot.ID
	plan, err := plans.Preview(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	job, _, err := store.EnqueuePlan(context.Background(), plan.ID, "business-sealed-input")
	if err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.Claim(context.Background()); err != nil || !found {
		t.Fatal("claim", err)
	}
	if _, err = store.SealInput(context.Background(), job.ID, plan.PreparedInput); err != nil {
		t.Fatal(err)
	}
	return &IntentService{Store: store, Plans: plans}, conversation, snapshot, job
}

func TestBusinessRouterConfirmsUniqueUnknownEndpointWithoutDuplicatingService(t *testing.T) {
	s, c, snapshot, job := businessRouterInputCase(t, func(in *domain.Input) {
		in.Assets[0].Services = in.Assets[0].Services[:2]
		in.Assets[0].Services[0].Role = "unknown"
	})
	first := handleBusiness(t, s, c.ID, "unknown-https-mixed", "确认Web服务，HTTPS443必须保留，管理来源未知")
	if first.Draft == nil || first.Preview == nil || len(first.Draft.Changes) != 1 {
		t.Fatal("mixed declaration did not produce one shared draft", first)
	}
	change := first.Draft.Changes[0]
	if len(change.Roles) != 2 || change.Roles[0].ServiceID != "svc-https" || change.Roles[0].Role != "web" {
		t.Fatal("explicit HTTPS declaration did not reuse the existing unknown endpoint", change.Roles)
	}
	if len(change.Access.Services) != 2 || change.Access.Services[0].ServiceID != "svc-https" || change.Access.Services[0].RequiredDeclared == nil || !*change.Access.Services[0].RequiredDeclared {
		t.Fatal("necessary access was attached to a new service instead of the existing endpoint", change.Access)
	}
	roles := handleBusiness(t, s, c.ID, "unknown-https-roles", "确认预览中的角色")
	if roles.State != "confirmed" {
		t.Fatal(roles)
	}
	if preview := handleBusiness(t, s, c.ID, "unknown-https-access-preview", "预览业务资料"); preview.Preview == nil {
		t.Fatal("role-only confirmation lost the pending necessary access", preview)
	}
	if confirmed := handleBusiness(t, s, c.ID, "unknown-https-access", "确认预览中的访问"); confirmed.State != "confirmed" {
		t.Fatal(confirmed)
	}
	after, _, err := s.Store.FreezeInput(context.Background(), snapshot.ID)
	if err != nil {
		t.Fatal(err)
	}
	asset := after.Assets[0]
	if len(asset.Services) != 2 || asset.Services[0].ID != "svc-https" || asset.Services[0].Role != "web" || asset.Services[0].RoleStatus != "confirmed" || !asset.Services[0].RequiredConfirmed || asset.Services[0].AllowedConfirmed || asset.Services[1].RoleStatus != "candidate" || asset.Services[1].AllowedConfirmed {
		t.Fatal("confirmation duplicated an endpoint or promoted unrelated authorization", asset)
	}
	sealed, err := s.Store.GetJobInput(context.Background(), job.ID)
	if err != nil || sealed.Assets[0].Services[0].Role != "unknown" || len(sealed.Assets[0].Services) != 2 {
		t.Fatal("later role confirmation rewrote original fixed input", err)
	}
}

func TestBusinessServiceMatchingUsesExplicitEndpointBeforeRole(t *testing.T) {
	https := []domain.PortRange{{From: 443, To: 443}}
	http := []domain.PortRange{{From: 80, To: 80}}
	for _, tc := range []struct {
		name      string
		text      string
		services  []domain.Service
		wantID    string
		ambiguous bool
	}{
		{name: "unknown endpoint", services: []domain.Service{{ID: "svc-https", Role: "unknown", Protocol: "tcp", Ports: https}}, wantID: "svc-https"},
		{name: "other web port does not steal explicit HTTPS", services: []domain.Service{{ID: "svc-http", Role: "web", Protocol: "tcp", Ports: http}, {ID: "svc-https", Role: "unknown", Protocol: "tcp", Ports: https}}, wantID: "svc-https"},
		{name: "new endpoint does not rewrite existing web port", services: []domain.Service{{ID: "svc-http", Role: "web", Protocol: "tcp", Ports: http}}},
		{name: "duplicate endpoints require service identity", services: []domain.Service{{ID: "svc-a", Role: "unknown", Protocol: "tcp", Ports: https}, {ID: "svc-b", Role: "unknown", Protocol: "tcp", Ports: https}}, ambiguous: true},
		{name: "different existing role requires explicit target", services: []domain.Service{{ID: "svc-admin", Role: "management", Protocol: "tcp", Ports: https}}, ambiguous: true},
		{name: "explicit service ID can change its role", text: "确认svc-admin为Web服务，HTTPS443必须保留", services: []domain.Service{{ID: "svc-admin", Role: "management", Protocol: "tcp", Ports: https}}, wantID: "svc-admin"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			text := tc.text
			if text == "" {
				text = "确认Web服务，HTTPS443必须保留"
			}
			got, ambiguous := businessTextService(tc.services, text, "web", "tcp", https)
			if got != tc.wantID || ambiguous != tc.ambiguous {
				t.Fatalf("service=%q ambiguous=%t, want service=%q ambiguous=%t", got, ambiguous, tc.wantID, tc.ambiguous)
			}
		})
	}
}

func handleBusiness(t *testing.T, s *IntentService, c, key, text string, typed ...ConversationBusinessRequest) ConversationBusinessResult {
	t.Helper()
	handled, result, err := s.HandleBusiness(context.Background(), c, key, text, typed...)
	if err != nil || !handled {
		t.Fatal("business turn not handled", result, err)
	}
	return result
}

func TestBusinessRouterMixedRoleNecessaryAndUnknownRemainIndependent(t *testing.T) {
	s, c, snapshot, job := businessRouterCase(t)
	first := handleBusiness(t, s, c.ID, "mixed", "确认Web服务，HTTPS443必须保留，管理来源未知")
	if first.State != "preview_required" || first.Preview == nil || first.Draft == nil || !guidanceContains(first.UnknownFields, "management_sources") || !guidanceContains(first.UnknownFields, "required_sources") {
		t.Fatal("unknown clause swallowed explicit role/necessary declaration", first)
	}
	change := first.Draft.Changes[0]
	if change.Roles[0].Role != "web" || change.Roles[0].Declared == nil || !*change.Roles[0].Declared || change.Access.AllowedDeclared == nil || *change.Access.AllowedDeclared {
		t.Fatal("role intent changed unrelated asset authorization", change)
	}
	var necessary *storage.ServiceAccessChange
	for i := range change.Access.Services {
		if change.Access.Services[i].ServiceID == snapshot.Input.Assets[0].Services[0].ID {
			necessary = &change.Access.Services[i]
		}
	}
	if necessary == nil || necessary.RequiredDeclared == nil || !*necessary.RequiredDeclared || !necessary.RequiredConfirmed || len(necessary.Required) != 1 || len(necessary.Required[0].Addresses) != 0 || necessary.Required[0].Applicability != "unknown" || len(necessary.Required[0].Ports) != 1 || necessary.Required[0].Ports[0].From != 443 {
		t.Fatal("necessary HTTPS invented source or lost explicit port", necessary)
	}
	before, heads, err := s.Store.FreezeInput(context.Background(), snapshot.ID)
	if err != nil || heads[0].Revision != 0 {
		t.Fatal("natural '确认Web' automatically confirmed without preview", err)
	}
	roles := handleBusiness(t, s, c.ID, "confirm-roles", "确认预览中的角色")
	if roles.State != "confirmed" || roles.Draft.ID != first.Draft.ID || roles.Draft.Status != "draft" {
		t.Fatal("role confirmation lost pending necessary section", roles)
	}
	read := handleBusiness(t, s, c.ID, "read-current", "查看业务草稿")
	if read.Draft.Revision != roles.Draft.Revision || read.Draft.ID != first.Draft.ID {
		t.Fatal("dialogue did not read common latest draft")
	}
	preview := handleBusiness(t, s, c.ID, "preview-necessary", "预览业务资料")
	if len(preview.Preview.ConfirmableSections) != 1 || preview.Preview.ConfirmableSections[0] != "access" {
		t.Fatal("confirmed role was offered again", preview)
	}
	confirmed := handleBusiness(t, s, c.ID, "confirm-necessary", "确认预览中的访问")
	if confirmed.State != "confirmed" {
		t.Fatal(confirmed)
	}
	after, _, err := s.Store.FreezeInput(context.Background(), snapshot.ID)
	if err != nil {
		t.Fatal(err)
	}
	asset := after.Assets[0]
	if asset.AllowedConfirmed || asset.DenyAll || asset.Services[0].AllowedConfirmed || !asset.Services[0].RequiredConfirmed || asset.Services[1].RoleStatus != "candidate" || asset.Services[1].AllowedConfirmed {
		t.Fatal("role/necessary confirmation promoted unknown management authorization", asset)
	}
	sealed, err := s.Store.GetJobInput(context.Background(), job.ID)
	if err != nil || !reflect.DeepEqual(sealed.Assets, before.Assets) {
		t.Fatal("later confirmation mutated original job input", err)
	}
	// Replay the original natural command after confirmations changed the draft.
	replay := handleBusiness(t, s, c.ID, "mixed", "确认Web服务，HTTPS443必须保留，管理来源未知")
	if !replay.Replayed || replay.Draft.ID != first.Draft.ID || replay.Draft.Revision != first.Draft.Revision || replay.Preview.ID != first.Preview.ID {
		t.Fatal("same request key rebuilt a draft after context advanced", replay)
	}
	if _, _, err = s.HandleBusiness(context.Background(), c.ID, "mixed", "确认数据库服务"); !errors.Is(err, storage.ErrConflict) {
		t.Fatal("same stable key accepted changed text", err)
	}
}

func TestBusinessRouterUnknownEmptyAndExplicitDenyAllAreDifferent(t *testing.T) {
	s, c, snapshot, _ := businessRouterCase(t)
	unknown := handleBusiness(t, s, c.ID, "unknown", "管理来源未知")
	if unknown.State != "unknown_preserved" || unknown.Draft != nil {
		t.Fatal("unknown business created a forbidden-all draft", unknown)
	}
	unknownRole := handleBusiness(t, s, c.ID, "unknown-role", "用途未知，先检查独立基线")
	if unknownRole.State != "unknown_preserved" || unknownRole.Draft != nil || !guidanceContains(unknownRole.UnknownFields, "role") {
		t.Fatal("unknown role did not have a local preservation exit", unknownRole)
	}
	empty := handleBusiness(t, s, c.ID, "empty", "允许集合就是空的")
	if empty.State != "needs_clarification" || !guidanceContains(empty.MissingInputs, "service_for_explicit_empty_permissions") {
		t.Fatal("empty statement without target was applied globally", empty)
	}
	denied := handleBusiness(t, s, c.ID, "deny-management", "这个管理服务明确不允许任何公网来源，允许集合就是空的")
	if denied.Preview == nil || denied.State != "preview_required" {
		t.Fatal(denied)
	}
	confirmed := handleBusiness(t, s, c.ID, "confirm-deny", "确认预览中的访问")
	if confirmed.State != "confirmed" {
		t.Fatal(confirmed)
	}
	frozen, _, err := s.Store.FreezeInput(context.Background(), snapshot.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !frozen.Assets[0].Services[1].DenyAll || !frozen.Assets[0].Services[1].AllowedComplete || !frozen.Assets[0].Services[1].AllowedConfirmed || frozen.Assets[0].AllowedConfirmed || frozen.Assets[0].Services[0].AllowedConfirmed {
		t.Fatal("service-only explicit empty set expanded to all assets/services", frozen.Assets[0])
	}
}

func TestBusinessRouterFormEditInvalidatesPreviewAndReadsLatestRevision(t *testing.T) {
	s, c, _, _ := businessRouterCase(t)
	first := handleBusiness(t, s, c.ID, "web", "确认Web服务")
	form := *first.Draft
	form.Changes[0].Roles[0].Role = "api-service"
	updated, err := s.Store.UpdateProfileDraft(context.Background(), form.ID, storage.UpdateDraftRequest{ExpectedDraftRevision: form.Revision, Changes: form.Changes})
	if err != nil {
		t.Fatal(err)
	}
	read := handleBusiness(t, s, c.ID, "read-form", "查看业务草稿")
	if read.Draft.Revision != updated.Revision || read.Draft.Changes[0].Roles[0].Role != "api-service" {
		t.Fatal("router used separate profile state", read)
	}
	old := handleBusiness(t, s, c.ID, "stale-confirm", "", ConversationBusinessRequest{Action: "confirm", ExpectedTargetRef: first.Target.Ref, ExpectedDraftRevision: first.Draft.Revision, PreviewID: first.Preview.ID, Sections: []string{"roles"}})
	if old.State != "conflict" || old.Confirmation != nil {
		t.Fatal("stale form preview was confirmed", old)
	}
	bare := handleBusiness(t, s, c.ID, "plain-confirm", "确认Web服务")
	if bare.State != "preview_required" || bare.Confirmation != nil {
		t.Fatal("new natural declaration bypassed new preview", bare)
	}
}

func TestBusinessRouterTypedNewServiceAndRequiredDependencyUseExistingDraft(t *testing.T) {
	s, c, snapshot, _ := businessRouterCase(t)
	required := []domain.Access{{Protocol: "tcp", Addresses: []string{}, Ports: []domain.PortRange{{From: 6379, To: 6379}}, Applicability: "unknown"}}
	result := handleBusiness(t, s, c.ID, "new-service", "", ConversationBusinessRequest{Action: "edit", Patches: []storage.ConversationBusinessPatch{{AssetID: snapshot.Input.Assets[0].ID, Role: &storage.RoleChange{Role: "cache", Protocol: "tcp", Ports: []domain.PortRange{{From: 6379, To: 6379}}}, Required: &required}}})
	if result.Preview == nil || !guidanceContains(result.Preview.SectionDependencies["access"], "roles") || len(result.Draft.Changes[0].Roles) != 4 {
		t.Fatal("new service failed to use established roles/access dependency", result)
	}
	foreign, err := s.Store.CreateConversation(context.Background(), "foreign without sealed data", snapshot.ScopeRef)
	if err != nil {
		t.Fatal(err)
	}
	missing := handleBusiness(t, s, foreign.ID, "no-context", "确认Web服务")
	if missing.State != "needs_clarification" || !guidanceContains(missing.MissingInputs, "sealed_asset_target") {
		t.Fatal("business router borrowed another conversation's input", missing)
	}
}

func TestBusinessRouterDoesNotHijackQuestionsOrStoreTypedSecrets(t *testing.T) {
	s, c, _, _ := businessRouterCase(t)
	for _, text := range []string{"为什么认为危险，会影响HTTPS吗？", "取消当前任务", "查看当前任务进度", "改为北京那台ECS", "改为北京名为 web-prod 的ECS，只运行规则", "改为北京 web-prod ECS", "确认审计计划", "确认检查名为 Web 的ECS", "改成只运行规则", "确认开始审计"} {
		handled, _, err := s.HandleBusiness(context.Background(), c.ID, "other-"+text, text)
		if err != nil || handled {
			t.Fatal("non-business intent was treated as business edit", text, err)
		}
	}
	s.SensitiveValues = []string{"known-private-marker"}
	blocked := handleBusiness(t, s, c.ID, "typed-sensitive", "", ConversationBusinessRequest{Action: "edit", Patches: []storage.ConversationBusinessPatch{{AssetID: "asset-demo-a", Role: &storage.RoleChange{Role: "known-private-marker", Protocol: "unknown"}}}})
	if blocked.State != "sensitive_input_blocked" || blocked.Draft != nil {
		t.Fatal("typed secret entered business draft", blocked)
	}
}
