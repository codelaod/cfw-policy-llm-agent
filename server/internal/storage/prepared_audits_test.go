package storage

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"cfw-policy-llm-agent/server/internal/audit"
	"cfw-policy-llm-agent/server/internal/domain"
	"cfw-policy-llm-agent/server/internal/workload"
)

func preparationCase(t *testing.T, fixture string, known bool) (*Store, ExecutionPlan, domain.Input, CloudContent) {
	t.Helper()
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "prepared.sqlite"), true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err = migratePreparedAuditSchema(s.db); err != nil {
		t.Fatal(err)
	}
	in, err := audit.SyntheticExample(fixture)
	if err != nil {
		t.Fatal(err)
	}
	in.SourceKind, in.SnapshotVersion = "live_readonly", "public-preparation-fixture-v1"
	in.Completeness.ObservationConsistency = "observed_stable"
	in.CollectedAt, in.EvaluationAt = time.Now().UTC().Format(time.RFC3339Nano), time.Now().UTC().Format(time.RFC3339Nano)
	query := domain.ResourceQuery{Region: "cn-test", ResourceType: "ecs", Name: "web-*"}
	selected := []PreparedResource{}
	if fixture == "nat-out" {
		in.Selection = domain.Selection{Kind: "gateway_policy", AssetIDs: []string{}}
		in.Assets = []domain.Asset{}
		in.Baseline.Exceptions = []domain.Exception{}
		in.GatewayMetadata = &domain.Resource{Provider: "aliyun", AccountRef: in.Scope.AccountRef, ResourceID: "ngw-public-fixture", Region: "cn-test", ResourceType: "nat_gateway", Name: "nat-business", SourceNames: map[string]string{"firewall_name": "nat-cfw"}, Source: "live_readonly", ObservedAt: in.CollectedAt}
		query.ResourceType, query.Name = "nat_gateway", "nat-cfw"
		if known {
			selected = append(selected, PreparedResource{ResourceID: "ngw-public-fixture", Region: "cn-test", ResourceType: "nat_gateway"})
		}
	} else {
		for i := range in.Assets {
			in.Assets[i].Name = "web-public-fixture"
			in.Assets[i].ProviderResourceID = "i-public-fixture"
			in.Assets[i].Region, in.Assets[i].ResourceType = "cn-test", "ecs"
			in.Assets[i].MetadataComplete = true
			in.Assets[i].Tags = map[string]string{"env": "test"}
			if known {
				selected = append(selected, PreparedResource{ResourceID: "i-public-fixture", Region: "cn-test", ResourceType: "ecs", AssetID: in.Assets[i].ID})
			}
		}
	}
	grant := ConnectionGrant{SourceRef: "explicit-preparation", ConfigRevision: "preparation-v1", AccountRef: in.Scope.AccountRef, ControlPlaneRef: in.Scope.ControlPlaneRef, Regions: []string{"cn-test"}, Boundaries: []string{in.Scope.Boundary + "/" + in.Scope.Direction}}
	if err = s.RegisterConnectionGrant(ctx, grant); err != nil {
		t.Fatal(err)
	}
	scope := domain.Scope{Provider: "aliyun", AccountRef: in.Scope.AccountRef, ControlPlaneRef: in.Scope.ControlPlaneRef, Boundary: in.Scope.Boundary, Direction: in.Scope.Direction, IPFamily: "ipv4"}
	if known {
		scope = in.Scope
		if err = s.RegisterConfiguredScopes(ctx, grant.SourceRef, grant.ConfigRevision, []domain.Scope{scope}); err != nil {
			t.Fatal(err)
		}
	}
	conversation, err := s.CreateConversation(ctx, "public preparation fixture", scope.Ref)
	if err != nil {
		t.Fatal(err)
	}
	p := ExecutionPlan{Kind: "audit", ConversationID: conversation.ID, ConversationRevision: conversation.Revision, DataSourceID: grant.SourceRef, ConfigRevision: grant.ConfigRevision, PolicyScopeRef: scope.Ref, Mode: "rules_only", BudgetRef: "original-budget", PreparedInput: domain.Input{Scope: scope, Baseline: in.Baseline}, EffectCeiling: EffectCeiling{AllowCloudReads: true, CloudHTTPAttempts: 7}, Preparation: &PreparationSpec{SourceRef: grant.SourceRef, ConfigRevision: grant.ConfigRevision, Query: query, Selected: selected, Boundary: in.Scope.Boundary, Direction: in.Scope.Direction, DataPolicy: "prefer_fresh", ResultLimit: 1}}
	content := CloudContent{Kind: "audit", ScopeRef: in.Scope.Ref, SourceRef: grant.SourceRef, ConfigRevision: grant.ConfigRevision, IdentityVerified: true, ProductReady: true, Observations: []CloudObservation{{Round: 1, Operation: "DescribeControlPolicy", Pages: 1, StartedAt: in.CollectedAt, FinishedAt: in.CollectedAt, EnumerationComplete: true, SemanticSHA256: strings.Repeat("a", 64)}, {Round: 2, Operation: "DescribeControlPolicy", Pages: 1, StartedAt: in.CollectedAt, FinishedAt: in.CollectedAt, EnumerationComplete: true, SemanticSHA256: strings.Repeat("a", 64)}}}
	return s, p, in, content
}

