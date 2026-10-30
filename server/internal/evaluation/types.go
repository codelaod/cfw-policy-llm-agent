// Package evaluation is a private experiment adapter over accepted application
// jobs. It adds no product endpoint, cloud capability or authorization period.
package evaluation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"regexp"
	"strings"
	"time"

	"cfw-policy-llm-agent/server/internal/agentquery"
	"cfw-policy-llm-agent/server/internal/agentrun"
	"cfw-policy-llm-agent/server/internal/audit"
	"cfw-policy-llm-agent/server/internal/domain"
	"cfw-policy-llm-agent/server/internal/modelprotocol"
	"cfw-policy-llm-agent/server/internal/sensitive"
	"cfw-policy-llm-agent/server/internal/storage"
	"cfw-policy-llm-agent/server/internal/workload"
	"github.com/eino-contrib/jsonschema"
)

type Arm string

const (
	RulesOnly      Arm = "rules_only"
	FixedModel     Arm = "fixed_model_pipeline"
	AgentSelection Arm = "agent_tool_selection"
)

func AllArms() []Arm { return []Arm{RulesOnly, FixedModel, AgentSelection} }

var ErrProtocol = errors.New("EVALUATION_PROTOCOL_INVALID")
var ErrBinding = errors.New("EVALUATION_BINDING_INVALID")
var ErrJournal = errors.New("EVALUATION_JOURNAL_UNAVAILABLE")
var ErrReplay = errors.New("EVALUATION_REPLAY_CONFLICT")
var publicRef = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
var shaDigest = regexp.MustCompile(`^[0-9a-f]{64}$`)
var controlledError = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,79}$`)

const ToolProjectionVersion = "agentquery-nine-v1"
const FinalSchemaVersion = "agentquery-output-v1"
const AgentPromptVersion = "agentrun-bounded-v1"
const FixedPromptVersion = "agentrun-fixed-v1"

type CaseDefinition struct {
	ID               string `json:"case_id"`
	ScenarioRef      string `json:"independent_scenario_ref"`
	Split            string `json:"split"`
	Repetitions      int    `json:"repetitions"`
	LabelVersionRef  string `json:"label_version_ref"`
	LabelReviewState string `json:"label_review_state"`
	LabelReviewRef   string `json:"label_review_ref,omitempty"`
}
type Sampling struct {
	Temperature float32 `json:"temperature"`
	TopP        float32 `json:"top_p"`
}
type Protocol struct {
	SchemaVersion         int                    `json:"schema_version"`
	Version               string                 `json:"protocol_version"`
	DatasetUse            string                 `json:"dataset_use"`
	Cases                 []CaseDefinition       `json:"cases"`
	CommonBriefVersion    string                 `json:"common_brief_version"`
	CommonBrief           string                 `json:"common_brief"`
	ModelConfigRef        string                 `json:"model_config_ref"`
	Sampling              Sampling               `json:"sampling"`
	Workload              workload.Resolved      `json:"workload"`
	ToolProjectionVersion string                 `json:"tool_projection_version"`
	FinalSchemaVersion    string                 `json:"final_schema_version"`
	AgentPromptVersion    string                 `json:"agent_prompt_version"`
	FixedPromptVersion    string                 `json:"fixed_prompt_version"`
	FixedPipeline         agentrun.FixedPipeline `json:"fixed_pipeline"`
}

// FrozenCase hides original business data from public run journals. Accessors
// return detached copies; labels have no field here and never enter a model.
type FrozenCase struct {
	Definition       CaseDefinition    `json:"definition"`
	InputDigest      string            `json:"input_digest"`
	CommonRuleDigest string            `json:"common_rule_digest"`
	SourceKind       string            `json:"source_kind"`
	Boundary         string            `json:"boundary"`
	Direction        string            `json:"direction"`
	RuleWorkload     workload.Resolved `json:"rule_workload"`
	input            domain.Input
	rules            domain.Result
}
type Assignment struct {
	RunID      string `json:"run_id"`
	CaseID     string `json:"case_id"`
	Arm        Arm    `json:"arm"`
	Repetition int    `json:"repetition"`
	JobID      string `json:"job_id"`
}
type Conditions struct {
	ProtocolDigest        string            `json:"protocol_digest"`
	InputDigest           string            `json:"input_digest"`
	CommonRuleDigest      string            `json:"common_rule_digest"`
	CommonBriefVersion    string            `json:"common_brief_version"`
	ModelConfigRef        string            `json:"model_config_ref,omitempty"`
	Sampling              *Sampling         `json:"sampling,omitempty"`
	Workload              workload.Resolved `json:"workload"`
	ToolProjectionVersion string            `json:"tool_projection_version"`
	ToolCatalogDigest     string            `json:"tool_catalog_digest"`
	FinalSchemaDigest     string            `json:"final_schema_digest"`
	PromptVersion         string            `json:"prompt_version,omitempty"`
	PipelineVersion       string            `json:"pipeline_version,omitempty"`
}
type ToolTrace struct {
	Sequence        int      `json:"sequence"`
	CallID          string   `json:"call_id"`
	Tool            string   `json:"tool"`
	SelectionOrigin string   `json:"selection_origin"`
	Status          string   `json:"status"`
	ElapsedMS       int64    `json:"elapsed_ms"`
	InputRefs       []string `json:"input_refs"`
	DependsOn       []string `json:"depends_on_call_ids"`
	EvidenceRefs    []string `json:"evidence_refs"`
	Truncated       bool     `json:"projected_data_truncated"`
}

// Usage is actual durable ledger accounting. Nil means unreadable/unknown;
// missing token reports never become an invented zero-token response.
type Usage struct {
	Status   string                `json:"status"`
	Attempts *storage.AttemptUsage `json:"attempts,omitempty"`
}

// Recovery records the journal failure independently of locally verified task
// products. Unknown presence/counts must not be read as an absent output/zero.
type Recovery struct {
	State               string `json:"state"`
	JournalStatus       string `json:"journal_status"`
	JournalErrorCode    string `json:"journal_error_code"`
	DetectedAt          string `json:"detected_at"`
	CheckedAt           string `json:"checked_at,omitempty"`
	OutputPresence      string `json:"output_presence"`
	RuleOutputPresence  string `json:"rule_output_presence"`
	ModelOutputPresence string `json:"model_output_presence"`
	CountersStatus      string `json:"counters_status"`
	TraceStatus         string `json:"trace_status"`
	TimingStatus        string `json:"timing_status"`
}
type RunRecord struct {
	SchemaVersion        int         `json:"schema_version"`
	RunID                string      `json:"run_id"`
	CaseID               string      `json:"case_id"`
	ScenarioRef          string      `json:"independent_scenario_ref"`
	DatasetUse           string      `json:"dataset_use"`
	Split                string      `json:"split"`
	ProtocolVersion      string      `json:"protocol_version"`
	LabelVersionRef      string      `json:"label_version_ref"`
	Arm                  Arm         `json:"arm"`
	Repetition           int         `json:"repetition"`
	JobID                string      `json:"job_id"`
	Conditions           Conditions  `json:"conditions"`
	Status               string      `json:"status"`
	ErrorCode            string      `json:"error_code,omitempty"`
	OutputAbsent         bool        `json:"output_absent"`
	ModelOutputAbsent    bool        `json:"model_output_absent"`
	ModelLayer           string      `json:"model_layer"`
	RuleAuditRef         string      `json:"rule_audit_ref,omitempty"`
	ModelOutputRef       string      `json:"model_output_ref,omitempty"`
	ReportRef            string      `json:"report_ref,omitempty"`
	StartedAt            string      `json:"started_at"`
	FinishedAt           string      `json:"finished_at,omitempty"`
	QueueMillis          *int64      `json:"queue_ms,omitempty"`
	ExecutionMillis      int64       `json:"execution_ms"`
	ModelStageMillis     int64       `json:"model_stage_ms"`
	FinalizeMillis       int64       `json:"finalize_ms"`
	ModelCalls           int         `json:"logical_model_calls"`
	ToolCalls            int         `json:"tool_queries"`
	Traces               []ToolTrace `json:"tool_trace"`
	Usage                Usage       `json:"usage"`
	RecommendationCount  int         `json:"recommendation_count"`
	InferenceReview      string      `json:"inference_review"`
	RecommendationReview string      `json:"recommendation_review"`
	Replayed             bool        `json:"replayed"`
	Recovery             *Recovery   `json:"recovery,omitempty"`
}
type RunStart struct {
	Record        RunRecord `json:"record"`
	RequestDigest string    `json:"request_digest"`
}
type RunOutcome struct {
	Record RunRecord `json:"record"`
	// Protected local products are available for an independent scorer. Public
	// serialization contains only artifact references and controlled counts.
	ModelOutput *agentquery.Output                 `json:"-"`
	Bindings    map[string]agentquery.LocalBinding `json:"-"`
}
type Journal interface {
	Begin(context.Context, RunStart) (existing *RunRecord, reserved bool, err error)
	End(context.Context, RunRecord) error
}
type Binding struct {
	Store   *storage.Store
	Factory *modelprotocol.Factory
	Journal Journal
	// These are existing server authority references, not newly created grants.
	RuntimeRevision string
	BudgetRef       string
	ModelConfigRef  string
	Sampling        Sampling
	SensitiveValues []string
}
type Runner struct{ binding Binding }

func validDefinition(d CaseDefinition) bool {
	return publicRef.MatchString(d.ID) && publicRef.MatchString(d.ScenarioRef) && (d.Split == "development" || d.Split == "formal") && d.Repetitions >= 1 && d.Repetitions <= 100 && publicRef.MatchString(d.LabelVersionRef) && (d.LabelReviewState == "proposed" || d.LabelReviewState == "human_reviewed") && (d.LabelReviewRef == "" || publicRef.MatchString(d.LabelReviewRef))
}
func validSampling(s Sampling) bool {
	return !math.IsNaN(float64(s.Temperature)) && !math.IsInf(float64(s.Temperature), 0) && s.Temperature >= 0 && s.Temperature <= 2 && !math.IsNaN(float64(s.TopP)) && !math.IsInf(float64(s.TopP), 0) && s.TopP > 0 && s.TopP <= 1
}
func (p Protocol) Validate() error {
	if p.SchemaVersion != 1 || !publicRef.MatchString(p.Version) || (p.DatasetUse != "development" && p.DatasetUse != "formal") || len(p.Cases) < 1 || len(p.Cases) > 1000 || !publicRef.MatchString(p.CommonBriefVersion) || !publicRef.MatchString(p.ModelConfigRef) || strings.TrimSpace(p.CommonBrief) == "" || len(p.CommonBrief) > 16<<10 || !validSampling(p.Sampling) || p.ToolProjectionVersion != ToolProjectionVersion || p.FinalSchemaVersion != FinalSchemaVersion || p.AgentPromptVersion != AgentPromptVersion || p.FixedPromptVersion != FixedPromptVersion || p.FixedPipeline.Validate() != nil {
		return ErrProtocol
	}
	if _, changed := sensitive.Redact(p.CommonBrief, nil); changed {
		return ErrProtocol
	}
	normalized, e := workload.Normalize("audit", p.Workload)
	if e != nil || normalized != p.Workload {
		return ErrProtocol
	}
	ids, scenarios := map[string]bool{}, map[string]bool{}
	formal := 0
	for _, d := range p.Cases {
		if !validDefinition(d) || ids[d.ID] || scenarios[d.ScenarioRef] {
			return ErrProtocol
		}
		ids[d.ID], scenarios[d.ScenarioRef] = true, true
		if d.Split == "formal" {
			formal++
			if d.LabelReviewState != "human_reviewed" || d.LabelReviewRef == "" {
				return ErrProtocol
			}
		}
	}
	if p.DatasetUse == "formal" && (formal < 20 || formal > 30 || formal != len(p.Cases)) {
		return ErrProtocol
	}
	return nil
}
func digest(value any) (string, error) {
	raw, e := json.Marshal(value)
	if e != nil {
		return "", e
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}
func copyJSON[T any](value T) (out T, err error) {
	raw, e := json.Marshal(value)
	if e != nil {
		return out, e
	}
	err = json.Unmarshal(raw, &out)
	return
}
func FreezeCase(ctx context.Context, d CaseDefinition, input domain.Input, profile workload.Resolved, sensitiveValues ...string) (FrozenCase, error) {
	if !validDefinition(d) {
		return FrozenCase{}, ErrProtocol
	}
	normalized, e := workload.Normalize("audit", profile)
	if e != nil {
		return FrozenCase{}, ErrProtocol
	}
	raw, e := json.Marshal(input)
	if e != nil || len(raw) > audit.MaxInputBytes {
		return FrozenCase{}, ErrProtocol
	}
	if _, changed := sensitive.Redact(string(raw), sensitiveValues); changed {
		return FrozenCase{}, ErrProtocol
	}
	detached, e := copyJSON(input)
	if e != nil {
		return FrozenCase{}, ErrProtocol
	}
	limits := normalized.Limits
	bounded, e := audit.WithExecutionLimits(ctx, audit.ExecutionLimits{MaxAssets: limits.AssetsPerBatch, MaxPolicies: limits.MaxPolicies, MaxQueryCells: limits.MaxQueryCells, Timeout: time.Duration(limits.RulesSeconds) * time.Second})
	if e != nil {
		return FrozenCase{}, ErrProtocol
	}
	rules, e := audit.Run(bounded, detached)
	if e != nil {
		return FrozenCase{}, e
	}
	inputHash, _ := digest(detached)
	ruleHash, _ := digest(rules)
	return FrozenCase{Definition: d, InputDigest: inputHash, CommonRuleDigest: ruleHash, SourceKind: detached.SourceKind, Boundary: detached.Scope.Boundary, Direction: detached.Scope.Direction, RuleWorkload: normalized, input: detached, rules: rules}, nil
}
func (c FrozenCase) Input() (domain.Input, error)  { return copyJSON(c.input) }
func (c FrozenCase) Rules() (domain.Result, error) { return copyJSON(c.rules) }
func NewRunner(b Binding) (*Runner, error) {
	if b.Store == nil || b.Journal == nil || b.RuntimeRevision == "" || b.BudgetRef == "" || !publicRef.MatchString(b.ModelConfigRef) || !validSampling(b.Sampling) {
		return nil, ErrBinding
	}
	b.SensitiveValues = append([]string(nil), b.SensitiveValues...)
	return &Runner{binding: b}, nil
}
func finalSchema() json.RawMessage {
	definition := jsonschema.Reflect(&agentquery.Output{})
	definition.Description = "Only use task-local references returned in prior tool rounds. source_kind, assessment_basis, scope_ref and rule_version must match the fixed task. Roles are unconfirmed candidates: web,database,management,middleware,cache,queue,background,unknown. Recommendation kind is policy_change,coverage_followup,component_consideration,confirm_information. basis_kind is known_gap,applicability_assessment,unknown_followup. Target kind is asset,asset_service,business_intent,scope,protection_scope,policy,policy_set. Actions are confirm_intent,collect_missing_evidence,review_rule,conditional_restrict,conditional_allow_required. Policy changes must use the exact original service's confirmed access_ref and business_ref; field is source for internet/in and destination for out/NAT. Never emit literal IPs, arbitrary URLs, shell commands, SDK methods, or claims of actual reachability. Every recommendation requires evidence, conditions, impact and a manual next step. Missing information stays a question."
	raw, _ := json.Marshal(definition)
	return raw
}
func protocolDigest(p Protocol) (string, error) {
	if e := p.Validate(); e != nil {
		return "", e
	}
	return digest(p)
}
func terminal(value string) bool {
	return value == "completed" || value == "partial" || value == "failed" || value == "cancelled" || value == "interrupted"
}
