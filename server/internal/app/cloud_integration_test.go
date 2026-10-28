package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"cfw-policy-llm-agent/server/internal/aliyunapi"
	"cfw-policy-llm-agent/server/internal/aliyuncollector"
	"cfw-policy-llm-agent/server/internal/audit"
	"cfw-policy-llm-agent/server/internal/domain"
	"cfw-policy-llm-agent/server/internal/outbound"
	"cfw-policy-llm-agent/server/internal/storage"
)

// These artificial provider responses exercise the actual fixed SDK, HTTP guard
// and normalizer together. They are not observations of an Alibaba Cloud account.
func cloudProtocolFixture(action string) string {
	switch action {
	case "GetCallerIdentity":
		return `{"AccountId":"1234567890123456","IdentityType":"RAMUser","Arn":"acs:ram::1234567890123456:user/synthetic","PrincipalId":"synthetic-user"}`
	case "DescribeNatFirewallList":
		return `{
 "TotalCount":1,
 "NatFirewallList":[{"NatGatewayId":"ngw-runtime-synthetic","RegionId":"cn-hangzhou","AliUid":1234567890123456,"ProxyId":"proxy-synthetic","ProxyStatus":"normal","StrictMode":1}]
}`
	case "DescribeNatFirewallControlPolicy":
		return `{"TotalCount":"1","Policys":[{"AclUuid":"acl-synthetic","NatGatewayId":"ngw-runtime-synthetic","Source":"10.0.0.0/24","SourceType":"net","Destination":"198.51.100.0/24","DestinationType":"net","Proto":"TCP","DestPortType":"port","DestPort":"443","AclAction":"accept","Release":"true","Order":1,"ApplicationNameList":["ANY"],"RepeatType":"Permanent"}]}`
	case "DescribeAssetList":
		return `{"TotalCount":1,"Assets":[{"BindInstanceId":"i-runtime-synthetic","ResourceInstanceId":"eip-synthetic","RegionID":"cn-hangzhou","AliUid":1234567890123456,"InternetAddress":"192.0.2.10","ProtectStatus":"open"}]}`
	case "DescribeInstances":
		return `{"NextToken":"","Instances":{"Instance":[{"InstanceId":"i-runtime-synthetic","RegionId":"cn-hangzhou","VpcAttributes":{"VpcId":"vpc-synthetic","PrivateIpAddress":{"IpAddress":["10.0.0.10"]}},"SecurityGroupIds":{"SecurityGroupId":["sg-synthetic"]}}]}}`
	case "DescribeSecurityGroupAttribute":
		return `{"SecurityGroupId":"sg-synthetic","RegionId":"cn-hangzhou","NextToken":"","Permissions":{"Permission":[{"IpProtocol":"tcp","PortRange":"443/443","Policy":"accept","SourceCidrIp":"0.0.0.0/0"}]}}`
	case "DescribeControlPolicy":
		return `{"TotalCount":"1","Policys":[{"AclUuid":"acl-synthetic","Direction":"in","SourceType":"net","Source":"0.0.0.0/0","DestinationType":"net","Destination":"192.0.2.10","DestPortType":"port","DestPort":"443","Proto":"TCP","AclAction":"accept","Release":"true","Order":1,"ApplicationNameList":["ANY"],"RepeatType":"Permanent"}]}`
	default:
		return ""
	}
}

