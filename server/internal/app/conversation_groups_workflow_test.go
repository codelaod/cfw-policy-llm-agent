package app

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"cfw-policy-llm-agent/server/internal/audit"
	"cfw-policy-llm-agent/server/internal/domain"
	"cfw-policy-llm-agent/server/internal/storage"
)

func selectedGroupDialog(t *testing.T, n int) (*IntentService, storage.Conversation, storage.IntentTurnResult) {
	t.Helper()
	s, c := conversationalCase(t)
	in, e := audit.SyntheticExample("internet-in")
	if e != nil {
		t.Fatal(e)
	}
	base := in.Assets[0]
	base.Services = []domain.Service{}
	base.Allowed = []domain.Access{}
	base.AllowedConfirmed = false
	base.AllowedComplete = false
	in.Assets = []domain.Asset{}
	in.Selection.AssetIDs = []string{}
	in.SnapshotVersion = "public-group-dialog"
	text := "检查杭州 ECS "
	for i := 0; i < n; i++ {
		a := base
		a.ID = fmt.Sprintf("asset-group-%02d", i)
		a.ProviderResourceID = fmt.Sprintf("i-group-%02d", i)
		a.Name = fmt.Sprintf("node-%02d", i)
		a.Region = "cn-hangzhou"
		a.ResourceType = "ecs"
		a.Address = fmt.Sprintf("192.0.2.%d", i+1)
		in.Assets = append(in.Assets, a)
		in.Selection.AssetIDs = append(in.Selection.AssetIDs, a.ID)
		text += a.ProviderResourceID + " "
	}
	if _, e = s.Store.SaveSnapshot(context.Background(), in); e != nil {
		t.Fatal(e)
	}
	selected := goalTurn(t, s, c.ID, "group-selected", text)
	if len(selected.Intent.Selection) != n {
		t.Fatal("explicit full set not retained", selected.Intent)
	}
	return s, c, selected
}
func confirmDialogGroup(t *testing.T, s *IntentService, c storage.Conversation) storage.ResourceGroupVersion {
	t.Helper()
	preview := goalTurn(t, s, c.ID, "group-preview", "这些对象归属客户甲，授权来源工单SEC-42业务负责人核对")
	if preview.Group == nil || preview.Group.Preview == nil {
		t.Fatal("no membership preview", preview.Group)
	}
	p := preview.Group.Preview
	confirm, e := s.Turn(context.Background(), c.ID, "group-confirm", IntentTurnRequest{ClientTurnID: "group-confirm", Group: &storage.ResourceGroupRequest{Action: "confirm", PreviewID: p.ID, ExpectedRevision: p.IntentRevision}})
	if e != nil || confirm.Group == nil || confirm.Group.Version == nil {
		t.Fatal("typed group confirmation failed", e, confirm.Group)
	}
	pending, e := s.Store.PendingResourceGroupPreview(context.Background(), c.ID)
	if e != nil || pending != nil {
		t.Fatal("confirmed preview remained executable", pending, e)
	}
	return *confirm.Group.Version
}
func TestTwentyCustomerObjectsComeFromConfirmedDialogueAndRunWholeSet(t *testing.T) {
	s, c, selected := selectedGroupDialog(t, 20)
	v := confirmDialogGroup(t, s, c)
	if len(v.Members) != 20 {
		t.Fatal("membership truncated")
	}
	// Confirmation itself is local; an old explicit-object preview is preserved.
	current, e := s.Current(context.Background(), c.ID)
	if e != nil || current.Revision != selected.Intent.Revision {
		t.Fatal("group confirmation replaced goal", e)
	}
	jobs, e := s.Store.ListJobs(context.Background(), c.ID, 0, 100)
	if e != nil || len(jobs) != 0 {
		t.Fatal("group confirmation executed work", e)
	}
	other, e := s.Store.CreateConversation(context.Background(), "审计客户组", "")
	if e != nil {
		t.Fatal(e)
	}
	goal := goalTurn(t, s, other.ID, "customer-audit", "审计客户甲的全部ECS入向")
	if goal.Proposal == nil || goal.Proposal.PlanID == "" || len(goal.Intent.Selection) != 20 || goal.Intent.ResourceGroupVersionID != v.ID {
		t.Fatal("confirmed group did not resolve whole set", goal.Intent)
	}
	p, e := s.Store.GetPlan(context.Background(), goal.Proposal.PlanID)
	if e != nil || p.ResourceGroupVersionID != v.ID || len(p.PreparedInput.Assets) != 20 {
		t.Fatal("plan lost group version or members", e)
	}
	_, e = s.Store.AcceptIntentProposal(context.Background(), other.ID, goal.Proposal.ID, "customer-accept", s.Plans.ConfigRevision, goal.Intent.Revision)
	if e != nil {
		t.Fatal(e)
	}
	job, found, e := s.Store.Claim(context.Background())
	if e != nil || !found {
		t.Fatal(e)
	}
	if e = NewWorkflow(s.Store).Run(context.Background(), job); e != nil {
		t.Fatal(e)
	}
	view, e := s.Store.ReadJobWorkspace(context.Background(), job.ID)
	if e != nil || view.Audit == nil || len(view.Audit.Input.Assets) != 20 || view.Job.Status != "completed" {
		t.Fatal("whole finite group not audited", e)
	}
}
func TestCustomerMembershipChangeInvalidatesUnacceptedPlanWithoutChangingOldVersion(t *testing.T) {
	s, c, _ := selectedGroupDialog(t, 20)
	v := confirmDialogGroup(t, s, c)
	other, e := s.Store.CreateConversation(context.Background(), "组版本核对", "")
	if e != nil {
		t.Fatal(e)
	}
	goal := goalTurn(t, s, other.ID, "old-group", "审计客户甲的全部ECS入向")
	if goal.Proposal == nil {
		t.Fatal(goal.Intent)
	}
	source, e := s.Current(context.Background(), c.ID)
	if e != nil {
		t.Fatal(e)
	}
	preview, e := s.Store.PreviewResourceGroup(context.Background(), c.ID, "客户甲", []string{v.Members[0].ResourceRef}, "工单SEC-43确认成员变化", source.Revision, "group-replace")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Store.ConfirmResourceGroup(context.Background(), c.ID, preview.ID, source.Revision, "group-replace-confirm"); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Store.AcceptIntentProposal(context.Background(), other.ID, goal.Proposal.ID, "stale-group-accept", s.Plans.ConfigRevision, goal.Intent.Revision); !errors.Is(e, storage.ErrPlanStale) {
		t.Fatal("changed group accepted old membership plan", e)
	}
	current, e := s.Current(context.Background(), other.ID)
	if e != nil || current.Proposal.Status != "stale" {
		t.Fatal("UI current proposal still executable after group update", e, current.Proposal)
	}
	old, e := s.Store.ResourceGroupVersion(context.Background(), v.ID)
	if e != nil || len(old.Members) != 20 {
		t.Fatal("old membership rewritten", e)
	}
}
