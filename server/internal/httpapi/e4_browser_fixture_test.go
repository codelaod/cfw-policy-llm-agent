//go:build e4browserfixture

package httpapi_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"cfw-policy-llm-agent/server/internal/aliyunapi"
	"cfw-policy-llm-agent/server/internal/aliyuncollector"
	"cfw-policy-llm-agent/server/internal/app"
	"cfw-policy-llm-agent/server/internal/audit"
	"cfw-policy-llm-agent/server/internal/cloudconfig"
	"cfw-policy-llm-agent/server/internal/httpapi"
	"cfw-policy-llm-agent/server/internal/outbound"
	"cfw-policy-llm-agent/server/internal/storage"
)

const e4FixtureNotice = "仅本机阿里云协议人工夹具；真实固定SDK/HTTP准入/两轮collector/SQLite/SSE，未调用真实云或模型，不能作为账号能力或真实整改验收。"

type e4Fixture struct {
	store         *storage.Store
	plans         *app.PlanService
	engine        *app.Engine
	api           *httptest.Server
	token, dbPath string
}

func e4ProtocolBody(action, direction string) string {
	switch action {
	case "GetCallerIdentity":
		return `{"AccountId":"1234567890123456","IdentityType":"RAMUser","Arn":"acs:ram::1234567890123456:user/synthetic","PrincipalId":"synthetic"}`
	case "DescribeAssetList":
		return `{"TotalCount":1,"Assets":[{"BindInstanceId":"i-synthetic-browser","ResourceInstanceId":"eip-synthetic","RegionID":"cn-hangzhou","AliUid":1234567890123456,"InternetAddress":"192.0.2.10","ProtectStatus":"open"}]}`
	case "DescribeInstances":
		return `{"NextToken":"","Instances":{"Instance":[{"InstanceId":"i-synthetic-browser","RegionId":"cn-hangzhou","VpcAttributes":{"VpcId":"vpc-synthetic","PrivateIpAddress":{"IpAddress":["10.0.0.10"]}},"SecurityGroupIds":{"SecurityGroupId":["sg-synthetic"]}}]}}`
	case "DescribeSecurityGroupAttribute":
		return `{"SecurityGroupId":"sg-synthetic","RegionId":"cn-hangzhou","NextToken":"","Permissions":{"Permission":[{"IpProtocol":"tcp","PortRange":"443/443","Policy":"accept","SourceCidrIp":"0.0.0.0/0"}]}}`
	case "DescribeNatFirewallList":
		return `{"TotalCount":1,"NatFirewallList":[{"NatGatewayId":"ngw-synthetic-browser","RegionId":"cn-hangzhou","AliUid":1234567890123456,"ProxyId":"proxy-synthetic","ProxyStatus":"normal","StrictMode":1}]}`
	case "DescribeNatFirewallControlPolicy":
		return `{"TotalCount":"1","Policys":[{"AclUuid":"acl-synthetic-nat","NatGatewayId":"ngw-synthetic-browser","Source":"10.0.0.0/24","SourceType":"net","Destination":"198.51.100.0/24","DestinationType":"net","Proto":"TCP","DestPortType":"port","DestPort":"443","AclAction":"accept","Release":"true","Order":1,"ApplicationNameList":["ANY"],"RepeatType":"Permanent"}]}`
	case "DescribeControlPolicy":
		if direction == "out" {
			return `{"TotalCount":"1","Policys":[{"AclUuid":"acl-synthetic-out","Direction":"out","SourceType":"net","Source":"10.0.0.10","DestinationType":"net","Destination":"198.51.100.0/24","DestPortType":"port","DestPort":"443","Proto":"TCP","AclAction":"accept","Release":"true","Order":1,"ApplicationNameList":["ANY"],"RepeatType":"Permanent"}]}`
		}
		return `{"TotalCount":"1","Policys":[{"AclUuid":"acl-synthetic-in","Direction":"in","SourceType":"net","Source":"0.0.0.0/0","DestinationType":"net","Destination":"192.0.2.10","DestPortType":"port","DestPort":"443","Proto":"TCP","AclAction":"accept","Release":"true","Order":1,"ApplicationNameList":["ANY"],"RepeatType":"Permanent"}]}`
	}
	return ""
}