func cloudIntegration(t *testing.T, boundary string, handler http.Handler) (*storage.Store, *PlanService, *CloudService, PlanRequest) {
	t.Helper()
	store, plans, _, _, _ := planCase(t)
	cfg := cloudRuntimeConfig(t, true, "e4-integration-period", 0)
	upstream := httptest.NewServer(handler)
	t.Cleanup(upstream.Close)
	sources := aliyunapi.Sources(cfg)
	for i := range sources {
		sources[i].Origin = upstream.URL
		sources[i].AllowLoopbackHTTP = true
	}
	plans.ConfigRevision = "e4-integration-config"
	plans.BudgetRef = cfg.Budget().Ref
	manager, err := outbound.NewManager(store, outbound.Config{Revision: plans.ConfigRevision, Sources: sources})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.Close)
	factory, err := aliyunapi.New(manager, aliyunapi.Config{Connection: cfg, FixtureOrigin: upstream.URL, AllowLoopbackFixture: true})
	if err != nil {
		t.Fatal(err)
	}
	cloud := &CloudService{Store: store, SourceRef: cfg.ConnectionRef(), ConfigRevision: plans.ConfigRevision, Configured: true, Ready: true, MaxHTTPAttempts: cfg.MaxHTTPAttempts(), Scopes: map[string]aliyuncollector.Config{}, NewReader: func(ctx context.Context, id string) (CloudReader, error) { return factory.Reader(ctx, id) }}
	var scope domain.Scope
	for _, s := range cfg.Scopes() {
		if s.Boundary == boundary {
			scope = s
		}
	}
	if scope.Ref == "" {
		t.Fatal("integration scope missing")
	}
	binding, _ := cfg.ScopeBinding(scope.Ref)
	cloud.Scopes[scope.Ref] = aliyuncollector.Config{Scope: scope, ExpectedAccountID: cfg.ExpectedAccountID(), AssetRegions: map[string]string{"i-runtime-synthetic": "cn-hangzhou"}, AllowedGatewayIDs: []string{"ngw-runtime-synthetic"}, GatewayID: binding.GatewayID, GatewayRegion: binding.Region, PageSize: 50, MaxAttempts: 20, MaxPolicies: 1000}
	if err = store.RegisterConfiguredScopes(context.Background(), cfg.ConnectionRef(), plans.ConfigRevision, []domain.Scope{scope}); err != nil {
		t.Fatal(err)
	}
	if err = store.ConfigureBudgetPolicy(context.Background(), storage.BudgetPolicy{Ref: plans.BudgetRef, ApplicationCloudAttempts: 100, SessionCloudAttempts: 50, ExpiresAt: cfg.Budget().ExpiresAt}); err != nil {
		t.Fatal(err)
	}
	conv, err := store.CreateConversation(context.Background(), "本机真实SDK链", scope.Ref)
	if err != nil {
		t.Fatal(err)
	}
	plans.Cloud = cloud
	selection := PlanSelection{Kind: "assets", AssetIDs: []string{"i-runtime-synthetic"}}
	if boundary == "nat" {
		selection = PlanSelection{Kind: "gateway_policy", GatewayRef: scope.GatewayRef}
	}
	return store, plans, cloud, PlanRequest{Kind: "collect", ConversationID: conv.ID, DataSourceID: cfg.ConnectionRef(), PolicyScopeRef: scope.Ref, Selection: selection, DataPolicy: DataPolicy{Mode: "force_refresh"}, BaselineVersionID: audit.ProjectBaseline().Version, Mode: "rules_only"}
}

