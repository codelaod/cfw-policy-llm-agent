package aliyuncollector

import (
	"context"
	"encoding/json"
	"testing"

	"cfw-policy-llm-agent/server/internal/audit"
	"cfw-policy-llm-agent/server/internal/domain"
)

type fixtureReader struct {
	calls int
	read  func(string, map[string]string) (json.RawMessage, error)
}

func (r *fixtureReader) Read(ctx context.Context, op string, query map[string]string) (json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.calls++
	return r.read(op, query)
}
func natConfig() Config {
	return Config{Scope: domain.Scope{Ref: "scope-nat", Provider: "aliyun", AccountRef: "account-alias", ControlPlaneRef: "control-plane-a", IPFamily: "ipv4", Boundary: "nat", Direction: "out", PolicySetRef: "set-nat", GatewayRef: "ngw-one"}, ExpectedAccountID: "1234567890123456", AllowedGatewayIDs: []string{"ngw-one"}, GatewayID: "ngw-one", GatewayRegion: "cn-hangzhou"}
}
func natFixture(op string, q map[string]string) (json.RawMessage, error) {
	switch op {
	case "DescribeNatFirewallList":
		return json.RawMessage(`{"TotalCount":1,"NatFirewallList":[{"NatGatewayId":"ngw-one","RegionId":"cn-hangzhou","AliUid":1234567890123456,"ProxyId":"proxy-one","ProxyStatus":"normal","StrictMode":1}]}`), nil
	case "DescribeNatFirewallControlPolicy":
		return json.RawMessage(`{"TotalCount":"1","Policys":[{"AclUuid":"acl-one","NatGatewayId":"ngw-one","Source":"10.0.0.0/24","SourceType":"net","Destination":"198.51.100.0/24","DestinationType":"net","Proto":"TCP","DestPortType":"port","DestPort":"443","AclAction":"accept","Release":"true","Order":1,"ApplicationNameList":["ANY"],"RepeatType":"Permanent"}]}`), nil
	default:
		panic(op)
	}
}
func TestNATTwoRoundsDoNotInventDefaultOrAssociation(t *testing.T) {
	r := &fixtureReader{read: natFixture}
	c, err := New(r, natConfig())
	if err != nil {
		t.Fatal(err)
	}
	if r.calls != 0 {
		t.Fatal("constructor performed a provider call")
	}
	out, err := c.Collect(context.Background(), domain.Selection{Kind: "gateway_policy"})
	if err != nil {
		t.Fatal(err)
	}
	if r.calls != 4 || out.Input.SourceKind != "live_readonly" || out.Input.Semantics.DefaultAction != "unknown" || !out.Input.Completeness.EnumerationComplete || out.Input.Completeness.ObservationConsistency != "observed_stable" || out.Input.Protection.State != "on" || len(out.Input.Policies) != 1 || len(out.Input.Assets) != 0 {
		t.Fatalf("invalid two-round projection: %+v; calls=%d", out.Input, r.calls)
	}
	if err := audit.Validate(out.Input); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	_ = json.Unmarshal(b, &m)
	if _, ok := m["artifacts"]; ok {
		t.Fatal("raw provider bodies exposed by ordinary JSON")
	}
}
