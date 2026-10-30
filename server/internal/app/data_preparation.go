package app

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"cfw-policy-llm-agent/server/internal/aliyuncollector"
	"cfw-policy-llm-agent/server/internal/audit"
	"cfw-policy-llm-agent/server/internal/domain"
	"cfw-policy-llm-agent/server/internal/storage"
)

func queryRegions(q domain.ResourceQuery) []string {
	if q.Region != "" {
		return []string{q.Region}
	}
	return append([]string(nil), q.Regions...)
}
func queryIDs(q domain.ResourceQuery) []string {
	if q.ResourceID != "" {
		return []string{q.ResourceID}
	}
	return append([]string(nil), q.ResourceIDs...)
}
func hasString(xs []string, x string) bool {
	for _, v := range xs {
		if x == v {
			return true
		}
	}
	return false
}

// preparationConfig uses only the accepted resource identities. The runtime
// resolver applies the original connection grant again to the actual region/ID.
func (s *CloudService) preparationConfig(boundary, direction string, resources []domain.Resource) (aliyuncollector.Config, domain.Selection, error) {
	if s.ResolveScope == nil {
		return aliyuncollector.Config{}, domain.Selection{}, ErrSourceNotConfigured
	}
	cfg, e := s.ResolveScope(boundary, direction, resources)
	if e != nil {
		return cfg, domain.Selection{}, e
	}
	selection := domain.Selection{Kind: "assets", AssetIDs: []string{}}
	if boundary == "nat" {
		selection.Kind = "gateway_policy"
		return cfg, selection, nil
	}
	cfg.AssetRegions = map[string]string{}
	for _, r := range resources {
		if r.ResourceType != "ecs" || r.ResourceID == "" || r.Region == "" {
			return cfg, selection, ErrScopeDenied
		}
		cfg.AssetRegions[r.ResourceID] = r.Region
		selection.AssetIDs = append(selection.AssetIDs, r.ResourceID)
	}
	sort.Strings(selection.AssetIDs)
	return cfg, selection, nil
}

func preparationVersions(boundary string) DataVersions {
	profile := "aliyun-cfw-internet-v1"
	if boundary == "nat" {
		profile = "aliyun-cfw-nat-v1"
	}
	return DataVersions{SchemaVersion: 1, NormalizerVersion: audit.NormalizerVersion, KernelVersion: audit.KernelVersion, SemanticsProfileID: profile, SemanticsVersion: audit.ProviderSemanticsVersion}
}
func (s *CloudService) reuseRequest(scope domain.Scope, selection domain.Selection, mode, freshAfter string) DataEligibilityRequest {
	policy := s.Freshness
	if policy.Version == "" {
		policy = LegacyDataFreshnessPolicy()
	}
	fresh, _ := time.Parse(time.RFC3339Nano, freshAfter)
	return DataEligibilityRequest{Scope: scope, Selection: selection, SourceRef: s.SourceRef, ConfigRevision: s.ConfigRevision, Versions: preparationVersions(scope.Boundary), Now: time.Now().UTC(), FreshAfter: fresh, Policy: policy, Mode: mode, RequireMetadata: true, RequireSecurityGroups: selection.Kind == "assets"}
}

func cloudContent(job storage.Job, p storage.ExecutionPlan, collected aliyuncollector.Collection) (storage.CloudContent, error) {
	content := storage.CloudContent{JobID: job.ID, Kind: "audit", ScopeRef: collected.Input.Scope.Ref, SourceRef: p.DataSourceID, ConfigRevision: p.ConfigRevision, IdentityVerified: true, ProductReady: true, Observations: []storage.CloudObservation{}, Limitations: []string{}}
	for _, o := range collected.Observations {
		content.Observations = append(content.Observations, storage.CloudObservation{Round: o.Round, Operation: o.Operation, Pages: o.Pages, StartedAt: o.StartedAt, FinishedAt: o.FinishedAt, EnumerationComplete: o.EnumerationComplete, SemanticSHA256: o.SemanticSHA256})
	}
	artifacts := make([]storage.CloudRawArtifact, 0, len(collected.Artifacts))
	for _, a := range collected.Artifacts {
		artifacts = append(artifacts, storage.CloudRawArtifact{Round: a.Round, Operation: a.Operation, Query: a.Query, Body: []byte(a.Body), SHA256: a.SHA256})
	}
	raw, e := json.Marshal(artifacts)
	if e != nil || len(raw) > 32<<20 {
		return content, JobFailure{Code: "COLLECTION_RAW_LIMIT"}
	}
	content.RawArtifacts = raw
	return content, nil
}

