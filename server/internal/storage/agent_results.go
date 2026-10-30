package storage

import (
	"context"
	"encoding/json"
	"io"
	"reflect"
	"strings"

	"cfw-policy-llm-agent/server/internal/agentquery"
	"cfw-policy-llm-agent/server/internal/audit"
	"cfw-policy-llm-agent/server/internal/domain"
	"gorm.io/gorm"
)

const MaxAgentContentBytes = 256 << 10

type ContextCoverage struct {
	IncludedMessages  int    `json:"included_messages"`
	ExcludedMessages  int    `json:"excluded_messages"`
	Truncated         bool   `json:"truncated"`
	DisclosureVersion string `json:"disclosure_version"`
}

// AgentContent contains only validated product output and server-local bindings.
// Raw model messages, reasoning, credentials and tool protocol frames have no
// fields in this persistent contract. JobID also distinguishes equal-input jobs.
type AgentContent struct {
	JobID      string                             `json:"job_id"`
	Manifest   domain.InputManifest               `json:"input_manifest"`
	Output     *agentquery.Output                 `json:"output,omitempty"`
	Bindings   map[string]agentquery.LocalBinding `json:"bindings,omitempty"`
	ModelCalls int                                `json:"model_calls"`
	ToolCalls  int                                `json:"tool_calls"`
	ErrorCode  string                             `json:"error_code,omitempty"`
	Context    ContextCoverage                    `json:"context"`
}

type AgentRecord struct {
	ID        string       `json:"id"`
	JobID     string       `json:"job_id"`
	CreatedAt string       `json:"created_at"`
	Content   AgentContent `json:"content" gorm:"-"`
	Body      string       `json:"-"`
}

func migrateAgentResults(tx *gorm.DB) error {
	return tx.Exec(`CREATE TABLE IF NOT EXISTS job_agent_results(id TEXT PRIMARY KEY,job_id TEXT UNIQUE NOT NULL REFERENCES e0_jobs(id),created_at TEXT NOT NULL,body TEXT NOT NULL CHECK(json_valid(body) AND length(CAST(body AS BLOB))<=262144))`).Error
}

func validAgentErrorCode(code string) bool {
	return enum(code,
		"AGENT_EXECUTION_FAILED", "AGENT_MODEL_LIMIT", "AGENT_TOOL_LIMIT", "AGENT_OUTPUT_INVALID", "AGENT_CONTEXT_REJECTED", "AGENT_CONTEXT_UNAVAILABLE", "AGENT_RESULT_SAVE_FAILED",
		"MODEL_NOT_CONFIGURED", "MODEL_CONFIGURATION_INVALID", "MODEL_INITIALIZATION_FAILED", "MODEL_REQUEST_FAILED", "MODEL_PROTOCOL_INVALID", "MODEL_OUTPUT_INVALID", "MODEL_ADAPTER_FAILED", "MODEL_OUTPUT_FILTERED", "MODEL_OUTPUT_TRUNCATED", "MODEL_REQUEST_INVALID", "MODEL_REQUEST_TOO_LARGE", "MODEL_RESPONSE_READ_FAILED", "MODEL_RESPONSE_TOO_LARGE", "MODEL_STREAM_UNSUPPORTED", "MODEL_TEXT_PROJECTION_REJECTED", "MODEL_CALL_LIMIT", "TOOL_CALL_LIMIT", "TOOL_CALL_ID_INVALID", "TOOL_ARGUMENT_INVALID", "TOOL_REFERENCE_DENIED", "TOOL_EXECUTION_FAILED", "MODEL_TOOL_CALL_INVALID",
		"QUERY_INPUT_INVALID", "QUERY_REFERENCE_DENIED", "QUERY_TOOL_UNKNOWN", "QUERY_OUTPUT_TOO_LARGE",
		"REQUEST_CANCELLED", "REQUEST_DEADLINE_EXCEEDED", "ATTEMPT_LIMIT", "EXECUTION_CLOSED", "BUDGET_NOT_CONFIGURED", "BUDGET_EXPIRED", "EFFECT_DENIED", "CONFIGURATION_STALE", "ATTEMPT_RECORD_FAILED", "ADMISSION_UNAVAILABLE", "OUTBOUND_STOPPING", "DESTINATION_REFUSED", "UPSTREAM_TRANSPORT", "UPSTREAM_HTTP_ERROR", "UPSTREAM_READ", "UPSTREAM_RESPONSE_TOO_LARGE", "UPSTREAM_ENCODING_UNSUPPORTED", "UPSTREAM_PROTOCOL", "REDIRECT_REFUSED")
}

