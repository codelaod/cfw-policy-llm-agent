package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"cfw-policy-llm-agent/server/internal/domain"
)

func discoveryCase(t *testing.T) (*Store, ExecutionPlan, Job, []domain.Resource, []domain.CatalogueCoverage, json.RawMessage) {
	t.Helper()
	s, p, in, content := preparationCase(t, "internet-in", false)
	plan, job, _ := acceptPreparation(t, s, p, content)
	asset := in.Assets[0]
	resources := []domain.Resource{{Provider: "aliyun", AccountRef: in.Scope.AccountRef, Region: asset.Region, ResourceType: "ecs", ResourceID: asset.ProviderResourceID, Name: asset.Name, Tags: asset.Tags, SourceNames: map[string]string{"instance_name": asset.Name}, Source: "live_readonly", ObservedAt: in.CollectedAt}}
	coverages := []domain.CatalogueCoverage{{AccountRef: in.Scope.AccountRef, Selector: p.Preparation.Query, Complete: true, Source: "live_readonly", ObservedAt: in.CollectedAt, Limitations: []string{}}}
	body := []byte(`{"Instances":{"Instance":[]},"private_discovery_marker":"keep-private"}`)
	hash := sha256.Sum256(body)
	raw, _ := json.Marshal([]CloudRawArtifact{{Round: 0, Operation: "DescribeInstances", Query: map[string]string{"RegionId": "cn-test", "InstanceName": "web-*", "MaxResults": "100"}, Body: body, SHA256: hex.EncodeToString(hash[:])}})
	return s, plan, job, resources, coverages, raw
}

func TestAuditDiscoveryPersistsDirectoryAndPrivateRawWithoutSealingOrFinishing(t *testing.T) {
	s, plan, job, resources, coverages, raw := discoveryCase(t)
	ctx := context.Background()
	record, err := s.SaveAuditDiscovery(ctx, job.ID, resources, coverages, raw, "")
	if err != nil {
		t.Fatal(err)
	}
	if !record.Complete || len(record.Resources) != 1 || record.Resources[0].ID == "" || len(record.UnverifiedResourceRefs) != 0 {
		t.Fatal("complete directory observation lost", record)
	}
	view, err := s.ReadJobWorkspace(ctx, job.ID)
	if err != nil || view.PreparationDiscovery == nil || view.PreparationDiscovery.ID != record.ID || view.InputSealed || view.Job.Status != "running" || view.CloudResult != nil || view.SnapshotID != "" {
		t.Fatal("discovery became a sealed/completed audit product", view, err)
	}
	original, err := s.GetPlan(ctx, plan.ID)
	if err != nil || original.Body != plan.Body {
		t.Fatal("discovery changed original accepted intent", err)
	}
	page, err := s.SearchResources(ctx, ResourceFilter{SourceRef: plan.DataSourceID, AccountRef: resources[0].AccountRef, Region: "cn-test", ResourceType: "ecs", Name: resources[0].Name}, "", 10)
	if err != nil || len(page.Items) != 1 || page.Items[0].ResourceID != resources[0].ResourceID {
		t.Fatal("directory and observation were not committed together", page, err)
	}
	encoded, _ := json.Marshal(view.PreparationDiscovery)
	if strings.Contains(string(encoded), "keep-private") || strings.Contains(string(encoded), "raw_artifacts") || strings.Contains(string(encoded), plan.ConfigRevision) {
		t.Fatal("private provenance/raw leaked into public view", string(encoded))
	}
	var storedRaw string
	if err = s.db.Raw("SELECT raw_artifacts FROM audit_discovery WHERE job_id=?", job.ID).Scan(&storedRaw).Error; err != nil || storedRaw != string(raw) {
		t.Fatal("original private artifact bytes were changed", err)
	}
	var artifacts []CloudRawArtifact
	if json.Unmarshal([]byte(storedRaw), &artifacts) != nil || artifacts[0].Round != 0 {
		t.Fatal("discovery was mislabeled as configuration collection")
	}
	var jobs, snapshots, budgets int64
	s.db.Raw("SELECT count(*) FROM e0_jobs").Scan(&jobs)
	s.db.Raw("SELECT count(*) FROM snapshots").Scan(&snapshots)
	s.db.Raw("SELECT count(*) FROM budget_policies").Scan(&budgets)
	if jobs != 1 || snapshots != 0 || budgets != 0 {
		t.Fatal("discovery created execution/snapshot/authorization", jobs, snapshots, budgets)
	}
	replay, err := s.SaveAuditDiscovery(ctx, job.ID, resources, coverages, raw, "")
	if err != nil || replay.ID != record.ID {
		t.Fatal("identical whole-round replay was not idempotent", err)
	}
	if _, err = s.SaveAuditDiscovery(ctx, job.ID, resources, coverages, raw, "DISCOVERY_FAILED"); !errors.Is(err, ErrConflict) {
		t.Fatal("changed discovery overwrote original round", err)
	}
}

