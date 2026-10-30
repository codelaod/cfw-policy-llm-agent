package domain

// ConversationIntent is a versioned customer goal. Execution authority comes
// from accepting its current server-owned proposal, never from this free text.
type ConversationIntent struct {
	BatchRequested bool `json:"batch_requested"`

	ResourceGroupVersionID string `json:"resource_group_version_id,omitempty"`

	ContextReportID string `json:"context_report_id,omitempty"`
	RecheckOfJobID  string `json:"recheck_of_job_id,omitempty"`

	AllowMultiple  bool              `json:"allow_multiple"`
	ID             string            `json:"id"`
	ConversationID string            `json:"conversation_id"`
	Revision       int64             `json:"revision"`
	Status         string            `json:"status"`
	Action         string            `json:"action"`
	Query          ResourceQuery     `json:"query"`
	Boundary       string            `json:"boundary"`
	Direction      string            `json:"direction"`
	Selection      []Resource        `json:"selection"`
	Candidates     []Resource        `json:"candidates"`
	Catalogue      CatalogueCoverage `json:"catalogue"`
	Clarification  string            `json:"clarification,omitempty"`
	DataPolicy     IntentDataPolicy  `json:"data_policy"`
	Mode           string            `json:"mode"`
	Proposal       *ActionProposal   `json:"proposal,omitempty"`
}
type ResourceQuery struct {
	Tags         map[string]string `json:"tags,omitempty"`
	ResourceIDs  []string          `json:"resource_ids,omitempty"`
	Regions      []string          `json:"regions,omitempty"`
	Name         string            `json:"name,omitempty"`
	ResourceID   string            `json:"resource_id,omitempty"`
	Region       string            `json:"region,omitempty"`
	ResourceType string            `json:"resource_type,omitempty"`
	Group        string            `json:"group,omitempty"`
}
type IntentDataPolicy struct {
	Mode       string `json:"mode"`
	SnapshotID string `json:"snapshot_id,omitempty"`
	FreshAfter string `json:"fresh_after,omitempty"`
}
type ActionProposal struct {
	ID                string          `json:"id"`
	IntentRevision    int64           `json:"intent_revision"`
	PlanID            string          `json:"plan_id,omitempty"`
	Summary           string          `json:"summary"`
	Gaps              []string        `json:"gaps"`
	Warnings          []string        `json:"warnings"`
	Effects           ProposalEffects `json:"effects"`
	RequiresDiscovery bool            `json:"requires_discovery"`
	Status            string          `json:"status"`
}
type ProposalEffects struct {
	AllowCloudReads    bool `json:"allow_cloud_reads"`
	AllowModelRequests bool `json:"allow_model_requests"`
}
