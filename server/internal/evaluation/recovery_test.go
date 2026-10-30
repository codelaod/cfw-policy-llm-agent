package evaluation

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func reopenForRecovery(t *testing.T, f *evaluationFixture) {
	t.Helper()
	if e := f.journal.Close(); e != nil {
		t.Fatal(e)
	}
	j, e := OpenFileJournal(f.journalPath)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = j.Close() })
	f.journal = j
	f.runner.binding.Journal = j
}
func TestRecoveryReconcilesCommittedProductsCountsAndKeepsJournalFailure(t *testing.T) {
	f := newEvaluationFixture(t, "success", nil)
	ctx := context.Background()
	var records []RunRecord
	for i, arm := range []Arm{RulesOnly, AgentSelection} {
		a := f.accepted(t, arm, []string{"recovery-rules", "recovery-agent"}[i])
		out, e := f.runner.Run(ctx, f.protocol, f.caseData, a)
		if e != nil || out.Record.Status != "completed" {
			t.Fatal(out.Record, e)
		}
		records = append(records, out.Record)
	}
	a := f.accepted(t, FixedModel, "recovery-fixed")
	f.runner.binding.Journal = endFailedJournal{base: f.journal}
	first, e := f.runner.Run(ctx, f.protocol, f.caseData, a)
	if !errors.Is(e, ErrJournal) || first.Record.Status != "completed" {
		t.Fatal(first.Record, e)
	}
	requests := f.requests.Load()
	reopenForRecovery(t, f)
	f.runner.binding.Factory = nil // local recovery remains available when disabled.
	restored, e := f.runner.Run(ctx, f.protocol, f.caseData, a)
	if e != nil || !restored.Record.Replayed || restored.Record.Status != "completed" || restored.Record.RuleAuditRef != first.Record.RuleAuditRef || restored.Record.ModelOutputRef != first.Record.ModelOutputRef || restored.Record.ReportRef != first.Record.ReportRef || restored.Record.ModelCalls != 1 || restored.Record.ToolCalls != 5 || restored.Record.ModelOutputAbsent || restored.Record.OutputAbsent || restored.ModelOutput == nil || f.requests.Load() != requests {
		t.Fatal(restored.Record, e)
	}
	r := restored.Record.Recovery
	if r == nil || r.State != "store_reconciled" || r.JournalStatus != "interrupted" || r.JournalErrorCode != "EVALUATION_RESTART_INTERRUPTED" || r.CountersStatus != "agent_recorded" || r.TraceStatus != "unknown" || r.TimingStatus != "unknown" {
		t.Fatal("lost interruption or invented trace/timing", r)
	}
	records = append(records, restored.Record)
	summary, e := Aggregate(f.protocol, []FrozenCase{f.caseData}, records)
	if e != nil || summary.Total.Total != 3 || summary.Total.TerminalStates["completed"] != 3 || summary.Total.JournalInterruptedRuns != 1 || summary.Total.ModelHTTPAttempts != 4 || summary.Total.LogicalCountersUnknown != 0 || summary.Total.ToolTraceUnknown != 1 || summary.Total.OutputPresenceUnknown != 0 {
		t.Fatal(summary, e)
	}
	raw, e := os.ReadFile(f.journalPath)
	if e != nil || strings.Count(string(raw), "\"type\":\"reconcile\"") != 1 || !strings.Contains(string(raw), "EVALUATION_RESTART_INTERRUPTED") {
		t.Fatal("recovery overwrote journal history", e)
	}
	reopenForRecovery(t, f)
	again, e := f.runner.Run(ctx, f.protocol, f.caseData, a)
	if e != nil || again.Record.Status != "completed" || again.Record.ReportRef != first.Record.ReportRef || again.Record.ModelCalls != 1 || f.requests.Load() != requests {
		t.Fatal("second restart lost reconciled state", again.Record, e)
	}
}

