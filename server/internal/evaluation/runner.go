package evaluation

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"cfw-policy-llm-agent/server/internal/agentquery"
	"cfw-policy-llm-agent/server/internal/agentrun"
	"cfw-policy-llm-agent/server/internal/audit"
	"cfw-policy-llm-agent/server/internal/report"
	"cfw-policy-llm-agent/server/internal/sensitive"
	"cfw-policy-llm-agent/server/internal/storage"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func validArm(a Arm) bool { return a == RulesOnly || a == FixedModel || a == AgentSelection }

// RunSerial processes this explicit subset in order. It neither claims queue
// entries nor grants permission. The caller injects an ordinary accepted,
// running job immediately before use; Aggregate requires the complete frozen
// matrix before producing a whole-experiment summary.
func (r *Runner) RunSerial(ctx context.Context, p Protocol, cases []FrozenCase, assignments []Assignment) ([]RunOutcome, error) {
	if p.Validate() != nil || len(assignments) == 0 {
		return nil, ErrProtocol
	}
	byID := map[string]FrozenCase{}
	for _, c := range cases {
		if _, duplicate := byID[c.Definition.ID]; duplicate {
			return nil, ErrProtocol
		}
		byID[c.Definition.ID] = c
	}
	ids, jobs, slots := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, a := range assignments {
		c, exists := byID[a.CaseID]
		key, _ := digest([]any{a.CaseID, a.Arm, a.Repetition})
		if !exists || !validAssignment(p, c, a) || ids[a.RunID] || jobs[a.JobID] || slots[key] {
			return nil, ErrProtocol
		}
		ids[a.RunID], jobs[a.JobID], slots[key] = true, true, true
	}
	results := make([]RunOutcome, 0, len(assignments))
	for _, a := range assignments {
		if ctx.Err() != nil {
			return results, ctx.Err()
		}
		out, e := r.Run(ctx, p, byID[a.CaseID], a)
		if out.Record.RunID != "" {
			results = append(results, out)
		}
		if e != nil {
			return results, e
		}
	}
	return results, nil
}
func validAssignment(p Protocol, c FrozenCase, a Assignment) bool {
	if !validArm(a.Arm) || !publicRef.MatchString(a.RunID) || !publicRef.MatchString(a.JobID) || a.CaseID != c.Definition.ID || a.Repetition < 1 || a.Repetition > c.Definition.Repetitions {
		return false
	}
	for _, definition := range p.Cases {
		if definition.ID == a.CaseID {
			return equalJSON(definition, c.Definition)
		}
	}
	return false
}
func (r *Runner) conditions(p Protocol, c FrozenCase) (Conditions, error) {
	protocolHash, e := protocolDigest(p)
	if e != nil || p.ModelConfigRef != r.binding.ModelConfigRef || p.Sampling != r.binding.Sampling {
		return Conditions{}, ErrBinding
	}
	if _, changed := sensitive.Redact(p.CommonBrief, r.binding.SensitiveValues); changed {
		return Conditions{}, ErrProtocol
	}
	inputHash, _ := digest(c.input)
	ruleHash, _ := digest(c.rules)
	if inputHash != c.InputDigest || ruleHash != c.CommonRuleDigest || !validDefinition(c.Definition) || !equalJSON(c.RuleWorkload, p.Workload) {
		return Conditions{}, ErrProtocol
	}
	bounded, e := queryContext(context.Background(), p)
	if e != nil {
		return Conditions{}, ErrProtocol
	}
	q, e := agentquery.NewWithContext(bounded, c.input, &c.rules)
	if e != nil {
		return Conditions{}, ErrProtocol
	}
	catalogHash, _ := digest(q.Tools())
	schemaHash, _ := digest(finalSchema())
	return Conditions{ProtocolDigest: protocolHash, InputDigest: c.InputDigest, CommonRuleDigest: c.CommonRuleDigest, CommonBriefVersion: p.CommonBriefVersion, Workload: p.Workload, ToolProjectionVersion: p.ToolProjectionVersion, ToolCatalogDigest: catalogHash, FinalSchemaDigest: schemaHash}, nil
}
func queryContext(ctx context.Context, p Protocol) (context.Context, error) {
	l := p.Workload.Limits
	ctx, e := audit.WithQueryCellLimit(ctx, l.MaxQueryCells)
	if e != nil {
		return nil, e
	}
	return audit.WithInputObjectLimits(ctx, l.AssetsPerBatch, l.MaxPolicies, l.MaxQueryCells)
}
func (r *Runner) validateJob(ctx context.Context, p Protocol, c FrozenCase, a Assignment) (storage.Job, storage.ExecutionPlan, error) {
	j, e := r.binding.Store.Get(ctx, a.JobID)
	if e != nil {
		return j, storage.ExecutionPlan{}, ErrBinding
	}
	plan, e := r.binding.Store.ExecutionPlanForJob(ctx, a.JobID)
	expected := plan.PreparedInput
	if expected.EvaluationAt == "" {
		expected.EvaluationAt = c.input.EvaluationAt
	}
	if e != nil || j.Kind != "audit" || j.ParentJobID != "" || plan.Kind != "audit" || plan.Batch != nil || plan.Preparation != nil || plan.ResourceGroupVersionID != "" || plan.ConsumedJobID != j.ID || plan.ConversationID != j.ConversationID || plan.RecheckOfJobID != "" || plan.ConfigRevision != r.binding.RuntimeRevision || plan.BudgetRef != r.binding.BudgetRef || j.BudgetRef != r.binding.BudgetRef || !equalJSON(plan.Workload, p.Workload) || !equalJSON(j.Workload, p.Workload) || plan.EffectCeiling.AllowCloudReads || plan.EffectCeiling.CloudHTTPAttempts != 0 || !equalJSON(expected, c.input) || plan.PolicyScopeRef != c.input.Scope.Ref || plan.EvaluationAt != "" && plan.EvaluationAt != c.input.EvaluationAt {
		return j, plan, ErrBinding
	}
	if a.Arm == RulesOnly {
		if plan.Mode != "rules_only" || plan.EffectCeiling.AllowModelRequests || plan.EffectCeiling.ModelHTTPAttempts != 0 {
			return j, plan, ErrBinding
		}
	} else if plan.Mode != "rules_and_agent" || !plan.EffectCeiling.AllowModelRequests || plan.EffectCeiling.ModelHTTPAttempts != p.Workload.Limits.ModelHTTPAttempts {
		return j, plan, ErrBinding
	}
	return j, plan, nil
}

