package app

import (
	"context"
	"errors"
	"testing"

	"cfw-policy-llm-agent/server/internal/domain"
)

func TestNaturalConfirmationRequiresReviewedVersionAndUnchangedEffects(t *testing.T) {
	for _, kind := range []string{"missing_revision", "changed_mode", "changed_data", "mixed_target", "stopped"} {
		t.Run(kind, func(t *testing.T) {
			s, c := conversationalCase(t)
			s.Engine = New(s.Store, nil)
			t.Cleanup(s.Engine.Stop)
			offer := goalTurn(t, s, c.ID, "guard-offer", "检查杭州 web-prod ECS，只运行规则")
			rev := offer.Intent.Revision
			req := IntentTurnRequest{ClientTurnID: "guard-confirm", Text: "确认按当前计划开始审计", ExpectedRevision: &rev}
			switch kind {
			case "missing_revision":
				req.ExpectedRevision = nil
			case "changed_mode":
				req.Mode = "rules_and_agent"
			case "changed_data":
				req.DataPolicy = &domain.IntentDataPolicy{Mode: "force_refresh"}
			case "mixed_target":
				req.Text = "开始吧，同时改查北京 web-prod ECS"
			case "stopped":
				s.Engine.Stop()
			}
			result, e := s.Turn(context.Background(), c.ID, "guard-confirm", req)
			if kind == "stopped" {
				if !errors.Is(e, ErrStopping) {
					t.Fatal("stopped engine accepted work", e)
				}
			} else if e != nil || result.Job != nil {
				t.Fatal("unreviewed variant executed", e)
			}
			jobs, e := s.Store.ListJobs(context.Background(), c.ID, 0, 100)
			if e != nil || len(jobs) != 0 {
				t.Fatal("unreviewed variant left a hidden job", e)
			}
		})
	}
}

func TestQueuedBatchCancellationClosesAllUnstartedSegmentsAndGuidance(t *testing.T) {
	s, c, _ := selectedGroupDialog(t, 5)
	s.Plans.Workloads = batchDialogProfile(t, 20)
	offer := goalTurn(t, s, c.ID, "queued-batch-offer", "按完整对象集分批审计")
	ctx := context.Background()
	accepted, e := s.Store.AcceptIntentProposal(ctx, c.ID, offer.Proposal.ID, "queued-batch-accept", s.Plans.ConfigRevision, offer.Intent.Revision)
	if e != nil || accepted.Job.Status != "queued" {
		t.Fatal(e)
	}
	job, e := s.Store.Cancel(ctx, accepted.Job.ID)
	if e != nil || job.Status != "cancelled" {
		t.Fatal(e)
	}
	view, e := s.Store.ReadJobWorkspace(ctx, job.ID)
	if e != nil || view.Batch == nil || view.Batch.Status != "cancelled" || len(view.Batch.Unprocessed) != 5 || len(view.Batch.Covered) != 0 || len(view.Batch.Failed) != 0 {
		t.Fatal("cancelled parent retained accepted/pending batch", e, view.Batch)
	}
	for _, seg := range view.Batch.Segments {
		if seg.Status != "unprocessed" || seg.ChildJobID != "" || seg.Manifest != nil {
			t.Fatal("unstarted segment acquired execution evidence", seg)
		}
	}
	guidance, e := s.Guidance(ctx, c.ID, job.ID, "", "audit")
	if e != nil || len(guidance.Items) != 1 || guidance.Items[0].ID != "GDE-BATCH-RESULTS" || guidance.Items[0].State != "available" || guidance.Items[0].Action.Target.ID != job.ID {
		t.Fatal("batch guidance invented a parent report or lost segment navigation", e, guidance)
	}
	if _, found, e := s.Store.Claim(ctx); e != nil || found {
		t.Fatal("cancelled queued batch could start", e)
	}
}