func acceptPreparation(t *testing.T, s *Store, p ExecutionPlan, content CloudContent) (ExecutionPlan, Job, CloudContent) {
	t.Helper()
	ctx := context.Background()
	plan, err := s.SavePlan(ctx, p)
	if err != nil {
		t.Fatal("save preparation plan", err)
	}
	job, _, err := s.EnqueuePlan(ctx, plan.ID, "accept_"+plan.ID)
	if err != nil {
		t.Fatal("accept preparation plan", err)
	}
	claimed, found, err := s.Claim(ctx)
	if err != nil || !found || claimed.ID != job.ID {
		t.Fatal("claim preparation", err)
	}
	content.JobID = job.ID
	return plan, claimed, content
}

func TestPreparedAuditOwnProductIsAtomicNonterminalAndBoundOnce(t *testing.T) {
	for _, fixture := range []string{"internet-in", "nat-out"} {
		t.Run(fixture, func(t *testing.T) {
			s, p, in, content := preparationCase(t, fixture, false)
			ctx := context.Background()
			plan, job, content := acceptPreparation(t, s, p, content)
			if _, err := s.SealInput(ctx, job.ID, in); !errors.Is(err, ErrInputNotSealed) {
				t.Fatal("direct sealing bypassed preparation", err)
			}
			record, err := s.SavePreparedCloudSnapshot(ctx, job.ID, content, in)
			if err != nil {
				t.Fatal("save own data", err)
			}
			if record.Content.Kind != "audit" || record.Content.SnapshotID == "" {
				t.Fatal("producer identity lost")
			}
			if _, err = s.GetJobInput(ctx, job.ID); !errors.Is(err, ErrInputNotSealed) {
				t.Fatal("saving data unexpectedly sealed input", err)
			}
			current, err := s.Get(ctx, job.ID)
			if err != nil || current.Status != "running" || current.BudgetRef != p.BudgetRef {
				t.Fatal("preparation ended task or replaced budget", current, err)
			}
			var jobs, budgets int64
			s.db.Raw("SELECT count(*) FROM e0_jobs").Scan(&jobs)
			s.db.Raw("SELECT count(*) FROM budget_policies").Scan(&budgets)
			if jobs != 1 || budgets != 0 {
				t.Fatal("preparation created queue work or authorization", jobs, budgets)
			}
			replay, err := s.SavePreparedCloudSnapshot(ctx, job.ID, content, in)
			if err != nil || replay.ID != record.ID {
				t.Fatal("identical data replay is not idempotent", err)
			}
			changed, _ := cloneConfirmationInput(in)
			changed.SnapshotVersion = "different-version"
			if _, err = s.SavePreparedCloudSnapshot(ctx, job.ID, content, changed); !errors.Is(err, ErrConflict) {
				t.Fatal("changed data replay accepted", err)
			}
			binding, err := s.BindPreparedAuditInput(ctx, job.ID, record.Content.SnapshotID)
			if err != nil {
				t.Fatal("bind own input", err)
			}
			if binding.Scope != in.Scope || binding.Manifest.CollectedAt != in.CollectedAt || binding.Manifest.SnapshotVersion != in.SnapshotVersion {
				t.Fatal("late binding changed cloud facts")
			}
			if len(binding.Input.Assets) != 0 && (binding.Input.Assets[0].AllowedConfirmed || len(binding.Input.Assets[0].Services) != 0) {
				t.Fatal("snapshot business claims bypassed accepted versions")
			}
			again, err := s.BindPreparedAuditInput(ctx, job.ID, record.Content.SnapshotID)
			if err != nil || !reflect.DeepEqual(binding, again) {
				t.Fatal("identical bind is not idempotent", err)
			}
			if _, err = s.BindPreparedAuditInput(ctx, job.ID, "different-snapshot"); !errors.Is(err, ErrConflict) {
				t.Fatal("second snapshot accepted", err)
			}
			if _, err = s.SealInput(ctx, job.ID, binding.Input); err != nil {
				t.Fatal("exact sealed replay rejected", err)
			}
			original, err := s.GetPlan(ctx, plan.ID)
			if err != nil || original.PolicyScopeRef != "" || original.SnapshotID != "" || original.PreparedInput.Scope.GatewayRef != "" || original.Body != plan.Body {
				t.Fatal("binding rewrote original intent", err)
			}
		})
	}
}

