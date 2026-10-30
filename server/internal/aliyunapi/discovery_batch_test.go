package aliyunapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"cfw-policy-llm-agent/server/internal/aliyuncollector"
	"cfw-policy-llm-agent/server/internal/cloudconfig"
	"cfw-policy-llm-agent/server/internal/outbound"
	"cfw-policy-llm-agent/server/internal/workload"
)

func unrestrictedFixture(t *testing.T, h http.Handler) (*Reader, *memoryLedger) {
	return discoveryPolicyFixture(t, h, []string{"cn-hangzhou"}, []cloudconfig.Gateway{})
}
func discoveryPolicyFixture(t *testing.T, h http.Handler, regions []string, gateways []cloudconfig.Gateway) (*Reader, *memoryLedger) {
	t.Helper()
	t.Setenv("API_PUBLIC_DISCOVERY_AK", "synthetic-access-key")
	t.Setenv("API_PUBLIC_DISCOVERY_SECRET", "synthetic-access-secret")
	raw := `{"enabled":true,"connection_ref":"public-discovery","credential_mode":"env_ak","access_key_id_env":"API_PUBLIC_DISCOVERY_AK","access_key_secret_env":"API_PUBLIC_DISCOVERY_SECRET","expected_account_id":"1234567890123456","control_plane_region":"cn-hangzhou","allowed_boundaries":["internet/in","nat/out"],"allowed_regions":["cn-hangzhou"],"allowed_assets":[],"allowed_nat_gateways":[],"timeout_seconds":10,"max_http_attempts":100,"budget":{"ref":"public-period","expires_at":"2099-01-01T00:00:00Z","application_cloud_attempts":1000,"session_cloud_attempts":500,"application_model_attempts":0,"session_model_attempts":0}}`
	var policy map[string]any
	if e := json.Unmarshal([]byte(raw), &policy); e != nil {
		t.Fatal(e)
	}
	policy["allowed_regions"] = regions
	policy["allowed_nat_gateways"] = gateways
	encoded, e := json.Marshal(policy)
	if e != nil {
		t.Fatal(e)
	}
	raw = string(encoded)
	p := filepath.Join(t.TempDir(), "public.json")
	if e := os.WriteFile(p, []byte(raw), 0600); e != nil {
		t.Fatal(e)
	}
	cfg, e := cloudconfig.Load(p)
	if e != nil {
		t.Fatal(e)
	}
	up := httptest.NewServer(h)
	t.Cleanup(up.Close)
	sources := Sources(cfg)
	for i := range sources {
		sources[i].Origin = up.URL
		sources[i].AllowLoopbackHTTP = true
	}
	ledger := &memoryLedger{}
	manager, e := outbound.NewManager(ledger, outbound.Config{Revision: "public-revision", Sources: sources})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(manager.Close)
	f, e := New(manager, Config{Connection: cfg, FixtureOrigin: up.URL, AllowLoopbackFixture: true})
	if e != nil {
		t.Fatal(e)
	}
	r, e := f.Reader(context.Background(), "public-discovery-job")
	if e != nil {
		t.Fatal(e)
	}
	return r, ledger
}
func TestMetadataNameTagsBatchIDsAndObservedCFWBinding(t *testing.T) {
	calls := 0
	r, l := unrestrictedFixture(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		switch action(req) {
		case "GetCallerIdentity":
			identity(w)
		case "DescribeInstances":
			req.ParseForm()
			if req.Form.Get("InstanceName") != "web-*" || req.Form.Get("Tag.1.Key") != "app" || req.Form.Get("Tag.1.Value") != "payment" {
				t.Error("typed metadata filters not preserved")
			}
			io.WriteString(w, `{"NextToken":"","Instances":{"Instance":[{"InstanceId":"i-not-prelisted","InstanceName":"web-public","Tags":{"Tag":[{"TagKey":"app","TagValue":"payment"}]},"RegionId":"cn-hangzhou","SecurityGroupIds":{"SecurityGroupId":["sg-shared"]}}]}}`)
		case "DescribeAssetList":
			io.WriteString(w, `{"TotalCount":1,"Assets":[{"BindInstanceId":"i-not-prelisted","RegionID":"cn-hangzhou","InternetAddress":"192.0.2.1"}]}`)
		case "DescribeSecurityGroupAttribute":
			io.WriteString(w, `{"SecurityGroupId":"sg-shared","RegionId":"cn-hangzhou","NextToken":"","Permissions":{"Permission":[]}}`)
		default:
			t.Error("unexpected action", action(req))
			io.WriteString(w, `{}`)
		}
	}))
	if e := r.VerifyIdentity(context.Background()); e != nil {
		t.Fatal(e)
	}
	if _, e := r.Read(context.Background(), "DescribeAssetList", map[string]string{"SearchItem": "i-not-prelisted", "CurrentPage": "1", "PageSize": "50", "IpVersion": "4"}); e == nil || calls != 1 {
		t.Fatal("CFW ID not proven in allowed region reached provider", e)
	}
	if _, e := r.Read(context.Background(), "DescribeInstances", map[string]string{"RegionId": "cn-hangzhou", "InstanceName": "web-*", "Tag.1.Key": "app", "Tag.1.Value": "payment", "MaxResults": "100"}); e != nil {
		t.Fatal(e)
	}
	if _, e := r.Read(context.Background(), "DescribeAssetList", map[string]string{"SearchItem": "i-not-prelisted", "CurrentPage": "1", "PageSize": "50", "IpVersion": "4"}); e != nil {
		t.Fatal(e)
	}
	if _, e := r.Read(context.Background(), "DescribeSecurityGroupAttribute", map[string]string{"RegionId": "cn-hangzhou", "SecurityGroupId": "sg-shared", "Direction": "all", "MaxResults": "1000"}); e != nil {
		t.Fatal(e)
	}
	if calls != 4 || l.reserved != 4 || l.recorded != 4 {
		t.Fatalf("physical attempts not unified %d %+v", calls, l)
	}
}
func TestOfficialHundredIDBatchIsAllowedButExtrasAndUnboundedActionsAreRefused(t *testing.T) {
	var sent int
	r, _ := unrestrictedFixture(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if action(req) == "GetCallerIdentity" {
			identity(w)
			return
		}
		req.ParseForm()
		var ids []string
		json.Unmarshal([]byte(req.Form.Get("InstanceIds")), &ids)
		sent = len(ids)
		rows := []any{}
		for _, id := range ids {
			rows = append(rows, map[string]string{"InstanceId": id, "RegionId": "cn-hangzhou"})
		}
		json.NewEncoder(w).Encode(map[string]any{"NextToken": "", "Instances": map[string]any{"Instance": rows}})
	}))
	if e := r.VerifyIdentity(context.Background()); e != nil {
		t.Fatal(e)
	}
	ids := []string{}
	for i := 0; i < 100; i++ {
		ids = append(ids, "i-public-"+strconv.Itoa(i))
	}
	raw, _ := json.Marshal(ids)
	q := map[string]string{"RegionId": "cn-hangzhou", "InstanceIds": string(raw), "MaxResults": "100"}
	if _, e := r.Read(context.Background(), "DescribeInstances", q); e != nil || sent != 100 {
		t.Fatal("100-ID contract refused", e, sent)
	}
	for _, query := range []map[string]string{{"RegionId": "cn-beijing", "InstanceName": "web-*", "MaxResults": "100"}, {"RegionId": "cn-hangzhou", "InstanceName": "web-*", "MaxResults": "101"}, {"RegionId": "cn-hangzhou", "Tag.21.Key": "x", "MaxResults": "50"}, {"RegionId": "cn-hangzhou", "Tag.1.Value": "x", "MaxResults": "50"}} {
		if _, e := r.Read(context.Background(), "DescribeInstances", query); e == nil {
			t.Fatal("bad selector accepted", query)
		}
	}
	if _, e := r.Read(context.Background(), "DescribeNatGateways", map[string]string{"RegionId": "cn-hangzhou"}); e == nil {
		t.Fatal("new VPC operation silently enabled")
	}
}
func TestNATProxyNameDiscoveryUsesAllowedRegionAndBindsLaterGatewayPolicy(t *testing.T) {
	calls := 0
	r, _ := unrestrictedFixture(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		switch action(req) {
		case "GetCallerIdentity":
			identity(w)
		case "DescribeNatFirewallList":
			req.ParseForm()
			if req.Form.Get("ProxyName") != "nat-prod" || req.Form.Get("RegionNo") != "cn-hangzhou" {
				t.Error("NAT metadata filters missing")
			}
			io.WriteString(w, `{"TotalCount":1,"NatFirewallList":[{"NatGatewayId":"ngw-not-prelisted","ProxyId":"proxy-different","ProxyName":"nat-prod","RegionId":"cn-hangzhou"}]}`)
		case "DescribeNatFirewallControlPolicy":
			io.WriteString(w, `{"TotalCount":"0","Policys":[]}`)
		default:
			t.Error("unexpected operation")
			io.WriteString(w, `{}`)
		}
	}))
	if e := r.VerifyIdentity(context.Background()); e != nil {
		t.Fatal(e)
	}
	if _, e := r.Read(context.Background(), "DescribeNatFirewallControlPolicy", map[string]string{"NatGatewayId": "ngw-not-prelisted", "Direction": "out", "CurrentPage": "1", "PageSize": "50"}); e == nil || calls != 1 {
		t.Fatal("unobserved gateway policy reached HTTP", e)
	}
	if _, e := r.Read(context.Background(), "DescribeNatFirewallList", map[string]string{"ProxyName": "nat-prod", "RegionNo": "cn-hangzhou", "PageNo": "1", "PageSize": "50"}); e != nil {
		t.Fatal(e)
	}
	if _, e := r.Read(context.Background(), "DescribeNatFirewallControlPolicy", map[string]string{"NatGatewayId": "ngw-not-prelisted", "Direction": "out", "CurrentPage": "1", "PageSize": "50"}); e != nil {
		t.Fatal(e)
	}
	if calls != 3 {
		t.Fatal("unexpected retry", calls)
	}
}
func TestForeignReturnedRegionDoesNotAuthorizeCFWOrGroupReads(t *testing.T) {
	calls := 0
	r, _ := unrestrictedFixture(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		if action(req) == "GetCallerIdentity" {
			identity(w)
		} else {
			io.WriteString(w, `{"NextToken":"","Instances":{"Instance":[{"InstanceId":"i-foreign","RegionId":"cn-beijing","SecurityGroupIds":{"SecurityGroupId":["sg-foreign"]}}]}}`)
		}
	}))
	if e := r.VerifyIdentity(context.Background()); e != nil {
		t.Fatal(e)
	}
	if _, e := r.Read(context.Background(), "DescribeInstances", map[string]string{"RegionId": "cn-hangzhou", "InstanceName": "web-*", "MaxResults": "50"}); e == nil {
		t.Fatal("foreign region accepted")
	}
	if _, e := r.Read(context.Background(), "DescribeSecurityGroupAttribute", map[string]string{"RegionId": "cn-hangzhou", "SecurityGroupId": "sg-foreign", "MaxResults": "50"}); e == nil || calls != 2 {
		t.Fatal("foreign group capability retained", e)
	}
}

