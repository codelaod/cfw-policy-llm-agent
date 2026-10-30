package aliyuncollector

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func discoveryConfig() Config {
	return Config{ExpectedAccountID: "1234567890123456", AllowedRegions: []string{"cn-hangzhou"}, Workload: explicitWorkload()}
}
func instanceRow(id string) map[string]any {
	return map[string]any{"InstanceId": id, "RegionId": "cn-hangzhou", "InstanceName": "web-" + id, "OSType": "linux", "Tags": map[string]any{"Tag": []any{map[string]string{"TagKey": "app", "TagValue": "payments"}}}}
}
func TestDiscoverOnlyReadsBoundedMetadataAndPreservesScope(t *testing.T) {
	r := &fixtureReader{read: func(op string, q map[string]string) (json.RawMessage, error) {
		if op != "DescribeInstances" {
			t.Fatalf("discovery fetched audit data: %s", op)
		}
		if q["RegionId"] != "cn-hangzhou" || q["InstanceName"] != "web-*" || q["Tag.1.Key"] != "app" || q["Tag.1.Value"] != "payments" {
			t.Fatalf("selector not forwarded: %+v", q)
		}
		return json.Marshal(map[string]any{"NextToken": "", "Instances": map[string]any{"Instance": []any{instanceRow("i-one"), instanceRow("i-two")}}})
	}}
	out, e := Discover(context.Background(), r, discoveryConfig(), Selector{Region: "cn-hangzhou", Type: "ecs", Name: "web-*", Tags: map[string]string{"app": "payments"}, MaxPages: 2, MaxObjects: 50})
	if e != nil {
		t.Fatal(e)
	}
	if r.calls != 1 || len(out.Resources) != 2 || !out.Coverage.Complete || out.NextCursor != "" {
		t.Fatalf("metadata enumeration invalid %+v calls%d", out, r.calls)
	}
	for _, v := range out.Resources {
		if v.ResourceType != "ecs" || v.Region != "cn-hangzhou" || v.ResourceID == "" || v.OperatingSystem != "linux" || v.Tags["app"] != "payments" || v.SourceRef != "" || v.GroupConfirmed {
			t.Fatalf("metadata upgraded or lost: %+v", v)
		}
	}
	raw, _ := json.Marshal(out)
	if strings.Contains(string(raw), "artifacts") || strings.Contains(string(raw), `"Instances":`) || strings.Contains(string(raw), `"body":`) {
		t.Fatal("raw provider body escaped discovery JSON")
	}
}
func TestDiscoverDoesNotNeedConfiguredGatewayAndNeverUsesProxyIDAsGatewayID(t *testing.T) {
	r := &fixtureReader{read: func(op string, q map[string]string) (json.RawMessage, error) {
		if op != "DescribeNatFirewallList" || q["RegionNo"] != "cn-hangzhou" || q["ProxyName"] != "" {
			t.Fatalf("unbounded gateway discovery %s %+v", op, q)
		}
		return json.RawMessage(`{"TotalCount":1,"NatFirewallList":[{"NatGatewayId":"ngw-public","NatGatewayName":"gateway-native","ProxyName":"nat-prod","ProxyId":"proxy-different","RegionId":"cn-hangzhou","AliUid":"1234567890123456"}]}`), nil
	}}
	out, e := Discover(context.Background(), r, discoveryConfig(), Selector{Region: "cn-hangzhou", Type: "nat_gateway", Name: "nat-prod", MaxPages: 2, MaxObjects: 20})
	if e != nil {
		t.Fatal(e)
	}
	if len(out.Resources) != 1 || out.Resources[0].ResourceID != "ngw-public" || out.Resources[0].ResourceID == "proxy-different" || out.Resources[0].Name != "gateway-native" || out.Resources[0].SourceNames["firewall_name"] != "nat-prod" || out.Resources[0].SourceNames["nat_gateway_name"] != "gateway-native" || !out.Coverage.Complete {
		t.Fatalf("gateway identity confused: %+v", out)
	}
}
func TestDiscoverPartialCursorDoesNotLosePageRemainderOrAcceptAnotherSelector(t *testing.T) {
	r := &fixtureReader{read: func(op string, q map[string]string) (json.RawMessage, error) {
		if op != "DescribeInstances" {
			t.Fatal(op)
		}
		return json.Marshal(map[string]any{"NextToken": "", "Instances": map[string]any{"Instance": []any{instanceRow("i-one"), instanceRow("i-two"), instanceRow("i-three")}}})
	}}
	s := Selector{Region: "cn-hangzhou", Type: "ecs", MaxPages: 2, MaxObjects: 1}
	first, e := Discover(context.Background(), r, discoveryConfig(), s)
	if e != nil || len(first.Resources) != 1 || first.Coverage.Complete || first.NextCursor == "" {
		t.Fatalf("truncation lost: %v %+v", e, first)
	}
	s.Cursor = first.NextCursor
	second, e := Discover(context.Background(), r, discoveryConfig(), s)
	if e != nil || len(second.Resources) != 1 || second.Resources[0].ResourceID == first.Resources[0].ResourceID {
		t.Fatalf("continuation lost/repeated item: %v %+v", e, second)
	}
	calls := r.calls
	s.Name = "different"
	if _, e = Discover(context.Background(), r, discoveryConfig(), s); e == nil || r.calls != calls {
		t.Fatal("cursor crossed selector", e)
	}
}
func TestDiscoveryUnknownTokenAndForeignIdentityAreNotCompleteEmpty(t *testing.T) {
	for _, mode := range []string{"missing-token", "wrong-region", "wrong-owner"} {
		t.Run(mode, func(t *testing.T) {
			r := &fixtureReader{read: func(_ string, _ map[string]string) (json.RawMessage, error) {
				row := instanceRow("i-one")
				if mode == "wrong-region" {
					row["RegionId"] = "cn-beijing"
				}
				if mode == "wrong-owner" {
					row["OwnerId"] = "1111111111111111"
				}
				body := map[string]any{"Instances": map[string]any{"Instance": []any{row}}}
				if mode != "missing-token" {
					body["NextToken"] = ""
				}
				return json.Marshal(body)
			}}
			out, e := Discover(context.Background(), r, discoveryConfig(), Selector{Region: "cn-hangzhou", Type: "ecs", MaxPages: 1, MaxObjects: 20})
			if mode == "missing-token" {
				if e != nil || out.Coverage.Complete || len(out.Resources) != 1 {
					t.Fatalf("unknown token treated empty/complete %v %+v", e, out)
				}
			} else if e == nil {
				t.Fatal("foreign metadata accepted")
			}
		})
	}
}
func TestDiscoveryScopeZeroProfileAndInvalidSelectorStopBeforeRead(t *testing.T) {
	for _, s := range []Selector{{Type: "ecs", MaxPages: 1, MaxObjects: 10}, {Region: "cn-beijing", Type: "ecs", MaxPages: 1, MaxObjects: 10}, {Region: "cn-hangzhou", Type: "vpc", MaxPages: 1, MaxObjects: 10}, {Region: "cn-hangzhou", Type: "ecs", MaxPages: 0, MaxObjects: 10}} {
		r := &fixtureReader{read: func(string, map[string]string) (json.RawMessage, error) {
			t.Fatal("invalid discovery reached provider")
			return nil, nil
		}}
		if _, e := Discover(context.Background(), r, discoveryConfig(), s); e == nil {
			t.Fatalf("selector accepted %+v", s)
		}
	}
	cfg := discoveryConfig()
	cfg.Workload.Limits.CloudHTTPAttempts = 0
	r := &fixtureReader{read: func(string, map[string]string) (json.RawMessage, error) { return nil, fmt.Errorf("should not reach") }}
	if _, e := Discover(context.Background(), r, cfg, Selector{Region: "cn-hangzhou", Type: "ecs", MaxPages: 1, MaxObjects: 10}); e == nil || r.calls != 0 {
		t.Fatal("explicit zero refilled")
	}
}