func TestAuditDiscoveryPreservesUnknownPredicatesAmbiguityAndFailures(t *testing.T) {
	for _, scenario := range []string{"unknown-name", "ambiguous", "truncated", "failed-empty", "unproved-empty", "cancelled"} {
		t.Run(scenario, func(t *testing.T) {
			s, _, job, resources, coverages, raw := discoveryCase(t)
			code := ""
			switch scenario {
			case "unknown-name":
				resources[0].Name = ""
				resources[0].SourceNames = nil
			case "ambiguous":
				other := resources[0]
				other.ResourceID, other.Name = "i-second", "web-second"
				other.SourceNames = map[string]string{"instance_name": "web-second"}
				resources = append(resources, other)
				code = "DISCOVERY_NEEDS_CLARIFICATION"
			case "truncated":
				coverages[0].Complete = false
				coverages[0].Limitations = []string{"DISCOVERY_OBJECT_LIMIT"}
			case "failed-empty":
				resources, coverages, raw = nil, nil, json.RawMessage(`[]`)
				code = "DISCOVERY_FAILED"
			case "unproved-empty":
				resources, raw = nil, json.RawMessage(`[]`)
			case "cancelled":
				if _, err := s.Cancel(context.Background(), job.ID); err != nil {
					t.Fatal(err)
				}
			}
			record, err := s.SaveAuditDiscovery(context.Background(), job.ID, resources, coverages, raw, code)
			if err != nil {
				t.Fatal("partial discovery evidence was discarded", err)
			}
			if record.Complete {
				t.Fatal("unknown/failed/truncated discovery claimed complete", record)
			}
			if strings.HasPrefix(scenario, "unknown-") && (len(record.UnverifiedResourceRefs) != 1 || record.Coverages[0].Complete) {
				t.Fatal("unknown predicate metadata was treated as confirmed match", record)
			}
			if scenario == "ambiguous" && len(record.Resources) != 2 {
				t.Fatal("ambiguous result was silently cut to plan result limit")
			}
			if scenario == "cancelled" && (record.Code != "CANCELLED" || record.Coverages[0].Complete) {
				t.Fatal("cancelling task claimed completion", record)
			}
			page, err := s.SearchResources(context.Background(), ResourceFilter{SourceRef: "explicit-preparation", AccountRef: "synthetic-account", Region: "cn-test", ResourceType: "ecs", Name: "web-*"}, "", 10)
			if err != nil || page.Coverage.Complete {
				t.Fatal("partial observation became trusted complete empty directory", page, err)
			}
		})
	}
}