func normalizeAgentContent(content AgentContent) (normalized AgentContent, body string, err error) {
	if content.JobID == "" || len(content.JobID) > 128 || content.ModelCalls < 0 || content.ToolCalls < 0 || content.Context.IncludedMessages < 0 || content.Context.IncludedMessages > AgentContextMessageLimit || content.Context.ExcludedMessages < 0 || content.Context.ExcludedMessages > 1_000_000_000 || content.Context.DisclosureVersion != "minimal-v1" {
		return normalized, "", ErrInputInvalid
	}
	if content.Output != nil {
		if content.ModelCalls < 1 || content.ErrorCode != "" {
			return normalized, "", ErrInputInvalid
		}
		if _, err = marshalBounded(content.Output, agentquery.MaxOutputBytes); err != nil {
			return normalized, "", err
		}
	} else if !validAgentErrorCode(content.ErrorCode) {
		return normalized, "", ErrInputInvalid
	}
	if content.ToolCalls > 0 && content.ModelCalls == 0 {
		return normalized, "", ErrInputInvalid
	}
	if content.ToolCalls == 0 {
		for _, binding := range content.Bindings {
			if binding.Returned {
				return normalized, "", ErrInputInvalid
			}
		}
	}
	encoded, err := marshalBounded(content, MaxAgentContentBytes)
	if err != nil {
		return normalized, "", err
	}
	if err = json.Unmarshal(encoded, &normalized); err != nil {
		return normalized, "", ErrInputInvalid
	}
	return normalized, string(encoded), nil
}

func jobAgentResult(tx *gorm.DB, jobID string) (*AgentRecord, error) {
	var record AgentRecord
	r := tx.Raw("SELECT * FROM job_agent_results WHERE job_id=?", jobID).Scan(&record)
	if r.Error != nil {
		return nil, r.Error
	}
	if r.RowsAffected == 0 {
		return nil, nil
	}
	if len(record.Body) > MaxAgentContentBytes {
		return nil, ErrInputInvalid
	}
	decoder := json.NewDecoder(strings.NewReader(record.Body))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&record.Content) != nil {
		return nil, ErrInputInvalid
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF || record.Content.JobID != record.JobID {
		return nil, ErrInputInvalid
	}
	return &record, nil
}

func (s *Store) GetJobAgentResult(ctx context.Context, jobID string) (*AgentRecord, error) {
	var record *AgentRecord
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if _, err := get(tx, jobID); err != nil {
			return err
		}
		var err error
		record, err = jobAgentResult(tx, jobID)
		return err
	})
	return record, err
}

func validateAgentContent(tx *gorm.DB, job Job, content AgentContent) error {
	if job.Status != "running" {
		return ErrJobTerminal
	}
	if content.Output != nil {
		if e := batchPublicationOpen(tx, job); e != nil {
			return e
		}
	}
	if content.JobID != job.ID || !enum(job.Kind, "audit", "assistant_turn") {
		return ErrInputInvalid
	}
	plan, err := executionPlanForJob(tx, job)
	if err != nil {
		return err
	}
	if plan.ConsumedJobID != job.ID || plan.ConversationID != job.ConversationID || plan.Kind != job.Kind || !plan.EffectCeiling.AllowModelRequests || plan.EffectCeiling.ModelHTTPAttempts < 1 || job.Kind == "audit" && plan.Mode != "rules_and_agent" || job.Kind == "assistant_turn" && plan.Mode != "assistant" {
		return ErrInputInvalid
	}
	modelLimit, toolLimit := plan.Workload.Limits.ModelLogicalCalls, plan.Workload.Limits.ToolCalls
	if content.ModelCalls > modelLimit || content.ToolCalls > toolLimit {
		return ErrInputInvalid
	}
	input, err := jobInput(tx, job.ID)
	if err != nil {
		return err
	}
	if input.Scope.Ref != plan.PolicyScopeRef || !reflect.DeepEqual(content.Manifest, manifestFor(input)) {
		return ErrInputInvalid
	}
	var request struct {
		DisclosurePolicyID string `json:"disclosure_policy_id"`
	}
	if json.Unmarshal(plan.Request, &request) != nil || request.DisclosurePolicyID != "" && request.DisclosurePolicyID != content.Context.DisclosureVersion {
		return ErrInputInvalid
	}
	var result *domain.Result
	if job.Kind == "audit" {
		if job.ResultRef == "" {
			return ErrInputInvalid
		}
		run, err := auditRun(tx, job.ResultRef)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(run.Input, input) {
			return ErrInputInvalid
		}
		result = &run.Result
	} else {
		if job.ResultRef != "" {
			return ErrInputInvalid
		}
		if plan.ContextReportID != "" {
			report, err := reportRecord(tx, plan.ContextReportID)
			if err != nil {
				return err
			}
			run, err := auditRun(tx, report.AuditID)
			if err != nil {
				return err
			}
			if !reflect.DeepEqual(run.Input, input) {
				return ErrInputInvalid
			}
			result = &run.Result
		}
	}
	validationCells := []int{}
	if plan.Workload.Limits.MaxQueryCells > 0 {
		validationCells = append(validationCells, plan.Workload.Limits.MaxQueryCells)
	}
	boundCtx, boundErr := audit.WithInputObjectLimits(tx.Statement.Context, plan.Workload.Limits.AssetsPerBatch, plan.Workload.Limits.MaxPolicies, validationCells...)
	if boundErr != nil {
		return ErrInputInvalid
	}
	if content.Output != nil {
		if err = agentquery.ValidateBoundOutputContext(boundCtx, input, result, *content.Output, content.Bindings); err != nil {
			return ErrInputInvalid
		}
	} else if len(content.Bindings) > 0 {
		if err = agentquery.ValidateLocalBindingsContext(boundCtx, input, result, content.Bindings); err != nil {
			return ErrInputInvalid
		}
	}
	return nil
}

