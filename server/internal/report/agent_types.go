package report

import (
	"cfw-policy-llm-agent/server/internal/agentquery"
	"cfw-policy-llm-agent/server/internal/domain"
)

// AgentSupplement is server-captured product data, never a model tool or an
// upload contract. Bindings contain local identities and are not serialized.
type AgentSupplement struct {
	Output     *agentquery.Output                 `json:"-"`
	Bindings   map[string]agentquery.LocalBinding `json:"-"`
	ModelCalls int
	ToolCalls  int
	ErrorCode  string
	Usage      *AgentUsage
}

// AgentUsage counts model HTTP attempts. Nil counters remain unknown; a known
// zero is represented only when the server supplies this ledger projection.
type AgentUsage struct {
	PhysicalAttempts     int64
	KnownUsageResponses  int64
	UnknownUsageAttempts int64
	ReportedTokens       *domain.ModelUsage
	UsageTotalsOverflow  bool
}
type AgentUsageProjection struct {
	Status               string             `json:"status"`
	PhysicalAttempts     *int64             `json:"physical_attempts"`
	KnownUsageResponses  *int64             `json:"known_usage_responses"`
	UnknownUsageAttempts *int64             `json:"unknown_usage_attempts"`
	ReportedTokens       *domain.ModelUsage `json:"reported_tokens"`
	UsageTotalsOverflow  bool               `json:"usage_totals_overflow"`
	Qualification        string             `json:"qualification"`
}
type AgentTarget struct {
	Kind         string `json:"kind"`
	Alias        string `json:"alias"`
	AssetAlias   string `json:"asset_alias,omitempty"`
	ServiceAlias string `json:"service_alias,omitempty"`
}
type AgentRole struct {
	Alias                 string      `json:"alias"`
	Target                AgentTarget `json:"target"`
	Role                  string      `json:"role"`
	Status                string      `json:"status"`
	SupportingEvidence    []string    `json:"supporting_evidence"`
	ContradictingEvidence []string    `json:"contradicting_evidence"`
	Conditions            []Notice    `json:"conditions"`
	Impact                string      `json:"impact"`
	ManualNextStep        string      `json:"manual_next_step"`
}
type AgentRecommendation struct {
	Alias           string      `json:"alias"`
	Kind            string      `json:"kind"`
	Status          string      `json:"status"`
	Target          AgentTarget `json:"target"`
	BasisKind       string      `json:"basis_kind"`
	FindingAlias    string      `json:"finding_alias,omitempty"`
	BusinessAlias   string      `json:"business_alias,omitempty"`
	AccessAlias     string      `json:"access_alias,omitempty"`
	EvidenceAliases []string    `json:"evidence_aliases"`
	Action          string      `json:"action,omitempty"`
	Field           string      `json:"field,omitempty"`
	ComponentTopic  string      `json:"component_topic,omitempty"`
	Explanation     string      `json:"explanation"`
	Conditions      []Notice    `json:"conditions"`
	Impact          string      `json:"impact"`
	ManualNextStep  string      `json:"manual_next_step"`
}
type AgentEvidence struct {
	Alias       string      `json:"alias"`
	Target      AgentTarget `json:"target"`
	Explanation string      `json:"explanation"`
}
type AgentLayer struct {
	ProjectionVersion string                `json:"projection_version"`
	ModelStage        string                `json:"model_stage"`
	SourceKind        string                `json:"source_kind"`
	AssessmentBasis   string                `json:"assessment_basis"`
	ScopeAlias        string                `json:"scope_alias"`
	RuleVersionAlias  string                `json:"rule_version_alias"`
	ModelCalls        int                   `json:"model_calls"`
	ToolCalls         int                   `json:"tool_calls"`
	Qualification     string                `json:"qualification"`
	Error             *Notice               `json:"error,omitempty"`
	Usage             AgentUsageProjection  `json:"usage"`
	Roles             []AgentRole           `json:"roles"`
	Recommendations   []AgentRecommendation `json:"recommendations"`
	Evidence          []AgentEvidence       `json:"evidence"`
	PendingQuestions  int                   `json:"pending_questions"`
	Limitations       []Notice              `json:"limitations"`
}