func TestCloudActualSDKCollectorLedgerAndSnapshotIntegration(t *testing.T) {
	for _, boundary := range []string{"nat", "internet"} {
		t.Run(boundary, func(t *testing.T) {
			var physical atomic.Int32
			store, plans, cloud, req := cloudIntegration(t, boundary, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				physical.Add(1)
				_ = r.ParseForm()
				action := r.Header.Get("x-acs-action")
				if action == "" {
					action = r.Form.Get("Action")
				}
				if r.Form.Get("MemberUid") != "" {
					t.Error("cross-account parameter entered SDK")
				}
				if action == "DescribeAssetList" && (r.Form.Get("SearchItem") != "i-runtime-synthetic" || r.Form.Get("RegionNo") != "") {
					t.Error("asset lookup mixed control-plane/asset regions")
				}
				if strings.HasPrefix(action, "DescribeNatFirewall") && r.Form.Get("NatGatewayId") != "ngw-runtime-synthetic" {
					t.Error("opaque gateway alias entered provider query")
				}
				body := cloudProtocolFixture(action)
				if body == "" {
					t.Error("unexpected provider operation", action)
					w.WriteHeader(400)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, body)
			}))
			p, err := plans.Preview(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			if physical.Load() != 0 {
				t.Fatal("preview contacted provider")
			}
			_, _, err = store.EnqueuePlan(context.Background(), p.ID, "accept-integration", plans.ConfigRevision)
			if err != nil {
				t.Fatal(err)
			}
			job, ok, err := store.Claim(context.Background())
			if err != nil || !ok {
				t.Fatal(err)
			}
			w := NewWorkflow(store)
			w.Cloud = cloud
			if err = w.Run(context.Background(), job); err != nil {
				t.Fatal(err)
			}
			view, err := store.ReadJobWorkspace(context.Background(), job.ID)
			if err != nil || view.CloudResult == nil || view.SnapshotID == "" || !view.InputSealed || view.Job.Status != "completed" {
				t.Fatalf("cloud finalization failed: %+v, %v", view, err)
			}
			expected := int32(5)
			if boundary == "internet" {
				expected = 9
			}
			if physical.Load() != expected || view.AttemptUsage.CloudAttempts != int64(expected) || view.AttemptUsage.ModelAttempts != 0 {
				t.Fatalf("HTTP/ledger mismatch physical=%d usage=%+v", physical.Load(), view.AttemptUsage)
			}
			snap, err := store.GetSnapshot(context.Background(), view.SnapshotID)
			if err != nil {
				t.Fatal(err)
			}
			if snap.Input.SourceKind != "live_readonly" || snap.Input.Completeness.ObservationConsistency != "observed_stable" || snap.Input.Semantics.DefaultAction != "unknown" {
				t.Fatal("protocol fixture input claimed stronger semantics")
			}
			// Collection does not run rules or consume a model grant. A new
			// explicit audit plan uses the resulting immutable snapshot.
			auditReq := PlanRequest{Kind: "audit", ConversationID: req.ConversationID, DataSourceID: "local-offline", PolicyScopeRef: req.PolicyScopeRef, Selection: req.Selection, DataPolicy: DataPolicy{Mode: "pinned_snapshot", SnapshotID: view.SnapshotID}, BaselineVersionID: audit.ProjectBaseline().Version, Mode: "rules_only"}
			ap, err := plans.Preview(context.Background(), auditReq)
			if err != nil {
				t.Fatal(err)
			}
			_, _, err = store.EnqueuePlan(context.Background(), ap.ID, "audit-collected-input", plans.ConfigRevision)
			if err != nil {
				t.Fatal(err)
			}
			auditJob, ok, err := store.Claim(context.Background())
			if err != nil || !ok {
				t.Fatal(err)
			}
			if err = w.Run(context.Background(), auditJob); err != nil {
				t.Fatal(err)
			}
			if physical.Load() != expected {
				t.Fatal("fixed-input audit refreshed cloud data")
			}
			auditView, err := store.ReadJobWorkspace(context.Background(), auditJob.ID)
			if err != nil || auditView.Audit == nil || !auditView.ReportPublished {
				t.Fatal("collected input did not reach the existing audit/report chain", err)
			}
		})
	}
}

func TestCloudActualIdentityMismatchMakesNoProductRequest(t *testing.T) {
	var physical atomic.Int32
	store, plans, cloud, req := cloudIntegration(t, "nat", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		physical.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		_, _ = io.WriteString(w, strings.Replace(cloudProtocolFixture("GetCallerIdentity"), "1234567890123456", "9999999999999999", -1))
	}))
	p, err := plans.Preview(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = store.EnqueuePlan(context.Background(), p.ID, "wrong-identity", plans.ConfigRevision)
	if err != nil {
		t.Fatal(err)
	}
	j, ok, err := store.Claim(context.Background())
	if err != nil || !ok {
		t.Fatal(err)
	}
	err = cloud.Run(context.Background(), j)
	if err == nil || !strings.Contains(err.Error(), "CLOUD_ACCOUNT_MISMATCH") || physical.Load() != 1 {
		t.Fatal("identity mismatch did not close product gate", err)
	}
	view, readErr := store.ReadJobWorkspace(context.Background(), j.ID)
	if readErr != nil || view.InputSealed || view.CloudResult != nil || view.AttemptUsage.CloudAttempts != 1 {
		t.Fatal("wrong identity fabricated product results", readErr)
	}
	encoded, _ := json.Marshal(view)
	if strings.Contains(string(encoded), "9999999999999999") {
		t.Fatal("rejected identity leaked into product view")
	}
}
