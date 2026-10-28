// Package domain contains the immutable, versioned audit contracts. It has no
// HTTP, database, model framework, or cloud SDK dependencies.
package domain

import "encoding/json"

type Scope struct {
	Ref             string `json:"ref"`
	Provider        string `json:"provider"`
	AccountRef      string `json:"account_ref"`
	ControlPlaneRef string `json:"control_plane_ref"`
	IPFamily        string `json:"ip_family"`
	Boundary        string `json:"boundary"`
	Direction       string `json:"direction"`
	PolicySetRef    string `json:"policy_set_ref"`
	GatewayRef      string `json:"gateway_ref,omitempty"`
}

type Semantics struct {
	ProfileID     string `json:"profile_id"`
	Version       string `json:"version"`
	DefaultAction string `json:"default_action"`
	SourceRef     string `json:"source_ref"`
}

type Completeness struct {
	Declared               bool     `json:"declared"`
	EnumerationComplete    bool     `json:"enumeration_complete"`
	ObservationConsistency string   `json:"observation_consistency"`
	KnownGaps              []string `json:"known_gaps"`
}

type Protection struct {
	Applicable  bool   `json:"applicable"`
	Required    bool   `json:"required"`
	State       string `json:"state"`
	EvidenceRef string `json:"evidence_ref"`
}

type Selection struct {
	Kind     string   `json:"kind"`
	AssetIDs []string `json:"asset_ids"`
}

type PortRange struct {
	From int `json:"from"`
	To   int `json:"to"`
}
type AddressSet struct {
	Values []string `json:"values"`
	Ref    string   `json:"ref,omitempty"`
}
type PortSet struct {
	Values []PortRange `json:"values"`
	Ref    string      `json:"ref,omitempty"`
}
type AddressReference struct {
	ScopeRef string   `json:"scope_ref"`
	Complete bool     `json:"complete"`
	Members  []string `json:"members"`
}
type PortReference struct {
	ScopeRef string      `json:"scope_ref"`
	Complete bool        `json:"complete"`
	Members  []PortRange `json:"members"`
}

type Policy struct {
	ID          string                     `json:"id"`
	ScopeRef    string                     `json:"scope_ref"`
	Source      AddressSet                 `json:"source"`
	Destination AddressSet                 `json:"destination"`
	Protocol    string                     `json:"protocol"`
	Ports       PortSet                    `json:"ports"`
	Action      string                     `json:"action"`
	Priority    int                        `json:"priority"`
	OrderKnown  bool                       `json:"order_known"`
	Enabled     string                     `json:"enabled"`
	Conditions  map[string]json.RawMessage `json:"conditions,omitempty"`
}

type Access struct {
	ScopeRef      string      `json:"scope_ref"`
	Protocol      string      `json:"protocol"`
	Addresses     []string    `json:"addresses"`
	Ports         []PortRange `json:"ports"`
	Applicability string      `json:"applicability"`
}

type Service struct {
	ID                string      `json:"id"`
	Protocol          string      `json:"protocol"`
	Ports             []PortRange `json:"ports"`
	Role              string      `json:"role"`
	RoleStatus        string      `json:"role_status"`
	PublicAccess      string      `json:"public_access"`
	AllowedConfirmed  bool        `json:"allowed_confirmed"`
	AllowedComplete   bool        `json:"allowed_complete"`
	DenyAll           bool        `json:"deny_all"`
	Allowed           []Access    `json:"allowed"`
	RequiredConfirmed bool        `json:"required_confirmed"`
	Required          []Access    `json:"required"`
}

type Association struct {
	Status           string `json:"status"`
	GatewayRef       string `json:"gateway_ref"`
	ObservationPoint string `json:"observation_point"`
	ObservedAt       string `json:"observed_at"`
	EvidenceRef      string `json:"evidence_ref"`
}

type Asset struct {
	ID               string      `json:"id"`
	ScopeRef         string      `json:"scope_ref"`
	Address          string      `json:"address"`
	AddressObserved  bool        `json:"address_observed"`
	Association      Association `json:"association"`
	AllowedConfirmed bool        `json:"allowed_confirmed"`
	AllowedComplete  bool        `json:"allowed_complete"`
	DenyAll          bool        `json:"deny_all"`
	Allowed          []Access    `json:"allowed"`
	Services         []Service   `json:"services"`
}

type RiskPort struct {
	Protocol string `json:"protocol"`
	Port     int    `json:"port"`
	Topic    string `json:"topic"`
}
type Exception struct {
	CheckID    string `json:"check_id"`
	SubjectRef string `json:"subject_ref"`
	ServiceRef string `json:"service_ref"`
	ScopeRef   string `json:"scope_ref"`
	ExpiresAt  string `json:"expires_at"`
	Reason     string `json:"reason"`
	Confirmed  bool   `json:"confirmed"`
}
type Baseline struct {
	Version       string      `json:"version"`
	EnabledChecks []string    `json:"enabled_checks"`
	RiskPorts     []RiskPort  `json:"risk_ports"`
	Exceptions    []Exception `json:"exceptions"`
}