func TestMissingFilterMetadataPreservesSeenIdentityWithoutCompleteEmpty(t *testing.T) {
	r := &fixtureReader{read: func(string, map[string]string) (json.RawMessage, error) {
		return json.RawMessage(`{"NextToken":"","Instances":{"Instance":[{"InstanceId":"i-seen","RegionId":"cn-hangzhou"}]}}`), nil
	}}
	out, e := Discover(context.Background(), r, discoveryConfig(), Selector{Region: "cn-hangzhou", Type: "ecs", Name: "web-*", Tags: map[string]string{"app": "payments"}, MaxPages: 1, MaxObjects: 20})
	if e != nil || len(out.Resources) != 1 || out.Coverage.Complete || len(out.Resources[0].Tags) != 0 {
		t.Fatalf("missing predicate became absence/full coverage: %v %+v", e, out.Coverage)
	}
}
func TestDiscoveryLongTokenCycleDoesNotBecomeExhausted(t *testing.T) {
	calls := 0
	r := &fixtureReader{read: func(string, map[string]string) (json.RawMessage, error) {
		calls++
		next := []string{"a", "b", "a"}[(calls-1)%3]
		return json.Marshal(map[string]any{"NextToken": next, "Instances": map[string]any{"Instance": []any{instanceRow(fmt.Sprintf("i-%d", calls))}}})
	}}
	out, e := Discover(context.Background(), r, discoveryConfig(), Selector{Region: "cn-hangzhou", Type: "ecs", MaxPages: 6, MaxObjects: 20})
	if e != nil || out.Coverage.Complete || out.Exhausted || calls != 3 {
		t.Fatalf("token cycle accepted as complete: %v calls%d %+v", e, calls, out.Coverage)
	}
}