func TestAuditDiscoveryUnknownTagMetadataRemainsPartial(t *testing.T) {
	s, p, in, content := preparationCase(t, "internet-in", false)
	p.Preparation.Query.Tags = map[string]string{"owner": "team-a"}
	_, job, _ := acceptPreparation(t, s, p, content)
	resource := domain.Resource{Provider: "aliyun", AccountRef: in.Scope.AccountRef, Region: "cn-test", ResourceType: "ecs", ResourceID: "i-public-fixture", Name: "web-public-fixture", Source: "live_readonly", ObservedAt: in.CollectedAt}
	coverage := domain.CatalogueCoverage{AccountRef: in.Scope.AccountRef, Selector: p.Preparation.Query, Complete: true, Source: "live_readonly", ObservedAt: in.CollectedAt}
	body := []byte(`{"Instances":{"Instance":[]}}`)
	hash := sha256.Sum256(body)
	raw, _ := json.Marshal([]CloudRawArtifact{{Round: 0, Operation: "DescribeInstances", Query: map[string]string{"RegionId": "cn-test", "InstanceName": "web-*", "Tag.1.Key": "owner", "Tag.1.Value": "team-a", "MaxResults": "100"}, Body: body, SHA256: hex.EncodeToString(hash[:])}})
	record, err := s.SaveAuditDiscovery(context.Background(), job.ID, []domain.Resource{resource}, []domain.CatalogueCoverage{coverage}, raw, "")
	if err != nil || record.Complete || len(record.UnverifiedResourceRefs) != 1 || record.Coverages[0].Complete {
		t.Fatal("missing tag collection became an absent/matching tag fact", record, err)
	}
}

func TestAuditDiscoveryNATPreservesOriginalNamesAndLimitedDirectory(t *testing.T) {
	s, p, in, content := preparationCase(t, "nat-out", false)
	_, job, _ := acceptPreparation(t, s, p, content)
	coverage := domain.CatalogueCoverage{AccountRef: in.Scope.AccountRef, Selector: p.Preparation.Query, Complete: true, Source: "live_readonly", ObservedAt: in.CollectedAt, Limitations: []string{"NAT_DIRECTORY_CFW_MANAGED_ONLY"}}
	body := []byte(`{"NatFirewallList":[]}`)
	hash := sha256.Sum256(body)
	raw, _ := json.Marshal([]CloudRawArtifact{{Round: 0, Operation: "DescribeNatFirewallList", Query: map[string]string{"RegionNo": "cn-test", "PageNo": "1", "PageSize": "50"}, Body: body, SHA256: hex.EncodeToString(hash[:])}})
	record, err := s.SaveAuditDiscovery(context.Background(), job.ID, []domain.Resource{*in.GatewayMetadata}, []domain.CatalogueCoverage{coverage}, raw, "")
	if err != nil || record.Complete || record.Coverages[0].Complete || record.Resources[0].SourceNames["firewall_name"] != "nat-cfw" || record.Resources[0].ResourceID != "ngw-public-fixture" {
		t.Fatal("limited NAT directory became whole account or lost provider identity", record, err)
	}
}

