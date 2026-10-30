package app

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"cfw-policy-llm-agent/server/internal/audit"
	"cfw-policy-llm-agent/server/internal/domain"
	"cfw-policy-llm-agent/server/internal/storage"
)

func conversationalCase(t *testing.T) (*IntentService, storage.Conversation) {
	t.Helper()
	st, e := storage.Open(filepath.Join(t.TempDir(), "goals.sqlite"), true)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { st.Close() })
	for _, region := range []string{"cn-hangzhou", "cn-beijing"} {
		in, _ := audit.SyntheticExample("internet-in")
		in.Assets[0].Name = "web-prod"
		in.Assets[0].Region = region
		in.Assets[0].ResourceType = "ecs"
		in.Assets[0].ProviderResourceID = "i-web-" + region
		in.SnapshotVersion = region
		if _, e = st.SaveSnapshot(context.Background(), in); e != nil {
			t.Fatal(e)
		}
	}
	c, e := st.CreateConversation(context.Background(), "审计目标", "")
	if e != nil {
		t.Fatal(e)
	}
	return &IntentService{Store: st, Plans: NewPlanService(st)}, c
}
func goalTurn(t *testing.T, s *IntentService, c, key, text string) storage.IntentTurnResult {
	t.Helper()
	r, e := s.Turn(context.Background(), c, key, IntentTurnRequest{ClientTurnID: key, Text: text})
	if e != nil {
		t.Fatal(e)
	}
	return r
}
func TestConversationFromBlankToDisambiguationAcceptedRulesReport(t *testing.T) {
	s, c := conversationalCase(t)
	ctx := context.Background()
	first := goalTurn(t, s, c.ID, "first", "检查 web-prod 的入向策略")
	if first.Intent.Status != "needs_clarification" || len(first.Intent.Candidates) != 2 || first.Proposal != nil {
		t.Fatal(first.Intent)
	}
	selected := first.Intent.Candidates[0]
	for _, r := range first.Intent.Candidates {
		if r.Region == "cn-hangzhou" {
			selected = r
		}
	}
	chosen, e := s.Choose(ctx, c.ID, "choice", IntentChoiceRequest{IntentID: first.Intent.ID, ExpectedRevision: first.Intent.Revision, CandidateIDs: []string{selected.ID}})
	if e != nil || chosen.Proposal == nil || chosen.Proposal.PlanID == "" || chosen.Intent.Selection[0].Region != "cn-hangzhou" {
		t.Fatal(chosen, e)
	}
	accepted, e := s.Store.AcceptIntentProposal(ctx, c.ID, chosen.Proposal.ID, "accept", s.Plans.ConfigRevision, chosen.Intent.Revision)
	if e != nil {
		t.Fatal(e)
	}
	again, e := s.Store.AcceptIntentProposal(ctx, c.ID, chosen.Proposal.ID, "accept", s.Plans.ConfigRevision, chosen.Intent.Revision)
	if e != nil || !again.Replayed || again.Job.ID != accepted.Job.ID {
		t.Fatal(again, e)
	}
	if _, e = s.Store.AcceptIntentProposal(ctx, c.ID, chosen.Proposal.ID, "accept", s.Plans.ConfigRevision, 999); !errors.Is(e, storage.ErrConflict) {
		t.Fatal("same acceptance key with altered revision accepted", e)
	}
	job, found, e := s.Store.Claim(ctx)
	if e != nil || !found {
		t.Fatal(job, e)
	}
	if e = NewWorkflow(s.Store).Run(ctx, job); e != nil {
		t.Fatal(e)
	}
	saved, run, e := s.Store.ReadJobAudit(ctx, job.ID)
	if e != nil || run == nil || saved.Status != "completed" || len(run.Findings) == 0 || run.Input.Assets[0].Region != "cn-hangzhou" {
		t.Fatal(saved, run, e)
	}
}
func TestConversationGoalRevisionInvalidatesAllOldPlanRoutes(t *testing.T) {
	s, c := conversationalCase(t)
	ctx := context.Background()
	a := goalTurn(t, s, c.ID, "a", "检查杭州 web-prod ECS")
	if a.Proposal == nil || a.Proposal.PlanID == "" {
		t.Fatal(a)
	}
	status := goalTurn(t, s, c.ID, "status", "查看任务进度")
	if status.Intent.Revision != a.Intent.Revision || status.Intent.Proposal.PlanID != a.Proposal.PlanID {
		t.Fatal("local status revoked goal", status)
	}
	b := goalTurn(t, s, c.ID, "b", "忽略刚才杭州，改查北京那台 ECS 出向，只做规则检查")
	if len(b.Intent.Selection) != 1 || b.Intent.Selection[0].Region != "cn-beijing" || b.Intent.Direction != "out" || b.Intent.Mode != "rules_only" {
		t.Fatal("new region inherited old selected target", b.Intent)
	}
	if _, _, e := s.Store.EnqueuePlan(ctx, a.Proposal.PlanID, "old-route", s.Plans.ConfigRevision); !errors.Is(e, storage.ErrPlanStale) {
		t.Fatal("old plan still consumable", e)
	}
	if _, e := s.Store.AcceptIntentProposal(ctx, c.ID, a.Proposal.ID, "old-proposal", s.Plans.ConfigRevision, a.Intent.Revision); !errors.Is(e, storage.ErrPlanStale) {
		t.Fatal(e)
	}
}
func TestConversationUnknownCustomerSecretAndPinnedDoNotInventScope(t *testing.T) {
	s, c := conversationalCase(t)
	a := goalTurn(t, s, c.ID, "a", "检查杭州 web-prod ECS")
	b := goalTurn(t, s, c.ID, "b", "检查客户乙的机器")
	if b.Intent.Status != "needs_clarification" || len(b.Intent.Selection) != 0 {
		t.Fatal(b.Intent)
	}
	_ = a
	secret := "sk-ARTIFICIAL_INTENT_12345678"
	r := goalTurn(t, s, c.ID, "secret", "密钥="+secret)
	if r.Message != nil || r.Intent.Revision != b.Intent.Revision {
		t.Fatal(r)
	}
	_, e := s.Turn(context.Background(), c.ID, "bad-pinned", IntentTurnRequest{ClientTurnID: "bad-pinned", Text: "检查杭州 web-prod", DataPolicy: &domain.IntentDataPolicy{Mode: "pinned_snapshot"}})
	if !errors.Is(e, storage.ErrInputInvalid) {
		t.Fatal("pinned without explicit version accepted", e)
	}
}
