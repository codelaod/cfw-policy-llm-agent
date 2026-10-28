package aliyunapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"cfw-policy-llm-agent/server/internal/cloudconfig"
	"cfw-policy-llm-agent/server/internal/outbound"
)

type memoryLedger struct {
	mu                 sync.Mutex
	reserved, recorded int
}

func (l *memoryLedger) ReserveAttempt(context.Context, string, string, string, string) (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.reserved++
	return "attempt", nil
}
func (l *memoryLedger) RecordAttemptResult(context.Context, string, string, string, int, int64) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.recorded++
	return nil
}
func testConfig(t *testing.T) cloudconfig.Config {
	t.Helper()
	t.Setenv("CFW_E4_API_AK", "synthetic-access-key")
	t.Setenv("CFW_E4_API_SECRET", "synthetic-access-secret")
	body := `{"enabled":true,"connection_ref":"connection-a","credential_mode":"env_ak","access_key_id_env":"CFW_E4_API_AK","access_key_secret_env":"CFW_E4_API_SECRET","expected_account_id":"1234567890123456","control_plane_region":"cn-hangzhou","allowed_boundaries":["internet/in","internet/out","nat/out"],"allowed_regions":["cn-hangzhou"],"allowed_assets":[{"region":"cn-hangzhou","instance_id":"i-synthetic"}],"allowed_nat_gateways":[{"region":"cn-hangzhou","id":"ngw-synthetic"}],"timeout_seconds":10,"max_http_attempts":30,"budget":{"ref":"explicit-period","expires_at":"2099-01-01T00:00:00Z","application_cloud_attempts":100,"session_cloud_attempts":50,"application_model_attempts":0,"session_model_attempts":0}}`
	p := filepath.Join(t.TempDir(), "cloud.json")
	if e := os.WriteFile(p, []byte(body), 0600); e != nil {
		t.Fatal(e)
	}
	c, e := cloudconfig.Load(p)
	if e != nil {
		t.Fatal(e)
	}
	return c
}
func readerFixture(t *testing.T, handler http.Handler) (*Reader, *memoryLedger) {
	t.Helper()
	up := httptest.NewServer(handler)
	t.Cleanup(up.Close)
	cfg := testConfig(t)
	sources := Sources(cfg)
	for i := range sources {
		sources[i].Origin = up.URL
		sources[i].AllowLoopbackHTTP = true
	}
	ledger := &memoryLedger{}
	m, e := outbound.NewManager(ledger, outbound.Config{Revision: "test-revision", Sources: sources})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(m.Close)
	f, e := New(m, Config{Connection: cfg, FixtureOrigin: up.URL, AllowLoopbackFixture: true})
	if e != nil {
		t.Fatal(e)
	}
	reader, e := f.Reader(context.Background(), "job-a")
	if e != nil {
		t.Fatal(e)
	}
	return reader, ledger
}
func identity(w http.ResponseWriter) {
	io.WriteString(w, `{"AccountId":"1234567890123456","IdentityType":"RAMUser","Arn":"acs:ram::1234567890123456:user/synthetic","PrincipalId":"synthetic"}`)
}
func action(r *http.Request) string {
	r.ParseForm()
	if a := r.Header.Get("x-acs-action"); a != "" {
		return a
	}
	return r.Form.Get("Action")
}
func TestConstructionIsLocalAndIdentityGatesProductReads(t *testing.T) {
	var calls atomic.Int32
	r, l := readerFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		identity(w)
	}))
	if calls.Load() != 0 || l.reserved != 0 {
		t.Fatal("constructing factory/reader sent HTTP")
	}
	_, e := r.Read(context.Background(), "DescribeControlPolicy", map[string]string{"Direction": "in", "CurrentPage": "1", "PageSize": "50", "IpVersion": "4"})
	if ErrorCode(e) != "CLOUD_IDENTITY_NOT_VERIFIED" || calls.Load() != 0 {
		t.Fatal("product read bypassed identity", e)
	}
	if e = r.VerifyIdentity(context.Background()); e != nil {
		t.Fatal(e)
	}
	if calls.Load() != 1 || l.reserved != 1 {
		t.Fatal("identity was not durably admitted exactly once")
	}
}
func TestPinnedSDKAndTypedRPCPreserveRawPresence(t *testing.T) {
	var names []string
	r, l := readerFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a := action(r)
		names = append(names, a)
		w.Header().Set("Content-Type", "application/json")
		if a == "GetCallerIdentity" {
			identity(w)
			return
		}
		if r.Method != "POST" || r.URL.Path != "/" || r.Header.Get("Authorization") == "" {
			t.Error("signed fixed RPC contract absent")
		}
		switch a {
		case "DescribeNatFirewallList":
			io.WriteString(w, `{"TotalCount":0,"NatFirewallList":[]}`)
		case "DescribeAssetList":
			io.WriteString(w, `{"TotalCount":0,"Assets":[]}`)
		case "DescribeAddressBook":
			io.WriteString(w, `{"TotalCount":"0","Acls":[]}`)
		case "DescribeInstances":
			io.WriteString(w, `{"Instances":{"Instance":[{"InstanceId":"i-synthetic","RegionId":"cn-hangzhou","SecurityGroupIds":{"SecurityGroupId":["sg-synthetic"]}}]},"NextToken":""}`)
		case "DescribeSecurityGroupAttribute":
			io.WriteString(w, `{"SecurityGroupId":"sg-synthetic","Permissions":{"Permission":[]},"NextToken":""}`)
		default:
			io.WriteString(w, `{"Policys":[],"TotalCount":"0","NewField":{"enabled":false}}`)
		}
	}))
	if e := r.VerifyIdentity(context.Background()); e != nil {
		t.Fatal(e)
	}
	cases := []struct {
		op string
		q  map[string]string
	}{{"DescribeControlPolicy", map[string]string{"Direction": "in", "IpVersion": "4", "CurrentPage": "1", "PageSize": "50"}}, {"DescribeNatFirewallList", map[string]string{"NatGatewayId": "ngw-synthetic", "RegionNo": "cn-hangzhou", "PageNo": "1", "PageSize": "50"}}, {"DescribeNatFirewallControlPolicy", map[string]string{"NatGatewayId": "ngw-synthetic", "Direction": "out", "CurrentPage": "1", "PageSize": "50"}}, {"DescribeAddressBook", map[string]string{"Query": "static-group", "GroupType": "ip", "CurrentPage": "1", "PageSize": "50"}}, {"DescribeAssetList", map[string]string{"CurrentPage": "1", "PageSize": "50", "IpVersion": "4", "SearchItem": "i-synthetic"}}, {"DescribeInstances", map[string]string{"RegionId": "cn-hangzhou", "InstanceIds": `["i-synthetic"]`, "MaxResults": "50"}}, {"DescribeSecurityGroupAttribute", map[string]string{"RegionId": "cn-hangzhou", "SecurityGroupId": "sg-synthetic", "MaxResults": "50"}}}
	for _, c := range cases {
		b, e := r.Read(context.Background(), c.op, c.q)
		if e != nil {
			t.Fatalf("%s: %v", c.op, e)
		}
		if !json.Valid(b) {
			t.Fatal("raw JSON missing")
		}
		if c.op == "DescribeControlPolicy" && !strings.Contains(string(b), `"NewField":{"enabled":false}`) {
			t.Fatal("SDK projection discarded unknown field/presence")
		}
	}
	if len(names) != 8 || l.reserved != 8 || l.recorded != 8 {
		t.Fatal("physical attempts mismatched", names, l)
	}
}
func TestParameterExpansionIsRefusedBeforeNetwork(t *testing.T) {
	var calls atomic.Int32
	r, _ := readerFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); identity(w) }))
	if e := r.VerifyIdentity(context.Background()); e != nil {
		t.Fatal(e)
	}
	for _, c := range []struct {
		op string
		q  map[string]string
	}{{"DeleteControlPolicy", map[string]string{}}, {"DescribeControlPolicy", map[string]string{"Direction": "all", "CurrentPage": "1", "PageSize": "50"}}, {"DescribeControlPolicy", map[string]string{"Direction": "in", "CurrentPage": "1", "PageSize": "50", "Release": "true"}}, {"DescribeNatFirewallList", map[string]string{"NatGatewayId": "ngw-other", "PageNo": "1", "PageSize": "50"}}, {"DescribeInstances", map[string]string{"RegionId": "cn-beijing", "InstanceIds": `["i-synthetic"]`, "MaxResults": "50"}}, {"DescribeInstances", map[string]string{"RegionId": "cn-hangzhou", "InstanceIds": `["i-other"]`, "MaxResults": "50"}}, {"DescribeInstances", map[string]string{"RegionId": "cn-hangzhou", "MaxResults": "50"}}, {"DescribeAddressBook", map[string]string{"CurrentPage": "1", "PageSize": "50"}}} {
		if _, e := r.Read(context.Background(), c.op, c.q); e == nil {
			t.Fatal("expanded operation accepted", c.op, c.q)
		}
	}
	if calls.Load() != 1 {
		t.Fatal("invalid input reached HTTP")
	}
}
func TestIdentityMismatchRootAndMalformedNeverUnlockProductCalls(t *testing.T) {
	for _, body := range []string{`{"AccountId":"9999999999999999","IdentityType":"RAMUser"}`, `{"AccountId":"1234567890123456","IdentityType":"Account"}`, `{"AccountId":"1234567890123456","IdentityType":"RAMUser","IdentityType":"AssumedRole"}`, `{"AccountId":1234567890123456,"IdentityType":"RAMUser"}`} {
		t.Run(body, func(t *testing.T) {
			var calls atomic.Int32
			r, _ := readerFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); io.WriteString(w, body) }))
			if e := r.VerifyIdentity(context.Background()); e == nil {
				t.Fatal("invalid identity accepted")
			}
			if _, e := r.Read(context.Background(), "DescribeControlPolicy", map[string]string{"Direction": "in", "CurrentPage": "1", "PageSize": "50"}); ErrorCode(e) != "CLOUD_IDENTITY_NOT_VERIFIED" || calls.Load() != 1 {
				t.Fatal("rejected identity opened product gate", e)
			}
		})
	}
}
func TestNoSDKRetryAndControlledErrorProjection(t *testing.T) {
	var calls atomic.Int32
	r, _ := readerFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if action(r) == "GetCallerIdentity" {
			identity(w)
			return
		}
		w.WriteHeader(500)
		io.WriteString(w, `{"Code":"InternalError","Message":"PRIVATE_ENDPOINT synthetic-access-secret"}`)
	}))
	if e := r.VerifyIdentity(context.Background()); e != nil {
		t.Fatal(e)
	}
	_, e := r.Read(context.Background(), "DescribeControlPolicy", map[string]string{"Direction": "in", "CurrentPage": "1", "PageSize": "50"})
	if e == nil || calls.Load() != 2 || strings.Contains(e.Error(), "PRIVATE") || strings.Contains(e.Error(), "127.0.0.1") || strings.Contains(e.Error(), "synthetic") {
		t.Fatal("SDK retry or private error escaped", e, calls.Load())
	}
}
func TestCancellationReachesActualHTTP(t *testing.T) {
	started := make(chan struct{})
	cancelled := make(chan struct{})
	r, _ := readerFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if action(r) == "GetCallerIdentity" {
			identity(w)
			return
		}
		io.Copy(io.Discard, r.Body)
		close(started)
		<-r.Context().Done()
		close(cancelled)
	}))
	if e := r.VerifyIdentity(context.Background()); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, e := r.Read(ctx, "DescribeControlPolicy", map[string]string{"Direction": "in", "CurrentPage": "1", "PageSize": "50"})
		done <- e
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("HTTP not started")
	}
	cancel()
	select {
	case e := <-done:
		if ErrorCode(e) != "REQUEST_CANCELLED" {
			t.Fatal(e)
		}
	case <-time.After(time.Second):
		t.Fatal("cancel did not return")
	}
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("physical HTTP did not cancel")
	}
}
