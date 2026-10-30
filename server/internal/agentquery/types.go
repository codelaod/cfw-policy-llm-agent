// Package agentquery projects frozen B2 inputs into task-local read-only tools.
package agentquery

import (
	"encoding/json"
	"errors"
)

const MaxArgumentsBytes = 4 << 10
const MaxOutputBytes = 32 << 10

var (
	ErrInput       = errors.New("QUERY_INPUT_INVALID")
	ErrDenied      = errors.New("QUERY_REFERENCE_DENIED")
	ErrUnknownTool = errors.New("QUERY_TOOL_UNKNOWN")
	ErrTooLarge    = errors.New("QUERY_OUTPUT_TOO_LARGE")
	ErrFinal       = errors.New("MODEL_OUTPUT_INVALID")
	ErrProjection  = errors.New("MODEL_TEXT_PROJECTION_REJECTED")
)

type ToolDefinition struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}
type InitialView struct {
	EvaluationAt           string           `json:"evaluation_at"`
	CollectedAt            string           `json:"collected_at"`
	ObservationConsistency string           `json:"observation_consistency"`
	ScopeRef               string           `json:"scope_ref"`
	RuleVersion            string           `json:"rule_version"`
	SourceKind             string           `json:"source_kind"`
	AssessmentBasis        string           `json:"assessment_basis"`
	Boundary               string           `json:"boundary"`
	Direction              string           `json:"direction"`
	SnapshotRef            string           `json:"snapshot_ref"`
	ProfileRef             string           `json:"profile_ref"`
	IntentRef              string           `json:"intent_ref"`
	Assets                 []string         `json:"asset_refs"`
	Findings               []FindingSummary `json:"findings"`
	FindingTotal           int              `json:"finding_total"`
	FindingsTruncated      bool             `json:"findings_truncated"`
	Qualification          string           `json:"qualification"`
}
type FindingSummary struct {
	FindingRef string `json:"finding_ref"`
	RuleID     string `json:"rule_id"`
	Subcheck   string `json:"subcheck,omitempty"`
	Verdict    string `json:"verdict"`
}
type ToolResult struct {
	Status          string   `json:"status"`
	SourceKind      string   `json:"source_kind"`
	AssessmentBasis string   `json:"assessment_basis"`
	EvidenceRefs    []string `json:"evidence_refs"`
	Data            any      `json:"data"`
}

// One task's scope/version/guarantee applies to every candidate. Prose is model
// interpretation only and is never written over deterministic rule results.
type Output struct {
	ScopeRef        string           `json:"scope_ref"`
	RuleVersion     string           `json:"rule_version"`
	SourceKind      string           `json:"source_kind"`
	AssessmentBasis string           `json:"assessment_basis"`
	Explanation     string           `json:"explanation"`
	Roles           []RoleCandidate  `json:"roles"`
	Recommendations []Recommendation `json:"recommendations"`
	Questions       []string         `json:"questions"`
}
type RoleCandidate struct {
	AssetRef              string   `json:"asset_ref"`
	ServiceRef            string   `json:"service_ref,omitempty"`
	Role                  string   `json:"role"`
	SupportingEvidence    []string `json:"supporting_evidence"`
	ContradictingEvidence []string `json:"contradicting_evidence"`
	Unknowns              []string `json:"unknowns"`
	Questions             []string `json:"questions"`
}
type Recommendation struct {
	Kind           string   `json:"kind"`
	TargetKind     string   `json:"target_kind"`
	TargetRef      string   `json:"target_ref"`
	BasisKind      string   `json:"basis_kind"`
	EvidenceRefs   []string `json:"evidence_refs"`
	BusinessRef    string   `json:"business_ref,omitempty"`
	FindingRef     string   `json:"finding_ref,omitempty"`
	Action         string   `json:"action,omitempty"`
	AccessRef      string   `json:"access_ref,omitempty"`
	Field          string   `json:"field,omitempty"`
	ComponentTopic string   `json:"component_topic,omitempty"`
	Conditions     []string `json:"conditions"`
	Impact         string   `json:"impact"`
	ManualNextStep string   `json:"manual_next_step"`
}
