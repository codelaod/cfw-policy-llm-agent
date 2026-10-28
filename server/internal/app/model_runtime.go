package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"time"

	"cfw-policy-llm-agent/server/internal/aliyunapi"
	"cfw-policy-llm-agent/server/internal/aliyuncollector"
	"cfw-policy-llm-agent/server/internal/cloudconfig"
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
	Cloud           *CloudService
	SensitiveValues []string
	manager         *outbound.Manager
}

func NewModelRuntime(store *storage.Store, cfg modelconfig.Config) (*ModelRuntime, error) {
	return NewRuntime(store, cfg, cloudconfig.Config{})
}

// NewRuntime composes explicitly configured cloud and model adapters around one
// manager and authorization period. Neither construction nor local catalog
// registration checks a remote identity or sends provider requests.
func NewRuntime(store *storage.Store, cfg modelconfig.Config, cloud cloudconfig.Config, additionalSecrets ...string) (*ModelRuntime, error) {
	if store == nil {
		return nil, storage.ErrInputInvalid
	}
	runtime := &ModelRuntime{Plans: NewPlanService(store), SensitiveValues: append([]string(nil), additionalSecrets...)}
	cloudSecrets := append(append([]string(nil), additionalSecrets...), cfg.APIKey(), cfg.BaseURL(), cfg.Endpoint(), cloud.AccessKeyID(), cloud.AccessKeySecret(), cloud.SecurityToken())
	if cfg.Summary().Configured {
		runtime.Plans.ModelConfigured = true
		runtime.Plans.ConfigRevision = cfg.Revision()
		runtime.Plans.BudgetRef = cfg.Budget().Ref
		runtime.Plans.ModelMode = "chat_completions"
		runtime.Plans.ModelHTTPAttempts = cfg.MaxHTTPAttempts()
		runtime.SensitiveValues = append(runtime.SensitiveValues, cfg.APIKey(), cfg.BaseURL(), cfg.Endpoint())
	}
	if cloud.Summary().Configured {
		if cfg.Summary().Configured {
			digest := sha256.Sum256([]byte(cfg.Revision() + "\x00" + cloud.Revision()))
			runtime.Plans.ConfigRevision = "runtime_" + hex.EncodeToString(digest[:])[:32]
		} else {
			runtime.Plans.ConfigRevision = cloud.Revision()
			runtime.Plans.BudgetRef = cloud.Budget().Ref
		}
		runtime.Cloud = &CloudService{Store: store, SourceRef: cloud.ConnectionRef(), ConfigRevision: runtime.Plans.ConfigRevision, Configured: true, Ready: cloud.Enabled(), MaxHTTPAttempts: cloud.MaxHTTPAttempts(), SensitiveValues: cloudSecrets, Scopes: map[string]aliyuncollector.Config{}}
		assetRegions := map[string]string{}
		for _, asset := range cloud.Assets() {
			assetRegions[asset.InstanceID] = asset.Region
		}
		gatewayIDs := []string{}
		for _, gateway := range cloud.Gateways() {
			gatewayIDs = append(gatewayIDs, gateway.ID)
		}
		for _, scope := range cloud.Scopes() {
			binding, ok := cloud.ScopeBinding(scope.Ref)
			if !ok {
				return nil, storage.ErrInputInvalid
			}
			runtime.Cloud.Scopes[scope.Ref] = aliyuncollector.Config{Scope: scope, ExpectedAccountID: cloud.ExpectedAccountID(), AssetRegions: assetRegions, AllowedGatewayIDs: gatewayIDs, GatewayID: binding.GatewayID, GatewayRegion: binding.Region, PageSize: 50, MaxAttempts: cloud.MaxHTTPAttempts(), MaxPolicies: 1000}
		}
		runtime.Plans.Cloud = runtime.Cloud
		runtime.SensitiveValues = append(runtime.SensitiveValues, cloud.SensitiveValues()...)
	}
	// A second adapter never creates a second root authorization period. A
	// disabled connection does not grant cloud work or create a budget policy.
	if cfg.Enabled() && cloud.Enabled() && cfg.Budget().Ref != cloud.Budget().Ref {
		return nil, storage.ErrInputInvalid
	}
	policy := storage.BudgetPolicy{}
	if cfg.Enabled() {
		b := cfg.Budget()
		policy = storage.BudgetPolicy{Ref: b.Ref, ApplicationModelAttempts: int64(b.ApplicationModelAttempts), SessionModelAttempts: int64(b.SessionModelAttempts), ExpiresAt: b.ExpiresAt}
	}
	if cloud.Enabled() {
		b := cloud.Budget()
		if policy.Ref == "" {
			policy.Ref, policy.ExpiresAt = b.Ref, b.ExpiresAt
		}
		policy.ApplicationCloudAttempts, policy.SessionCloudAttempts = int64(b.ApplicationCloudAttempts), int64(b.SessionCloudAttempts)
		if cfg.Enabled() {
			// Both files describe the same period. Explicit zero remains a zero
			// ceiling; adding cloud work cannot expand an existing model grant.
			policy.ApplicationModelAttempts = min(policy.ApplicationModelAttempts, int64(b.ApplicationModelAttempts))
			policy.SessionModelAttempts = min(policy.SessionModelAttempts, int64(b.SessionModelAttempts))
			left, e1 := time.Parse(time.RFC3339Nano, policy.ExpiresAt)
			right, e2 := time.Parse(time.RFC3339Nano, b.ExpiresAt)
			if e1 != nil || e2 != nil {
				return nil, storage.ErrInputInvalid
			}
			if right.Before(left) {
				policy.ExpiresAt = b.ExpiresAt
			}
		}
	}
	modelEnabled := cfg.Enabled() && policy.ApplicationModelAttempts > 0 && policy.SessionModelAttempts > 0
	sources := []outbound.Source{}
	if modelEnabled {
		u, err := url.Parse(cfg.Endpoint())
		if err != nil {
			return nil, storage.ErrInputInvalid
		}
		origin := (&url.URL{Scheme: u.Scheme, Host: u.Host}).String()
		sources = append(sources, outbound.Source{Ref: "configured-model", Class: "model", Origin: origin, Routes: []outbound.Route{{Method: "POST", Path: u.Path}}, Timeout: cfg.Timeout(), MaxResponseBytes: modelprotocol.MaxResponseBytes})
	}
	if cloud.Enabled() {
		sources = append(sources, aliyunapi.Sources(cloud)...)
	}
	if len(sources) > 0 {
		manager, err := outbound.NewManager(store, outbound.Config{Revision: runtime.Plans.ConfigRevision, Sources: sources})
		if err != nil {
			return nil, err
		}
		runtime.manager = manager
		fail := func(err error) (*ModelRuntime, error) { manager.Close(); return nil, err }
		if modelEnabled {
			factory, err := modelprotocol.New(manager, modelprotocol.Config{BaseURL: cfg.BaseURL(), APIKey: cfg.APIKey(), Model: cfg.Model(), ProviderProfile: cfg.ProviderProfile(), MaxOutputTokens: cfg.MaxOutputTokens(), SensitiveValues: runtime.SensitiveValues})
			if err != nil {
				return fail(modelprotocol.Failure("MODEL_CONFIGURATION_INVALID"))
			}
			runtime.Factory, runtime.Plans.ModelReady = factory, true
		}
		if cloud.Enabled() {
			// An identity response naturally contains the expected account.
			// Only credentials/private model endpoints are reflection blockers
			// at this protocol boundary; account projection stays server-side.
			factory, err := aliyunapi.New(manager, aliyunapi.Config{Connection: cloud, SensitiveValues: cloudSecrets})
			if err != nil {
				return fail(err)
			}
			runtime.Cloud.NewReader = func(ctx context.Context, jobID string) (CloudReader, error) { return factory.Reader(ctx, jobID) }
		}
		if err := store.ConfigureBudgetPolicy(context.Background(), policy); err != nil {
			return fail(err)
		}
		runtime.Plans.BudgetRef = policy.Ref
	}
	if cloud.Summary().Configured {
		if err := store.RegisterConfiguredScopes(context.Background(), cloud.ConnectionRef(), runtime.Plans.ConfigRevision, cloud.Scopes()); err != nil {
			runtime.Close()
			return nil, err
		}
	}
	return runtime, nil
}

func (r *ModelRuntime) Close() {
	if r != nil && r.manager != nil {
		r.manager.Close()
	}
}