func saveJobAgentResult(tx *gorm.DB, job *Job, content AgentContent, body string) (record AgentRecord, err error) {
	old, err := jobAgentResult(tx, job.ID)
	if err != nil {
		return record, err
	}
	if old != nil {
		if old.Body != body {
			return record, ErrConflict
		}
		return *old, nil
	}
	if err = validateAgentContent(tx, *job, content); err != nil {
		return record, err
	}
	id, err := opaqueID("agent_result")
	if err != nil {
		return record, err
	}
	record = AgentRecord{ID: id, JobID: job.ID, CreatedAt: now(), Content: content, Body: body}
	if err = tx.Exec("INSERT INTO job_agent_results(id,job_id,created_at,body) VALUES(?,?,?,?)", id, job.ID, record.CreatedAt, body).Error; err != nil {
		return record, err
	}
	err = appendEvent(tx, job, "stage_changed", map[string]any{"phase": "L3", "stage": "agent_result_saved", "agent_result_ref": id})
	return record, err
}

func (s *Store) SaveJobAgentResult(ctx context.Context, jobID string, content AgentContent) (record AgentRecord, err error) {
	if err = ctx.Err(); err != nil {
		return record, err
	}
	normalized, body, err := normalizeAgentContent(content)
	if err != nil {
		return record, err
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		job, e := get(tx, jobID)
		if e != nil {
			return e
		}
		record, e = saveJobAgentResult(tx, &job, normalized, body)
		return e
	})
	if err != nil {
		record = AgentRecord{}
	}
	return
}

// FinalizeAssistant commits only accepted assistant work. A previously committed
// cancellation closes the whole transaction before normal model output is saved.
func (s *Store) FinalizeAssistant(ctx context.Context, jobID string, content AgentContent) (record AgentRecord, err error) {
	if err = ctx.Err(); err != nil {
		return record, err
	}
	normalized, body, err := normalizeAgentContent(content)
	if err != nil {
		return record, err
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		job, e := get(tx, jobID)
		if e != nil {
			return e
		}
		if job.Status != "running" {
			return ErrJobTerminal
		}
		if job.Kind != "assistant_turn" {
			return ErrInputInvalid
		}
		var turn ConversationTurn
		turnRow := tx.Raw("SELECT * FROM conversation_turns WHERE job_id=?", job.ID).Scan(&turn)
		if turnRow.Error != nil {
			return turnRow.Error
		}
		if turnRow.RowsAffected != 1 {
			return ErrInputInvalid
		}
		if turn.ReplyMessageID != "" {
			return ErrConflict
		}
		record, e = saveJobAgentResult(tx, &job, normalized, body)
		if e != nil {
			return e
		}
		plan, e := executionPlanForJob(tx, job)
		if e != nil {
			return e
		}
		status, code, role, origin, text := "completed", "", "assistant", "model", ""
		if normalized.Output != nil {
			text = normalized.Output.Explanation
		} else {
			status, code, role, origin, text = "failed", normalized.ErrorCode, "event", "application", "本轮助手未完成；请查看受控错误摘要，已有资料保持可查。"
		}
		message, replay, e := insertJobConversationMessage(tx, job.ID, MessageInput{ClientMessageID: "agent_reply_" + job.ID, Role: role, Origin: origin, PublicText: text, PolicyScopeRef: plan.PolicyScopeRef, JobID: job.ID, RefKind: "job", RefID: job.ID})
		if e != nil {
			return e
		}
		if replay {
			return ErrConflict
		}
		update := tx.Exec("UPDATE conversation_turns SET reply_message_id=? WHERE job_id=?", message.ID, job.ID)
		if update.Error != nil {
			return update.Error
		}
		if update.RowsAffected != 1 {
			return ErrInputInvalid
		}
		if e = appendEvent(tx, &job, "message_available", map[string]any{"message_ref": message.ID, "complete_message": true}); e != nil {
			return e
		}
		return finishJob(tx, &job, status, code)
	})
	if err != nil {
		record = AgentRecord{}
	}
	return
}
