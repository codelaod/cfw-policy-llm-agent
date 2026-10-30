package app

import (
	"context"
	"testing"

	"cfw-policy-llm-agent/server/internal/domain"
)

func TestHistoricalModelGuidancePrefillBuildsReportAExplanationAfterCurrentTargetB(t *testing.T) {
	s, c, oldJob, old := rootWorkflowReviewReport(t)
	s.Plans.ModelReady, s.Plans.ModelHTTPAttempts = true, 2
	changed := goalTurn(t, s, c.ID, "historical-guidance-current-b", "改查北京 web-prod ECS，使用模型补充")
	if len(changed.Intent.Selection) != 1 || changed.Intent.Selection[0].Region != "cn-beijing" {
		t.Fatal("fixture did not select current B")
	}
	ctx := context.Background()
	guide, e := s.Guidance(ctx, c.ID, oldJob.ID, old.ReportRef, "report")
	if e != nil {
		t.Fatal(e)
	}
	item := guidanceItem(t, guide, "GDE-05")
	if item.Behavior != "fill_input" || item.Intent.SnapshotID != old.Audit.SnapshotID || item.Intent.ContextReportID != old.ReportRef || item.Effects.AllowCloudReads || item.Effects.AllowModelRequests {
		t.Fatal("history guidance opened a current recheck or lost A", item)
	}
	for _, producer := range []string{oldJob.ID, ""} {
		withoutConversation, e := s.Guidance(ctx, "", producer, old.ReportRef, "report")
		if e != nil {
			t.Fatal(e)
		}
		fromProducer := guidanceItem(t, withoutConversation, "GDE-05")
		if fromProducer.Behavior != "fill_input" || fromProducer.Intent.ContextReportID != old.ReportRef || fromProducer.Intent.SnapshotID != old.Audit.SnapshotID || fromProducer.Action.Target.ConversationID != c.ID {
			t.Fatal("report surface did not resolve its own producer conversation", fromProducer)
		}
	}
	next, e := s.Turn(ctx, c.ID, "historical-guidance-send", IntentTurnRequest{ClientTurnID: "historical-guidance-send", Text: item.Intent.Text, Mode: item.Intent.Mode, DataPolicy: &domain.IntentDataPolicy{Mode: item.Intent.DataPolicy, SnapshotID: item.Intent.SnapshotID}})
	if e != nil || next.Proposal == nil || next.Proposal.PlanID == "" {
		t.Fatal("protected prefill did not reach a local explanation preview", e)
	}
	p, e := s.Store.GetPlan(ctx, next.Proposal.PlanID)
	if e != nil || p.Kind != "assistant_turn" || p.ContextReportID != old.ReportRef || p.SnapshotID != old.Audit.SnapshotID || p.EffectCeiling.AllowCloudReads || p.RecheckOfJobID != "" || p.PreparedInput.Assets[0].ProviderResourceID != old.Audit.Input.Assets[0].ProviderResourceID {
		t.Fatal("historical explanation became a current B recheck", e, p)
	}
	jobs, e := s.Store.ListJobs(ctx, c.ID, 0, 100)
	if e != nil || len(jobs) != 1 || jobs[0].ID != oldJob.ID {
		t.Fatal("guidance or preview executed model/cloud work", e)
	}
}
