package app

import (
	"context"
	"encoding/json"
	"testing"

	"cfw-policy-llm-agent/server/internal/recheck"
)

func TestIndependentNaturalRecheckCanExplicitlyReuseOriginalEvaluationInstant(t *testing.T) {
	s, cloud, c, physical := preparationIntegration(t)
	oldJob, _ := executeGoal(t, s, cloud, c, "independent-eval-original", "审计杭州名为 new-web 的ECS入向，只运行规则")
	old, err := s.Store.ReadJobWorkspace(context.Background(), oldJob.ID)
	if err != nil || old.Audit == nil || old.ReportRef == "" {
		t.Fatal(err)
	}
	text := "我已手动改完，重新采集复审，沿用原评估时点，只运行规则"
	offered, err := s.Turn(context.Background(), c.ID, "independent-eval-recheck", IntentTurnRequest{ClientTurnID: "independent-eval-recheck", Text: text, ContextReportID: old.ReportRef})
	if err != nil || offered.Proposal == nil || offered.Proposal.PlanID == "" {
		t.Fatal("fixture did not prepare recheck", err, offered.Intent)
	}
	p, err := s.Store.GetPlan(context.Background(), offered.Proposal.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	if p.RecheckOfJobID != oldJob.ID || p.EvaluationAt != old.Audit.Input.EvaluationAt {
		t.Fatalf("explicit original-evaluation recheck silently uses a new analysis instant and can never be comparable: original=%s plan=%q linked=%s want=%s", old.Audit.Input.EvaluationAt, p.EvaluationAt, p.RecheckOfJobID, oldJob.ID)
	}
	before := physical.Load()
	accepted, err := s.Store.AcceptIntentProposal(context.Background(), c.ID, offered.Proposal.ID, "independent-eval-accept", s.Plans.ConfigRevision, offered.Intent.Revision)
	if err != nil {
		t.Fatal(err)
	}
	job, found, err := s.Store.Claim(context.Background())
	if err != nil || !found || job.ID != accepted.Job.ID {
		t.Fatal(err)
	}
	workflow := NewWorkflow(s.Store)
	workflow.Cloud = cloud
	if err = workflow.Run(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	view, err := s.Store.ReadJobWorkspace(context.Background(), job.ID)
	if err != nil || view.Audit == nil || view.Recheck == nil || physical.Load() <= before || view.Audit.Input.EvaluationAt != old.Audit.Input.EvaluationAt || view.Audit.Input.CollectedAt == old.Audit.Input.CollectedAt {
		t.Fatal("new collection did not preserve the separately frozen evaluation instant", err, view.Job)
	}
	var comparison recheck.Comparison
	if err = json.Unmarshal(view.Recheck.Comparison, &comparison); err != nil {
		t.Fatal(err)
	}
	for _, reason := range comparison.Reasons {
		if reason == "evaluation_time_changed" {
			t.Fatal("the accepted original instant changed during late binding")
		}
	}
}
