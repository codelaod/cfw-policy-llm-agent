package aliyuncollector

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"cfw-policy-llm-agent/server/internal/audit"
	"cfw-policy-llm-agent/server/internal/domain"
)

func TestSelectionAndScopeRejectedBeforeRead(t *testing.T) {
	cases := []domain.Selection{{}, {Kind: "assets"}, {Kind: "assets", AssetIDs: []string{"outside"}}, {Kind: "gateway_policy", AssetIDs: []string{"anything"}}}
	for _, s := range cases {
		r := &fixtureReader{read: natFixture}
		c, e := New(r, natConfig())
		if e != nil {
			t.Fatal(e)
		}
		if _, e = c.Collect(context.Background(), s); e == nil || r.calls != 0 {
			t.Fatalf("scope widened: %+v calls%d err%v", s, r.calls, e)
		}
	}
	cfg := natConfig()
	cfg.GatewayID = "not-allowed"
	r := &fixtureReader{read: natFixture}
	if _, e := New(r, cfg); e == nil || r.calls != 0 {
		t.Fatal("gateway allowlist ignored")
	}
}
func TestTwoRoundsCompareConfigurationButIgnoreHitStatistics(t *testing.T) {
	for _, change := range []bool{false, true} {
		t.Run(map[bool]string{false: "only statistics", true: "actual config"}[change], func(t *testing.T) {
			aclCalls := 0
			r := &fixtureReader{read: func(op string, q map[string]string) (json.RawMessage, error) {
				b, e := natFixture(op, q)
				if op != "DescribeNatFirewallControlPolicy" {
					return b, e
				}
				aclCalls++
				var o map[string]any
				_ = json.Unmarshal(b, &o)
				row := o["Policys"].([]any)[0].(map[string]any)
				row["HitTimes"] = aclCalls
				row["HitLastTime"] = aclCalls * 100
				if change && aclCalls == 2 {
					row["AclAction"] = "drop"
				}
				return json.Marshal(o)
			}}
			c, e := New(r, natConfig())
			if e != nil {
				t.Fatal(e)
			}
			out, e := c.Collect(context.Background(), domain.Selection{Kind: "gateway_policy"})
			if e != nil {
				t.Fatal(e)
			}
			want := "observed_stable"
			if change {
				want = "changed"
			}
			if out.Input.Completeness.ObservationConsistency != want {
				t.Fatalf("want%s got%+v", want, out.Input.Completeness)
			}
			if out.Input.Policies[0].Action != "accept" {
				t.Fatal("later observation silently replaced fixed first input")
			}
		})
	}
}
func TestMissingArrayAndTotalAreNotSuccessfulEmptyPolicySet(t *testing.T) {
	for _, body := range []string{`{"TotalCount":"0"}`, `{"TotalCount":"0","Policys":null}`, `{"Policys":[]}`, `{"TotalCount":"bad","Policys":[]}`, `{"TotalCount":0,"Policys":[]}`} {
		t.Run(body, func(t *testing.T) {
			r := &fixtureReader{read: func(op string, q map[string]string) (json.RawMessage, error) {
				if op == "DescribeNatFirewallControlPolicy" {
					return json.RawMessage(body), nil
				}
				return natFixture(op, q)
			}}
			c, e := New(r, natConfig())
			if e != nil {
				t.Fatal(e)
			}
			out, e := c.Collect(context.Background(), domain.Selection{Kind: "gateway_policy"})
			if e != nil {
				t.Fatal(e)
			}
			if out.Input.Completeness.EnumerationComplete || out.Input.Completeness.ObservationConsistency == "observed_stable" {
				t.Fatal("missing collection treated as complete empty")
			}
		})
	}
	r := &fixtureReader{read: func(op string, q map[string]string) (json.RawMessage, error) {
		if op == "DescribeNatFirewallControlPolicy" {
			return json.RawMessage(`{"TotalCount":"0","Policys":[]}`), nil
		}
		return natFixture(op, q)
	}}
	c, _ := New(r, natConfig())
	out, e := c.Collect(context.Background(), domain.Selection{Kind: "gateway_policy"})
	if e != nil || !out.Input.Completeness.EnumerationComplete || len(out.Input.Policies) != 0 {
		t.Fatalf("real empty rejected:%v %+v", e, out.Input)
	}
}
func TestPaginationContradictionsAndDuplicateIdentities(t *testing.T) {
	for _, mode := range []string{"normal", "changing_total", "empty_middle", "duplicate", "conflict", "wrong_page"} {
		t.Run(mode, func(t *testing.T) {
			cfg := natConfig()
			cfg.PageSize = 1
			r := &fixtureReader{read: func(op string, q map[string]string) (json.RawMessage, error) {
				if op != "DescribeNatFirewallControlPolicy" {
					return natFixture(op, q)
				}
				row := natPolicy(t)
				total := "2"
				page := q["CurrentPage"]
				if page == "2" {
					row["AclUuid"] = rawValue("acl-two")
					row["Order"] = rawValue(2)
				}
				body := map[string]any{"TotalCount": total, "Policys": []any{row}, "PageNo": page, "PageSize": "1"}
				if page == "2" {
					switch mode {
					case "changing_total":
						body["TotalCount"] = "3"
					case "empty_middle":
						body["Policys"] = []any{}
					case "duplicate":
						row["AclUuid"] = rawValue("acl-one")
						row["Order"] = rawValue(1)
					case "conflict":
						row["AclUuid"] = rawValue("acl-one")
					case "wrong_page":
						body["PageNo"] = "1"
					}
				}
				return json.Marshal(body)
			}}
			c, e := New(r, cfg)
			if e != nil {
				t.Fatal(e)
			}
			out, e := c.Collect(context.Background(), domain.Selection{Kind: "gateway_policy"})
			if mode == "conflict" {
				if e != Failure("PROVIDER_IDENTITY_CONFLICT") {
					t.Fatalf("conflicting ID accepted:%v", e)
				}
				return
			}
			if e != nil {
				t.Fatal(e)
			}
			if out.Input.Completeness.EnumerationComplete != (mode == "normal") {
				t.Fatalf("pagination accepted invalid state: %+v", out.Input.Completeness)
			}
		})
	}
}
func TestWrongAccountGatewayAndMalformedJSONRejectInput(t *testing.T) {
	for _, mode := range []string{"account", "gateway", "region", "duplicate_key", "missing_acl_id", "too_large"} {
		t.Run(mode, func(t *testing.T) {
			r := &fixtureReader{read: func(op string, q map[string]string) (json.RawMessage, error) {
				b, e := natFixture(op, q)
				if op == "DescribeNatFirewallList" {
					var o map[string]any
					_ = json.Unmarshal(b, &o)
					row := o["NatFirewallList"].([]any)[0].(map[string]any)
					switch mode {
					case "account":
						row["AliUid"] = 1111111111111111
					case "gateway":
						row["NatGatewayId"] = "ngw-other"
					case "region":
						row["RegionId"] = "cn-beijing"
					case "duplicate_key":
						return json.RawMessage(`{"TotalCount":1,"TotalCount":0,"NatFirewallList":[]}`), nil
					case "too_large":
						return json.RawMessage(strings.Repeat(" ", 2<<20+1)), nil
					}
					return json.Marshal(o)
				}
				if mode == "missing_acl_id" && op == "DescribeNatFirewallControlPolicy" {
					return json.RawMessage(`{"TotalCount":"1","Policys":[{"Source":"0.0.0.0/0"}]}`), nil
				}
				return b, e
			}}
			c, _ := New(r, natConfig())
			if _, e := c.Collect(context.Background(), domain.Selection{Kind: "gateway_policy"}); e == nil {
				t.Fatal("unsafe provider input sealed")
			}
		})
	}
}
func TestSecondReadBudgetAndCancellationDoNotRestartCollection(t *testing.T) {
	cfg := natConfig()
	cfg.MaxAttempts = 3
	r := &fixtureReader{read: natFixture}
	c, _ := New(r, cfg)
	out, e := c.Collect(context.Background(), domain.Selection{Kind: "gateway_policy"})
	if e != nil || r.calls != 3 || out.Input.Completeness.EnumerationComplete || out.Input.Completeness.ObservationConsistency != "unverified" {
		t.Fatalf("budget was reset or second failure lost: calls%d %v %+v", r.calls, e, out.Input.Completeness)
	}
	ctx, cancel := context.WithCancel(context.Background())
	r = &fixtureReader{read: func(op string, q map[string]string) (json.RawMessage, error) { cancel(); return natFixture(op, q) }}
	c, _ = New(r, natConfig())
	if _, e = c.Collect(ctx, domain.Selection{Kind: "gateway_policy"}); !errors.Is(e, context.Canceled) || r.calls != 1 {
		t.Fatalf("cancelled collection retried:%v calls%d", e, r.calls)
	}
	r = &fixtureReader{read: func(string, map[string]string) (json.RawMessage, error) {
		return nil, errors.New("private endpoint secret")
	}}
	c, _ = New(r, natConfig())
	if _, e = c.Collect(context.Background(), domain.Selection{Kind: "gateway_policy"}); e != Failure("COLLECTION_READ_FAILED") {
		t.Fatalf("private error reflected: %v", e)
	}
}
func TestPolicyLimitPreservesPartialAndNoTrustedPath(t *testing.T) {
	cfg := natConfig()
	cfg.MaxPolicies = 1
	r := &fixtureReader{read: func(op string, q map[string]string) (json.RawMessage, error) {
		b, e := natFixture(op, q)
		if op == "DescribeNatFirewallControlPolicy" {
			var o map[string]any
			_ = json.Unmarshal(b, &o)
			o["TotalCount"] = "2"
			return json.Marshal(o)
		}
		return b, e
	}}
	c, _ := New(r, cfg)
	out, e := c.Collect(context.Background(), domain.Selection{Kind: "gateway_policy"})
	if e != nil || out.Input.Completeness.EnumerationComplete || len(out.Input.Policies) != 1 {
		t.Fatalf("limit invented full set:%v %+v", e, out.Input)
	}
	if e = audit.Validate(out.Input); e != nil {
		t.Fatal(e)
	}
}
