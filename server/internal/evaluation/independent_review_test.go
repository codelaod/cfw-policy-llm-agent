package evaluation

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func TestIndependentReviewCrashAfterCommittedOutputMustNotClaimAbsent(t *testing.T) {
	f := newEvaluationFixture(t, "success", nil)
	a := f.accepted(t, FixedModel, "independent-post-output-crash")
	f.runner.binding.Journal = endFailedJournal{base: f.journal}
	first, err := f.runner.Run(context.Background(), f.protocol, f.caseData, a)
	if !errors.Is(err, ErrJournal) || first.Record.ReportRef == "" || first.Record.ModelOutputAbsent {
		t.Fatal("fixture did not commit actual output before journal failure", err, first.Record)
	}
	view, err := f.store.ReadJobWorkspace(context.Background(), a.JobID)
	if err != nil || !view.ReportPublished || view.Audit == nil || view.AgentResult == nil {
		t.Fatal("fixture actual saved products unavailable", err)
	}
	before := f.requests.Load()
	if err = f.journal.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenFileJournal(f.journalPath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	f.runner.binding.Journal = reopened
	restored, err := f.runner.Run(context.Background(), f.protocol, f.caseData, a)
	if err != nil || !restored.Record.Replayed || f.requests.Load() != before {
		t.Fatal("recovery failed or replayed provider", err, restored.Record)
	}
	if restored.Record.OutputAbsent || restored.Record.ModelOutputAbsent || restored.Record.RuleAuditRef == "" || restored.Record.ModelOutputRef == "" || restored.Record.ReportRef == "" {
		t.Fatalf("committed outputs lost from recovery accounting: store_job=%s store_report=%s journal=%+v", view.Job.Status, view.ReportRef, restored.Record)
	}
	if restored.Record.ModelCalls != first.Record.ModelCalls || restored.Record.ToolCalls != first.Record.ToolCalls || restored.Record.Recovery == nil || restored.Record.Recovery.CountersStatus != "agent_recorded" || restored.Record.Recovery.TraceStatus != "unknown" || restored.Record.Usage.Attempts == nil || restored.Record.Usage.Attempts.ModelAttempts != int64(before) {
		t.Fatal("saved counters were lost or absent traces were invented", restored.Record)
	}
	raw, err := os.ReadFile(f.journalPath)
	if err != nil || strings.Count(string(raw), `"type":"end"`) != 1 || strings.Count(string(raw), `"type":"reconcile"`) != 1 {
		t.Fatal("recovery overwrote interruption instead of retaining a local reconciliation", err)
	}
	if err = reopened.Close(); err != nil {
		t.Fatal(err)
	}
	again, err := OpenFileJournal(f.journalPath)
	if err != nil {
		t.Fatal("reconciled journal could not reopen", err)
	}
	defer again.Close()
	f.runner.binding.Journal, f.runner.binding.Factory = again, nil
	replayed, err := f.runner.Run(context.Background(), f.protocol, f.caseData, a)
	if err != nil || !replayed.Record.Replayed || replayed.Record.ReportRef != restored.Record.ReportRef || replayed.Record.ModelCalls != restored.Record.ModelCalls || f.requests.Load() != before {
		t.Fatal("reconciled metadata was not stable offline across another restart", err, replayed.Record)
	}
}

func TestIndependentReviewSameAcceptedJobCannotRunTwiceWithDifferentRunIDs(t *testing.T) {
	f := newEvaluationFixture(t, "cancel", nil)
	a := f.accepted(t, FixedModel, "independent-one-job-first-run")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type result struct {
		out RunOutcome
		err error
	}
	first := make(chan result, 1)
	go func() { out, e := f.runner.Run(ctx, f.protocol, f.caseData, a); first <- result{out, e} }()
	deadline := time.Now().Add(3 * time.Second)
	for f.requests.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if f.requests.Load() != 1 {
		t.Fatal("fixture first request did not begin")
	}
	second := make(chan result, 1)
	duplicate := a
	duplicate.RunID = "independent-one-job-second-run"
	go func() { out, e := f.runner.Run(ctx, f.protocol, f.caseData, duplicate); second <- result{out, e} }()
	deadline = time.Now().Add(3 * time.Second)
	secondEnded := false
	for f.requests.Load() < 2 && time.Now().Before(deadline) {
		select {
		case <-second:
			secondEnded = true
		default:
		}
		if secondEnded {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	actual := f.requests.Load()
	cancel()
	select {
	case <-first:
	case <-time.After(5 * time.Second):
		t.Fatal("first did not stop")
	}
	if !secondEnded {
		select {
		case <-second:
		case <-time.After(5 * time.Second):
			t.Fatal("second did not stop")
		}
	}
	if actual != 1 {
		t.Fatalf("one accepted running job spent duplicate model requests under different journal IDs: HTTP=%d", actual)
	}
}