func startE4Fixture(t *testing.T, privateDir string, failure ...string) *e4Fixture {
	t.Helper()
	f := &e4Fixture{token: "synthetic-browser-e4-" + rand.Text(), dbPath: filepath.Join(privateDir, "e4-browser-"+rand.Text()+".sqlite")}
	var err error
	f.store, err = storage.Open(f.dbPath, true)
	if err != nil {
		t.Fatal("fixture storage unavailable")
	}
	t.Cleanup(func() { f.store.Close() })
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/" {
			w.WriteHeader(404)
			return
		}
		if r.ParseForm() != nil {
			w.WriteHeader(422)
			return
		}
		action := r.Header.Get("x-acs-action")
		if action == "" {
			action = r.Form.Get("Action")
		}
		if len(failure) > 0 && failure[0] == "malformed_acl" && action == "DescribeControlPolicy" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"TotalCount":"1","TotalCount":"2","Policys":[]}`)
			return
		}
		body := e4ProtocolBody(action, r.Form.Get("Direction"))
		if body == "" {
			w.WriteHeader(422)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(provider.Close)
	t.Setenv("CFW_E4_BROWSER_AK", "synthetic-browser-access-key")
	t.Setenv("CFW_E4_BROWSER_SECRET", "synthetic-browser-access-secret")
	configPath := filepath.Join(privateDir, "e4-config-"+rand.Text()+".json")
	config := `{"enabled":true,"connection_ref":"configured-aliyun-fixture","credential_mode":"env_ak","access_key_id_env":"CFW_E4_BROWSER_AK","access_key_secret_env":"CFW_E4_BROWSER_SECRET","expected_account_id":"1234567890123456","control_plane_region":"cn-hangzhou","allowed_boundaries":["internet/in","internet/out","nat/out"],"allowed_regions":["cn-hangzhou"],"allowed_assets":[{"region":"cn-hangzhou","instance_id":"i-synthetic-browser"}],"allowed_nat_gateways":[{"region":"cn-hangzhou","id":"ngw-synthetic-browser"}],"timeout_seconds":10,"max_http_attempts":30,"budget":{"ref":"local-cloud-fixture-period","expires_at":"2099-01-01T00:00:00Z","application_cloud_attempts":100,"session_cloud_attempts":60,"application_model_attempts":0,"session_model_attempts":0}}`
	if err = os.WriteFile(configPath, []byte(config), 0600); err != nil {
		t.Fatal("fixture configuration unavailable")
	}
	t.Cleanup(func() { os.Remove(configPath) })
	cfg, err := cloudconfig.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	f.plans = app.NewPlanService(f.store)
	f.plans.ConfigRevision = "local-cloud-browser-fixture"
	f.plans.BudgetRef = cfg.Budget().Ref
	sources := aliyunapi.Sources(cfg)
	for i := range sources {
		sources[i].Origin = provider.URL
		sources[i].AllowLoopbackHTTP = true
	}
	manager, err := outbound.NewManager(f.store, outbound.Config{Revision: f.plans.ConfigRevision, Sources: sources})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.Close)
	factory, err := aliyunapi.New(manager, aliyunapi.Config{Connection: cfg, FixtureOrigin: provider.URL, AllowLoopbackFixture: true})
	if err != nil {
		t.Fatal(err)
	}
	cloud := &app.CloudService{Store: f.store, SourceRef: cfg.ConnectionRef(), ConfigRevision: f.plans.ConfigRevision, Configured: true, Ready: true, MaxHTTPAttempts: cfg.MaxHTTPAttempts(), Scopes: map[string]aliyuncollector.Config{}, NewReader: func(ctx context.Context, id string) (app.CloudReader, error) { return factory.Reader(ctx, id) }}
	for _, s := range cfg.Scopes() {
		binding, _ := cfg.ScopeBinding(s.Ref)
		cloud.Scopes[s.Ref] = aliyuncollector.Config{Scope: s, ExpectedAccountID: cfg.ExpectedAccountID(), AssetRegions: map[string]string{"i-synthetic-browser": "cn-hangzhou"}, AllowedGatewayIDs: []string{"ngw-synthetic-browser"}, GatewayID: binding.GatewayID, GatewayRegion: binding.Region, PageSize: 50, MaxAttempts: 30, MaxPolicies: 1000}
	}
	if err = f.store.RegisterConfiguredScopes(context.Background(), cfg.ConnectionRef(), f.plans.ConfigRevision, cfg.Scopes()); err != nil {
		t.Fatal(err)
	}
	if err = f.store.ConfigureBudgetPolicy(context.Background(), storage.BudgetPolicy{Ref: cfg.Budget().Ref, ApplicationCloudAttempts: 100, SessionCloudAttempts: 60, ExpiresAt: time.Now().Add(35 * time.Minute).UTC().Format(time.RFC3339Nano)}); err != nil {
		t.Fatal(err)
	}
	f.plans.Cloud = cloud
	if _, err = app.NewAuditService(f.store).LoadExample(context.Background(), "internet-in"); err != nil {
		t.Fatal(err)
	}
	w := app.NewWorkflow(f.store)
	w.Cloud = cloud
	f.engine = app.New(f.store, w.Run)
	f.engine.Start()
	t.Cleanup(f.engine.Stop)
	root, _ := filepath.Abs(filepath.Join("..", "..", ".."))
	router, err := httpapi.New(f.engine, f.token, httpapi.Options{WebDir: filepath.Join(root, "web", "dist"), Plans: f.plans, SensitiveValues: cfg.SensitiveValues()})
	if err != nil {
		t.Fatal(err)
	}
	f.api = httptest.NewServer(router)
	t.Cleanup(f.api.Close)
	return f
}

func TestE4BrowserFixtureSmoke(t *testing.T) {
	f := startE4Fixture(t, t.TempDir())
	for _, scope := range f.plans.Cloud.Options().Scopes {
		if scope.Scope.Direction != "in" && scope.Scope.Boundary != "nat" {
			continue
		}
		conv, err := f.store.CreateConversation(context.Background(), "人工云协议烟测", scope.Scope.Ref)
		if err != nil {
			t.Fatal(err)
		}
		selection := app.PlanSelection{Kind: "assets", AssetIDs: []string{"i-synthetic-browser"}}
		if scope.Scope.Boundary == "nat" {
			selection = app.PlanSelection{Kind: "gateway_policy", GatewayRef: scope.Scope.GatewayRef}
		}
		preview, err := f.plans.Preview(context.Background(), app.PlanRequest{Kind: "collect", ConversationID: conv.ID, DataSourceID: f.plans.Cloud.SourceRef, PolicyScopeRef: scope.Scope.Ref, Selection: selection, DataPolicy: app.DataPolicy{Mode: "force_refresh"}, BaselineVersionID: audit.ProjectBaseline().Version, Mode: "rules_only"})
		if err != nil {
			t.Fatal(err)
		}
		job, _, err := f.engine.EnqueuePlan(context.Background(), preview.ID, "smoke-"+rand.Text(), f.plans.ConfigRevision)
		if err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(10 * time.Second)
		for {
			job, err = f.store.Get(context.Background(), job.ID)
			if err != nil {
				t.Fatal(err)
			}
			if storage.Terminal(job.Status) {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("cloud fixture did not finish")
			}
			time.Sleep(20 * time.Millisecond)
		}
		if job.Status != "completed" {
			t.Fatal("cloud fixture incomplete", job.Status, job.ErrorCode)
		}
	}
}

// Only this explicit tagged invocation starts a waiting browser service.
func TestE4BrowserFixtureServe(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "runtime")
	if err = os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	tokenPath, statePath, stopPath := filepath.Join(dir, "e4-browser-token"), filepath.Join(dir, "e4-browser-state.json"), filepath.Join(dir, "e4-browser-stop")
	if _, err = os.Stat(statePath); err == nil {
		t.Fatal("existing fixture state must be resolved")
	}
	f := startE4Fixture(t, dir)
	token, err := os.OpenFile(tokenPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal("fixture token path already in use")
	}
	if _, err = token.WriteString(f.token); err != nil {
		token.Close()
		os.Remove(tokenPath)
		t.Fatal("fixture token write failed")
	}
	token.Close()
	t.Cleanup(func() { os.Remove(tokenPath); os.Remove(statePath); os.Remove(stopPath) })
	os.Remove(stopPath)
	state := struct {
		APIURL    string `json:"api_url"`
		TokenPath string `json:"token_path"`
		DBPath    string `json:"db_path"`
		Notice    string `json:"fixture_notice"`
	}{f.api.URL, tokenPath, f.dbPath, e4FixtureNotice}
	encoded, _ := json.MarshalIndent(state, "", "  ")
	if err = os.WriteFile(statePath, append(encoded, '\n'), 0600); err != nil {
		t.Fatal("fixture state write failed")
	}
	fmt.Printf("E4 local cloud fixture ready; application URL=%s; state path=%s; stop marker=%s\n", f.api.URL, statePath, stopPath)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	limit := time.NewTimer(30 * time.Minute)
	defer limit.Stop()
	for {
		select {
		case <-limit.C:
			return
		case <-ticker.C:
			if _, err = os.Stat(stopPath); err == nil {
				return
			}
		}
	}
}