func TestDiscoveryECSTotalCountMismatchStaysIncompleteAcrossContinuation(t *testing.T) {
	for _, continued := range []bool{false, true} {
		t.Run(fmt.Sprint(continued), func(t *testing.T) {
			r := &fixtureReader{read: func(_ string, q map[string]string) (json.RawMessage, error) {
				next, id := "", "i-final"
				if continued && q["NextToken"] == "" {
					next, id = "next-page", "i-first"
				}
				return json.Marshal(map[string]any{"TotalCount": 3, "NextToken": next, "Instances": map[string]any{"Instance": []any{instanceRow(id)}}})
			}}
			s := Selector{Region: "cn-hangzhou", Type: "ecs", MaxPages: 1, MaxObjects: 20}
			out, e := Discover(context.Background(), r, discoveryConfig(), s)
			if e != nil {
				t.Fatal(e)
			}
			if continued {
				if out.NextCursor == "" {
					t.Fatal("first page lost continuation")
				}
				s.Cursor = out.NextCursor
				out, e = Discover(context.Background(), r, discoveryConfig(), s)
			}
			if e != nil || out.Coverage.Complete || out.Exhausted || !strings.Contains(strings.Join(out.Coverage.Limitations, ","), "DISCOVERY_ENUMERATION_UNVERIFIED") {
				t.Fatalf("provider total mismatch became exhausted/complete: %v %+v", e, out)
			}
		})
	}
}

func TestDiscoveryMalformedNameCannotProduceCompleteEmptyMatch(t *testing.T) {
	r := &fixtureReader{read: func(string, map[string]string) (json.RawMessage, error) {
		return json.RawMessage(`{"NextToken":"","Instances":{"Instance":[{"InstanceId":"i-seen","InstanceName":123,"RegionId":"cn-hangzhou"}]}}`), nil
	}}
	out, e := Discover(context.Background(), r, discoveryConfig(), Selector{Region: "cn-hangzhou", Type: "ecs", Name: "web-*", MaxPages: 1, MaxObjects: 20})
	if e != nil || len(out.Resources) != 1 || out.Coverage.Complete || !strings.Contains(strings.Join(out.Coverage.Limitations, ","), "RESOURCE_FILTER_UNVERIFIED") {
		t.Fatalf("malformed name became complete empty match: %v %+v", e, out)
	}
}

