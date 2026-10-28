package app

import (
	"context"
	"net/url"

	"cfw-policy-llm-agent/server/internal/modelconfig"
	"cfw-policy-llm-agent/server/internal/modelprotocol"
	"cfw-policy-llm-agent/server/internal/outbound"
	"cfw-policy-llm-agent/server/internal/storage"
)

// ModelRuntime owns one application's shared physical admission. Factories
// create fresh Eino components for individual jobs, while counters and slots
// remain shared across conversations and survive configuration key rotation.
type ModelRuntime struct {
	Plans           *PlanService
	Factory         *modelprotocol.Factory
	SensitiveValues []string
	manager         *outbound.Manager
}

func NewModelRuntime(store *storage.Store, cfg modelconfig.Config) (*ModelRuntime, error) {
	if store == nil {
		return nil, storage.ErrInputInvalid
	}
	runtime := &ModelRuntime{Plans: NewPlanService(store)}
	if !cfg.Summary().Configured {
		return runtime, nil
	}
	runtime.Plans.ModelConfigured = true
	runtime.Plans.ConfigRevision = cfg.Revision()
	runtime.Plans.BudgetRef = cfg.Budget().Ref
	runtime.Plans.ModelMode = "chat_completions"
	runtime.Plans.ModelHTTPAttempts = cfg.MaxHTTPAttempts()
	runtime.SensitiveValues = []string{cfg.APIKey(), cfg.BaseURL(), cfg.Endpoint()}
	if !cfg.Enabled() {
		return runtime, nil
	}
	budget := cfg.Budget()
	if err := store.ConfigureBudgetPolicy(context.Background(), storage.BudgetPolicy{Ref: budget.Ref, ApplicationModelAttempts: int64(budget.ApplicationModelAttempts), SessionModelAttempts: int64(budget.SessionModelAttempts), ExpiresAt: budget.ExpiresAt}); err != nil {
		return nil, err
	}
	u, err := url.Parse(cfg.Endpoint())
	if err != nil {
		return nil, storage.ErrInputInvalid
	}
	origin := (&url.URL{Scheme: u.Scheme, Host: u.Host}).String()
	manager, err := outbound.NewManager(store, outbound.Config{Revision: cfg.Revision(), Sources: []outbound.Source{{Ref: "configured-model", Class: "model", Origin: origin, Routes: []outbound.Route{{Method: "POST", Path: u.Path}}, Timeout: cfg.Timeout(), MaxResponseBytes: modelprotocol.MaxResponseBytes}}})
	if err != nil {
		return nil, modelprotocol.Failure("MODEL_CONFIGURATION_INVALID")
	}
	factory, err := modelprotocol.New(manager, modelprotocol.Config{BaseURL: cfg.BaseURL(), APIKey: cfg.APIKey(), Model: cfg.Model(), ProviderProfile: cfg.ProviderProfile(), MaxOutputTokens: cfg.MaxOutputTokens(), SensitiveValues: runtime.SensitiveValues})
	if err != nil {
		manager.Close()
		return nil, modelprotocol.Failure("MODEL_CONFIGURATION_INVALID")
	}
	runtime.manager = manager
	runtime.Factory = factory
	runtime.Plans.ModelReady = true
	return runtime, nil
}

func (r *ModelRuntime) Close() {
	if r != nil && r.manager != nil {
		r.manager.Close()
	}
}
