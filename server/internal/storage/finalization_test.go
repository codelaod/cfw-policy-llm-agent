package storage

import (
	"context"
	"errors"
	"testing"
	"time"

	"cfw-policy-llm-agent/server/internal/audit"
)

func preparedFinalization(t *testing.T) (*Store, Job, AuditRecord, FinalizeJobReportRequest) {
	t.Helper()
	ctx := context.Background()
	store, _, _, initial := executionCase(t)
	plan, err := store.SavePlan(ctx, initial)
	if err != nil {
		t.Fatal(err)
	}
	job, _, err := store.EnqueuePlan(ctx, plan.ID, "atomic-finalization")
	if err != nil {
		t.Fatal(err)
	}
	claimed, found, err := store.Claim(ctx)
	if err != nil || !found || claimed.ID != job.ID {
		t.Fatal("job not claimed", err)
	}
	input := plan.PreparedInput
	input.EvaluationAt = time.Now().UTC().Format(time.RFC3339Nano)
	sealed, err := store.SealInput(ctx, job.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	result, err := audit.Run(ctx, sealed)
	if err != nil {
		t.Fatal(err)
	}
	record, err := store.SaveJobAudit(ctx, job.ID, result)
	if err != nil {
		t.Fatal(err)
	}
	_, heads, err := store.CaptureReportInput(ctx, record.ID)
	if err != nil {
		t.Fatal(err)
	}
	return store, claimed, record, FinalizeJobReportRequest{Format: "json", Heads: heads, Content: []byte(`{"source":"synthetic","fixed":true}`), Key: "atomic-fixed-report", Status: "completed"}
}

func assertNoFinalArtifacts(t *testing.T, store *Store, job Job, auditID, status string) {
	t.Helper()
	ctx := context.Background()
	current, record, err := store.ReadJobAudit(ctx, job.ID)
	if err != nil || record == nil || record.ID != auditID || current.Status != status {
		t.Fatal("finalization changed L2 or partially changed status", err)
	}
	reports, err := store.ListReports(ctx, auditID, 100)
	if err != nil || len(reports) != 0 {
		t.Fatal("failed finalization persisted an unlinked report", err)
	}
	messages, err := store.ConversationMessages(ctx, job.ConversationID, 0, 100)
	if err != nil || len(messages.Items) != 1 {
		t.Fatal("failed finalization persisted a normal completion reply", err)
	}
	events, _, err := store.Page(ctx, job.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events.Events {
		if event.Type == "report_available" || event.Type == "message_available" || event.Type == "job_terminal" {
			t.Fatal("failed finalization left a visible final event")
		}
	}
}

func TestFinalizationCancellationWinsBeforeReportTransaction(t *testing.T) {
	ctx := context.Background()
	store, job, record, request := preparedFinalization(t)
	if _, err := store.Cancel(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	returned, err := store.FinalizeJobWithReport(ctx, job.ID, request)
	if !errors.Is(err, ErrJobTerminal) || returned.ID != "" {
		t.Fatal("late finalizer bypassed cancellation", err)
	}
	assertNoFinalArtifacts(t, store, job, record.ID, "cancelling")
	if err = store.FinishWithCode(ctx, job.ID, "partial", "FINALIZATION_SAVE_FAILED"); err != nil {
		t.Fatal(err)
	}
	current, err := store.Get(ctx, job.ID)
	if err != nil || current.Status != "cancelled" {
		t.Fatal("late callback overwrote cancelled state", err)
	}
}

func TestFinalizationFailuresRollbackAllArtifactsAndKeepRules(t *testing.T) {
	for _, stage := range []string{"report", "message", "terminal"} {
		t.Run(stage, func(t *testing.T) {
			store, job, record, request := preparedFinalization(t)
			trigger := map[string]string{
				"report":   `CREATE TRIGGER reject_finalization BEFORE INSERT ON e0_events WHEN NEW.type='report_available' BEGIN SELECT RAISE(ABORT,'synthetic report event failure'); END`,
				"message":  `CREATE TRIGGER reject_finalization BEFORE INSERT ON e0_events WHEN NEW.type='message_available' BEGIN SELECT RAISE(ABORT,'synthetic message event failure'); END`,
				"terminal": `CREATE TRIGGER reject_finalization BEFORE INSERT ON e0_events WHEN NEW.type='job_terminal' BEGIN SELECT RAISE(ABORT,'synthetic terminal event failure'); END`,
			}[stage]
			if err := store.db.Exec(trigger).Error; err != nil {
				t.Fatal(err)
			}
			returned, err := store.FinalizeJobWithReport(context.Background(), job.ID, request)
			if err == nil || returned.ID != "" {
				t.Fatal("failed finalization reported saved artifacts")
			}
			assertNoFinalArtifacts(t, store, job, record.ID, "running")
			if err = store.db.Exec("DROP TRIGGER reject_finalization").Error; err != nil {
				t.Fatal(err)
			}
			if _, err = store.FinalizeJobWithReport(context.Background(), job.ID, request); err != nil {
				t.Fatal("rollback could not be retried", err)
			}
		})
	}
}

func TestFinalizationReportMessageAndTerminalBecomeVisibleTogether(t *testing.T) {
	ctx := context.Background()
	store, job, record, request := preparedFinalization(t)
	fixed, err := store.FinalizeJobWithReport(ctx, job.ID, request)
	if err != nil || fixed.ID == "" || fixed.AuditID != record.ID {
		t.Fatal("finalization failed", err)
	}
	current, err := store.Get(ctx, job.ID)
	if err != nil || current.Status != "completed" {
		t.Fatal("report became visible before terminal", err)
	}
	page, _, err := store.Page(ctx, job.ID, 0, 100)
	if err != nil || len(page.Events) < 3 {
		t.Fatal(err)
	}
	last := page.Events[len(page.Events)-3:]
	for n, kind := range []string{"report_available", "message_available", "job_terminal"} {
		if last[n].Type != kind || n > 0 && last[n].Seq != last[n-1].Seq+1 {
			t.Fatal("final event order not preserved")
		}
	}
	seq := current.ViewSeq
	if err = store.FinishWithCode(ctx, job.ID, "failed", "LATE_CALLBACK"); err != nil {
		t.Fatal(err)
	}
	if current, err = store.Get(ctx, job.ID); err != nil || current.Status != "completed" || current.ViewSeq != seq {
		t.Fatal("engine terminal replay changed fixed finalization", err)
	}
	if _, err = store.FinalizeJobWithReport(ctx, job.ID, request); !errors.Is(err, ErrJobTerminal) {
		t.Fatal("terminal finalizer appended another report", err)
	}
}
