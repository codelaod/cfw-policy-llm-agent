// Package aliyuncollector turns bounded, explicitly scoped provider observations
// into the same immutable input used by offline audits. It performs no credential
// lookup or endpoint discovery and exposes no provider operation to a model.
package aliyuncollector

import (
	"context"
	"encoding/json"

	"cfw-policy-llm-agent/server/internal/domain"
	"cfw-policy-llm-agent/server/internal/workload"
)

// Reader is a job-bound, identity-verified internal provider adapter. Implementors
// must preserve the original API body and enforce the shared physical ledger.
// New and Collect never widen the configured scope or choose another identity.
type Reader interface {
	Read(context.Context, string, map[string]string) (json.RawMessage, error)
}

type Config struct {
	Workload          workload.Resolved
	AllowedRegions    []string
	SourceRef         string
	Scope             domain.Scope
	ExpectedAccountID string
	AssetRegions      map[string]string
	GatewayRegions    map[string]string
	AllowedGatewayIDs []string
	GatewayID         string
	GatewayRegion     string
	PageSize          int
	MaxAttempts       int
	MaxPolicies       int
}

type RawArtifact struct {
	Round     int               `json:"round"`
	Operation string            `json:"operation"`
	Query     map[string]string `json:"query"`
	Body      json.RawMessage   `json:"body"`
	SHA256    string            `json:"sha256"`
}

type Observation struct {
	Round               int               `json:"round"`
	Operation           string            `json:"operation"`
	Filters             map[string]string `json:"filters"`
	Pages               int               `json:"pages"`
	StartedAt           string            `json:"started_at"`
	FinishedAt          string            `json:"finished_at"`
	EnumerationComplete bool              `json:"enumeration_complete"`
	SemanticSHA256      string            `json:"semantic_sha256"`
}

type Collection struct {
	Input        domain.Input  `json:"input"`
	Observations []Observation `json:"observations"`
	// Original provider bodies stay private. The caller may persist them only in
	// its private raw store; they are intentionally excluded from normal JSON.
	Artifacts []RawArtifact `json:"-"`
}

type Failure string

func (e Failure) Error() string     { return string(e) }
func (e Failure) ErrorCode() string { return string(e) }