func TestRecoveryAfterHTTPBeforeAgentSaveKeepsLogicalCountsUnknown(t *testing.T) {
	f := newEvaluationFixture(t, "http-error", nil)
	ctx := context.Background()
	a := f.accepted(t, FixedModel, "recovery-before-agent-save")
	f.runner.binding.Journal = startOnlyJournal{base: f.journal}
	if _, e := f.runner.Run(ctx, f.protocol, f.caseData, a); !errors.Is(e, ErrJournal) {
		t.Fatal(e)
	}
	if _, e := f.store.SealInput(ctx, a.JobID, f.caseData.input); e != nil {
		t.Fatal(e)
	}
	auditRecord, e := f.store.SaveJobAudit(ctx, a.JobID, f.caseData.rules)
	if e != nil {
		t.Fatal(e)
	}
	// This is a real managed HTTP attempt; simulate losing the process before
	// AgentContent could store its logical/tool counters. Recovery must not use
	// the physical-attempt count as a substitute for those unknown counters.
	m, e := f.runner.binding.Factory.NewModel(ctx, a.JobID)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = m.Generate(ctx, []*schema.Message{schema.UserMessage("本机人工请求，仅用于模拟结果保存前中断。")}, model.WithTemperature(f.protocol.Sampling.Temperature), model.WithTopP(f.protocol.Sampling.TopP)); e == nil {
		t.Fatal("HTTP error fixture unexpectedly succeeded")
	}
	if e = f.store.FinishWithCode(ctx, a.JobID, "interrupted", "FIXTURE_PROCESS_INTERRUPTED"); e != nil {
		t.Fatal(e)
	}
	before := f.requests.Load()
	reopenForRecovery(t, f)
	out, e := f.runner.Run(ctx, f.protocol, f.caseData, a)
	if e != nil || out.Record.Status != "interrupted" || out.Record.RuleAuditRef != auditRecord.ID || out.Record.ModelOutputRef != "" || out.Record.OutputAbsent || !out.Record.ModelOutputAbsent || out.Record.Recovery == nil || out.Record.Recovery.CountersStatus != "unknown" || out.Record.Recovery.TraceStatus != "unknown" || out.Record.Usage.Attempts == nil || out.Record.Usage.Attempts.ModelAttempts != 1 || f.requests.Load() != before {
		t.Fatal(out.Record, e)
	}
	records := []RunRecord{out.Record}
	for i, arm := range []Arm{RulesOnly, AgentSelection} {
		assignment := f.accepted(t, arm, []string{"unknown-count-rules", "unknown-count-agent"}[i])
		result, e := f.runner.Run(ctx, f.protocol, f.caseData, assignment)
		if e != nil {
			t.Fatal(e)
		}
		records = append(records, result.Record)
	}
	summary, e := Aggregate(f.protocol, []FrozenCase{f.caseData}, records)
	if e != nil || summary.Total.Total != 3 || summary.Total.TerminalStates["completed"] != 1 || summary.Total.TerminalStates["partial"] != 1 || summary.Total.TerminalStates["interrupted"] != 1 || summary.Total.LogicalCountersUnknown != 1 || summary.Total.ModelHTTPAttempts != 2 || summary.Total.OutputAbsent != 0 || summary.Total.RuleOutputAbsent != 0 || summary.Total.UsageUnknownRuns != 0 {
		t.Fatal("unknown counters or interrupted run disappeared", summary, e)
	}
}

func TestMissingFactoryNewRunKeepsRulesAndExplicitUnexecutedModel(t *testing.T) {
	f := newEvaluationFixture(t, "success", nil)
	a := f.accepted(t, FixedModel, "disabled-new-run")
	f.runner.binding.Factory = nil
	out, e := f.runner.Run(context.Background(), f.protocol, f.caseData, a)
	if e != nil || out.Record.Status != "partial" || out.Record.ErrorCode != "MODEL_NOT_CONFIGURED" || out.Record.RuleAuditRef == "" || !out.Record.ModelOutputAbsent || out.Record.ModelCalls != 0 || out.Record.ToolCalls != 0 || f.requests.Load() != 0 {
		t.Fatal(out.Record, e)
	}
}
