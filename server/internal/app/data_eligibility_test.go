package app

import (
	"cfw-policy-llm-agent/server/internal/audit"
	"cfw-policy-llm-agent/server/internal/domain"
	"cfw-policy-llm-agent/server/internal/storage"
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func eligibilityFixture(t *testing.T) (storage.SnapshotCandidate, DataEligibilityRequest) {
	t.Helper()
	now := time.Date(2026, 10, 29, 9, 0, 0, 0, time.UTC)
	in, err := audit.SyntheticExample("internet-in")
	if err != nil {
		t.Fatal(err)
	}
	stamp := now.Add(-5 * time.Minute).Format(time.RFC3339Nano)
	in.SourceKind = "live_readonly"
	in.CollectedAt = stamp
	in.Completeness.ObservationConsistency = "observed_stable"
	a := &in.Assets[0]
	a.Protection = &domain.Protection{Applicable: true, Required: true, State: "on", EvidenceRef: "asset-protection-artificial"}
	a.MetadataComplete = true
	a.Region = "cn-hangzhou"
	a.ResourceType = "ecs"
	a.ProviderResourceID = "i-artificial"
	a.SecurityGroupCollection = "complete"
	a.SecurityGroups = []domain.SecurityGroupEvidence{{ID: "sg-artificial", Region: a.Region, Complete: true, ObservedAt: stamp, Permissions: []domain.SecurityGroupPermission{}}}
	c := storage.SnapshotCandidate{Snapshot: storage.SnapshotRecord{ID: "snapshot-artificial", ScopeRef: in.Scope.Ref, SourceKind: in.SourceKind, CreatedAt: now.Format(time.RFC3339Nano), Input: in}, ProducerStatus: "completed", Cloud: &storage.CloudRecord{ID: "cloud-artificial", SourceRef: "configured-cloud", ConfigRevision: "config-current", Content: storage.CloudContent{Kind: "collect", ScopeRef: in.Scope.Ref, SnapshotID: "snapshot-artificial", IdentityVerified: true, ProductReady: true, Observations: []storage.CloudObservation{{Round: 1, Operation: "DescribeControlPolicy", Pages: 1, StartedAt: stamp, FinishedAt: stamp, EnumerationComplete: true}, {Round: 2, Operation: "DescribeControlPolicy", Pages: 1, StartedAt: stamp, FinishedAt: stamp, EnumerationComplete: true}, {Round: 1, Operation: "DescribeInstances", Pages: 1, StartedAt: stamp, FinishedAt: stamp, EnumerationComplete: true}}}}}
	req := DataEligibilityRequest{Scope: in.Scope, Selection: in.Selection, SourceRef: "configured-cloud", ConfigRevision: "config-current", Versions: DataVersions{SchemaVersion: in.SchemaVersion, NormalizerVersion: in.NormalizerVersion, KernelVersion: in.KernelVersion, SemanticsProfileID: in.Semantics.ProfileID, SemanticsVersion: in.Semantics.Version}, Now: now, Policy: LegacyDataFreshnessPolicy(), Mode: "prefer_fresh", RequireMetadata: true, RequireSecurityGroups: true}
	return c, req
}
func reasonHas(d DataEligibilityDecision, code string) bool {
	for _, r := range d.Reasons {
		if r == code {
			return true
		}
	}
	return false
}
func TestFreshCommittedEligibilityKeepsOriginalTimeAndProjectsSelection(t *testing.T) {
	c, req := eligibilityFixture(t)
	other := c.Snapshot.Input.Assets[0]
	other.ID = "outside-asset"
	other.Name = "must-not-enter-selected-input"
	c.Snapshot.Input.Assets = append(c.Snapshot.Input.Assets, other)
	c.Snapshot.Input.Selection.AssetIDs = append(c.Snapshot.Input.Selection.AssetIDs, other.ID)
	original := c.Snapshot.Input
	d := EvaluateDataEligibility(c, req)
	if !d.Eligible || len(d.Reasons) != 0 || d.Input == nil {
		t.Fatalf("fresh data rejected: %+v", d)
	}
	if len(d.Input.Assets) != 1 || d.Input.Assets[0].ID != req.Selection.AssetIDs[0] || d.Input.CollectedAt != original.CollectedAt || d.Provenance.Manifest.SnapshotVersion != original.SnapshotVersion {
		t.Fatal("projection widened selection or renewed original manifest/time")
	}
	d.Input.Assets[0].Tags = map[string]string{"changed": "only-result"}
	if !reflect.DeepEqual(c.Snapshot.Input, original) {
		t.Fatal("projection mutated committed candidate")
	}
	encoded, _ := json.Marshal(d)
	if strings.Contains(string(encoded), "must-not-enter-selected-input") || strings.Contains(string(encoded), "config-current") {
		t.Fatal("decision serialized broad/private candidate data")
	}
}

func TestPreparedAuditEligibilityUsesDataStatusAndStillChecksAllEvidence(t *testing.T) {
	for _, status := range []string{"completed", "partial", "failed", "cancelled", "interrupted", "queued", "running", "cancelling"} {
		t.Run(status, func(t *testing.T) {
			c, req := eligibilityFixture(t)
			c.Cloud.Content.Kind, c.ProducerStatus, c.DataStatus = "audit", status, "complete"
			d := EvaluateDataEligibility(c, req)
			if d.Eligible != storage.Terminal(status) {
				t.Fatal("parent outcome replaced data qualification", d.Reasons)
			}
		})
	}
	for _, damage := range []string{"data-partial", "data-unknown", "window", "config", "source", "incomplete", "metadata", "rounds"} {
		t.Run(damage, func(t *testing.T) {
			c, req := eligibilityFixture(t)
			c.Cloud.Content.Kind, c.ProducerStatus, c.DataStatus = "audit", "partial", "complete"
			switch damage {
			case "data-partial":
				c.DataStatus = "partial"
			case "data-unknown":
				c.DataStatus = ""
			case "window":
				req.Now = req.Now.Add(time.Hour)
			case "config":
				c.Cloud.ConfigRevision = "other-config"
			case "source":
				c.Cloud.SourceRef = "other-source"
			case "incomplete":
				c.Snapshot.Input.Completeness.EnumerationComplete = false
			case "metadata":
				c.Snapshot.Input.Assets[0].MetadataComplete = false
			case "rounds":
				c.Cloud.Content.Observations = c.Cloud.Content.Observations[:1]
			}
			if d := EvaluateDataEligibility(c, req); d.Eligible {
				t.Fatal("complete status bypassed evidence qualification")
			}
		})
	}
}
func TestEligibilityRejectsCurrentDataForEveryIndependentGap(t *testing.T) {
	cases := []struct {
		name, reason string
		change       func(*storage.SnapshotCandidate, *DataEligibilityRequest)
	}{
		{"stale", "CONFIGURATION_STALE", func(c *storage.SnapshotCandidate, r *DataEligibilityRequest) { r.Now = r.Now.Add(20 * time.Minute) }},
		{"changed", "CONFIGURATION_CHANGED", func(c *storage.SnapshotCandidate, r *DataEligibilityRequest) {
			c.Snapshot.Input.Completeness.ObservationConsistency = "changed"
		}},
		{"partial", "ENUMERATION_INCOMPLETE", func(c *storage.SnapshotCandidate, r *DataEligibilityRequest) {
			c.Snapshot.Input.Completeness.EnumerationComplete = false
		}},
		{"old-permission", "CONFIG_REVISION_MISMATCH", func(c *storage.SnapshotCandidate, r *DataEligibilityRequest) {
			c.Cloud.ConfigRevision = "older-permission"
		}},
		{"source", "SOURCE_MISMATCH", func(c *storage.SnapshotCandidate, r *DataEligibilityRequest) { c.Cloud.SourceRef = "another-cloud" }},
		{"object", "ASSET_NOT_COVERED", func(c *storage.SnapshotCandidate, r *DataEligibilityRequest) {
			r.Selection.AssetIDs = []string{"missing-asset"}
		}},
		{"direction", "SCOPE_MISMATCH", func(c *storage.SnapshotCandidate, r *DataEligibilityRequest) { r.Scope.Direction = "out" }},
		{"gateway", "SCOPE_MISMATCH", func(c *storage.SnapshotCandidate, r *DataEligibilityRequest) { r.Scope.GatewayRef = "other-gateway" }},
		{"normalizer", "NORMALIZER_VERSION_MISMATCH", func(c *storage.SnapshotCandidate, r *DataEligibilityRequest) { r.Versions.NormalizerVersion = "new" }},
		{"kernel", "KERNEL_VERSION_MISMATCH", func(c *storage.SnapshotCandidate, r *DataEligibilityRequest) { r.Versions.KernelVersion = "new" }},
		{"schema", "SCHEMA_VERSION_MISMATCH", func(c *storage.SnapshotCandidate, r *DataEligibilityRequest) { r.Versions.SchemaVersion = 2 }},
		{"semantics", "SEMANTICS_VERSION_MISMATCH", func(c *storage.SnapshotCandidate, r *DataEligibilityRequest) { r.Versions.SemanticsVersion = "new" }},
		{"after-change", "BEFORE_FRESH_AFTER", func(c *storage.SnapshotCandidate, r *DataEligibilityRequest) { r.FreshAfter = r.Now.Add(-time.Minute) }},
		{"known-change", "KNOWN_CHANGE_AFTER_SNAPSHOT", func(c *storage.SnapshotCandidate, r *DataEligibilityRequest) {
			r.KnownChangedAt = r.Now.Add(-time.Minute)
		}},
		{"producer-partial", "PRODUCER_PARTIAL", func(c *storage.SnapshotCandidate, r *DataEligibilityRequest) { c.ProducerStatus = "partial" }},
		{"producer-limitation", "PRODUCER_LIMITATIONS", func(c *storage.SnapshotCandidate, r *DataEligibilityRequest) {
			c.Cloud.Content.Limitations = []string{"OBSERVATION_UNVERIFIED"}
		}},
		{"missing-binding", "CLOUD_BINDING_MISSING", func(c *storage.SnapshotCandidate, r *DataEligibilityRequest) { c.Cloud = nil }},
		{"incomplete-page", "OBSERVATION_INCOMPLETE", func(c *storage.SnapshotCandidate, r *DataEligibilityRequest) {
			c.Cloud.Content.Observations[0].EnumerationComplete = false
		}},
		{"reference", "REFERENCE_NOT_CLOSED", func(c *storage.SnapshotCandidate, r *DataEligibilityRequest) {
			c.Snapshot.Input.Policies[0].Source.Ref = "missing-reference"
		}},
		{"sg-uncollected", "SECURITY_GROUPS_NOT_COLLECTED", func(c *storage.SnapshotCandidate, r *DataEligibilityRequest) {
			c.Snapshot.Input.Assets[0].SecurityGroupCollection = ""
		}},
		{"sg-partial", "SECURITY_GROUPS_PARTIAL", func(c *storage.SnapshotCandidate, r *DataEligibilityRequest) {
			c.Snapshot.Input.Assets[0].SecurityGroups[0].Complete = false
		}},
		{"sg-association", "SECURITY_GROUP_ASSOCIATION_INVALID", func(c *storage.SnapshotCandidate, r *DataEligibilityRequest) {
			c.Snapshot.Input.Assets[0].SecurityGroups[0].Region = "cn-beijing"
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, r := eligibilityFixture(t)
			tc.change(&c, &r)
			d := EvaluateDataEligibility(c, r)
			if d.Eligible || d.Input != nil || !reasonHas(d, tc.reason) {
				t.Fatalf("missing specific rejection %s: %+v", tc.reason, d)
			}
		})
	}
}
func TestPinnedAndOfflineArtifactDoNotPretendToBeCurrentCloud(t *testing.T) {
	c, r := eligibilityFixture(t)
	c.Snapshot.SourceKind = "imported"
	c.Snapshot.Input.SourceKind = "imported"
	c.Snapshot.Input.Completeness.ObservationConsistency = "fixed_artifact"
	c.Cloud = nil
	if d := EvaluateDataEligibility(c, r); d.Eligible || !reasonHas(d, "SOURCE_NOT_LIVE") {
		t.Fatal("import fulfilled current cloud")
	}
	r.Mode = "force_refresh"
	if d := EvaluateDataEligibility(c, r); d.Eligible || !reasonHas(d, "FORCE_REFRESH_REQUIRED") {
		t.Fatal("artifact fulfilled force refresh")
	}
	r.Mode = "pinned_snapshot"
	r.PinnedSnapshotID = c.Snapshot.ID
	r.Now = r.Now.Add(100 * 24 * time.Hour)
	c.Snapshot.Input.Completeness.EnumerationComplete = false
	d := EvaluateDataEligibility(c, r)
	if !d.Eligible || d.Provenance.Guarantee != "fixed_history" || len(d.Warnings) == 0 {
		t.Fatalf("explicit historical artifact was blocked or promoted: %+v", d)
	}
	r.Mode = "prefer_fresh"
	r.SourceRef = "local-offline"
	r.ConfigRevision = ""
	d = EvaluateDataEligibility(c, r)
	if !d.Eligible || d.Provenance.Guarantee != "imported_assumption" {
		t.Fatal("explicit offline artifact route lost")
	}
}
func TestKnownEmptySGCollectionIsDifferentFromUnknownAndTTLIsNotACompletenessProof(t *testing.T) {
	c, r := eligibilityFixture(t)
	c.Snapshot.Input.Assets[0].SecurityGroups = nil
	if d := EvaluateDataEligibility(c, r); !d.Eligible {
		t.Fatalf("known complete empty group association rejected: %+v", d)
	}
	c.Snapshot.Input.Assets[0].SecurityGroupCollection = "not_collected"
	if d := EvaluateDataEligibility(c, r); d.Eligible {
		t.Fatal("unknown group set accepted from fresh TTL")
	}
	r.Policy = DataFreshnessPolicy{Version: "explicit-short", ConfigurationMaxAgeSeconds: 1, MetadataMaxAgeSeconds: 1}
	if d := EvaluateDataEligibility(c, r); !reasonHas(d, "CONFIGURATION_STALE") {
		t.Fatal("explicit policy ignored")
	}
}

type candidateReader struct {
	items []storage.SnapshotCandidate
	calls int
}

func (r *candidateReader) SnapshotCandidates(context.Context, storage.SnapshotCandidateQuery) ([]storage.SnapshotCandidate, error) {
	r.calls++
	return r.items, nil
}
func TestFindDoesNotFallBackBeforeKnownChangeAndForceDoesNotSearch(t *testing.T) {
	old, r := eligibilityFixture(t)
	changed, _ := eligibilityFixture(t)
	changed.Snapshot.ID = "new-changed"
	changed.Cloud.Content.SnapshotID = changed.Snapshot.ID
	changed.Snapshot.Input.CollectedAt = r.Now.Add(-time.Minute).Format(time.RFC3339Nano)
	changed.Snapshot.Input.Completeness.ObservationConsistency = "changed"
	reader := &candidateReader{items: []storage.SnapshotCandidate{changed, old}}
	plan, err := FindEligibleSnapshot(context.Background(), reader, r, 10)
	if err != nil || plan.Reuse || plan.Input != nil || len(plan.Decisions) != 2 || !reasonHas(plan.Decisions[1], "KNOWN_CHANGE_AFTER_SNAPSHOT") {
		t.Fatalf("older stable cache escaped new known change: %+v %v", plan, err)
	}
	r.Mode = "force_refresh"
	before := reader.calls
	plan, err = FindEligibleSnapshot(context.Background(), reader, r, 10)
	if err != nil || plan.Reuse || reader.calls != before {
		t.Fatal("force refresh searched or reused cache", err)
	}
}

func TestFindPreparedAuditChangePreventsOlderStableFallback(t *testing.T) {
	for _, status := range []string{"completed", "partial", "failed", "cancelled", "interrupted", "running"} {
		t.Run(status, func(t *testing.T) {
			old, req := eligibilityFixture(t)
			changed, _ := eligibilityFixture(t)
			changed.Snapshot.ID = "new-prepared-changed"
			changed.Cloud.Content.SnapshotID = changed.Snapshot.ID
			changed.Cloud.Content.Kind = "audit"
			changed.ProducerStatus, changed.DataStatus = status, "partial"
			changed.Snapshot.Input.CollectedAt = req.Now.Add(-time.Minute).Format(time.RFC3339Nano)
			changed.Snapshot.Input.Completeness.ObservationConsistency = "changed"
			changed.Cloud.Content.Limitations = []string{"CONFIGURATION_NOT_STABLE"}
			reader := &candidateReader{items: []storage.SnapshotCandidate{changed, old}}
			plan, err := FindEligibleSnapshot(context.Background(), reader, req, 10)
			if err != nil {
				t.Fatal(err)
			}
			if storage.Terminal(status) {
				if plan.Reuse || !reasonHas(plan.Decisions[1], "KNOWN_CHANGE_AFTER_SNAPSHOT") {
					t.Fatal("new committed audit change allowed older stable cache", plan)
				}
			} else if !plan.Reuse {
				t.Fatal("uncommitted audit observation became cache change authority", plan)
			}
		})
	}
}

func TestFamilyWindowsRemainIndependentAndUseAdapterCanonicalTypes(t *testing.T) {
	c, r := eligibilityFixture(t)
	old := r.Now.Add(-50 * time.Minute).Format(time.RFC3339Nano)
	c.Snapshot.Input.CollectedAt = old
	c.Snapshot.Input.Assets[0].ResourceType = "ecs"
	r.RequireSecurityGroups = false
	for i := range c.Cloud.Content.Observations {
		if c.Cloud.Content.Observations[i].Operation == "DescribeInstances" {
			c.Cloud.Content.Observations[i].StartedAt = old
			c.Cloud.Content.Observations[i].FinishedAt = old
		}
	}
	d := EvaluateDataEligibility(c, r)
	if !d.Eligible {
		t.Fatalf("fresh configuration and eligible older metadata were conflated: %+v", d)
	}
	if d.Provenance.CollectedAt != old || d.Input.CollectedAt != old || d.Provenance.MetadataWindow.StartedAt != old || d.Provenance.ConfigurationWindow.StartedAt == old {
		t.Fatal("family windows or original manifest were renewed")
	}
	r.Policy.MetadataMaxAgeSeconds = 40 * 60
	d = EvaluateDataEligibility(c, r)
	if d.Eligible || !reasonHas(d, "METADATA_STALE") {
		t.Fatal("metadata-specific policy ignored")
	}
}
func TestNATGatewayAndAssetAssociationsDoNotCrossOrInferPaths(t *testing.T) {
	c, r := eligibilityFixture(t)
	c.Snapshot.Input.Scope.Boundary = "nat"
	c.Snapshot.Input.Scope.Direction = "out"
	c.Snapshot.Input.Scope.GatewayRef = "gateway-one"
	r.Scope = c.Snapshot.Input.Scope
	r.RequireNATAssociation = true
	d := EvaluateDataEligibility(c, r)
	if d.Eligible || !reasonHas(d, "NAT_ASSOCIATION_INCOMPLETE") {
		t.Fatal("missing NAT association was inferred")
	}
	c.Snapshot.Input.Assets[0].Association = domain.Association{Status: "evidenced", GatewayRef: "gateway-one", ObservationPoint: "before_translation", ObservedAt: c.Snapshot.Input.CollectedAt, EvidenceRef: "evidence-one"}
	if d = EvaluateDataEligibility(c, r); !d.Eligible {
		t.Fatalf("closed NAT relation rejected: %+v", d)
	}
	r.Scope.GatewayRef = "gateway-two"
	if d = EvaluateDataEligibility(c, r); d.Eligible || !reasonHas(d, "SCOPE_MISMATCH") {
		t.Fatal("other gateway cache reused")
	}
}

func TestFreshPoliciesCannotCertifyStaleGroupOrNATAssociations(t *testing.T) {
	c, r := eligibilityFixture(t)
	old := r.Now.Add(-20 * time.Minute).Format(time.RFC3339Nano)
	for i := range c.Cloud.Content.Observations {
		if c.Cloud.Content.Observations[i].Operation == "DescribeInstances" {
			c.Cloud.Content.Observations[i].StartedAt = old
			c.Cloud.Content.Observations[i].FinishedAt = old
		}
	}
	if d := EvaluateDataEligibility(c, r); d.Eligible || !reasonHas(d, "SECURITY_GROUP_ASSOCIATION_STALE") {
		t.Fatal("fresh SG rules certified stale instance-to-group binding")
	}
	c, r = eligibilityFixture(t)
	c.Snapshot.Input.Scope.Boundary = "nat"
	c.Snapshot.Input.Scope.Direction = "out"
	c.Snapshot.Input.Scope.GatewayRef = "gateway-one"
	r.Scope = c.Snapshot.Input.Scope
	r.RequireNATAssociation = true
	c.Snapshot.Input.Assets[0].Association = domain.Association{Status: "evidenced", GatewayRef: "gateway-one", ObservationPoint: "before_translation", ObservedAt: old, EvidenceRef: "old-association"}
	if d := EvaluateDataEligibility(c, r); d.Eligible || !reasonHas(d, "NAT_ASSOCIATION_STALE") {
		t.Fatal("fresh NAT ACL certified stale asset relation")
	}
}

func TestCommittedWarmReuseUsesStoreOnlyAndKeepsOriginalManifest(t *testing.T) {
	store, _, _, _, _ := planCase(t)
	c, r := eligibilityFixture(t)
	r.RequireMetadata = false
	r.RequireSecurityGroups = false
	if err := store.RegisterConfiguredScopes(context.Background(), r.SourceRef, r.ConfigRevision, []domain.Scope{r.Scope}); err != nil {
		t.Fatal(err)
	}
	conv, err := store.CreateConversation(context.Background(), "人工热缓存fixture", r.Scope.Ref)
	if err != nil {
		t.Fatal(err)
	}
	prepared := domain.Input{Scope: r.Scope, Selection: r.Selection, Baseline: c.Snapshot.Input.Baseline}
	p, err := store.SavePlan(context.Background(), storage.ExecutionPlan{Kind: "collect", ConversationID: conv.ID, ConversationRevision: conv.Revision, DataSourceID: r.SourceRef, PolicyScopeRef: r.Scope.Ref, Mode: "rules_only", ConfigRevision: r.ConfigRevision, BudgetRef: "artificial-cache-period", PreparedInput: prepared, EffectCeiling: storage.EffectCeiling{AllowCloudReads: true, CloudHTTPAttempts: 6}})
	if err != nil {
		t.Fatal(err)
	}
	job, _, err := store.EnqueuePlan(context.Background(), p.ID, "cache-producer-fixture", p.ConfigRevision)
	if err != nil {
		t.Fatal(err)
	}
	job, found, err := store.Claim(context.Background())
	if err != nil || !found {
		t.Fatal("producer claim", err)
	}
	observations := c.Cloud.Content.Observations
	for i := range observations {
		observations[i].SemanticSHA256 = strings.Repeat("a", 64)
	}
	produced, err := store.FinalizeCloud(context.Background(), job.ID, storage.CloudContent{JobID: job.ID, Kind: "collect", ScopeRef: r.Scope.Ref, SourceRef: r.SourceRef, ConfigRevision: r.ConfigRevision, IdentityVerified: true, ProductReady: true, Observations: observations, Limitations: []string{}}, &c.Snapshot.Input)
	if err != nil {
		t.Fatal(err)
	}
	before, err := store.GetSnapshot(context.Background(), produced.Content.SnapshotID)
	if err != nil {
		t.Fatal(err)
	}
	result, err := FindEligibleSnapshot(context.Background(), store, r, 10)
	if err != nil || !result.Reuse || result.NeedRefresh || result.SnapshotID != before.ID || result.Input.CollectedAt != before.Input.CollectedAt {
		t.Fatalf("committed warm cache unavailable: %+v %v", result, err)
	}
	after, err := store.GetSnapshot(context.Background(), before.ID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("warm lookup rewrote original snapshot", err)
	}
	usage, err := store.BudgetUsage(context.Background(), "artificial-cache-period", conv.ID, job.ID)
	if err != nil || usage.CloudAttempts != 0 || usage.ModelAttempts != 0 {
		t.Fatal("warm reuse performed cloud/identity/model work", err)
	}
}

func TestQualifiedBroadAuditSnapshotBindsAcceptedSubset(t *testing.T) {
	store, _, _, _, _ := planCase(t)
	candidate, req := eligibilityFixture(t)
	ctx := context.Background()
	in := candidate.Snapshot.Input
	in.Assets[0].Name = "web-first"
	other := in.Assets[0]
	other.ID, other.ProviderResourceID, other.Name = "asset-second", "i-second", "web-second"
	in.Assets = append(in.Assets, other)
	in.Selection.AssetIDs = append(in.Selection.AssetIDs, other.ID)
	grant := storage.ConnectionGrant{SourceRef: req.SourceRef, ConfigRevision: req.ConfigRevision, AccountRef: req.Scope.AccountRef, ControlPlaneRef: req.Scope.ControlPlaneRef, Regions: []string{"cn-hangzhou"}, Boundaries: []string{"internet/in"}}
	if err := store.RegisterConnectionGrant(ctx, grant); err != nil {
		t.Fatal(err)
	}
	if err := store.RegisterConfiguredScopes(ctx, req.SourceRef, req.ConfigRevision, []domain.Scope{req.Scope}); err != nil {
		t.Fatal(err)
	}
	producerConversation, err := store.CreateConversation(ctx, "broad cache producer", req.Scope.Ref)
	if err != nil {
		t.Fatal(err)
	}
	base := storage.ExecutionPlan{Kind: "audit", ConversationID: producerConversation.ID, ConversationRevision: producerConversation.Revision, DataSourceID: req.SourceRef, PolicyScopeRef: req.Scope.Ref, Mode: "rules_only", ConfigRevision: req.ConfigRevision, BudgetRef: "same-cache-budget", PreparedInput: domain.Input{Scope: req.Scope, Baseline: in.Baseline}, EffectCeiling: storage.EffectCeiling{AllowCloudReads: true, CloudHTTPAttempts: 6}, Preparation: &storage.PreparationSpec{SourceRef: req.SourceRef, ConfigRevision: req.ConfigRevision, Query: domain.ResourceQuery{Region: "cn-hangzhou", ResourceType: "ecs", Name: "web-*"}, Boundary: "internet", Direction: "in", DataPolicy: "prefer_fresh", AllowMultiple: true, ResultLimit: 2}}
	producerPlan, err := store.SavePlan(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	producer, _, err := store.EnqueuePlan(ctx, producerPlan.ID, "broad-producer")
	if err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.Claim(ctx); err != nil || !found {
		t.Fatal("producer claim", err)
	}
	content := candidate.Cloud.Content
	content.JobID, content.Kind, content.SnapshotID = producer.ID, "audit", ""
	content.SourceRef, content.ConfigRevision = req.SourceRef, req.ConfigRevision
	for i := range content.Observations {
		content.Observations[i].SemanticSHA256 = strings.Repeat("a", 64)
	}
	product, err := store.SavePreparedCloudSnapshot(ctx, producer.ID, content, in)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.FinishWithCode(ctx, producer.ID, "partial", "MODEL_FAILED"); err != nil {
		t.Fatal(err)
	}
	reuse, err := FindEligibleSnapshot(ctx, store, req, 100)
	if err != nil || !reuse.Reuse || reuse.SnapshotID != product.Content.SnapshotID || len(reuse.Input.Assets) != 1 {
		t.Fatal("broad cache was not qualified for selected subset", reuse, err)
	}
	consumerConversation, err := store.CreateConversation(ctx, "one accepted object", req.Scope.Ref)
	if err != nil {
		t.Fatal(err)
	}
	base.ConversationID, base.ConversationRevision = consumerConversation.ID, consumerConversation.Revision
	spec := *base.Preparation
	spec.Query.Name, spec.Query.ResourceID = "", in.Assets[0].ProviderResourceID
	spec.AllowMultiple, spec.ResultLimit = false, 1
	spec.Selected = []storage.PreparedResource{{ResourceID: in.Assets[0].ProviderResourceID, Region: in.Assets[0].Region, ResourceType: "ecs", AssetID: in.Assets[0].ID}}
	base.Preparation = &spec
	consumerPlan, err := store.SavePlan(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	consumer, _, err := store.EnqueuePlan(ctx, consumerPlan.ID, "subset-consumer")
	if err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.Claim(ctx); err != nil || !found {
		t.Fatal("consumer claim", err)
	}
	binding, err := store.BindPreparedAuditInput(ctx, consumer.ID, reuse.SnapshotID)
	if err != nil || len(binding.Input.Assets) != 1 || binding.Input.Assets[0].ID != in.Assets[0].ID || len(binding.Input.Policies) != len(in.Policies) || binding.Input.CollectedAt != in.CollectedAt || binding.Input.SnapshotVersion != in.SnapshotVersion {
		t.Fatal("qualified subset could not bind without changing original configuration", binding, err)
	}
	stored, err := store.GetSnapshot(ctx, product.Content.SnapshotID)
	if err != nil || len(stored.Input.Assets) != 2 {
		t.Fatal("subset binding modified immutable broad snapshot", err)
	}
}
