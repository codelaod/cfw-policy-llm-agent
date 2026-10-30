package domain

// Resource is locator metadata, not proof of a listener, business ownership,
// access authorization or current protection. Identity never depends on Name.
type Resource struct {
	SourceNames     map[string]string `json:"source_names,omitempty"`
	IdentityKind    string            `json:"identity_kind"`
	LocalAssetID    string            `json:"local_asset_id,omitempty"`
	IdentityContext string            `json:"identity_context,omitempty"`
	IdentityRef     string            `json:"identity_ref"`
	ID              string            `json:"id"`
	Provider        string            `json:"provider"`
	AccountRef      string            `json:"account_ref"`
	Region          string            `json:"region"`
	ResourceType    string            `json:"resource_type"`
	ResourceID      string            `json:"resource_id"`
	Name            string            `json:"name"`
	Tags            map[string]string `json:"tags,omitempty"`
	OperatingSystem string            `json:"operating_system,omitempty"`
	Group           string            `json:"group,omitempty"`
	GroupConfirmed  bool              `json:"group_confirmed"`
	Source          string            `json:"source"`
	SourceRef       string            `json:"source_ref"`
	ObservedAt      string            `json:"observed_at"`
}

type CatalogueCoverage struct {
	AccountRef  string        `json:"account_ref,omitempty"`
	Selector    ResourceQuery `json:"selector"`
	Complete    bool          `json:"complete"`
	ObservedAt  string        `json:"observed_at,omitempty"`
	Source      string        `json:"source,omitempty"`
	Limitations []string      `json:"limitations"`
	Revision    int64         `json:"revision"`
}

// SecurityGroupEvidence preserves all provider conditions and the specific
// association. Missing or unsupported conditions are never reduced to ANY.
type SecurityGroupEvidence struct {
	ID          string                    `json:"id"`
	Region      string                    `json:"region"`
	Complete    bool                      `json:"complete"`
	ObservedAt  string                    `json:"observed_at"`
	Permissions []SecurityGroupPermission `json:"permissions"`
	Limitations []string                  `json:"limitations"`
}
type SecurityGroupPermission struct {
	Direction   string            `json:"direction"`
	Action      string            `json:"action"`
	Protocol    string            `json:"protocol"`
	Source      string            `json:"source"`
	Destination string            `json:"destination"`
	PortRange   string            `json:"port_range"`
	Priority    string            `json:"priority"`
	Conditions  map[string]string `json:"conditions,omitempty"`
}
