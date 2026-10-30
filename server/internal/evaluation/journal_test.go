package evaluation

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Simulate a process stopping after the durable start and before any request.
type startOnlyJournal struct{ base Journal }

func (j startOnlyJournal) Begin(ctx context.Context, s RunStart) (*RunRecord, bool, error) {
	_, _, e := j.base.Begin(ctx, s)
	if e != nil {
		return nil, false, e
	}
	return nil, false, ErrJournal
}
func (j startOnlyJournal) End(context.Context, RunRecord) error { return ErrJournal }

func TestJournalRestartRetainsInterruptedRunWithoutAutomaticRequest(t *testing.T) {
	f := newEvaluationFixture(t, "success", nil)
	a := f.accepted(t, FixedModel, "interrupted-run")
	f.runner.binding.Journal = startOnlyJournal{base: f.journal}
	if _, e := f.runner.Run(context.Background(), f.protocol, f.caseData, a); !errors.Is(e, ErrJournal) {
		t.Fatal(e)
	}
	if f.requests.Load() != 0 {
		t.Fatal("crash fixture already called model")
	}
	if e := f.journal.Close(); e != nil {
		t.Fatal(e)
	}
	journal, e := OpenFileJournal(f.journalPath)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = journal.Close() })
	f.runner.binding.Journal = journal
	out, e := f.runner.Run(context.Background(), f.protocol, f.caseData, a)
	if e != nil || !out.Record.Replayed || out.Record.Status != "interrupted" || out.Record.ErrorCode != "EVALUATION_RESTART_INTERRUPTED" || out.Record.Usage.Status != "unknown" || out.Record.Usage.Attempts != nil || f.requests.Load() != 0 {
		t.Fatal(out.Record, e)
	}
	if out.Record.Recovery == nil || out.Record.Recovery.OutputPresence != "unknown" || out.Record.Recovery.ModelOutputPresence != "unknown" || out.Record.Recovery.CountersStatus != "unknown" {
		t.Fatal("lost start falsely asserted no output/zero counters", out.Record)
	}
	counts := addCount(newCounts(), out.Record)
	if counts.OutputAbsent != 0 || counts.ModelOutputAbsent != 0 || counts.RuleOutputAbsent != 0 || counts.OutputPresenceUnknown != 1 || counts.ModelOutputPresenceUnknown != 1 || counts.RuleOutputPresenceUnknown != 1 || counts.LogicalCountersUnknown != 1 {
		t.Fatal(counts)
	}
	score, e := ScoreModel(out, nil)
	if e != nil || score.Status != "unknown_pending_reconciliation" {
		t.Fatal(score, e)
	}
	a.Arm = AgentSelection
	if _, e = f.runner.Run(context.Background(), f.protocol, f.caseData, a); !errors.Is(e, ErrReplay) {
		t.Fatal("changed replay conditions accepted", e)
	}
	raw, e := os.ReadFile(f.journalPath)
	if e != nil || strings.Count(string(raw), "\"type\":\"start\"") != 1 || strings.Count(string(raw), "\"type\":\"end\"") != 1 {
		t.Fatal("non append-only recovery", e)
	}
}
func journalStartFixture() RunStart {
	r := RunRecord{SchemaVersion: 1, RunID: "run-one", CaseID: "case-one", ScenarioRef: "scenario-one", JobID: "job-one", ProtocolVersion: "protocol-one", LabelVersionRef: "labels-one", DatasetUse: "development", Split: "development", ModelLayer: "not_produced", Arm: RulesOnly, Repetition: 1, Status: "running", OutputAbsent: true, ModelOutputAbsent: true, StartedAt: time.Now().UTC().Format(time.RFC3339Nano), Usage: Usage{Status: "unknown"}}
	return RunStart{Record: r, RequestDigest: strings.Repeat("a", 64)}
}
func TestJournalExclusiveIdempotentAndRejectsMalformedOrSecretData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	j, e := OpenFileJournal(path, "private-sentinel")
	if e != nil {
		t.Fatal(e)
	}
	if other, e := OpenFileJournal(path); e == nil {
		other.Close()
		t.Fatal("concurrent journal writer allowed")
	}
	start := journalStartFixture()
	if _, reserved, e := j.Begin(context.Background(), start); e != nil || !reserved {
		t.Fatal(reserved, e)
	}
	end := start.Record
	end.Status, end.FinishedAt = "failed", time.Now().UTC().Format(time.RFC3339Nano)
	end.ErrorCode = "PRIVATE_SENTINEL"
	if e = j.End(context.Background(), end); e != nil {
		t.Fatal(e)
	}
	if e = j.End(context.Background(), end); e != nil {
		t.Fatal("identical terminal replay failed", e)
	}
	if existing, reserved, e := j.Begin(context.Background(), start); e != nil || reserved || existing == nil || existing.Status != "failed" {
		t.Fatal(existing, reserved, e)
	}
	secret := start
	secret.Record.RunID = "run-secret"
	secret.Record.ScenarioRef = "private-sentinel"
	if _, _, e = j.Begin(context.Background(), secret); !errors.Is(e, ErrJournal) {
		t.Fatal("secret journal accepted", e)
	}
	_ = j.Close()
	if e = os.WriteFile(path, []byte("{\"type\":\"start\"}"), 0600); e != nil {
		t.Fatal(e)
	}
	if j, e = OpenFileJournal(path); e == nil {
		j.Close()
		t.Fatal("incomplete JSONL accepted")
	}
	if e = os.WriteFile(path, []byte("{\"type\":\"nonsense\"}\n"), 0600); e != nil {
		t.Fatal(e)
	}
	if j, e = OpenFileJournal(path); e == nil {
		j.Close()
		t.Fatal("unknown entry accepted")
	}
}