func TestPreparedAuditActualMetadataMustMatchAcceptedSelectorAndGrant(t *testing.T) {
	for _, scenario := range []string{"raw-id", "region", "type", "name", "tags", "account", "control-plane", "direction", "selected-id", "selected-local-id", "count", "stale-grant", "nat-name-glob", "nat-missing-metadata"} {
		t.Run(scenario, func(t *testing.T) {
			fixture := "internet-in"
			if strings.HasPrefix(scenario, "nat-") {
				fixture = "nat-out"
			}
			s, p, in, content := preparationCase(t, fixture, false)
			switch scenario {
			case "raw-id":
				p.Preparation.Query.Name, p.Preparation.Query.ResourceID = "", "i-other"
			case "tags":
				p.Preparation.Query.Tags = map[string]string{"env": "prod"}
			case "selected-id":
				p.Preparation.Selected = []PreparedResource{{ResourceID: "i-other", Region: "cn-test", ResourceType: "ecs"}}
			case "selected-local-id":
				p.Preparation.Selected = []PreparedResource{{ResourceID: "i-public-fixture", Region: "cn-test", ResourceType: "ecs", AssetID: "other-local-id"}}
			case "nat-name-glob":
				p.Preparation.Query.Name = "nat-*"
			}
			_, job, content := acceptPreparation(t, s, p, content)
			switch scenario {
			case "region":
				in.Assets[0].Region = "cn-other"
			case "type":
				in.Assets[0].ResourceType = "not-ecs"
			case "name":
				in.Assets[0].Name = "database"
			case "account":
				in.Scope.AccountRef = "other-account"
			case "control-plane":
				in.Scope.ControlPlaneRef = "other-control"
			case "direction":
				in.Scope.Direction = "out"
			case "count":
				second := in.Assets[0]
				second.ID, second.ProviderResourceID = "second-local", "i-second"
				in.Assets = append(in.Assets, second)
				in.Selection.AssetIDs = append(in.Selection.AssetIDs, second.ID)
			case "stale-grant":
				grant := ConnectionGrant{SourceRef: p.DataSourceID, ConfigRevision: "new-revision", AccountRef: in.Scope.AccountRef, ControlPlaneRef: in.Scope.ControlPlaneRef, Regions: []string{"cn-test"}, Boundaries: []string{"internet/in"}}
				if err := s.RegisterConnectionGrant(context.Background(), grant); err != nil {
					t.Fatal(err)
				}
			case "nat-missing-metadata":
				in.GatewayMetadata = nil
			}
			if _, err := s.SavePreparedCloudSnapshot(context.Background(), job.ID, content, in); err == nil {
				t.Fatal("actual metadata violated accepted selector/grant")
			}
			var count int64
			s.db.Raw("SELECT count(*) FROM snapshots").Scan(&count)
			if count != 0 {
				t.Fatal("rejected product leaked snapshot")
			}
		})
	}
}

func insertPreparedProfile(t *testing.T, s *Store, in domain.Input, snapshotID, id, role, at string) {
	t.Helper()
	body, _ := json.Marshal(profilePayload{Services: []profileService{{ID: "service-fixture", Role: role, RoleStatus: "confirmed", Protocol: "tcp", Ports: []domain.PortRange{{From: 443, To: 443}}}}})
	if err := s.db.Exec("INSERT INTO profile_versions(id,scope_key,asset_key,body,source_snapshot_id,confirmed_at) VALUES(?,?,?,?,?,?)", id, canonicalBusinessScope(in.Scope), in.Assets[0].ID, string(body), snapshotID, at).Error; err != nil {
		t.Fatal(err)
	}
}

