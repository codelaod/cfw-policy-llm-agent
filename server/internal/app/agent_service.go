package app

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"cfw-policy-llm-agent/server/internal/agentquery"
	"cfw-policy-llm-agent/server/internal/agentrun"
	"cfw-policy-llm-agent/server/internal/audit"
	"cfw-policy-llm-agent/server/internal/domain"
	"cfw-policy-llm-agent/server/internal/storage"
	"cfw-policy-llm-agent/server/internal/workload"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/eino-contrib/jsonschema"
)

type AgentModelFactory interface {
	NewModel(context.Context, string) (model.BaseChatModel, error)
}
type AgentModelFactoryFunc func(context.Context, string) (model.BaseChatModel, error)

func (f AgentModelFactoryFunc) NewModel(ctx context.Context, id string) (model.BaseChatModel, error) {
	return f(ctx, id)
}

type AgentService struct {
	Store           *storage.Store
	Factory         AgentModelFactory
	ConfigRevision  string
	SensitiveValues []string
	ModelTimeout    time.Duration
}

func NewAgentService(store *storage.Store, factory AgentModelFactory, revision string, sensitive []string) *AgentService {
	return &AgentService{Store: store, Factory: factory, ConfigRevision: revision, SensitiveValues: append([]string(nil), sensitive...), ModelTimeout: 0}
}

type agentQueryAdapter struct {
	service *agentquery.Service
	tools   []*schema.ToolInfo
}

func newAgentQueryAdapter(service *agentquery.Service) (*agentQueryAdapter, error) {
	adapter := &agentQueryAdapter{service: service}
	for _, definition := range service.Tools() {
		var params jsonschema.Schema
		if json.Unmarshal(definition.Parameters, &params) != nil {
			return nil, agentrun.Controlled("INVALID_TOOL_DEFINITIONS")
		}
		adapter.tools = append(adapter.tools, &schema.ToolInfo{Name: definition.Name, Desc: definition.Description, ParamsOneOf: schema.NewParamsOneOfByJSONSchema(&params)})
	}
	return adapter, nil
}
func (a *agentQueryAdapter) InitialContext() string    { return a.service.InitialContext() }
func (a *agentQueryAdapter) BeginTurn()                { a.service.BeginTurn() }
func (a *agentQueryAdapter) Tools() []*schema.ToolInfo { return a.tools }
func (a *agentQueryAdapter) Invoke(ctx context.Context, name, args string) (string, error) {
	result, err := a.service.Invoke(ctx, name, args)
	if err == nil {
		return result, nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return "", err
	}
	switch {
	case errors.Is(err, agentquery.ErrDenied):
		return "", agentrun.Controlled("QUERY_REFERENCE_DENIED")
	case errors.Is(err, agentquery.ErrTooLarge):
		return "", agentrun.Controlled("QUERY_OUTPUT_TOO_LARGE")
	case errors.Is(err, agentquery.ErrUnknownTool):
		return "", agentrun.Controlled("QUERY_TOOL_UNKNOWN")
	default:
		return "", agentrun.Controlled("QUERY_INPUT_INVALID")
	}
}
func (a *agentQueryAdapter) ValidateFinal(text string) (json.RawMessage, error) {
	output, err := a.service.ValidateFinal(text)
	if err != nil {
		return nil, agentrun.Controlled("MODEL_OUTPUT_INVALID")
	}
	encoded, err := json.Marshal(output)
	if err != nil {
		return nil, agentrun.Controlled("MODEL_OUTPUT_INVALID")
	}
	return encoded, nil
}

