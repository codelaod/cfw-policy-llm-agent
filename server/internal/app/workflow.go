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

// Workflow executes accepted, fixed-effect plans. Data preparation runs inside
// its audit before sealing; later model tools can only read the fixed input.
type Workflow struct {
	Store *storage.Store
	Agent *AgentService
	Cloud *CloudService
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
	if job.Kind == "collect" || job.Kind == "connection_check" {
		return w.Cloud.Run(ctx, job)
	}
	if job.Kind != "audit" && job.Kind != "assistant_turn" {
		return JobFailure{Code: "EXECUTION_NOT_CONFIGURED"}
	}
	plan, err := w.Store.GetPlan(ctx, job.PlanID)
	if err != nil || plan.Kind != job.Kind || plan.ConversationID != job.ConversationID || plan.Preparation == nil && (plan.EffectCeiling.AllowCloudReads || plan.EffectCeiling.CloudHTTPAttempts != 0) {
		return workflowFailure(ctx, "INPUT_PREPARATION_FAILED", false)
	}
	if plan.Batch != nil {
		return w.runBatch(ctx, job, plan)
	}
	limits := plan.Workload.Limits
	if job.Kind == "audit" {
		var limitErr error
		ctx, limitErr = audit.WithExecutionLimits(ctx, audit.ExecutionLimits{MaxAssets: limits.AssetsPerBatch, MaxPolicies: limits.MaxPolicies, MaxQueryCells: limits.MaxQueryCells, Timeout: time.Duration(limits.RulesSeconds) * time.Second})
		if limitErr != nil {
			return workflowFailure(ctx, "EXECUTION_POLICY_INVALID", false)
		}
	}
	analysisCtx, stopAnalysis := reserveFinalization(ctx, time.Duration(limits.FinalizeSeconds)*time.Second)
	defer stopAnalysis()
	input := plan.PreparedInput
	input.EvaluationAt = plan.EvaluationAt
	if input.EvaluationAt == "" {
		input.EvaluationAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	var sealed domain.Input
	if plan.Preparation != nil {
		sealed, err = w.Cloud.PrepareAudit(analysisCtx, job, plan)
		if err != nil {
			return err
		}
		plan, err = w.Store.ExecutionPlanForJob(ctx, job.ID)
		if err != nil {
			return workflowFailure(ctx, "INPUT_BIND_FAILED", false)
		}
	} else {
		sealed, err = w.Store.SealInput(ctx, job.ID, input)
		if err != nil {
			return workflowFailure(ctx, "INPUT_SEAL_FAILED", false)
		}
	}
	if job.Kind == "assistant_turn" {
		if w.Agent == nil {
			return JobFailure{Code: "MODEL_NOT_CONFIGURED"}
		}
		_, err = w.Agent.Run(analysisCtx, job)
		return err
	}
	if err = w.Store.AppendJobEvent(ctx, job.ID, "stage_changed", map[string]any{"phase": "L2", "stage": "rules_started"}); err != nil {
		return workflowFailure(ctx, "STAGE_SAVE_FAILED", false)
	}
	ruleCtx, stopRules := context.WithTimeout(analysisCtx, time.Duration(limits.RulesSeconds)*time.Second)
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
	execution := report.Execution{Workload: &plan.Workload, Mode: plan.Mode, ModelStage: "not_requested"}
	partialCode, agentResultID := "", ""
	if !sealed.Completeness.Declared || !sealed.Completeness.EnumerationComplete || sealed.Completeness.ObservationConsistency == "unverified" || sealed.Completeness.ObservationConsistency == "changed" || len(sealed.Completeness.KnownGaps) != 0 {
		partialCode = "COLLECTION_INCOMPLETE"
		for _, code := range sealed.Completeness.KnownGaps {
			if permanentBatchFailure(code) {
				partialCode = code
				break
			}
		}
	}
	if plan.Mode == "rules_and_agent" {
		if w.Agent == nil || !plan.EffectCeiling.AllowModelRequests {
			execution.ModelStage = "not_configured"
			if partialCode == "" {
				partialCode = "MODEL_NOT_CONFIGURED"
			}
			if err = w.Store.AppendJobEvent(ctx, job.ID, "stage_changed", map[string]any{"phase": "L3", "stage": "model_not_configured"}); err != nil {
				return workflowFailure(ctx, "STAGE_SAVE_FAILED", true)
			}
		} else {
			if err = w.Store.AppendJobEvent(ctx, job.ID, "stage_changed", map[string]any{"phase": "L3", "stage": "agent_started"}); err != nil {
				return workflowFailure(ctx, "STAGE_SAVE_FAILED", true)
			}
			modelResult, modelErr := w.Agent.Run(analysisCtx, job)
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if modelErr != nil {
				execution.ModelStage = "not_completed"
				if partialCode == "" {
					partialCode = "AGENT_EXECUTION_FAILED"
					var failure JobFailure
					if errors.As(modelErr, &failure) {
						partialCode = failure.Code
					}
				}
			} else {
				execution.ModelStage = "completed"
				agentResultID = modelResult.ID
			}
		}
	}
	finalCtx, stopFinal := context.WithTimeout(ctx, finalizationWindow(ctx, time.Duration(limits.FinalizeSeconds)*time.Second))
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
	if err = w.Store.AppendJobEvent(finalCtx, job.ID, "stage_changed", map[string]any{"phase": "L4", "stage": "report_building"}); err != nil {
		return workflowFailure(finalCtx, "STAGE_SAVE_FAILED", true)
	}
	execution.JobStatus = "completed"
	if partialCode != "" {
		execution.JobStatus = "partial"
	}
	heads, content, err := service.prepareReport(finalCtx, record.ID, "json", &execution)
	if err != nil {
		return workflowFailure(finalCtx, "REPORT_SAVE_FAILED", true)
	}
	status, code := "completed", ""
	if partialCode != "" {
		status, code = "partial", partialCode
	}
	if _, err = w.Store.FinalizeJobWithReport(finalCtx, job.ID, storage.FinalizeJobReportRequest{Format: "json", Heads: heads, Content: content, Key: "workflow_report_" + job.ID, Status: status, Code: code, AgentResultID: agentResultID}); err != nil {
		return workflowFailure(finalCtx, "FINALIZATION_SAVE_FAILED", true)
	}
	if partialCode != "" {
		return JobFailure{Code: partialCode, Partial: true}
	}
	return nil
}

// Reserve only shortens the earlier phase. It never extends a root deadline.
func reserveFinalization(ctx context.Context, reserve time.Duration) (context.Context, context.CancelFunc) {
	deadline, ok := ctx.Deadline()
	if !ok || reserve <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithDeadline(ctx, deadline.Add(-reserve))
}
func finalizationWindow(ctx context.Context, configured time.Duration) time.Duration {
	if configured > 0 {
		return configured
	}
	if deadline, ok := ctx.Deadline(); ok {
		return max(time.Until(deadline), time.Nanosecond)
	}
	return time.Nanosecond
}
