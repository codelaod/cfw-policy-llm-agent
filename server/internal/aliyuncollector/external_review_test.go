package aliyuncollector

import (
	"context"
	"encoding/json"
	"testing"

	"cfw-policy-llm-agent/server/internal/audit"
	"cfw-policy-llm-agent/server/internal/domain"
)

func TestReviewConflictingAddressBookRepresentationsRemainUnknown(t *testing.T) {
	r := &fixtureReader{read: func(op string, q map[string]string) (json.RawMessage, error) {
		if op == "DescribeAddressBook" {
			return json.RawMessage(`{"TotalCount":"1","Acls":[{"GroupUuid":"uuid-one","GroupName":"named-sources","GroupType":"ip","AddressList":["10.0.0.0/24"],"AddressListCount":1,"Addresses":[{"Address":"192.0.2.0/24","Note":"synthetic"}],"AutoAddTagEcs":0}]}`), nil
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
	c, err := New(r, natConfig())
	if err != nil {
		t.Fatal(err)
	}
	out, err := c.Collect(context.Background(), domain.Selection{Kind: "gateway_policy"})
	if err != nil {
		t.Fatal(err)
	}
	for _, ref := range out.Input.AddressRefs {
		if ref.Complete {
			t.Fatal("contradictory AddressList and Addresses were published as one complete static set")
		}
	}
}

func TestReviewMatchingAnnotatedAddressBooksStayComplete(t *testing.T) {
	for _, kind := range []string{"ip", "port"} {
		t.Run(kind, func(t *testing.T) {
			r := &fixtureReader{read: func(op string, q map[string]string) (json.RawMessage, error) {
				if op == "DescribeAddressBook" {
					values := []string{"10.0.0.0/24", "192.0.2.0/24"}
					annotated := []map[string]string{{"Address": "192.0.2.0/24", "Note": "synthetic note"}, {"Address": "10.0.0.0/24"}}
					if kind == "port" {
						values = []string{"443", "8443/8444"}
						annotated = []map[string]string{{"Address": "8443/8444"}, {"Address": "443/443", "Note": "synthetic note"}}
					}
					return json.Marshal(map[string]any{"TotalCount": "1", "Acls": []any{map[string]any{"GroupUuid": "uuid-one", "GroupName": "named-values", "GroupType": kind, "AddressList": values, "AddressListCount": 2, "Addresses": annotated, "AutoAddTagEcs": 0}}})
				}
				if op == "DescribeNatFirewallControlPolicy" {
					row := natPolicy(t)
					if kind == "ip" {
						row["Source"] = rawValue("named-values")
						row["SourceType"] = rawValue("group")
						row["SourceGroupType"] = rawValue("ip")
					} else {
						row["DestPortType"] = rawValue("group")
						row["DestPortGroup"] = rawValue("named-values")
					}
					return json.Marshal(map[string]any{"TotalCount": "1", "Policys": []any{row}})
				}
				return natFixture(op, q)
			}}
			c, err := New(r, natConfig())
			if err != nil {
				t.Fatal(err)
			}
			out, err := c.Collect(context.Background(), domain.Selection{Kind: "gateway_policy"})
			if err != nil {
				t.Fatal(err)
			}
			if kind == "ip" {
				if len(out.Input.AddressRefs) != 1 {
					t.Fatal("expected one IP book reference")
				}
				for _, ref := range out.Input.AddressRefs {
					if !ref.Complete {
						t.Fatal("matching reordered IP representations became incomplete")
					}
				}
			} else {
				if len(out.Input.PortRefs) != 1 {
					t.Fatal("expected one port book reference")
				}
				for _, ref := range out.Input.PortRefs {
					if !ref.Complete {
						t.Fatal("matching reordered port representations became incomplete")
					}
				}
			}
		})
	}
}

func TestReviewDocumentedAllPortSyntaxDoesNotBroadenOtherZeroPorts(t *testing.T) {
	for _, value := range []string{"0/0", "0", "0/443", "ANY", "0/0/0"} {
		t.Run(value, func(t *testing.T) {
			row := natPolicy(t)
			row["Proto"] = rawValue("ANY")
			row["DestPort"] = rawValue(value)
			policy, _, err := normalizePolicy(natConfig().Scope, "ngw-one", row)
			if err != nil {
				t.Fatal(err)
			}
			if value == "0/0" {
				if policy.Protocol != "any" || len(policy.Conditions) != 0 || len(policy.Ports.Values) != 1 || policy.Ports.Values[0] != (domain.PortRange{From: 1, To: 65535}) {
					t.Fatal("documented all-port syntax lost its supported-domain projection")
				}
			} else if len(policy.Ports.Values) != 0 || len(policy.Conditions) == 0 {
				t.Fatal("undocumented zero/ANY port syntax was broadened")
			}
		})
	}
}

func TestReviewConflictingProtectionObservationsCannotPassCurrentCoverage(t *testing.T) {
	reads := 0
	r := &fixtureReader{read: func(op string, q map[string]string) (json.RawMessage, error) {
		body, err := natFixture(op, q)
		if op == "DescribeNatFirewallList" {
			reads++
			if reads == 2 {
				var envelope map[string]any
				if json.Unmarshal(body, &envelope) != nil {
					t.Fatal("invalid fixture")
				}
				envelope["NatFirewallList"].([]any)[0].(map[string]any)["ProxyStatus"] = "closed"
				return json.Marshal(envelope)
			}
		}
		return body, err
	}}
	c, err := New(r, natConfig())
	if err != nil {
		t.Fatal(err)
	}
	out, err := c.Collect(context.Background(), domain.Selection{Kind: "gateway_policy"})
	if err != nil {
		t.Fatal(err)
	}
	if out.Input.Completeness.ObservationConsistency != "changed" {
		t.Fatal("fixture failed to record the protection change")
	}
	result, err := audit.Run(context.Background(), out.Input)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, finding := range result.Findings {
		if finding.RuleID == "NAT-02" && finding.Subcheck == "CHK-05" {
			found = true
			if finding.Verdict == "pass" {
				t.Fatal("first on observation hid the later known closed state and passed current coverage")
			}
		}
	}
	if !found {
		t.Fatal("coverage finding absent")
	}
}

func TestReviewProtectionRequiresSecondObservationButNotCompleteACL(t *testing.T) {
	for _, denyAt := range []int{3, 4} {
		t.Run(map[int]string{3: "second protection unavailable", 4: "only second ACL unavailable"}[denyAt], func(t *testing.T) {
			calls := 0
			r := &fixtureReader{read: func(op string, q map[string]string) (json.RawMessage, error) {
				calls++
				if calls == denyAt {
					return nil, codedFixtureFailure("CLOUD_ACCESS_DENIED")
				}
				return natFixture(op, q)
			}}
			c, err := New(r, natConfig())
			if err != nil {
				t.Fatal(err)
			}
			out, err := c.Collect(context.Background(), domain.Selection{Kind: "gateway_policy"})
			if err != nil {
				t.Fatal(err)
			}
			want := "unknown"
			if denyAt == 4 {
				want = "on"
			}
			if out.Input.Protection.State != want {
				t.Fatalf("protection=%s want=%s", out.Input.Protection.State, want)
			}
		})
	}
}