type Input struct {
	SchemaVersion     int                         `json:"schema_version"`
	SnapshotVersion   string                      `json:"snapshot_version"`
	ProfileVersion    string                      `json:"profile_version"`
	IntentVersion     string                      `json:"intent_version"`
	NormalizerVersion string                      `json:"normalizer_version"`
	KernelVersion     string                      `json:"kernel_version"`
	SourceKind        string                      `json:"source_kind"`
	EvaluationAt      string                      `json:"evaluation_at"`
	CollectedAt       string                      `json:"collected_at"`
	Scope             Scope                       `json:"scope"`
	Semantics         Semantics                   `json:"provider_semantics"`
	Completeness      Completeness                `json:"completeness"`
	Protection        Protection                  `json:"protection"`
	Selection         Selection                   `json:"selection"`
	Assets            []Asset                     `json:"assets"`
	Policies          []Policy                    `json:"policies"`
	AddressRefs       map[string]AddressReference `json:"address_refs"`
	PortRefs          map[string]PortReference    `json:"port_refs"`
	Baseline          Baseline                    `json:"baseline"`
}

type InputManifest struct {
	SnapshotVersion   string    `json:"snapshot_version"`
	ProfileVersion    string    `json:"profile_version"`
	IntentVersion     string    `json:"intent_version"`
	BaselineVersion   string    `json:"baseline_version"`
	NormalizerVersion string    `json:"normalizer_version"`
	KernelVersion     string    `json:"kernel_version"`
	ProviderSemantics Semantics `json:"provider_semantics"`
	PolicyScope       Scope     `json:"policy_scope"`
	EvaluationAt      string    `json:"evaluation_at"`
	CollectedAt       string    `json:"collected_at"`
	SourceKind        string    `json:"source_kind"`
}
type Query struct {
	SubjectRef   string      `json:"subject_ref"`
	ServiceRef   string      `json:"service_ref"`
	FixedAddress string      `json:"fixed_address"`
	Protocol     string      `json:"protocol"`
	Addresses    []string    `json:"addresses"`
	Ports        []PortRange `json:"ports"`
}
type Cell struct {
	AddressFrom string `json:"address_from"`
	AddressTo   string `json:"address_to"`
	PortFrom    int    `json:"port_from"`
	PortTo      int    `json:"port_to"`
	Action      string `json:"action"`
	PolicyRef   string `json:"policy_ref,omitempty"`
	Broad       bool   `json:"broad"`
	Reason      string `json:"reason,omitempty"`
}
type PathResult struct {
	Query          Query    `json:"query"`
	Cells          []Cell   `json:"cells"`
	Complete       bool     `json:"complete"`
	Reasons        []string `json:"reasons"`
	EvaluatedCells int      `json:"evaluated_cells"`
}
type Evidence struct {
	ID              string `json:"id"`
	SnapshotVersion string `json:"snapshot_version"`
	ScopeRef        string `json:"scope_ref"`
	SubjectRef      string `json:"subject_ref"`
	PolicyRef       string `json:"policy_ref,omitempty"`
	Field           string `json:"field"`
	Summary         string `json:"summary"`
	ObservedAt      string `json:"observed_at"`
}
type Finding struct {
	ID                  string   `json:"id"`
	RuleID              string   `json:"rule_id"`
	Subcheck            string   `json:"subcheck,omitempty"`
	RuleVersion         string   `json:"rule_version"`
	Origin              string   `json:"origin"`
	ScopeRef            string   `json:"policy_scope_ref"`
	SubjectRef          string   `json:"subject_ref"`
	ServiceRef          string   `json:"service_ref,omitempty"`
	IssueKey            string   `json:"issue_key"`
	Verdict             string   `json:"verdict"`
	AssessmentBasis     string   `json:"assessment_basis"`
	Assumptions         []string `json:"assumptions"`
	Severity            string   `json:"severity"`
	RemediationPriority string   `json:"remediation_priority"`
	Summary             string   `json:"summary"`
	QueryScope          Query    `json:"query_scope"`
	Differences         []Cell   `json:"differences"`
	EvidenceRefs        []string `json:"evidence_refs"`
	Limitations         []string `json:"limitations"`
	AcceptedException   bool     `json:"accepted_exception"`
}
type Coverage struct {
	Complete         bool     `json:"complete"`
	EvaluatedQueries int      `json:"evaluated_queries"`
	EvaluatedCells   int      `json:"evaluated_cells"`
	Limitations      []string `json:"limitations"`
}
type Result struct {
	SchemaVersion int           `json:"schema_version"`
	Manifest      InputManifest `json:"input_manifest"`
	Findings      []Finding     `json:"findings"`
	Evidence      []Evidence    `json:"evidence"`
	Coverage      Coverage      `json:"coverage"`
}
