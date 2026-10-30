package aliyuncollector

import (
	"context"
	"errors"
	"strings"
	"time"

	"cfw-policy-llm-agent/server/internal/audit"
	"cfw-policy-llm-agent/server/internal/domain"
)

// A failed first pass may still contain bounded, verified observations. Only
// a full set of actually returned resource identities can become an input;
// missing objects, invalid responses and cancellation remain failure records.
func (r *run) firstPartialInput(started string, selection domain.Selection, first family, cause error) (domain.Input, bool) {
	cause = controlled(cause)
	if r.ctx.Err() != nil || errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) || fatal(cause) {
		return domain.Input{}, false
	}
	cfg := r.collector.cfg
	if selection.Kind == "assets" {
		present := map[string]bool{}
		for _, asset := range first.assets {
			present[asset.ID] = true
		}
		for _, id := range selection.AssetIDs {
			if present[id] {
				continue
			}
			row, observed := r.instances[id]
			if !observed || !r.instanceComplete[id] {
				return domain.Input{}, false
			}
			tags, err := instanceTags(row)
			if err != nil {
				return domain.Input{}, false
			}
			os := text(row, "OSType")
			if os == "" {
				os = text(row, "OSName")
			}
			// The identity was already returned and checked in collectInstances.
			// Missing downstream CFW/SG reads do not erase that observation and
			// cannot authorize any extra call after the failure.
			first.assets = append(first.assets, domain.Asset{ID: id, ScopeRef: cfg.Scope.Ref, ProviderResourceID: id, Region: cfg.AssetRegions[id], ResourceType: "ecs", Name: text(row, "InstanceName"), Tags: tags, OperatingSystem: os, MetadataComplete: true, Services: []domain.Service{}, Allowed: []domain.Access{}, Association: domain.Association{Status: "unknown"}, SecurityGroupCollection: "not_collected"})
		}
		if len(first.assets) != len(selection.AssetIDs) {
			return domain.Input{}, false
		}
		selected := map[string]bool{}
		for _, id := range selection.AssetIDs {
			selected[id] = true
		}
		for _, asset := range first.assets {
			if !selected[asset.ID] || asset.ProviderResourceID != asset.ID || asset.Region != cfg.AssetRegions[asset.ID] || asset.ResourceType != "ecs" || !asset.MetadataComplete {
				return domain.Input{}, false
			}
			delete(selected, asset.ID)
		}
		if len(selected) != 0 {
			return domain.Input{}, false
		}
	} else if first.gateway == nil || first.gateway.ResourceID != cfg.GatewayID || first.gateway.Region != cfg.GatewayRegion || first.gateway.ResourceType != "nat_gateway" {
		return domain.Input{}, false
	}
	gaps := append([]string{}, first.gaps...)
	gaps = append(gaps, "FIRST_OBSERVATION_INCOMPLETE", cause.Error(), "OBSERVATION_UNVERIFIED")
	protection := first.protection
	if protection.State != "unknown" {
		protection.State = "unknown"
		gaps = append(gaps, "PROTECTION_UNVERIFIED")
	}
	// A single protection observation does not certify current state, including
	// independently observed assets in a failed first pass.
	if cfg.Scope.Boundary == "internet" {
		for i := range first.assets {
			first.assets[i].Protection = assetProtectionAssertion(cfg.Scope.Ref, first.assets[i].ID, nil, false)
		}
	}
	for _, observation := range r.observations {
		if observation.Round == 1 {
			first.fingerprints = append(first.fingerprints, observation.Operation+"\x00"+queryKey(observation.Filters)+"\x00"+observation.SemanticSHA256)
		}
	}
	profile := "aliyun-cfw-internet-v1"
	if cfg.Scope.Boundary == "nat" {
		profile = "aliyun-cfw-nat-v1"
	}
	version := hash([]byte(started + strings.Join(first.fingerprints, "\x00")))
	input := domain.Input{SchemaVersion: 1, SnapshotVersion: "live-" + version[:32], ProfileVersion: "unconfirmed-v1", IntentVersion: "unconfirmed-v1", NormalizerVersion: audit.NormalizerVersion, KernelVersion: audit.KernelVersion, SourceKind: "live_readonly", EvaluationAt: time.Now().UTC().Format(time.RFC3339Nano), CollectedAt: started, Scope: cfg.Scope, Semantics: domain.Semantics{ProfileID: profile, Version: audit.ProviderSemanticsVersion, DefaultAction: "unknown", SourceRef: "T-06"}, Completeness: domain.Completeness{Declared: true, EnumerationComplete: false, ObservationConsistency: "unverified", KnownGaps: unique(gaps)}, Protection: protection, Selection: selection, Assets: first.assets, GatewayMetadata: first.gateway, Policies: first.policies, AddressRefs: first.addresses, PortRefs: first.ports, Baseline: audit.ProjectBaseline()}
	validationContext, err := audit.WithInputObjectLimits(r.ctx, cfg.Workload.Limits.AssetsPerBatch, cfg.MaxPolicies)
	if err != nil || audit.ValidateContext(validationContext, input) != nil {
		return domain.Input{}, false
	}
	return input, true
}