func agentFinalSchema() json.RawMessage {
	definition := jsonschema.Reflect(&agentquery.Output{})
	definition.Description = "Only use task-local references from tools. source_kind, assessment_basis, scope_ref and rule_version must match the fixed task. Roles are unconfirmed candidates: web,database,management,middleware,cache,queue,background,unknown. Recommendation kind is policy_change,coverage_followup,component_consideration,confirm_information. basis_kind is known_gap,applicability_assessment,unknown_followup. Target kind is asset,asset_service,business_intent,scope,protection_scope,policy,policy_set. Actions are confirm_intent,collect_missing_evidence,review_rule,conditional_restrict,conditional_allow_required. Policy changes must use the exact original service's confirmed access_ref and business_ref; field is source for internet/in and destination for out/NAT. Never emit literal IPs, arbitrary URLs, shell commands, SDK methods, or claims of actual reachability. Every recommendation requires evidence, conditions, impact and a manual next step. Missing information stays a question."
	encoded, _ := json.Marshal(definition)
	return encoded
}

func agentManifest(input domain.Input) domain.InputManifest {
	return domain.InputManifest{SnapshotVersion: input.SnapshotVersion, ProfileVersion: input.ProfileVersion, IntentVersion: input.IntentVersion, BaselineVersion: input.Baseline.Version, NormalizerVersion: input.NormalizerVersion, KernelVersion: input.KernelVersion, ProviderSemantics: input.Semantics, PolicyScope: input.Scope, EvaluationAt: input.EvaluationAt, CollectedAt: input.CollectedAt, SourceKind: input.SourceKind}
}

func agentFailureCode(code string) string {
	switch code {
	case "MODEL_CALL_LIMIT":
		return "AGENT_MODEL_LIMIT"
	case "TOOL_CALL_LIMIT", "TOOL_BATCH_LIMIT":
		return "AGENT_TOOL_LIMIT"
	case "INVALID_FINAL", "FINAL_VALIDATION_FAILED", "MODEL_OUTPUT_INVALID":
		return "AGENT_OUTPUT_INVALID"
	case "INVALID_HISTORY", "SENSITIVE_OR_INVALID_INPUT", "SENSITIVE_VALUE_BLOCKED", "MODEL_INPUT_INVALID", "MODEL_INPUT_LIMIT", "AGENT_CONTEXT_UNAVAILABLE":
		return "AGENT_CONTEXT_REJECTED"
	case "EVENT_SAVE_FAILED":
		return "AGENT_RESULT_SAVE_FAILED"
	case "CANCELLED":
		return "REQUEST_CANCELLED"
	case "TASK_DEADLINE", "MODEL_TIMEOUT":
		return "REQUEST_DEADLINE_EXCEEDED"
	case "MODEL_NOT_CONFIGURED", "MODEL_CONFIGURATION_INVALID", "MODEL_INITIALIZATION_FAILED", "MODEL_REQUEST_FAILED", "MODEL_PROTOCOL_INVALID", "MODEL_ADAPTER_FAILED", "MODEL_OUTPUT_FILTERED", "MODEL_OUTPUT_TRUNCATED", "MODEL_REQUEST_INVALID", "MODEL_REQUEST_TOO_LARGE", "MODEL_RESPONSE_READ_FAILED", "MODEL_RESPONSE_TOO_LARGE", "MODEL_STREAM_UNSUPPORTED", "QUERY_INPUT_INVALID", "QUERY_REFERENCE_DENIED", "QUERY_TOOL_UNKNOWN", "QUERY_OUTPUT_TOO_LARGE", "REQUEST_CANCELLED", "REQUEST_DEADLINE_EXCEEDED", "ATTEMPT_LIMIT", "EXECUTION_CLOSED", "BUDGET_NOT_CONFIGURED", "BUDGET_EXPIRED", "EFFECT_DENIED", "CONFIGURATION_STALE", "ATTEMPT_RECORD_FAILED", "ADMISSION_UNAVAILABLE", "OUTBOUND_STOPPING", "DESTINATION_REFUSED", "UPSTREAM_TRANSPORT", "UPSTREAM_HTTP_ERROR", "UPSTREAM_READ", "UPSTREAM_RESPONSE_TOO_LARGE", "UPSTREAM_ENCODING_UNSUPPORTED", "UPSTREAM_PROTOCOL", "REDIRECT_REFUSED":
		return code
	default:
		return "AGENT_EXECUTION_FAILED"
	}
}