func TestDiscoveryCountSurvivesPageRemainderWithoutDoubleCounting(t *testing.T) {
	r := &fixtureReader{read: func(_ string, q map[string]string) (json.RawMessage, error) {
		rows, next := []any{instanceRow("i-one"), instanceRow("i-two")}, "next-page"
		if q["NextToken"] != "" {
			rows, next = []any{instanceRow("i-three")}, ""
		}
		return json.Marshal(map[string]any{"TotalCount": 3, "NextToken": next, "Instances": map[string]any{"Instance": rows}})
	}}
	s := Selector{Region: "cn-hangzhou", Type: "ecs", MaxPages: 1, MaxObjects: 1}
	var ids []string
	for i := 0; i < 3; i++ {
		out, e := Discover(context.Background(), r, discoveryConfig(), s)
		if e != nil || len(out.Resources) != 1 {
			t.Fatalf("valid total/remainder rejected: %v %+v", e, out)
		}
		ids = append(ids, out.Resources[0].ResourceID)
		if i < 2 && out.NextCursor == "" || i == 2 && (!out.Exhausted || out.NextCursor != "") {
			t.Fatalf("continuation count drifted: %+v", out)
		}
		s.Cursor = out.NextCursor
	}
	if strings.Join(ids, ",") != "i-one,i-two,i-three" {
		t.Fatalf("page remainder repeated or lost: %v", ids)
	}
}

func TestDiscoveryPairedGatewayRegionAndCursorBindingsRemainNarrow(t *testing.T) {
	cfg := discoveryConfig()
	cfg.AllowedRegions = []string{"cn-hangzhou", "cn-beijing"}
	cfg.GatewayRegions = map[string]string{"ngw-h1": "cn-hangzhou", "ngw-h2": "cn-hangzhou", "ngw-b": "cn-beijing"}
	cfg.AllowedGatewayIDs = []string{"ngw-h1", "ngw-h2", "ngw-b"}
	r := &fixtureReader{read: func(op string, q map[string]string) (json.RawMessage, error) {
		if op != "DescribeNatFirewallList" || q["RegionNo"] != "cn-hangzhou" || (q["NatGatewayId"] != "ngw-h1" && q["NatGatewayId"] != "ngw-h2") {
			t.Fatalf("paired scope widened: %s %+v", op, q)
		}
		return json.Marshal(map[string]any{"TotalCount": 1, "NatFirewallList": []any{map[string]string{"NatGatewayId": q["NatGatewayId"], "RegionId": "cn-hangzhou", "ProxyName": "firewall"}}})
	}}
	s := Selector{Region: "cn-hangzhou", Type: "nat_gateway", MaxPages: 3, MaxObjects: 1}
	first, err := Discover(context.Background(), r, cfg, s)
	if err != nil || len(first.Resources) != 1 || first.NextCursor == "" {
		t.Fatalf("bounded paired enumeration failed: %v %+v", err, first)
	}
	calls := r.calls
	wrong := s
	wrong.ResourceIDs = []string{"ngw-b"}
	if _, err = Discover(context.Background(), r, cfg, wrong); err == nil || r.calls != calls {
		t.Fatal("foreign-region explicit gateway reached read", err)
	}
	s.Cursor = first.NextCursor
	cfg.GatewayRegions["ngw-b"] = "cn-hangzhou"
	if _, err = Discover(context.Background(), r, cfg, s); err == nil || r.calls != calls {
		t.Fatal("cursor survived changed paired permission scope", err)
	}
}
func TestCollectorFreezesAndChecksGatewayRegionPair(t *testing.T) {
	cfg := natConfig()
	cfg.AllowedRegions = []string{"cn-hangzhou", "cn-beijing"}
	cfg.GatewayRegions = map[string]string{cfg.GatewayID: cfg.GatewayRegion}
	r := &fixtureReader{}
	c, err := New(r, cfg)
	if err != nil {
		t.Fatal(err)
	}
	cfg.GatewayRegions[cfg.GatewayID] = "cn-beijing"
	if c.cfg.GatewayRegions[cfg.GatewayID] != "cn-hangzhou" {
		t.Fatal("caller mutated fixed gateway permission pair")
	}
	if _, err = New(r, cfg); err == nil {
		t.Fatal("collector accepted wrong-region gateway pair")
	}
}
