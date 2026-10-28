package aliyuncollector

import (
	"context"
	"encoding/json"
	"testing"

	"cfw-policy-llm-agent/server/internal/domain"
)

func natPolicy(t *testing.T) map[string]json.RawMessage {
	t.Helper()
	raw, _ := natFixture("DescribeNatFirewallControlPolicy", nil)
	o, e := object(raw)
	if e != nil {
		t.Fatal(e)
	}
	rows, _, e := list(o, "Policys")
	if e != nil {
		t.Fatal(e)
	}
	return rows[0]
}
func TestConflictingLegacyApplicationCannotHideNewList(t *testing.T) {
	row := natPolicy(t)
	row["ApplicationName"] = rawValue("ANY")
	row["ApplicationNameList"] = rawValue([]string{"HTTP"})
	p, _, err := normalizePolicy(natConfig().Scope, "ngw-one", row)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := p.Conditions["ApplicationNameList"]; !exists {
		t.Fatal("legacy ANY hid a non-ANY current application list")
	}
}
func TestPolicyUnknownsNeverBecomeBroadEnabledRules(t *testing.T) {
	for _, tc := range []struct {
		name, field string
		value       any
	}{
		{"missing source type", "SourceType", nil}, {"domain destination", "DestinationType", "domain"}, {"malformed ports", "DestPort", "443/invalid"}, {"unknown release", "Release", "future"}, {"new advanced condition", "FutureMatch", map[string]any{"enabled": true}}, {"scheduled", "RepeatType", "Daily"}, {"ipv6 mismatch", "IpVersion", 6},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row := natPolicy(t)
			if tc.value == nil {
				delete(row, tc.field)
			} else {
				row[tc.field] = rawValue(tc.value)
			}
			p, _, err := normalizePolicy(natConfig().Scope, "ngw-one", row)
			if err != nil {
				t.Fatal(err)
			}
			if len(p.Conditions) == 0 {
				t.Fatalf("condition lost: %+v", p)
			}
			if tc.field == "SourceType" && len(p.Source.Values) != 0 {
				t.Fatal("unknown source type became a literal set")
			}
			if tc.field == "Release" && p.Enabled != "unknown" {
				t.Fatal("unknown release became enabled or disabled")
			}
		})
	}
}
func TestBookNameIsQueriedAndDynamicMembersRemainUnknown(t *testing.T) {
	for _, dynamic := range []bool{false, true} {
		t.Run(map[bool]string{false: "static", true: "dynamic"}[dynamic], func(t *testing.T) {
			r := &fixtureReader{read: func(op string, q map[string]string) (json.RawMessage, error) {
				if op == "DescribeAddressBook" {
					if q["Query"] != "named-sources" || q["GroupUuid"] != "" || q["GroupType"] != "ip" {
						t.Fatalf("book name treated as UUID: %+v", q)
					}
					body := map[string]any{"TotalCount": "1", "Acls": []any{map[string]any{"GroupUuid": "uuid-one", "GroupName": "named-sources", "GroupType": "ip", "AddressList": []string{"10.0.0.0/24"}, "AddressListCount": 1, "AutoAddTagEcs": 0}}}
					if dynamic {
						body["Acls"].([]any)[0].(map[string]any)["AutoAddTagEcs"] = 1
					}
					return json.Marshal(body)
				}
				if op == "DescribeNatFirewallControlPolicy" {
					row := natPolicy(t)
					row["Source"] = rawValue("named-sources")
					row["SourceType"] = rawValue("group")
					row["SourceGroupType"] = rawValue("ip")
					return json.Marshal(map[string]any{"TotalCount": "1", "Policys": []any{row}})
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
			if len(out.Input.AddressRefs) != 1 || out.Input.Policies[0].Source.Ref == "" {
				t.Fatal("reference missing")
			}
			for _, ref := range out.Input.AddressRefs {
				if ref.Complete == dynamic {
					t.Fatalf("dynamic/static state wrong: %+v", ref)
				}
			}
		})
	}
}
func TestReusedBookInlineConflictCannotDisappear(t *testing.T) {
	r := &fixtureReader{read: func(op string, q map[string]string) (json.RawMessage, error) {
		switch op {
		case "DescribeAddressBook":
			return json.RawMessage(`{"TotalCount":"1","Acls":[{"GroupUuid":"uuid-one","GroupName":"group-one","GroupType":"ip","AddressList":["10.0.0.0/24"],"AddressListCount":1}]}`), nil
		case "DescribeNatFirewallControlPolicy":
			rows := []any{}
			for i, inline := range [][]string{{"10.0.0.0/24"}, {"10.1.0.0/24"}} {
				row := natPolicy(t)
				row["AclUuid"] = rawValue([]string{"acl-one", "acl-two"}[i])
				row["Order"] = rawValue(i + 1)
				row["Source"] = rawValue("group-one")
				row["SourceType"] = rawValue("group")
				row["SourceGroupType"] = rawValue("ip")
				row["SourceGroupCidrs"] = rawValue(inline)
				rows = append(rows, row)
			}
			return json.Marshal(map[string]any{"TotalCount": "2", "Policys": rows})
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
	p := out.Input.Policies[1]
	if len(p.Conditions) == 0 {
		ref := out.Input.AddressRefs[p.Source.Ref]
		if ref.Complete || len(ref.Members) > 0 {
			t.Fatal("cached book ignored a conflicting inline member set")
		}
	}
}
func TestNewNATMetadataFieldDoesNotClaimComplete(t *testing.T) {
	r := &fixtureReader{read: func(op string, q map[string]string) (json.RawMessage, error) {
		b, e := natFixture(op, q)
		if op == "DescribeNatFirewallList" {
			var o map[string]any
			_ = json.Unmarshal(b, &o)
			o["NatFirewallList"].([]any)[0].(map[string]any)["FutureRuleGroup"] = true
			return json.Marshal(o)
		}
		return b, e
	}}
	c, e := New(r, natConfig())
	if e != nil {
		t.Fatal(e)
	}
	out, e := c.Collect(context.Background(), domain.Selection{Kind: "gateway_policy"})
	if e != nil {
		t.Fatal(e)
	}
	if out.Input.Completeness.EnumerationComplete {
		t.Fatal("unhandled metadata claimed complete semantics")
	}
}
