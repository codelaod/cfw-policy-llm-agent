package app

import (
	"context"
	"errors"
	"time"

	"cfw-policy-llm-agent/server/internal/audit"
	"cfw-policy-llm-agent/server/internal/domain"
	"cfw-policy-llm-agent/server/internal/report"
	"cfw-policy-llm-agent/server/internal/storage"
)

// Workflow executes accepted local audit plans. Cloud reads and model requests
// have no adapters here; their absence is never replaced with synthetic output.
type Workflow struct {
	Store *storage.Store
	rules func(context.Context, domain.Input) (domain.Result, error)
}

func NewWorkflow(store *storage.Store) *Workflow {
	return &Workflow{Store: store, rules: audit.Run}
}

func workflowFailure(ctx context.Context, code string, partial bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return JobFailure{Code: code, Partial: partial}
}

func (w *Workflow) Run(ctx context.Context, job storage.Job) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if job.Kind != "audit" {
		return JobFailure{Code: "EXECUTION_NOT_CONFIGURED"}
	}
	plan, err := w.Store.GetPlan(ctx, job.PlanID)
	if err != nil || plan.Kind != job.Kind || plan.ConversationID != job.ConversationID || plan.EffectCeiling.AllowCloudReads || plan.EffectCeiling.CloudHTTPAttempts != 0 {
		return workflowFailure(ctx, "INPUT_PREPARATION_FAILED", false)
	}
	input := plan.PreparedInput
	input.EvaluationAt = plan.EvaluationAt
	if input.EvaluationAt == "" {
		input.EvaluationAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	sealed, err := w.Store.SealInput(ctx, job.ID, input)
	if err != nil {
		return workflowFailure(ctx, "INPUT_SEAL_FAILED", false)
	}
	if err = w.Store.AppendJobEvent(ctx, job.ID, "stage_changed", map[string]any{"phase": "L2", "stage": "rules_started"}); err != nil {
		return workflowFailure(ctx, "STAGE_SAVE_FAILED", false)
	}
	ruleCtx, stopRules := context.WithTimeout(ctx, 20*time.Second)
	result, ruleErr := w.rules(ruleCtx, sealed)
	if ruleErr == nil {
		ruleErr = ruleCtx.Err()
	}
	stopRules()
	if ruleErr != nil && result.SchemaVersion != 1 {
		if errors.Is(ruleErr, context.Canceled) || errors.Is(ruleErr, context.DeadlineExceeded) {
			return ruleErr
		}
		return workflowFailure(ctx, "RULE_STAGE_FAILED", false)
	}
	if ruleErr != nil {
		result.Coverage.Complete = false
		result.Coverage.Limitations = append(append([]string(nil), result.Coverage.Limitations...), "RULE_STAGE_INTERRUPTED")
	}
	// Only the already computed local result survives cancellation. This bounded
	// service context cannot resume rules, model requests or cloud collection.
	saveCtx, stopSave := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	record, saveErr := w.Store.SaveJobAudit(saveCtx, job.ID, result)
	stopSave()
	if saveErr != nil {
		return workflowFailure(ctx, "RESULT_SAVE_FAILED", false)
	}
	if ruleErr != nil {
		if errors.Is(ruleErr, context.Canceled) || errors.Is(ruleErr, context.DeadlineExceeded) {
			return ruleErr
		}
		return JobFailure{Code: "RULE_STAGE_FAILED", Partial: true}
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	finalCtx, stopFinal := context.WithTimeout(ctx, 10*time.Second)
	defer stopFinal()
	service := NewAuditService(w.Store)
	if plan.RecheckOfJobID != "" {
		_, old, readErr := w.Store.ReadJobAudit(finalCtx, plan.RecheckOfJobID)
		if readErr != nil || old == nil {
			return workflowFailure(finalCtx, "RECHECK_COMPARISON_FAILED", true)
		}
		if _, err = service.CompareRuns(finalCtx, old.ID, record.ID); err != nil {
			return workflowFailure(finalCtx, "RECHECK_COMPARISON_FAILED", true)
		}
	}
	execution := report.Execution{Mode: plan.Mode, ModelStage: "not_requested"}
	if plan.Mode == "rules_and_agent" {
		execution.ModelStage = "not_configured"
		if err = w.Store.AppendJobEvent(finalCtx, job.ID, "stage_changed", map[string]any{"phase": "L3", "stage": "model_not_configured"}); err != nil {
			return workflowFailure(finalCtx, "STAGE_SAVE_FAILED", true)
		}
	}
	if err = w.Store.AppendJobEvent(finalCtx, job.ID, "stage_changed", map[string]any{"phase": "L4", "stage": "report_building"}); err != nil {
		return workflowFailure(finalCtx, "STAGE_SAVE_FAILED", true)
	}
	heads, content, err := service.prepareReport(finalCtx, record.ID, "json", &execution)
	if err != nil {
		return workflowFailure(finalCtx, "REPORT_SAVE_FAILED", true)
	}
	status, code := "completed", ""
	if execution.ModelStage == "not_configured" {
		status, code = "partial", "MODEL_NOT_CONFIGURED"
	}
	if _, err = w.Store.FinalizeJobWithReport(finalCtx, job.ID, storage.FinalizeJobReportRequest{Format: "json", Heads: heads, Content: content, Key: "workflow_report_" + job.ID, Status: status, Code: code}); err != nil {
		return workflowFailure(finalCtx, "FINALIZATION_SAVE_FAILED", true)
	}
	if execution.ModelStage == "not_configured" {
		return JobFailure{Code: "MODEL_NOT_CONFIGURED", Partial: true}
	}
	return nil
}
