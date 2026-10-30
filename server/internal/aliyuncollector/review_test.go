package aliyuncollector

import (
	"context"
	"encoding/json"
	"testing"

	"cfw-policy-llm-agent/server/internal/domain"
)

func TestGroupEnvelopeConfigurationParticipatesInStability(t *testing.T) {
	calls := 0
	r := &fixtureReader{read: func(op string, q map[string]string) (json.RawMessage, error) {
		b, e := internetFixture(op, q)
		if op == "DescribeSecurityGroupAttribute" {
			calls++
			var o map[string]any
			_ = json.Unmarshal(b, &o)
			o["InnerAccessPolicy"] = []string{"Accept", "Drop"}[calls-1]
			return json.Marshal(o)
		}
		return b, e
	}}
	c, _ := New(r, internetConfig())
	out, e := c.Collect(context.Background(), domain.Selection{Kind: "assets", AssetIDs: []string{"i-one"}})
	if e != nil {
		t.Fatal(e)
	}
	if out.Input.Completeness.ObservationConsistency != "changed" {
		t.Fatal("SG envelope configuration disappeared from family fingerprint")
	}
}
func TestGroupMissingIdentityCannotBeComplete(t *testing.T) {
	r := &fixtureReader{read: func(op string, q map[string]string) (json.RawMessage, error) {
		b, e := internetFixture(op, q)
		if op == "DescribeSecurityGroupAttribute" {
			var o map[string]any
			_ = json.Unmarshal(b, &o)
			delete(o, "SecurityGroupId")
			return json.Marshal(o)
		}
		return b, e
	}}
	c, _ := New(r, internetConfig())
	out, e := c.Collect(context.Background(), domain.Selection{Kind: "assets", AssetIDs: []string{"i-one"}})
	if e != nil {
		t.Fatal(e)
	}
	if out.Input.Completeness.EnumerationComplete {
		t.Fatal("missing group identity became complete context")
	}
}
func TestMissingNATModeIsNotImplicitZero(t *testing.T) {
	r := &fixtureReader{read: func(op string, q map[string]string) (json.RawMessage, error) {
		b, e := natFixture(op, q)
		if op == "DescribeNatFirewallList" {
			var o map[string]any
			_ = json.Unmarshal(b, &o)
			delete(o["NatFirewallList"].([]any)[0].(map[string]any), "StrictMode")
			return json.Marshal(o)
		}
		return b, e
	}}
	c, _ := New(r, natConfig())
	out, e := c.Collect(context.Background(), domain.Selection{Kind: "gateway_policy"})
	if e != nil {
		t.Fatal(e)
	}
	if out.Input.Completeness.EnumerationComplete {
		t.Fatal("missing mode treated as explicit zero")
	}
}
func TestFirstRoundBudgetRetainsPartialGatewayAndPrivateArtifacts(t *testing.T) {
	cfg := natConfig()
	cfg.MaxAttempts = 1
	r := &fixtureReader{read: natFixture}
	c, _ := New(r, cfg)
	out, e := c.Collect(context.Background(), domain.Selection{Kind: "gateway_policy"})
	if e != nil {
		t.Fatal("verified gateway observation was discarded", e)
	}
	if len(out.Artifacts) != 1 || len(out.Observations) == 0 || out.Input.SchemaVersion != 1 || out.Input.GatewayMetadata == nil || out.Input.Completeness.EnumerationComplete || out.Input.Completeness.ObservationConsistency != "unverified" || out.Input.Protection.State != "unknown" {
		t.Fatal("bounded partial gateway lost evidence or invented complete input")
	}
	b, _ := json.Marshal(out)
	var o map[string]json.RawMessage
	_ = json.Unmarshal(b, &o)
	if _, ok := o["artifacts"]; ok {
		t.Fatal("private artifacts exposed")
	}
}

type codedFixtureFailure string

func (e codedFixtureFailure) Error() string     { return "never persist private provider wrapper" }
func (e codedFixtureFailure) ErrorCode() string { return string(e) }
func TestControlledProviderErrorCategoriesRemainUseful(t *testing.T) {
	r := &fixtureReader{read: func(string, map[string]string) (json.RawMessage, error) {
		return nil, codedFixtureFailure("CLOUD_ACCESS_DENIED")
	}}
	c, _ := New(r, natConfig())
	if _, e := c.Collect(context.Background(), domain.Selection{Kind: "gateway_policy"}); e != Failure("CLOUD_ACCESS_DENIED") {
		t.Fatalf("permission category lost:%v", e)
	}
	calls := 0
	r = &fixtureReader{read: func(op string, q map[string]string) (json.RawMessage, error) {
		calls++
		if calls == 3 {
			return nil, codedFixtureFailure("CLOUD_OWNER_MISMATCH")
		}
		return natFixture(op, q)
	}}
	c, _ = New(r, natConfig())
	if out, e := c.Collect(context.Background(), domain.Selection{Kind: "gateway_policy"}); e != Failure("CLOUD_OWNER_MISMATCH") || out.Input.SchemaVersion != 0 {
		t.Fatalf("second round scope mismatch became ordinary partial:%v %+v", e, out.Input)
	}
}
func TestProviderGatewayIDDoesNotReplaceOpaqueScopeRef(t *testing.T) {
	cfg := natConfig()
	cfg.Scope.GatewayRef = "gateway-opaque-one"
	r := &fixtureReader{read: func(op string, q map[string]string) (json.RawMessage, error) {
		if q["NatGatewayId"] != "ngw-one" {
			t.Fatalf("opaque ref sent to provider:%+v", q)
		}
		return natFixture(op, q)
	}}
	c, e := New(r, cfg)
	if e != nil {
		t.Fatal(e)
	}
	out, e := c.Collect(context.Background(), domain.Selection{Kind: "gateway_policy"})
	if e != nil {
		t.Fatal(e)
	}
	if out.Input.Scope.GatewayRef != "gateway-opaque-one" {
		t.Fatal("provider ID replaced private scope alias")
	}
}