func TestPreparedAuditAcceptanceFreezesKnownHeadsAndUnknownWatermarks(t *testing.T) {
	for _, known := range []bool{false, true} {
		t.Run(map[bool]string{false: "watermark", true: "exact-head"}[known], func(t *testing.T) {
			s, p, in, content := preparationCase(t, "internet-in", known)
			ctx := context.Background()
			prior, err := s.SaveSnapshot(ctx, in)
			if err != nil {
				t.Fatal(err)
			}
			before := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano)
			insertPreparedProfile(t, s, in, prior.ID, "profile-before", "web", before)
			if known {
				if err = s.db.Exec("INSERT INTO asset_heads(scope_key,asset_key,revision,profile_version_id) VALUES(?,?,1,?)", canonicalBusinessScope(in.Scope), in.Assets[0].ID, "profile-before").Error; err != nil {
					t.Fatal(err)
				}
			}
			_, job, content := acceptPreparation(t, s, p, content)
			// Backdating a later immutable row cannot move it below acceptance's
			// highwater. A new current head also cannot replace frozen heads.
			insertPreparedProfile(t, s, in, prior.ID, "profile-after", "database", before)
			if known {
				s.db.Exec("UPDATE asset_heads SET revision=2,profile_version_id='profile-after' WHERE scope_key=? AND asset_key=?", canonicalBusinessScope(in.Scope), in.Assets[0].ID)
			}
			record, err := s.SavePreparedCloudSnapshot(ctx, job.ID, content, in)
			if err != nil {
				t.Fatal(err)
			}
			bound, err := s.BindPreparedAuditInput(ctx, job.ID, record.Content.SnapshotID)
			if err != nil {
				t.Fatal(err)
			}
			if len(bound.Heads) != 1 || bound.Heads[0].ProfileVersionID != "profile-before" || len(bound.Input.Assets[0].Services) != 1 || bound.Input.Assets[0].Services[0].Role != "web" {
				t.Fatal("binding consumed post-acceptance business version", bound.Heads, bound.Input.Assets[0].Services)
			}
		})
	}
}

func TestConnectionGrantVersionAndDirectionRemainImmutable(t *testing.T) {
	s, p, in, _ := preparationCase(t, "internet-in", false)
	ctx := context.Background()
	grant := ConnectionGrant{SourceRef: p.DataSourceID, ConfigRevision: p.ConfigRevision, AccountRef: in.Scope.AccountRef, ControlPlaneRef: in.Scope.ControlPlaneRef, Regions: []string{"cn-test"}, Boundaries: []string{"internet/in"}}
	if err := s.RegisterConnectionGrant(ctx, grant); err != nil {
		t.Fatal("same registration not idempotent", err)
	}
	grant.Boundaries = []string{"internet/out"}
	if err := s.RegisterConnectionGrant(ctx, grant); !errors.Is(err, ErrConflict) {
		t.Fatal("same grant revision changed permission", err)
	}
	p.Preparation.Direction, p.PreparedInput.Scope.Direction = "out", "out"
	if _, err := s.SavePlan(ctx, p); err == nil {
		t.Fatal("internet/in authority expanded to out")
	}
}