func TestAuditDiscoveryRecordFailureRollsBackDirectoryAndEvents(t *testing.T) {
	s, _, job, resources, coverages, raw := discoveryCase(t)
	before, _, err := s.Page(context.Background(), job.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.db.Exec("CREATE TRIGGER fail_discovery_insert BEFORE INSERT ON audit_discovery BEGIN SELECT RAISE(ABORT,'fixture failure'); END").Error; err != nil {
		t.Fatal(err)
	}
	if _, err = s.SaveAuditDiscovery(context.Background(), job.ID, resources, coverages, raw, ""); err == nil {
		t.Fatal("trigger failure not surfaced")
	}
	var catalog, covers int64
	s.db.Raw("SELECT count(*) FROM resource_catalog").Scan(&catalog)
	s.db.Raw("SELECT count(*) FROM resource_catalogue_coverage").Scan(&covers)
	after, _, err := s.Page(context.Background(), job.ID, 0, 100)
	if err != nil || catalog != 0 || covers != 0 || after.LastSeq != before.LastSeq {
		t.Fatal("record failure leaked directory or event", catalog, covers, err)
	}
}

func TestAuditDiscoveryRejectsForeignEvidenceAndRollsBackDirectory(t *testing.T) {
	for _, scenario := range []string{"account", "region", "type", "source", "name", "duplicate", "coverage-selector", "coverage-region", "coverage-source", "free-error", "raw-scope", "raw-name", "raw-operation", "raw-round", "raw-hash", "raw-secret", "stale-grant", "terminal", "cancelled-context"} {
		t.Run(scenario, func(t *testing.T) {
			s, plan, job, resources, coverages, raw := discoveryCase(t)
			ctx := context.Background()
			code := ""
			var artifacts []CloudRawArtifact
			if json.Unmarshal(raw, &artifacts) != nil {
				t.Fatal("fixture raw")
			}
			switch scenario {
			case "account":
				resources[0].AccountRef = "foreign-account"
			case "region":
				resources[0].Region = "cn-foreign"
			case "type":
				resources[0].ResourceType = "nat_gateway"
			case "source":
				resources[0].SourceRef = "foreign-source"
			case "name":
				resources[0].Name = "database"
				resources[0].SourceNames = nil
			case "duplicate":
				resources = append(resources, resources[0])
			case "coverage-selector":
				coverages[0].Selector.Name = "unrelated*"
			case "coverage-region":
				coverages[0].Selector.Region = "cn-foreign"
			case "coverage-source":
				coverages[0].Source = "untrusted"
			case "free-error":
				code = "private upstream URL and response"
			case "raw-scope":
				artifacts[0].Query["RegionId"] = "cn-foreign"
			case "raw-name":
				artifacts[0].Query["InstanceName"] = "unrelated*"
			case "raw-operation":
				artifacts[0].Operation = "DescribeControlPolicy"
			case "raw-round":
				artifacts[0].Round = 1
			case "raw-hash":
				artifacts[0].SHA256 = strings.Repeat("b", 64)
			case "raw-secret":
				artifacts[0].Body = []byte(`{"Credentials":{"private":"never-save"}}`)
				hash := sha256.Sum256(artifacts[0].Body)
				artifacts[0].SHA256 = hex.EncodeToString(hash[:])
			case "stale-grant":
				grant := ConnectionGrant{SourceRef: plan.DataSourceID, ConfigRevision: "new-config", AccountRef: resources[0].AccountRef, ControlPlaneRef: plan.PreparedInput.Scope.ControlPlaneRef, Regions: []string{"cn-test"}, Boundaries: []string{"internet/in"}}
				if err := s.RegisterConnectionGrant(ctx, grant); err != nil {
					t.Fatal(err)
				}
			case "terminal":
				if err := s.FinishWithCode(ctx, job.ID, "interrupted", "SERVICE_RESTARTED"); err != nil {
					t.Fatal(err)
				}
			case "cancelled-context":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			if strings.HasPrefix(scenario, "raw-") {
				raw, _ = json.Marshal(artifacts)
			}
			if _, err := s.SaveAuditDiscovery(ctx, job.ID, resources, coverages, raw, code); err == nil {
				t.Fatal("foreign/untrusted evidence was saved")
			}
			var observations, catalog int64
			s.db.Raw("SELECT count(*) FROM audit_discovery").Scan(&observations)
			s.db.Raw("SELECT count(*) FROM resource_catalog").Scan(&catalog)
			if observations != 0 || catalog != 0 {
				t.Fatal("rejected observation partially committed", observations, catalog)
			}
		})
	}
}

func TestAuditDiscoverySchemaDamageNeedsExplicitMigration(t *testing.T) {
	s, _, job, _, _, _ := discoveryCase(t)
	var path string
	if err := s.db.Raw("SELECT file FROM pragma_database_list WHERE name='main'").Scan(&path).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Exec("ALTER TABLE audit_discovery RENAME COLUMN raw_artifacts TO missing_raw").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetAuditDiscovery(context.Background(), job.ID); err != nil {
		t.Fatal("normal reads unnecessarily selected private raw column", err)
	}
	if err := checkWorkflowShape(s.db); err == nil {
		t.Fatal("incomplete discovery schema was accepted")
	}
}
