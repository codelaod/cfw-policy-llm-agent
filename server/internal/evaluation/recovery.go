package evaluation

import (
	"context"
	"time"

	"cfw-policy-llm-agent/server/internal/agentquery"
)

type reconciliationJournal interface {
	Reconcile(context.Context, RunRecord, RunRecord) error
}

// reconcile reads immutable local products only. It cannot seal input, rerun a
// rule/model/tool, finalize a report, claim a job, or reserve an HTTP attempt.
func (r *Runner) reconcile(ctx context.Context, p Protocol, c FrozenCase, original RunRecord) (RunOutcome, error) {
	record, e := copyJSON(original)
	if e != nil {
		return RunOutcome{}, ErrJournal
	}
	if record.Recovery == nil {
		record.Recovery = &Recovery{State: "pending_store_reconciliation", JournalStatus: "interrupted", JournalErrorCode: "EVALUATION_RESTART_INTERRUPTED", DetectedAt: original.FinishedAt, OutputPresence: "unknown", RuleOutputPresence: "unknown", ModelOutputPresence: "unknown", CountersStatus: "unknown", TraceStatus: "unknown", TimingStatus: "unknown"}
		record.OutputAbsent, record.ModelOutputAbsent, record.ModelLayer = false, false, "unknown"
		record.InferenceReview, record.RecommendationReview = "unknown", "unknown"
	}
	out := RunOutcome{Record: record}
	view, e := r.binding.Store.ReadJobWorkspace(ctx, record.JobID)
	if e != nil || view.Job.ID != record.JobID || view.Job.Kind != "audit" {
		out.Record.Replayed = true
		return out, ErrBinding
	}
	if view.Audit != nil {
		inputHash, _ := digest(view.Audit.Input)
		ruleHash, _ := digest(view.Audit.Result)
		if inputHash != record.Conditions.InputDigest || ruleHash != record.Conditions.CommonRuleDigest || view.Job.ResultRef != view.Audit.ID {
			out.Record.Replayed = true
			return out, ErrBinding
		}
		record.RuleAuditRef = view.Audit.ID
	} else if view.InputSealed {
		input, e := r.binding.Store.GetJobInput(ctx, record.JobID)
		h, _ := digest(input)
		if e != nil || h != record.Conditions.InputDigest {
			out.Record.Replayed = true
			return out, ErrBinding
		}
	}
	if view.ReportPublished {
		report, e := r.binding.Store.GetReport(ctx, view.ReportRef)
		if e != nil || view.Audit == nil || report.AuditID != view.Audit.ID {
			out.Record.Replayed = true
			return out, ErrBinding
		}
		record.ReportRef = report.ID
	}
	if view.AgentResult != nil {
		a := view.AgentResult
		if view.Audit == nil || record.Arm == RulesOnly || a.JobID != record.JobID || a.Content.JobID != record.JobID || !equalJSON(a.Content.Manifest, c.rules.Manifest) || a.Content.ModelCalls < 0 || a.Content.ModelCalls > p.Workload.Limits.ModelLogicalCalls || a.Content.ToolCalls < 0 || a.Content.ToolCalls > p.Workload.Limits.ToolCalls {
			out.Record.Replayed = true
			return out, ErrBinding
		}
		bounded, e := queryContext(ctx, p)
		if e != nil {
			out.Record.Replayed = true
			return out, ErrBinding
		}
		if a.Content.Output != nil {
			if a.Content.ModelCalls < 1 || a.Content.ErrorCode != "" || agentquery.ValidateBoundOutputContext(bounded, c.input, &c.rules, *a.Content.Output, a.Content.Bindings) != nil {
				out.Record.Replayed = true
				return out, ErrBinding
			}
		} else if agentquery.ValidateLocalBindingsContext(bounded, c.input, &c.rules, a.Content.Bindings) != nil {
			out.Record.Replayed = true
			return out, ErrBinding
		}
		record.ModelOutputRef = a.ID
		record.ModelCalls, record.ToolCalls = a.Content.ModelCalls, a.Content.ToolCalls
		record.Recovery.CountersStatus = "agent_recorded"
		if a.Content.Output != nil {
			out.ModelOutput, out.Bindings = a.Content.Output, a.Content.Bindings
			record.ModelLayer, record.ModelOutputAbsent = "produced", false
			record.RecommendationCount = len(a.Content.Output.Recommendations)
			record.InferenceReview, record.RecommendationReview = "pending_independent_review", "pending_independent_review"
			record.Recovery.ModelOutputPresence = "present"
		} else {
			record.ModelLayer, record.ModelOutputAbsent = "not_produced", true
			record.Recovery.ModelOutputPresence = "absent"
			record.InferenceReview, record.RecommendationReview = "not_produced", "not_produced"
		}
	}
	jobTerminal := terminal(view.Job.Status)
	if record.Arm == RulesOnly {
		record.ModelCalls, record.ToolCalls = 0, 0
		record.ModelLayer, record.ModelOutputAbsent = "not_produced", true
		record.Recovery.ModelOutputPresence, record.Recovery.CountersStatus = "absent", "rules_only_zero"
		record.InferenceReview, record.RecommendationReview = "not_produced", "not_produced"
	}
	progress := jobTerminal || view.InputSealed || view.Audit != nil || view.AgentResult != nil || view.ReportPublished || view.AttemptUsage.ModelAttempts > 0 || view.AttemptUsage.CloudAttempts > 0
	if !progress {
		// The lost start does not prove that a model stage never began. Without
		// durable progress retain unknown counts/presence and the original
		// interruption. In particular no-HTTP start-only recovery stays unknown.
		out.Record = record
		out.Record.Replayed = true
		return out, nil
	}
	record.Recovery.State = "store_partial"
	record.Recovery.CheckedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if jobTerminal {
		record.Status, record.ErrorCode = view.Job.Status, view.Job.ErrorCode
		record.Recovery.State = "store_reconciled"
		if _, e := time.Parse(time.RFC3339Nano, view.Job.FinishedAt); e == nil {
			record.FinishedAt = view.Job.FinishedAt
		}
	}
	if view.Audit != nil {
		record.Recovery.RuleOutputPresence = "present"
	} else if jobTerminal {
		record.Recovery.RuleOutputPresence = "absent"
	}
	if record.Arm == RulesOnly {
		record.ModelCalls, record.ToolCalls = 0, 0
		record.ModelLayer, record.ModelOutputAbsent = "not_produced", true
		record.Recovery.ModelOutputPresence, record.Recovery.CountersStatus = "absent", "rules_only_zero"
	} else if view.AgentResult == nil && jobTerminal {
		record.ModelLayer, record.ModelOutputAbsent = "not_produced", true
		record.Recovery.ModelOutputPresence = "absent"
		// Physical attempts cannot reconstruct logical model/tool counts.
		record.Recovery.CountersStatus = "unknown"
		record.InferenceReview, record.RecommendationReview = "not_produced", "not_produced"
	}
	if view.Audit != nil || out.ModelOutput != nil || view.ReportPublished {
		record.OutputAbsent = false
		record.Recovery.OutputPresence = "present"
	} else if jobTerminal {
		record.OutputAbsent = true
		record.Recovery.OutputPresence = "absent"
	}
	record.Usage = Usage{Status: "ledger_recorded", Attempts: &view.AttemptUsage}
	// AgentContent preserves exact aggregate counters, not the lost trace's
	// input refs/dependencies or per-stage timings. Do not fabricate those.
	record.Traces = []ToolTrace{}
	record.ExecutionMillis, record.ModelStageMillis, record.FinalizeMillis = 0, 0, 0
	record.Replayed = false
	out.Record = record
	journal, ok := r.binding.Journal.(reconciliationJournal)
	if !ok {
		out.Record.Replayed = true
		return out, ErrJournal
	}
	if e = journal.Reconcile(ctx, original, record); e != nil {
		out.Record.Replayed = true
		return out, e
	}
	out.Record.Replayed = true
	return out, nil
}
