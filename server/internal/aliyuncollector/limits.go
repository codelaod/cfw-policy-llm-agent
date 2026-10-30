package aliyuncollector

import (
	"cfw-policy-llm-agent/server/internal/workload"
	"sort"
	"strings"
)

// Workload is already resolved against configuration and authorization ceilings.
// Deprecated numeric fields are used only by the explicit legacy compatibility path.
func resolveConfig(cfg Config) (Config, error) {
	if cfg.Workload.ProfileRef == "" {
		if cfg.Workload != (workload.Resolved{}) {
			return cfg, Failure("COLLECTOR_CONFIGURATION_INVALID")
		}
		l := workload.Default("collect")
		if cfg.MaxAttempts != 0 {
			l.CloudHTTPAttempts = cfg.MaxAttempts
		}
		if cfg.MaxPolicies != 0 {
			l.MaxPolicies = cfg.MaxPolicies
		}
		if l.CloudHTTPAttempts < 1 || l.CloudHTTPAttempts > 100 || l.MaxPolicies < 1 || l.MaxPolicies > 1000 {
			return cfg, Failure("COLLECTOR_CONFIGURATION_INVALID")
		}
		cfg.Workload = workload.Resolved{ProfileRef: workload.LegacyProfileRef, Legacy: true, Limits: l}
	} else if !alias(cfg.Workload.ProfileRef) || (cfg.Workload.Limits.Validate("collect") != nil && cfg.Workload.Limits.Validate("audit") != nil) {
		return cfg, Failure("COLLECTOR_CONFIGURATION_INVALID")
	}
	cfg.MaxAttempts = cfg.Workload.Limits.CloudHTTPAttempts
	cfg.MaxPolicies = cfg.Workload.Limits.MaxPolicies
	if cfg.PageSize == 0 {
		cfg.PageSize = 50
	}
	if cfg.PageSize < 1 || cfg.PageSize > 50 {
		return cfg, Failure("COLLECTOR_CONFIGURATION_INVALID")
	}
	if len(cfg.AllowedRegions) == 0 {
		for _, region := range cfg.AssetRegions {
			cfg.AllowedRegions = append(cfg.AllowedRegions, region)
		}
		for _, region := range cfg.GatewayRegions {
			cfg.AllowedRegions = append(cfg.AllowedRegions, region)
		}
		if cfg.GatewayRegion != "" {
			cfg.AllowedRegions = append(cfg.AllowedRegions, cfg.GatewayRegion)
		}
	}
	cfg.AllowedRegions = unique(append([]string(nil), cfg.AllowedRegions...))
	sort.Strings(cfg.AllowedRegions)
	for _, region := range cfg.AllowedRegions {
		if !alias(region) || strings.TrimSpace(region) != region || strings.ContainsAny(region, " /\\") {
			return cfg, Failure("COLLECTOR_CONFIGURATION_INVALID")
		}
	}
	gateways := make(map[string]string, len(cfg.GatewayRegions))
	for id, region := range cfg.GatewayRegions {
		if !alias(id) || !strings.HasPrefix(id, "ngw-") || !allowedRegion(cfg, region) {
			return cfg, Failure("COLLECTOR_CONFIGURATION_INVALID")
		}
		gateways[id] = region
	}
	cfg.GatewayRegions = gateways
	return cfg, nil
}
func allowedRegion(cfg Config, region string) bool {
	for _, v := range cfg.AllowedRegions {
		if v == region {
			return true
		}
	}
	return false
}
func ecsPageSize(cfg Config) int {
	n := cfg.PageSize
	if n < 10 {
		n = 10
	}
	if n > 100 {
		n = 100
	}
	return n
}
func groupPageSize(cfg Config) int {
	n := cfg.PageSize
	if n < 10 {
		n = 10
	}
	if n > 1000 {
		n = 1000
	}
	return n
}

// GatewayRegions is the paired permission ceiling. The legacy ID slice, when
// present, can only narrow it; it cannot move a gateway into another region.
func gatewayAllowed(cfg Config, id, region string) bool {
	if len(cfg.GatewayRegions) > 0 && cfg.GatewayRegions[id] != region {
		return false
	}
	if len(cfg.AllowedGatewayIDs) > 0 {
		for _, allowed := range cfg.AllowedGatewayIDs {
			if id == allowed {
				return true
			}
		}
		return false
	}
	return true
}