type endFailedJournal struct{ base Journal }

func (j endFailedJournal) Begin(ctx context.Context, s RunStart) (*RunRecord, bool, error) {
	return j.base.Begin(ctx, s)
}
func (j endFailedJournal) End(context.Context, RunRecord) error { return ErrJournal }
func TestSerialRunnerStopsAfterJournalEndFailure(t *testing.T) {
	f := newEvaluationFixture(t, "success", nil)
	assignments := []Assignment{f.accepted(t, FixedModel, "run-first"), f.accepted(t, AgentSelection, "run-second")}
	f.runner.binding.Journal = endFailedJournal{base: f.journal}
	results, e := f.runner.RunSerial(context.Background(), f.protocol, []FrozenCase{f.caseData}, assignments)
	if !errors.Is(e, ErrJournal) || len(results) != 1 || results[0].Record.Status != "completed" || f.requests.Load() != 1 {
		t.Fatal(results, e, f.requests.Load())
	}
}

func TestCancellationAndDeadlinePreserveActualAttemptsAndRuleOutput(t *testing.T) {
	for _, kind := range []string{"cancel", "deadline"} {
		t.Run(kind, func(t *testing.T) {
			f := newEvaluationFixture(t, "cancel", nil)
			a := f.accepted(t, FixedModel, "stop-run")
			ctx, cancel := context.WithCancel(context.Background())
			if kind == "deadline" {
				cancel()
				ctx, cancel = context.WithTimeout(context.Background(), 300*time.Millisecond)
			}
			defer cancel()
			type result struct {
				out RunOutcome
				err error
			}
			done := make(chan result, 1)
			go func() { out, e := f.runner.Run(ctx, f.protocol, f.caseData, a); done <- result{out, e} }()
			until := time.Now().Add(3 * time.Second)
			for f.requests.Load() == 0 && time.Now().Before(until) {
				time.Sleep(5 * time.Millisecond)
			}
			if f.requests.Load() != 1 {
				t.Fatal("model request never began")
			}
			if kind == "cancel" {
				cancel()
			}
			select {
			case got := <-done:
				want := "cancelled"
				if kind == "deadline" {
					want = "partial"
				}
				if got.err != nil || got.out.Record.Status != want || got.out.Record.RuleAuditRef == "" || got.out.Record.ReportRef != "" || !got.out.Record.ModelOutputAbsent || got.out.Record.Usage.Attempts == nil || got.out.Record.Usage.Attempts.ModelAttempts != 1 {
					t.Fatal(got.out.Record, got.err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("cancel did not terminate")
			}
		})
	}
}