// Run preserves every admitted terminal state. Execution failures are data in
// RunOutcome, while protocol, binding or journal errors stop serial admission.
// Neither a retry nor a process restart silently spends a second model request.
func (r *Runner) Run(ctx context.Context, p Protocol, c FrozenCase, a Assignment) (out RunOutcome, err error) {
	if r == nil || !validAssignment(p, c, a) {
		return out, ErrProtocol
	}
	conditions, e := r.conditions(p, c)
	if e != nil {
		return out, e
	}
	if a.Arm != RulesOnly {
		conditions.ModelConfigRef, conditions.Sampling = p.ModelConfigRef, &p.Sampling
		conditions.PromptVersion = p.AgentPromptVersion
		if a.Arm == FixedModel {
			conditions.PromptVersion, conditions.PipelineVersion = p.FixedPromptVersion, p.FixedPipeline.Version
		}
	}
	job, _, e := r.validateJob(ctx, p, c, a)
	if e != nil {
		return out, e
	}
	started := time.Now()
	base := RunRecord{SchemaVersion: 1, RunID: a.RunID, CaseID: a.CaseID, ScenarioRef: c.Definition.ScenarioRef, DatasetUse: p.DatasetUse, Split: c.Definition.Split, ProtocolVersion: p.Version, LabelVersionRef: c.Definition.LabelVersionRef, Arm: a.Arm, Repetition: a.Repetition, JobID: a.JobID, Conditions: conditions, Status: "running", OutputAbsent: true, ModelLayer: "not_produced", StartedAt: started.UTC().Format(time.RFC3339Nano), Traces: []ToolTrace{}, Usage: Usage{Status: "unknown"}, InferenceReview: "not_produced", RecommendationReview: "not_produced"}
	requestHash, _ := digest(struct {
		Assignment Assignment
		Conditions Conditions
	}{a, conditions})
	base.ModelOutputAbsent = true
	existing, reserved, e := r.binding.Journal.Begin(ctx, RunStart{Record: base, RequestDigest: requestHash})
	if e != nil {
		return out, e
	}
	if !reserved {
		if existing == nil {
			return out, ErrJournal
		}
		if needsReconciliation(*existing) {
			return r.reconcile(ctx, p, c, *existing)
		}
		existing.Replayed = true
		return RunOutcome{Record: *existing}, nil
	}
	out.Record = base
	defer func() {
		if recover() != nil {
			r.finishFailure(ctx, &out.Record, "EVALUATION_INTERNAL_ERROR", "failed")
		}
		out.Record.ExecutionMillis = time.Since(started).Milliseconds()
		out.Record.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
		usageCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer stop()
		if usage, usageErr := r.binding.Store.BudgetUsage(usageCtx, r.binding.BudgetRef, "", job.ID); usageErr == nil {
			out.Record.Usage = Usage{Status: "ledger_recorded", Attempts: &usage}
		}
		if e := r.binding.Journal.End(usageCtx, out.Record); e != nil {
			err = ErrJournal
		}
	}()
	if job.Status != "running" {
		out.Record.Status, out.Record.ErrorCode = "failed", "EVALUATION_JOB_NOT_RUNNING"
		return out, nil
	}
	created, ce := time.Parse(time.RFC3339Nano, job.CreatedAt)
	claimed, se := time.Parse(time.RFC3339Nano, job.StartedAt)
	if ce == nil && se == nil && !claimed.Before(created) {
		q := claimed.Sub(created).Milliseconds()
		out.Record.QueueMillis = &q
	}
	deadline, e := time.Parse(time.RFC3339Nano, job.ExecutionDeadline)
	rootDeadline, e2 := time.Parse(time.RFC3339Nano, job.RootDeadline)
	if e != nil || e2 != nil {
		r.finishFailure(ctx, &out.Record, "EVALUATION_DEADLINE_INVALID", "failed")
		return out, nil
	}
	if rootDeadline.Before(deadline) {
		deadline = rootDeadline
	}
	runCtx, stop := context.WithDeadline(ctx, deadline)
	defer stop()
	if e = runCtx.Err(); e != nil {
		r.finishFailure(runCtx, &out.Record, contextCode(e), "failed")
		return out, nil
	}
	input, e := r.binding.Store.SealInput(runCtx, job.ID, c.input)
	if e != nil || !equalJSON(input, c.input) {
		r.finishFailure(runCtx, &out.Record, "EVALUATION_INPUT_NOT_SEALED", "failed")
		return out, nil
	}
	auditRecord, e := r.binding.Store.SaveJobAudit(runCtx, job.ID, c.rules)
	if e != nil {
		r.finishFailure(runCtx, &out.Record, "EVALUATION_RULE_RESULT_NOT_SAVED", "failed")
		return out, nil
	}
	out.Record.RuleAuditRef = auditRecord.ID
	var agent *storage.AgentRecord
	if a.Arm != RulesOnly {
		stageAt := time.Now()
		agent, e = r.model(runCtx, p, c, job, &out)
		out.Record.ModelStageMillis = time.Since(stageAt).Milliseconds()
		if e != nil {
			r.finishFailure(runCtx, &out.Record, out.Record.ErrorCode, "partial")
			return out, nil
		}
	}
	if runCtx.Err() != nil {
		r.finishFailure(runCtx, &out.Record, contextCode(runCtx.Err()), "partial")
		return out, nil
	}
	finalAt := time.Now()
	finalCtx, finalStop := runCtx, func() {}
	if p.Workload.Limits.FinalizeSeconds > 0 {
		finalCtx, finalStop = context.WithTimeout(runCtx, time.Duration(p.Workload.Limits.FinalizeSeconds)*time.Second)
	}
	defer finalStop()
	e = r.finalize(finalCtx, p, c, job, auditRecord, agent, &out.Record)
	out.Record.FinalizeMillis = time.Since(finalAt).Milliseconds()
	if e != nil {
		r.finishFailure(runCtx, &out.Record, "EVALUATION_REPORT_NOT_SAVED", "partial")
	}
	return out, nil
}
func (r *Runner) model(ctx context.Context, p Protocol, c FrozenCase, job storage.Job, out *RunOutcome) (*storage.AgentRecord, error) {
	if r.binding.Factory == nil {
		out.Record.ErrorCode = "MODEL_NOT_CONFIGURED"
		return nil, agentrun.Controlled("MODEL_NOT_CONFIGURED")
	}
	bounded, e := queryContext(ctx, p)
	if e != nil {
		out.Record.ErrorCode = "EVALUATION_CONTEXT_INVALID"
		return nil, e
	}
	service, e := agentquery.NewWithContext(bounded, c.input, &c.rules)
	if e != nil {
		out.Record.ErrorCode = "EVALUATION_CONTEXT_INVALID"
		return nil, e
	}
	origin := "model"
	if out.Record.Arm == FixedModel {
		origin = "predefined_pipeline"
	}
	initial := agentquery.InitialView{}
	_ = json.Unmarshal([]byte(service.InitialContext()), &initial)
	initialRefs := map[string]bool{initial.ScopeRef: true, initial.SnapshotRef: true, initial.ProfileRef: true, initial.IntentRef: true}
	for _, ref := range initial.Assets {
		initialRefs[ref] = true
	}
	for _, finding := range initial.Findings {
		initialRefs[finding.FindingRef] = true
	}
	sink := &traceSink{store: r.binding.Store, jobID: job.ID, origin: origin, active: -1, origins: map[string]string{}, initial: initialRefs}
	adapter, e := newQueryAdapter(service, sink)
	if e != nil {
		out.Record.ErrorCode = "EVALUATION_CONTEXT_INVALID"
		return nil, e
	}
	brief, e := service.ProjectText(p.CommonBrief)
	if e != nil {
		out.Record.ErrorCode = "EVALUATION_BRIEF_REJECTED"
		return nil, e
	}
	task := agentrun.Task{Kind: "audit", History: []agentrun.HistoryMessage{{Role: "user", Text: brief}}, Queries: adapter, NewModel: func(callCtx context.Context) (model.BaseChatModel, error) {
		m, e := r.binding.Factory.NewModel(callCtx, job.ID)
		if e != nil {
			return nil, e
		}
		return &sampledModel{inner: m, sampling: p.Sampling}, nil
	}}
	cfg := agentrun.Config{Workload: p.Workload, SensitiveValues: r.binding.SensitiveValues, FinalSchema: finalSchema()}
	var run agentrun.Outcome
	if out.Record.Arm == FixedModel {
		run, e = agentrun.RunFixed(ctx, task, cfg, p.FixedPipeline, sink)
	} else {
		run, e = agentrun.Run(ctx, task, cfg, sink)
	}
	out.Record.ModelCalls, out.Record.ToolCalls, out.Record.Traces = run.ModelCalls, run.ToolCalls, sink.snapshot()
	content := storage.AgentContent{JobID: job.ID, Manifest: c.rules.Manifest, ModelCalls: run.ModelCalls, ToolCalls: run.ToolCalls, Bindings: service.LocalBindings(), Context: storage.ContextCoverage{DisclosureVersion: "minimal-v1", IncludedMessages: 1}}
	if e == nil {
		var output agentquery.Output
		if json.Unmarshal(run.Output, &output) != nil {
			e = agentrun.Controlled("MODEL_OUTPUT_INVALID")
			run.ErrorCode = "MODEL_OUTPUT_INVALID"
		} else {
			content.Output = &output
		}
	}
	if e != nil {
		out.Record.ErrorCode = run.ErrorCode
		if out.Record.ErrorCode == "" {
			out.Record.ErrorCode = "EVALUATION_MODEL_FAILED"
		}
		content.ErrorCode = storageFailureCode(out.Record.ErrorCode)
	}
	if ctx.Err() != nil {
		out.Record.ErrorCode = contextCode(ctx.Err())
		return nil, ctx.Err()
	}
	// Production agent storage assumes a model selected every tool. Fixed
	// prefetch can fail before its first model call; retain its actual zero and
	// complete tool trace in the journal, without inventing a model invocation.
	if content.ModelCalls == 0 && content.ToolCalls > 0 {
		return nil, agentrun.Controlled(out.Record.ErrorCode)
	}
	saved, saveErr := r.binding.Store.SaveJobAgentResult(ctx, job.ID, content)
	if saveErr != nil {
		out.Record.ErrorCode = "EVALUATION_MODEL_RESULT_NOT_SAVED"
		return nil, saveErr
	}
	out.Record.ModelOutputRef = saved.ID
	if content.Output != nil {
		out.ModelOutput, out.Bindings = content.Output, content.Bindings
		out.Record.ModelOutputAbsent = false
		out.Record.ModelLayer = "produced"
		out.Record.RecommendationCount = len(content.Output.Recommendations)
		out.Record.InferenceReview, out.Record.RecommendationReview = "pending_independent_review", "pending_independent_review"
	}
	return &saved, nil
}
func (r *Runner) finalize(ctx context.Context, p Protocol, c FrozenCase, job storage.Job, auditRecord storage.AuditRecord, agent *storage.AgentRecord, record *RunRecord) error {
	_, heads, e := r.binding.Store.CaptureReportInput(ctx, auditRecord.ID)
	if e != nil {
		return e
	}
	publicHeads := make([]report.ReviewHead, 0, len(heads))
	for _, h := range heads {
		publicHeads = append(publicHeads, report.ReviewHead{FindingID: h.FindingKey, Revision: h.Revision, Plan: h.Plan})
	}
	status, code, stage, mode := "completed", "", "not_requested", "rules_only"
	if record.Arm != RulesOnly {
		stage, mode = "completed", "rules_and_agent"
		if agent == nil || agent.Content.Output == nil {
			status, stage = "partial", "not_completed"
			code = record.ErrorCode
			if code == "" {
				code = "EVALUATION_MODEL_FAILED"
			}
		}
	}
	execution := report.Execution{Workload: &p.Workload, Mode: mode, ModelStage: stage, JobStatus: status}
	var content []byte
	if agent != nil {
		var usage *report.AgentUsage
		if u, ue := r.binding.Store.BudgetUsage(ctx, job.BudgetRef, "", job.ID); ue == nil {
			usage = &report.AgentUsage{PhysicalAttempts: u.ModelAttempts, KnownUsageResponses: u.KnownUsageResponses, UnknownUsageAttempts: u.UnknownUsageAttempts, ReportedTokens: u.ReportedTokens, UsageTotalsOverflow: u.UsageTotalsOverflow}
		}
		a := agent.Content
		content, e = report.BuildWithAgent(ctx, c.input, c.rules, publicHeads, "json", execution, report.AgentSupplement{Output: a.Output, Bindings: a.Bindings, ModelCalls: a.ModelCalls, ToolCalls: a.ToolCalls, ErrorCode: a.ErrorCode, Usage: usage})
	} else {
		content, e = report.BuildWithExecution(ctx, c.rules, publicHeads, "json", execution)
	}
	if e != nil {
		return e
	}
	request := storage.FinalizeJobReportRequest{Format: "json", Heads: heads, Content: content, Key: "evaluation_" + record.RunID, Status: status, Code: code}
	if agent != nil && agent.Content.Output != nil {
		request.AgentResultID = agent.ID
	}
	saved, e := r.binding.Store.FinalizeJobWithReport(ctx, job.ID, request)
	if e != nil {
		return e
	}
	record.Status, record.ErrorCode, record.ReportRef = status, code, saved.ID
	record.OutputAbsent = false // a common-rule report is output even without a model layer.
	return nil
}
func contextCode(e error) string {
	if errors.Is(e, context.DeadlineExceeded) {
		return "REQUEST_DEADLINE_EXCEEDED"
	}
	return "REQUEST_CANCELLED"
}
func (r *Runner) finishFailure(ctx context.Context, record *RunRecord, code, status string) {
	if code == "" {
		code = "EVALUATION_EXECUTION_FAILED"
	}
	finishCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer stop()
	if errors.Is(ctx.Err(), context.Canceled) {
		_, _ = r.binding.Store.Cancel(finishCtx, record.JobID)
	}
	_ = r.binding.Store.FinishWithCode(finishCtx, record.JobID, status, code)
	record.Status, record.ErrorCode = status, code
	if j, e := r.binding.Store.Get(finishCtx, record.JobID); e == nil && terminal(j.Status) {
		record.Status = j.Status
	}
	record.OutputAbsent = record.RuleAuditRef == "" && record.ModelOutputRef == ""
}
func storageFailureCode(code string) string {
	switch code {
	case "MODEL_CALL_LIMIT":
		return "AGENT_MODEL_LIMIT"
	case "TOOL_CALL_LIMIT", "TOOL_BATCH_LIMIT", "FIXED_PIPELINE_SELECTION_LIMIT":
		return "AGENT_TOOL_LIMIT"
	case "INVALID_FINAL", "FINAL_VALIDATION_FAILED", "MODEL_OUTPUT_INVALID", "FIXED_MODEL_TOOL_SELECTION_DENIED":
		return "AGENT_OUTPUT_INVALID"
	case "MODEL_TIMEOUT", "TASK_DEADLINE":
		return "REQUEST_DEADLINE_EXCEEDED"
	case "CANCELLED":
		return "REQUEST_CANCELLED"
	case "QUERY_REFERENCE_DENIED", "QUERY_OUTPUT_TOO_LARGE", "QUERY_TOOL_UNKNOWN", "QUERY_INPUT_INVALID", "MODEL_NOT_CONFIGURED", "MODEL_CONFIGURATION_INVALID", "MODEL_INITIALIZATION_FAILED", "MODEL_REQUEST_FAILED", "MODEL_PROTOCOL_INVALID", "MODEL_ADAPTER_FAILED", "MODEL_OUTPUT_FILTERED", "MODEL_OUTPUT_TRUNCATED", "MODEL_REQUEST_INVALID", "MODEL_REQUEST_TOO_LARGE", "MODEL_RESPONSE_READ_FAILED", "MODEL_RESPONSE_TOO_LARGE", "REQUEST_CANCELLED", "REQUEST_DEADLINE_EXCEEDED", "ATTEMPT_LIMIT", "EXECUTION_CLOSED", "BUDGET_NOT_CONFIGURED", "BUDGET_EXPIRED", "EFFECT_DENIED", "CONFIGURATION_STALE", "ATTEMPT_RECORD_FAILED", "ADMISSION_UNAVAILABLE", "OUTBOUND_STOPPING", "DESTINATION_REFUSED", "UPSTREAM_TRANSPORT", "UPSTREAM_HTTP_ERROR", "UPSTREAM_READ", "UPSTREAM_RESPONSE_TOO_LARGE", "UPSTREAM_ENCODING_UNSUPPORTED", "UPSTREAM_PROTOCOL", "REDIRECT_REFUSED":
		return code
	default:
		return "AGENT_EXECUTION_FAILED"
	}
}

type sampledModel struct {
	inner    model.BaseChatModel
	sampling Sampling
}

func (m *sampledModel) Generate(ctx context.Context, messages []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	options := append([]model.Option{}, opts...)
	options = append(options, model.WithTemperature(m.sampling.Temperature), model.WithTopP(m.sampling.TopP))
	return m.inner.Generate(ctx, messages, options...)
}
func (m *sampledModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, agentrun.Controlled("STREAMING_DISABLED")
}
