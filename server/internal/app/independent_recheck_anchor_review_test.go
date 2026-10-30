package app

import (
	"context"
	"testing"
)

func TestIndependentRecheckTimeClarificationKeepsExplicitOriginalReportAnchor(t *testing.T) {
	s, cloud, c, _ := preparationIntegration(t)
	a, _ := executeGoal(t, s, cloud, c, "anchor-a", "审计杭州名为 new-web 的ECS入向，只运行规则")
	av, err := s.Store.ReadJobWorkspace(context.Background(), a.ID)
	if err != nil || av.ReportRef == "" {
		t.Fatal(err)
	}
	b, _ := executeGoal(t, s, cloud, c, "anchor-b", "强制刷新杭州名为 new-web 的ECS入向，只运行规则")
	if a.ID == b.ID {
		t.Fatal("fixture did not publish a second report")
	}
	conflict, err := s.Turn(context.Background(), c.ID, "anchor-time-conflict", IntentTurnRequest{ClientTurnID: "anchor-time-conflict", Text: "我已改完，重新采集复审，使用当前评估时点，并沿用原评估时点，只运行规则", ContextReportID: av.ReportRef})
	if err != nil {
		t.Fatal(err)
	}
	if conflict.Intent.Status != "needs_clarification" || conflict.Intent.RecheckOfJobID != a.ID {
		t.Fatal("fixture did not preserve explicit A during clarification", conflict.Intent)
	}
	answer, err := s.Turn(context.Background(), c.ID, "anchor-time-answer", IntentTurnRequest{ClientTurnID: "anchor-time-answer", Text: "沿用原评估时点，只运行规则"})
	if err != nil {
		t.Fatal(err)
	}
	if answer.Intent.RecheckOfJobID != a.ID {
		t.Fatalf("time clarification switched explicit report A to latest report B: original_A=%s latest_B=%s chosen=%s status=%s", a.ID, b.ID, answer.Intent.RecheckOfJobID, answer.Intent.Status)
	}
}