func TestPreparedAuditWatermarkUsesExactGoTimestamp(t *testing.T) {
	s, p, in, content := preparationCase(t, "internet-in", false)
	prior, err := s.SaveSnapshot(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	insertPreparedProfile(t, s, in, prior.ID, "profile-future", "database", "2099-01-01T00:00:00Z")
	_, job, content := acceptPreparation(t, s, p, content)
	record, err := s.SavePreparedCloudSnapshot(context.Background(), job.ID, content, in)
	if err != nil {
		t.Fatal(err)
	}
	bound, err := s.BindPreparedAuditInput(context.Background(), job.ID, record.Content.SnapshotID)
	if err != nil || len(bound.Input.Assets[0].Services) != 0 || bound.Heads[0].ProfileVersionID != "" {
		t.Fatal("future confirmed_at bypassed acceptance clock", err)
	}
}

func TestPreparedAuditReuseRequiresTerminalProducerAndCompleteData(t *testing.T) {
	for _, scenario := range []string{"running", "complete-partial-parent", "partial-data", "force-refresh", "grant-changed-before-bind"} {
		t.Run(scenario, func(t *testing.T) {
			s, p, in, content := preparationCase(t, "internet-in", false)
			ctx := context.Background()
			_, producer, content := acceptPreparation(t, s, p, content)
			if scenario == "partial-data" {
				content.ProductReady = false
			}
			record, err := s.SavePreparedCloudSnapshot(ctx, producer.ID, content, in)
			if err != nil {
				t.Fatal(err)
			}
			if scenario != "running" {
				if err = s.FinishWithCode(ctx, producer.ID, "partial", "MODEL_FAILED"); err != nil {
					t.Fatal(err)
				}
			}
			conversation, err := s.CreateConversation(ctx, "another accepted audit", "")
			if err != nil {
				t.Fatal(err)
			}
			p.ConversationID, p.ConversationRevision = conversation.ID, conversation.Revision
			if scenario == "force-refresh" {
				p.Preparation.DataPolicy = "force_refresh"
			}
			_, consumer, _ := acceptPreparation(t, s, p, content)
			if scenario == "grant-changed-before-bind" {
				grant := ConnectionGrant{SourceRef: p.DataSourceID, ConfigRevision: "new-revision", AccountRef: in.Scope.AccountRef, ControlPlaneRef: in.Scope.ControlPlaneRef, Regions: []string{"cn-test"}, Boundaries: []string{"internet/in"}}
				if err = s.RegisterConnectionGrant(ctx, grant); err != nil {
					t.Fatal(err)
				}
			}
			_, err = s.BindPreparedAuditInput(ctx, consumer.ID, record.Content.SnapshotID)
			if scenario == "complete-partial-parent" && err != nil {
				t.Fatal("parent model failure invalidated complete cloud data", err)
			}
			if scenario != "complete-partial-parent" && err == nil {
				t.Fatal("ineligible prepared product was rebound")
			}
		})
	}
}

func TestPreparedAuditResolvedScopePreservesOtherActiveScopes(t *testing.T) {
	s, p, in, content := preparationCase(t, "nat-out", false)
	other := in.Scope
	other.Ref, other.PolicySetRef, other.GatewayRef = "other-active-scope", "other-active-policy", "other-gateway-alias"
	ctx := context.Background()
	if err := s.RegisterConfiguredScopes(ctx, p.DataSourceID, p.ConfigRevision, []domain.Scope{other}); err != nil {
		t.Fatal(err)
	}
	_, job, content := acceptPreparation(t, s, p, content)
	if _, err := s.SavePreparedCloudSnapshot(ctx, job.ID, content, in); err != nil {
		t.Fatal(err)
	}
	var active int64
	if err := s.db.Raw("SELECT count(*) FROM configured_scope_catalog WHERE source_ref=? AND active=1", p.DataSourceID).Scan(&active).Error; err != nil || active != 2 {
		t.Fatal("resolved scope deactivated unrelated scope", active, err)
	}
}

func TestPreparedAuditUsesFrozenPolicyObjectLimit(t *testing.T) {
	s, p, in, content := preparationCase(t, "internet-in", false)
	limits := workload.Default("audit")
	limits.MaxPolicies = 1
	p.Workload = workload.Resolved{ProfileRef: "small-frozen-input", Limits: limits}
	_, job, content := acceptPreparation(t, s, p, content)
	if len(in.Policies) < 2 {
		t.Fatal("fixture must exercise policy count")
	}
	if _, err := s.SavePreparedCloudSnapshot(context.Background(), job.ID, content, in); err == nil {
		t.Fatal("storage inherited broader caller/default input limit")
	}
}

func TestPreparedAuditFreshAfterChecksOriginalObservationStart(t *testing.T) {
	s, p, in, content := preparationCase(t, "internet-in", false)
	minimum := time.Now().UTC().Add(-time.Minute)
	p.Preparation.FreshAfter = minimum.Format(time.RFC3339Nano)
	content.Observations[0].StartedAt = minimum.Add(-time.Nanosecond).Format(time.RFC3339Nano)
	_, job, content := acceptPreparation(t, s, p, content)
	if _, err := s.SavePreparedCloudSnapshot(context.Background(), job.ID, content, in); err == nil {
		t.Fatal("new collected_at concealed pre-change observation")
	}
}

func TestPreparedAuditGrantPreservesResourceRegionPairs(t *testing.T) {
	s, p, in, content := preparationCase(t, "internet-in", false)
	grant := ConnectionGrant{SourceRef: p.DataSourceID, ConfigRevision: "paired-v2", AccountRef: in.Scope.AccountRef, ControlPlaneRef: in.Scope.ControlPlaneRef, Regions: []string{"cn-test", "cn-other"}, Boundaries: []string{"internet/in"}, Assets: []PreparedResource{{ResourceID: "i-public-fixture", Region: "cn-other", ResourceType: "ecs"}}}
	if err := s.RegisterConnectionGrant(context.Background(), grant); err != nil {
		t.Fatal(err)
	}
	p.ConfigRevision, p.Preparation.ConfigRevision, content.ConfigRevision = grant.ConfigRevision, grant.ConfigRevision, grant.ConfigRevision
	_, job, content := acceptPreparation(t, s, p, content)
	if _, err := s.SavePreparedCloudSnapshot(context.Background(), job.ID, content, in); err == nil {
		t.Fatal("asset ID crossed granted region pair")
	}
}

func TestPreparedAuditAsOfComparisonKeepsNanosecondPrecision(t *testing.T) {
	s, _, in, _ := preparationCase(t, "internet-in", false)
	snapshot, err := s.SaveSnapshot(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	insertPreparedProfile(t, s, in, snapshot.ID, "same-second-earlier", "web", "2026-10-29T09:00:00Z")
	insertPreparedProfile(t, s, in, snapshot.ID, "same-ms-later", "database", "2026-10-29T09:00:00.000000101Z")
	accepted, _ := time.Parse(time.RFC3339Nano, "2026-10-29T09:00:00.000000100Z")
	id, _, err := acceptedBusinessVersion(s.db, "profile_versions", canonicalBusinessScope(in.Scope), in.Assets[0].ID, "", 100, accepted)
	if err != nil || id != "same-second-earlier" {
		t.Fatal("SQLite timestamp rounding/lexical order replaced exact as-of time", id, err)
	}
}

func TestPreparedAuditProjectionRequiresExactAcceptedIdentities(t *testing.T) {
	for _, scenario := range []string{"foreign", "duplicate", "missing", "wrong-region", "wrong-type", "wrong-local-id", "unbounded-name"} {
		t.Run(scenario, func(t *testing.T) {
			_, p, in, _ := preparationCase(t, "internet-in", true)
			p.Preparation.Query.Name, p.Preparation.Query.ResourceID = "", "i-public-fixture"
			switch scenario {
			case "foreign":
				p.Preparation.Selected[0].ResourceID = "i-foreign"
			case "duplicate":
				other := in.Assets[0]
				other.ID = "duplicate-provider-object"
				in.Assets = append(in.Assets, other)
				in.Selection.AssetIDs = append(in.Selection.AssetIDs, other.ID)
				p.Preparation.Selected[0].AssetID = ""
			case "missing":
				in.Assets = nil
			case "wrong-region":
				in.Assets[0].Region = "cn-other"
			case "wrong-type":
				in.Assets[0].ResourceType = "different-type"
			case "wrong-local-id":
				p.Preparation.Selected[0].AssetID = "foreign-local"
			case "unbounded-name":
				p.Preparation.Selected = nil
				p.Preparation.Query.ResourceID, p.Preparation.Query.Name = "", "web-*"
				other := in.Assets[0]
				other.ID, other.ProviderResourceID = "other-local", "i-other"
				in.Assets = append(in.Assets, other)
				in.Selection.AssetIDs = append(in.Selection.AssetIDs, other.ID)
			}
			projected, err := projectPreparedSelection(p, in)
			if scenario == "unbounded-name" {
				if err != nil || len(projected.Assets) != 2 {
					t.Fatal("unbounded discovery plan silently truncated to first match", err)
				}
			} else if err == nil {
				t.Fatal("invalid accepted identity was projected", projected)
			}
		})
	}
}

func TestPreparedAuditEmptyTagValueMeansKeyExists(t *testing.T) {
	for _, present := range []bool{true, false} {
		t.Run(map[bool]string{true: "present-nonempty", false: "missing"}[present], func(t *testing.T) {
			s, p, in, content := preparationCase(t, "internet-in", false)
			p.Preparation.Query.Tags = map[string]string{"env": ""}
			if !present {
				delete(in.Assets[0].Tags, "env")
			}
			_, job, content := acceptPreparation(t, s, p, content)
			_, err := s.SavePreparedCloudSnapshot(context.Background(), job.ID, content, in)
			if (err == nil) != present {
				t.Fatal("tag key-presence semantics changed", err)
			}
		})
	}
}
