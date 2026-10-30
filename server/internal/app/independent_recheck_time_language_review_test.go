package app

import (
	"context"
	"testing"
)

func TestIndependentRecheckEvaluationNegationDoesNotInvertRequestedInstant(t *testing.T) {
	s, cloud, c, _ := preparationIntegration(t)
	oldJob, _ := executeGoal(t, s, cloud, c, "time-neg-original", "审计杭州名为 new-web 的ECS入向，只运行规则")
	old, err := s.Store.ReadJobWorkspace(context.Background(), oldJob.ID)
	if err != nil || old.Audit == nil {
		t.Fatal(err)
	}
	out, err := s.Turn(context.Background(), c.ID, "time-neg-recheck", IntentTurnRequest{ClientTurnID: "time-neg-recheck", Text: "我已手动改完，重新采集复审，不要用当前评估时点，沿用原评估时点，只运行规则", ContextReportID: old.ReportRef})
	if err != nil {
		t.Fatal(err)
	}
	if out.Proposal == nil || out.Proposal.PlanID == "" {
		return
	}
	p, err := s.Store.GetPlan(context.Background(), out.Proposal.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	if p.EvaluationAt != old.Audit.Input.EvaluationAt {
		t.Fatalf("negated current instant became a current-instant plan: original=%s actual=%s", old.Audit.Input.EvaluationAt, p.EvaluationAt)
	}
}
func TestIndependentConflictingEvaluationInstantsRequireClarification(t *testing.T) {
	s, cloud, c, _ := preparationIntegration(t)
	oldJob, _ := executeGoal(t, s, cloud, c, "time-conflict-original", "审计杭州名为 new-web 的ECS入向，只运行规则")
	old, err := s.Store.ReadJobWorkspace(context.Background(), oldJob.ID)
	if err != nil || old.Audit == nil {
		t.Fatal(err)
	}
	out, err := s.Turn(context.Background(), c.ID, "time-conflict-recheck", IntentTurnRequest{ClientTurnID: "time-conflict-recheck", Text: "我已手动改完，重新采集复审，使用当前评估时点，并沿用原评估时点，只运行规则", ContextReportID: old.ReportRef})
	if err != nil {
		t.Fatal(err)
	}
	if out.Proposal != nil && out.Proposal.PlanID != "" {
		t.Fatal("mutually exclusive requested instants produced an executable plan", out.Proposal)
	}
}