func discoveryCollectorConfig() aliyuncollector.Config {
	limits := workload.Default("collect")
	limits.CloudHTTPAttempts = 20
	return aliyuncollector.Config{ExpectedAccountID: "1234567890123456", AllowedRegions: []string{"cn-hangzhou"}, Workload: workload.Resolved{ProfileRef: "public-directory", Limits: limits}}
}
func TestSignedNATGenericNameMatchesNativeGatewayWithoutMisusingProxyName(t *testing.T) {
	calls := 0
	reader, ledger := unrestrictedFixture(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if action(req) == "GetCallerIdentity" {
			identity(w)
			return
		}
		if action(req) != "DescribeNatFirewallList" {
			t.Error("directory expanded API surface", action(req))
			io.WriteString(w, `{}`)
			return
		}
		calls++
		req.ParseForm()
		if req.Form.Get("RegionNo") != "cn-hangzhou" {
			t.Error("unbounded region")
		}
		rows := []any{}
		// Emulate the provider's strict firewall-name filter: NatGatewayName is a
		// different source field and cannot be found through ProxyName.
		if req.Form.Get("ProxyName") == "" || req.Form.Get("ProxyName") == "firewall-prod" {
			rows = append(rows, map[string]any{"NatGatewayId": "ngw-public", "NatGatewayName": "gateway-native", "ProxyName": "firewall-prod", "RegionId": "cn-hangzhou"})
		}
		json.NewEncoder(w).Encode(map[string]any{"TotalCount": len(rows), "NatFirewallList": rows})
	}))
	if err := reader.VerifyIdentity(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"gateway-native", "firewall-prod", "gateway"} {
		out, err := aliyuncollector.Discover(context.Background(), reader, discoveryCollectorConfig(), aliyuncollector.Selector{Region: "cn-hangzhou", Type: "nat_gateway", Name: name, MaxPages: 2, MaxObjects: 20})
		want := 1
		if name == "gateway" {
			want = 0
		}
		if err != nil || len(out.Resources) != want || !out.Coverage.Complete || !out.Exhausted {
			t.Fatalf("native/firewall exact-name metadata invalid: name=%q err=%v coverage=%+v resources=%d", name, err, out.Coverage, len(out.Resources))
		}
	}
	if _, err := aliyuncollector.Discover(context.Background(), reader, discoveryCollectorConfig(), aliyuncollector.Selector{Region: "cn-hangzhou", Type: "nat_gateway", Name: "gateway-*", MaxPages: 2, MaxObjects: 20}); err == nil {
		t.Fatal("unsupported NAT wildcard accepted")
	}
	if calls != 3 || ledger.reserved != 4 || ledger.recorded != 4 {
		t.Fatalf("unified bounded attempts changed: HTTP%d reserve%d record%d", calls, ledger.reserved, ledger.recorded)
	}
}
func TestSignedNATDiscoveryRestrictsGatewayAllowlistByPairedRegion(t *testing.T) {
	calls := map[string]int{}
	reader, _ := discoveryPolicyFixture(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if action(req) == "GetCallerIdentity" {
			identity(w)
			return
		}
		if action(req) != "DescribeNatFirewallList" {
			t.Error("unexpected API", action(req))
			io.WriteString(w, `{}`)
			return
		}
		req.ParseForm()
		region, id := req.Form.Get("RegionNo"), req.Form.Get("NatGatewayId")
		calls[region+"/"+id]++
		expected := map[string]string{"cn-hangzhou": "ngw-h", "cn-beijing": "ngw-b"}[region]
		if expected == "" || id != expected {
			t.Error("wrong-region gateway reached signed HTTP", region, id)
		}
		json.NewEncoder(w).Encode(map[string]any{"TotalCount": 1, "NatFirewallList": []any{map[string]any{"NatGatewayId": id, "NatGatewayName": "native-" + id, "ProxyName": "firewall-" + id, "RegionId": region}}})
	}), []string{"cn-hangzhou", "cn-beijing"}, []cloudconfig.Gateway{{Region: "cn-hangzhou", ID: "ngw-h"}, {Region: "cn-beijing", ID: "ngw-b"}})
	if err := reader.VerifyIdentity(context.Background()); err != nil {
		t.Fatal(err)
	}
	cfg := discoveryCollectorConfig()
	cfg.AllowedRegions = []string{"cn-hangzhou", "cn-beijing"}
	cfg.AllowedGatewayIDs = []string{"ngw-h", "ngw-b"}
	cfg.GatewayRegions = map[string]string{"ngw-h": "cn-hangzhou", "ngw-b": "cn-beijing"}
	for _, region := range cfg.AllowedRegions {
		out, err := aliyuncollector.Discover(context.Background(), reader, cfg, aliyuncollector.Selector{Region: region, Type: "nat_gateway", MaxPages: 3, MaxObjects: 20})
		if err != nil || len(out.Resources) != 1 || !out.Coverage.Complete || out.Resources[0].Region != region {
			t.Fatalf("paired regional allowlist mixed: region=%s err=%v coverage=%+v resources=%d", region, err, out.Coverage, len(out.Resources))
		}
	}
	if len(calls) != 2 || calls["cn-hangzhou/ngw-h"] != 1 || calls["cn-beijing/ngw-b"] != 1 {
		t.Fatalf("wrong metadata reads: %v", calls)
	}
}