// Run supplements the fixed task. It reads public history and local tools only;
// the factory's bound client is the sole external effect. A stage failure never
// edits or re-saves the deterministic audit result.
func (s *AgentService) Run(ctx context.Context, accepted storage.Job) (storage.AgentRecord, error) {
	partial := accepted.Kind == "audit"
	failure := func(code string) (storage.AgentRecord, error) {
		if ctx.Err() != nil {
			return storage.AgentRecord{}, ctx.Err()
		}
		return storage.AgentRecord{}, JobFailure{Code: code, Partial: partial}
	}
	if s == nil || s.Store == nil || s.Factory == nil {
		return failure("MODEL_NOT_CONFIGURED")
	}
	job, run, err := s.Store.ReadJobAudit(ctx, accepted.ID)
	if err != nil {
		return failure("AGENT_CONTEXT_REJECTED")
	}
	partial = job.Kind == "audit"
	if job.Status != "running" || job.Kind != "audit" && job.Kind != "assistant_turn" {
		return failure("AGENT_CONTEXT_REJECTED")
	}
	plan, err := s.Store.ExecutionPlanForJob(ctx, job.ID)
	if err != nil {
		return failure("AGENT_CONTEXT_REJECTED")
	}
	if plan.ConsumedJobID != job.ID || plan.Kind != job.Kind || plan.ConversationID != job.ConversationID || !plan.EffectCeiling.AllowModelRequests || plan.EffectCeiling.ModelHTTPAttempts < 1 {
		return failure("EFFECT_DENIED")
	}
	input, err := s.Store.GetJobInput(ctx, job.ID)
	if err != nil {
		return failure("AGENT_CONTEXT_REJECTED")
	}
	content := storage.AgentContent{JobID: job.ID, Manifest: agentManifest(input), Context: storage.ContextCoverage{DisclosureVersion: "minimal-v1"}}
	var result *domain.Result
	if job.Kind == "audit" {
		if run == nil {
			return failure("AGENT_CONTEXT_REJECTED")
		}
		result = &run.Result
	} else if plan.ContextReportID != "" {
		fixedReport, e := s.Store.GetReport(ctx, plan.ContextReportID)
		if e != nil {
			return failure("AGENT_CONTEXT_REJECTED")
		}
		fixed, e := s.Store.GetAudit(ctx, fixedReport.AuditID)
		if e != nil {
			return failure("AGENT_CONTEXT_REJECTED")
		}
		result = &fixed.Result
	}
	var stageErr error
	var queries *agentquery.Service
	if plan.ConfigRevision != s.ConfigRevision {
		content.ErrorCode = "CONFIGURATION_STALE"
	} else {
		validationCells := []int{}
		if plan.Workload.Limits.MaxQueryCells > 0 {
			validationCells = append(validationCells, plan.Workload.Limits.MaxQueryCells)
		}
		ctx, _ = audit.WithQueryCellLimit(ctx, plan.Workload.Limits.MaxQueryCells)
		boundCtx, boundErr := audit.WithInputObjectLimits(ctx, plan.Workload.Limits.AssetsPerBatch, plan.Workload.Limits.MaxPolicies, validationCells...)
		if boundErr != nil {
			stageErr = boundErr
		} else {
			queries, stageErr = agentquery.NewWithContext(boundCtx, input, result)
		}
		if stageErr != nil {
			content.ErrorCode = "AGENT_CONTEXT_REJECTED"
		} else {
			public, e := BuildAgentContext(ctx, s.Store, job.ID, queries.ProjectText)
			content.Context = storage.ContextCoverage{IncludedMessages: public.IncludedMessages, ExcludedMessages: public.ExcludedMessages, Truncated: public.Truncated, DisclosureVersion: public.DisclosureVersion}
			if e != nil {
				stageErr = e
				content.ErrorCode = "AGENT_CONTEXT_REJECTED"
			} else {
				adapter, e := newAgentQueryAdapter(queries)
				if e != nil {
					stageErr = e
					content.ErrorCode = "AGENT_EXECUTION_FAILED"
				} else {
					history := make([]agentrun.HistoryMessage, 0, len(public.History))
					for _, message := range public.History {
						history = append(history, agentrun.HistoryMessage{Role: message.Role, Text: message.Text})
					}
					stageLimit, limitErr := workload.Seconds(plan.Workload.Limits.ModelSeconds)
					if limitErr != nil || stageLimit <= 0 {
						return failure("MODEL_NOT_CONFIGURED")
					}
					stageCtx, stop := context.WithTimeout(ctx, stageLimit)
					sink := agentrun.SinkFunc(func(eventCtx context.Context, event agentrun.Event) error {
						payload := map[string]any{"phase": "L3", "tool": event.ToolName, "call_id": event.CallID, "status": event.Status, "elapsed_ms": event.ElapsedMS, "evidence_refs": event.EvidenceRefs}
						if e := s.Store.AppendJobEvent(eventCtx, job.ID, event.Type, payload); e != nil {
							return agentrun.Controlled("EVENT_SAVE_FAILED")
						}
						return nil
					})
					out, e := agentrun.Run(stageCtx, agentrun.Task{Kind: job.Kind, History: history, Queries: adapter, NewModel: func(modelCtx context.Context) (model.BaseChatModel, error) {
						return s.Factory.NewModel(modelCtx, job.ID)
					}}, agentrun.Config{Workload: plan.Workload, SensitiveValues: s.SensitiveValues, ModelTimeout: s.ModelTimeout, FinalSchema: agentFinalSchema()}, sink)
					content.ModelCalls, content.ToolCalls = out.ModelCalls, out.ToolCalls
					stageErr = e
					if stageErr == nil && stageCtx.Err() != nil {
						stageErr = stageCtx.Err()
					}
					if stageErr == nil {
						var output agentquery.Output
						if json.Unmarshal(out.Output, &output) != nil {
							content.ErrorCode = "AGENT_OUTPUT_INVALID"
						} else {
							content.Output = &output
						}
					} else {
						content.ErrorCode = agentFailureCode(out.ErrorCode)
					}
					stop()
				}
			}
			content.Bindings = queries.LocalBindings()
		}
	}
	if ctx.Err() != nil {
		content.Output = nil
		content.ErrorCode = "REQUEST_CANCELLED"
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			content.ErrorCode = "REQUEST_DEADLINE_EXCEEDED"
		}
	}
	if content.Output == nil && content.ErrorCode == "" {
		content.ErrorCode = "AGENT_EXECUTION_FAILED"
	}
	// Normal products and terminal replies remain cancellable while waiting for
	// SQLite. Only failure metadata may use a separate finite service context;
	// it cannot publish a late normal answer or authorize another HTTP call.
	saveBase := ctx
	if content.Output == nil && job.Kind == "audit" {
		saveBase = context.WithoutCancel(ctx)
	}
	if content.Output == nil && ctx.Err() != nil {
		saveBase = context.WithoutCancel(ctx)
	}
	saveCtx, finish := context.WithTimeout(saveBase, 5*time.Second)
	defer finish()
	var saved storage.AgentRecord
	if job.Kind == "assistant_turn" && ctx.Err() == nil {
		saved, err = s.Store.FinalizeAssistant(saveCtx, job.ID, content)
	} else {
		saved, err = s.Store.SaveJobAgentResult(saveCtx, job.ID, content)
	}
	if ctx.Err() != nil {
		return saved, ctx.Err()
	}
	if err != nil {
		return failure("AGENT_RESULT_SAVE_FAILED")
	}
	if content.ErrorCode != "" {
		return saved, JobFailure{Code: content.ErrorCode, Partial: partial}
	}
	return saved, nil
}
