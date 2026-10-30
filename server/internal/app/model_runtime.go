package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/url"

	"cfw-policy-llm-agent/server/internal/aliyunapi"
	"cfw-policy-llm-agent/server/internal/aliyuncollector"
	"cfw-policy-llm-agent/server/internal/cloudconfig"
	"cfw-policy-llm-agent/server/internal/domain"
	"cfw-policy-llm-agent/server/internal/modelconfig"
	"cfw-policy-llm-agent/server/internal/modelprotocol"
	"cfw-policy-llm-agent/server/internal/outbound"
	"cfw-policy-llm-agent/server/internal/storage"
	"cfw-policy-llm-agent/server/internal/workload"
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
	return NewRuntimeWithExecution(store, cfg, cloud, workload.Config{}, additionalSecrets...)
}
func NewRuntimeWithExecution(store *storage.Store, cfg modelconfig.Config, cloud cloudconfig.Config, execution workload.Config, additionalSecrets ...string) (*ModelRuntime, error) {
	if store == nil {
		return nil, storage.ErrInputInvalid
	}
	if cfg.Summary().SourceKind == "env_file" && cfg.RequestedEnabled() && cfg.Budget().Ref == "" && !execution.HasBudget() {
		policy, e := store.BudgetForOwner(context.Background(), "application-root")
		if e == nil {
			cfg, e = cfg.WithBudget(modelconfig.Budget{Ref: policy.Ref, ExpiresAt: policy.ExpiresAt, ApplicationModelAttempts: int(policy.ApplicationModelAttempts), SessionModelAttempts: int(policy.SessionModelAttempts)})
			if e != nil {
				return nil, e
			}
		} else if !errors.Is(e, storage.ErrBudgetNotConfigured) {
			return nil, e
		}
	}
	var settingsError error
	cfg, cloud, settingsError = BindExecutionSettings(cfg, cloud, execution)
	if settingsError != nil {
		return nil, settingsError
	}
	runtime := &ModelRuntime{Plans: NewPlanService(store), SensitiveValues: append([]string(nil), additionalSecrets...)}
	runtime.Plans.Workloads = execution
	runtime.Plans.ModelTimeout = cfg.Timeout()
	if cfg.Summary().SourceKind == "env_file" {
		calls := cfg.MaxHTTPAttempts()
		runtime.Plans.ModelLogicalCalls = &calls
	}

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
		gatewayRegions := map[string]string{}
		for _, gateway := range cloud.Gateways() {
			gatewayIDs = append(gatewayIDs, gateway.ID)
			gatewayRegions[gateway.ID] = gateway.Region
		}
		for _, scope := range cloud.Scopes() {
			binding, ok := cloud.ScopeBinding(scope.Ref)
			if !ok {
				return nil, storage.ErrInputInvalid
			}
			runtime.Cloud.Scopes[scope.Ref] = aliyuncollector.Config{Scope: scope, ExpectedAccountID: cloud.ExpectedAccountID(), AssetRegions: assetRegions, AllowedGatewayIDs: gatewayIDs, GatewayRegions: gatewayRegions, GatewayID: binding.GatewayID, GatewayRegion: binding.Region, PageSize: 50, MaxAttempts: cloud.MaxHTTPAttempts(), MaxPolicies: 1000}
		}

		if policy, ok := execution.DataFreshness(); ok {
			runtime.Cloud.Freshness = DataFreshnessPolicy{Version: policy.Version, ConfigurationMaxAgeSeconds: policy.ConfigurationMaxAgeSeconds, MetadataMaxAgeSeconds: policy.MetadataMaxAgeSeconds}
		}
		runtime.Cloud.Grant = storage.ConnectionGrant{SourceRef: cloud.ConnectionRef(), ConfigRevision: runtime.Plans.ConfigRevision, AccountRef: cloud.AccountAlias(), ControlPlaneRef: cloud.ControlPlaneAlias(), Regions: cloud.Regions(), Boundaries: cloud.AllowedBoundaries()}
		for _, asset := range cloud.Assets() {
			runtime.Cloud.Grant.Assets = append(runtime.Cloud.Grant.Assets, storage.PreparedResource{ResourceID: asset.InstanceID, Region: asset.Region, ResourceType: "ecs"})
		}
		for _, gateway := range cloud.Gateways() {
			runtime.Cloud.Grant.Gateways = append(runtime.Cloud.Grant.Gateways, storage.PreparedResource{ResourceID: gateway.ID, Region: gateway.Region, ResourceType: "nat_gateway"})
		}
		runtime.Cloud.DiscoveryConfig = aliyuncollector.Config{SourceRef: cloud.ConnectionRef(), ExpectedAccountID: cloud.ExpectedAccountID(), AllowedRegions: cloud.Regions(), AssetRegions: assetRegions, AllowedGatewayIDs: gatewayIDs, GatewayRegions: gatewayRegions, Scope: domain.Scope{Ref: "directory_" + cloud.ConnectionRef(), Provider: "aliyun", AccountRef: cloud.AccountAlias(), ControlPlaneRef: cloud.ControlPlaneAlias()}, PageSize: 50}
		runtime.Cloud.ResolveScope = func(boundary, direction string, resources []domain.Resource) (aliyuncollector.Config, error) {
			if !cloud.AllowsBoundary(boundary + "/" + direction) {
				return aliyuncollector.Config{}, ErrScopeDenied
			}
			var binding cloudconfig.Binding
			found := false
			if boundary == "nat" {
				if len(resources) != 1 {
					return aliyuncollector.Config{}, ErrScopeDenied
				}
				binding, found = cloud.ScopeForGateway(resources[0].Region, resources[0].ResourceID)
			} else {
				for _, scope := range cloud.Scopes() {
					if scope.Boundary == boundary && scope.Direction == direction {
						binding, found = cloud.ScopeBinding(scope.Ref)
						break
					}
				}
				for _, r := range resources {
					if !cloud.AllowsAsset(r.Region, r.ResourceID) {
						return aliyuncollector.Config{}, ErrScopeDenied
					}
				}
			}
			if !found {
				return aliyuncollector.Config{}, ErrScopeDenied
			}
			return aliyuncollector.Config{Scope: binding.Scope, SourceRef: cloud.ConnectionRef(), ExpectedAccountID: cloud.ExpectedAccountID(), AllowedRegions: cloud.Regions(), GatewayID: binding.GatewayID, GatewayRegion: binding.Region, AllowedGatewayIDs: gatewayIDs, GatewayRegions: gatewayRegions, PageSize: 50}, nil
		}
		runtime.Plans.Cloud = runtime.Cloud
		runtime.SensitiveValues = append(runtime.SensitiveValues, cloud.SensitiveValues()...)
	}
	policy, policyErr := runtimeAuthorization(store, cfg, cloud, execution)
	if policyErr != nil {
		return nil, policyErr
	}
	if execution.Configured() {
		digest := sha256.Sum256([]byte(runtime.Plans.ConfigRevision + "\x00" + execution.Revision()))
		runtime.Plans.ConfigRevision = "runtime_" + hex.EncodeToString(digest[:])[:32]
		if runtime.Cloud != nil {
			runtime.Cloud.ConfigRevision = runtime.Plans.ConfigRevision
			runtime.Cloud.Grant.ConfigRevision = runtime.Plans.ConfigRevision
		}
	}
	if policy.Ref != "" {
		owners := []string{}
		if cfg.Summary().Configured && cfg.RequestedEnabled() {
			owners = append(owners, "model-default")
		}
		if cloud.Summary().Configured && cloud.RequestedEnabled() {
			owner := sha256.Sum256([]byte(cloud.ExpectedAccountID()))
			owners = append(owners, "cloud-account:"+hex.EncodeToString(owner[:16]))
		}
		if err := store.ConfigureBudgetForOwners(context.Background(), policy, owners); err != nil {
			return nil, err
		}
		runtime.Plans.BudgetRef = policy.Ref
	}
	modelCeiling := modelAdapterCeiling(cfg, cloud)
	modelEnabled := cfg.Enabled() && policy.ApplicationModelAttempts > 0 && policy.SessionModelAttempts > 0
	if modelCeiling != nil && (*modelCeiling.ApplicationAttempts <= 0 || *modelCeiling.SessionAttempts <= 0) {
		modelEnabled = false
	}
	cloudEnabled := cloud.Enabled() && policy.ApplicationCloudAttempts > 0 && policy.SessionCloudAttempts > 0
	if runtime.Cloud != nil {
		runtime.Cloud.Ready = cloudEnabled
	}
	sources := []outbound.Source{}
	if modelEnabled {
		u, err := url.Parse(cfg.Endpoint())
		if err != nil {
			return nil, storage.ErrInputInvalid
		}
		origin := (&url.URL{Scheme: u.Scheme, Host: u.Host}).String()
		sources = append(sources, outbound.Source{Ceiling: modelCeiling, Ref: "configured-model", Class: "model", Origin: origin, Routes: []outbound.Route{{Method: "POST", Path: u.Path}}, Timeout: cfg.Timeout(), MaxResponseBytes: modelprotocol.MaxResponseBytes})
	}
	if cloudEnabled {
		entries := aliyunapi.Sources(cloud)
		b := cloud.Budget()
		for i := range entries {
			entries[i].Ceiling = adapterCeiling(b.Ref, b.ApplicationCloudAttempts, b.SessionCloudAttempts, b.ExpiresAt)
		}
		sources = append(sources, entries...)
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
		if cloudEnabled {
			// An identity response naturally contains the expected account.
			// Only credentials/private model endpoints are reflection blockers
			// at this protocol boundary; account projection stays server-side.
			factory, err := aliyunapi.New(manager, aliyunapi.Config{Connection: cloud, SensitiveValues: cloudSecrets})
			if err != nil {
				return fail(err)
			}
			runtime.Cloud.NewReader = func(ctx context.Context, jobID string) (CloudReader, error) { return factory.Reader(ctx, jobID) }
		}

	}
	if cloud.Summary().Configured {
		if err := store.RegisterConnectionGrant(context.Background(), runtime.Cloud.Grant); err != nil {
			runtime.Close()
			return nil, err
		}
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