// PrepareAudit performs discovery and collection inside the original accepted
// audit. It creates no child job, new budget, or independent execution deadline.
func (s *CloudService) PrepareAudit(ctx context.Context, job storage.Job, p storage.ExecutionPlan) (domain.Input, error) {
	fail := func(code string) (domain.Input, error) { return domain.Input{}, workflowFailure(ctx, code, false) }
	if s == nil || s.Store == nil || p.Preparation == nil || p.Kind != "audit" || p.ConsumedJobID != job.ID || p.DataSourceID != s.SourceRef || p.ConfigRevision != s.ConfigRevision {
		return fail("PREPARATION_PLAN_INVALID")
	}
	if b, e := s.Store.GetPreparedAuditBinding(ctx, job.ID); e == nil {
		return b.Input, nil
	} else if !errors.Is(e, storage.ErrNotFound) {
		return domain.Input{}, e
	}
	spec := p.Preparation
	resources := []domain.Resource{}
	for _, r := range spec.Selected {
		resources = append(resources, domain.Resource{Provider: "aliyun", AccountRef: s.Grant.AccountRef, ResourceID: r.ResourceID, Region: r.Region, ResourceType: r.ResourceType, LocalAssetID: r.AssetID})
	}
	// Re-evaluate at execution, never merely trust preview eligibility.
	if len(resources) > 0 && spec.DataPolicy == "prefer_fresh" {
		cfg, sel, e := s.preparationConfig(spec.Boundary, spec.Direction, resources)
		if e != nil {
			return fail("PREPARATION_SCOPE_DENIED")
		}
		reuse, e := FindEligibleSnapshot(ctx, s.Store, s.reuseRequest(cfg.Scope, sel, spec.DataPolicy, spec.FreshAfter), 100)
		if e != nil {
			return domain.Input{}, e
		}
		if reuse.Reuse {
			b, e := s.Store.BindPreparedAuditInput(ctx, job.ID, reuse.SnapshotID)
			if e != nil {
				return fail("INPUT_BIND_FAILED")
			}
			if e = s.Store.AppendJobEvent(ctx, job.ID, "stage_changed", map[string]any{"phase": "L1", "stage": "qualified_data_reused", "snapshot_ref": reuse.SnapshotID, "freshness_policy": reuse.PolicyVersion}); e != nil {
				return domain.Input{}, e
			}
			return b.Input, nil
		}
	}
	if !s.Ready || s.NewReader == nil || !p.EffectCeiling.AllowCloudReads || p.EffectCeiling.CloudHTTPAttempts <= 0 {
		return fail("CLOUD_NOT_CONFIGURED")
	}
	collectCtx, cancel := context.WithTimeout(ctx, time.Duration(p.Workload.Limits.CollectionSeconds)*time.Second)
	defer cancel()
	reader, e := s.NewReader(collectCtx, job.ID)
	if e != nil {
		return domain.Input{}, cloudFailure(collectCtx, e, "CLOUD_CONFIGURATION_FAILED")
	}
	if e = reader.VerifyIdentity(collectCtx); e != nil {
		return domain.Input{}, cloudFailure(collectCtx, e, "CLOUD_IDENTITY_FAILED")
	}
	if e = s.Store.AppendJobEvent(collectCtx, job.ID, "stage_changed", map[string]any{"phase": "L1", "stage": "preparing_data"}); e != nil {
		return domain.Input{}, e
	}
	if len(resources) == 0 {
		cfg := s.DiscoveryConfig
		cfg.Workload = p.Workload
		cfg.SourceRef = s.SourceRef
		all := []domain.Resource{}
		coverages := []domain.CatalogueCoverage{}
		artifacts := []storage.CloudRawArtifact{}
		complete := true
		saveDiscovery := func(code string) error {
			raw, err := json.Marshal(artifacts)
			if err != nil {
				return err
			}
			saveCtx, cancelSave := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancelSave()
			_, err = s.Store.SaveAuditDiscovery(saveCtx, job.ID, all, coverages, raw, code)
			return err
		}
		discoveryCode := ""
		var discoveryErr error
		for _, region := range queryRegions(spec.Query) {
			ids := queryIDs(spec.Query)
			paired := cfg.AssetRegions
			if spec.Query.ResourceType == "nat_gateway" {
				paired = cfg.GatewayRegions
			}
			regionIDs := []string{}
			for _, id := range ids {
				if paired[id] == "" || paired[id] == region {
					regionIDs = append(regionIDs, id)
				}
			}
			if len(ids) > 0 && len(regionIDs) == 0 {
				continue
			}
			discovery, err := aliyuncollector.Discover(collectCtx, reader, cfg, aliyuncollector.Selector{Region: region, Type: spec.Query.ResourceType, Name: spec.Query.Name, ResourceIDs: regionIDs, Tags: spec.Query.Tags, MaxPages: p.EffectCeiling.CloudHTTPAttempts, MaxObjects: spec.ResultLimit + 1})
			encoded, _ := json.Marshal(discovery.Resources)
			if cloudContainsSecret(encoded, s.SensitiveValues) {
				return fail("CLOUD_RESPONSE_CONTAINS_CREDENTIAL")
			}
			for _, a := range discovery.Artifacts {
				q, _ := json.Marshal(a.Query)
				if cloudContainsSecret(q, s.SensitiveValues) || cloudContainsSecret(a.Body, s.SensitiveValues) {
					return fail("CLOUD_RESPONSE_CONTAINS_CREDENTIAL")
				}
				artifacts = append(artifacts, storage.CloudRawArtifact{Round: a.Round, Operation: a.Operation, Query: a.Query, Body: []byte(a.Body), SHA256: a.SHA256})
			}
			all = append(all, discovery.Resources...)
			if discovery.Coverage.ObservedAt != "" {
				coverages = append(coverages, discovery.Coverage)
			}
			complete = complete && discovery.Exhausted && discovery.Coverage.Complete
			if err != nil {
				discoveryErr = cloudFailure(collectCtx, err, "DISCOVERY_FAILED")
				discoveryCode = "DISCOVERY_FAILED"
				break
			}
			if len(all) > spec.ResultLimit {
				complete = false
				break
			}
		}
		if discoveryErr == nil {
			switch {
			case !complete || len(all) > spec.ResultLimit || !spec.AllowMultiple && len(all) > 1:
				discoveryCode = "DISCOVERY_NEEDS_CLARIFICATION"
			case len(all) == 0:
				discoveryCode = "DISCOVERY_NO_MATCH"
			case len(queryIDs(spec.Query)) > 0 && len(queryIDs(spec.Query)) != len(all):
				discoveryCode = "DISCOVERY_MISSING_OBJECTS"
			}
		}
		if e = saveDiscovery(discoveryCode); e != nil {
			return fail("DISCOVERY_SAVE_FAILED")
		}
		if discoveryErr != nil {
			return domain.Input{}, discoveryErr
		}
		if discoveryCode != "" {
			return fail(discoveryCode)
		}
		resources = all
	}
	cfg, selection, e := s.preparationConfig(spec.Boundary, spec.Direction, resources)
	if e != nil {
		return fail("PREPARATION_SCOPE_DENIED")
	}
	cfg.Workload = p.Workload
	var collected aliyuncollector.Collection
	if s.collect != nil {
		collected, e = s.collect(collectCtx, reader, cfg, selection)
	} else {
		var collector *aliyuncollector.Collector
		collector, e = aliyuncollector.New(reader, cfg)
		if e == nil {
			collected, e = collector.Collect(collectCtx, selection)
		}
	}
	if e != nil {
		return domain.Input{}, s.preserveCollectionFailure(collectCtx, job, p, cfg.Scope, collected, e)
	}
	encoded, e := json.Marshal(collected.Input)
	if e != nil || cloudContainsSecret(encoded, s.SensitiveValues) {
		return fail("CLOUD_RESPONSE_CONTAINS_CREDENTIAL")
	}
	for _, a := range collected.Artifacts {
		q, _ := json.Marshal(a.Query)
		if cloudContainsSecret(q, s.SensitiveValues) || cloudContainsSecret(a.Body, s.SensitiveValues) {
			return fail("CLOUD_RESPONSE_CONTAINS_CREDENTIAL")
		}
	}
	content, e := cloudContent(job, p, collected)
	if e != nil {
		return domain.Input{}, e
	}
	saved, e := s.Store.SavePreparedCloudSnapshot(collectCtx, job.ID, content, collected.Input)
	if e != nil {
		return domain.Input{}, s.collectionPublicationFailure(collectCtx, job, p, cfg.Scope, collected, "PREPARED_DATA_SAVE_FAILED")
	}
	b, e := s.Store.BindPreparedAuditInput(collectCtx, job.ID, saved.Content.SnapshotID)
	if e != nil {
		return fail("INPUT_BIND_FAILED")
	}
	return b.Input, nil
}
