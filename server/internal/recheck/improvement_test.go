package recheck

import (
	"context"
	"math/rand"
	"testing"

	"cfw-policy-llm-agent/server/internal/audit"
	"cfw-policy-llm-agent/server/internal/domain"
)

// A real deterministic audit still fails after a known public portion is
// denied. Its exact violation set became smaller; it did not become repaired.
func TestActualKernelRecheckRecognizesStrictlySmallerViolationSet(t *testing.T) {
	oldInput := actualKernelInput()
	oldInput.Policies = []domain.Policy{{ID: "drop-unclassified-source", ScopeRef: oldInput.Scope.Ref, Source: domain.AddressSet{Values: []string{"192.88.99.0/24"}}, Destination: domain.AddressSet{Values: []string{"192.0.2.1/32"}}, Protocol: "tcp", Ports: domain.PortSet{Values: []domain.PortRange{{From: 22, To: 22}}}, Action: "drop", Priority: 1, OrderKnown: true, Enabled: "enabled"}}
	old, e := audit.Run(context.Background(), oldInput)
	if e != nil || !old.Coverage.Complete || !hasFail(old.Findings) {
		t.Fatal("old known-public fixture did not establish a complete failure", e)
	}
	newInput := copyInput(oldInput)
	newInput.SnapshotVersion = "snapshot-public-range-reduced"
	newInput.CollectedAt = "2026-10-28T03:59:00Z"
	newInput.Policies = append(newInput.Policies, domain.Policy{ID: "deny-known-subset", ScopeRef: oldInput.Scope.Ref, Source: domain.AddressSet{Values: []string{"8.8.8.0/24"}}, Destination: domain.AddressSet{Values: []string{"192.0.2.1/32"}}, Protocol: "tcp", Ports: domain.PortSet{Values: []domain.PortRange{{From: 22, To: 22}}}, Action: "drop", Priority: 2, OrderKnown: true, Enabled: "enabled"})
	next, e := audit.Run(context.Background(), newInput)
	if e != nil || !next.Coverage.Complete || !hasFail(next.Findings) {
		t.Fatal("new fixture did not preserve an independently failing remainder", e)
	}
	comparison, e := Compare(oldInput, old, newInput, next)
	if e != nil || !comparison.Comparable || len(comparison.Issues) != 1 || comparison.Issues[0].State != "improved" {
		t.Fatalf("strict subset remained indistinguishable from unchanged failure: %v %+v", e, comparison)
	}
	if comparison.Issues[0].OldFindingIDs[0] != old.Findings[0].ID || comparison.Issues[0].NewFindingIDs[0] != next.Findings[0].ID || next.Findings[0].Verdict != "fail" {
		t.Fatal("improvement changed original fail or lost old/new evidence references")
	}
}

func exactFlowCell(from, to string, low, high int) domain.Cell {
	return domain.Cell{AddressFrom: from, AddressTo: to, PortFrom: low, PortTo: high, Action: "accept", PolicyRef: "policy-before"}
}

func exactFlowPair() (domain.Input, domain.Result) {
	i, r := fixedPair()
	r.Findings[0].QueryScope = domain.Query{SubjectRef: "asset", ServiceRef: "service", FixedAddress: "192.0.2.1", Protocol: "tcp", Addresses: []string{"0.0.0.0/0"}, Ports: []domain.PortRange{{From: 1, To: 65535}}}
	return i, r
}

func TestRecheckImprovementUsesSetContainmentRatherThanCountsOrVolume(t *testing.T) {
	whole := exactFlowCell("198.51.100.0", "198.51.100.255", 20, 30)
	left := exactFlowCell("198.51.100.0", "198.51.100.127", 20, 30)
	other := exactFlowCell("198.51.101.0", "198.51.101.127", 20, 30)
	tests := []struct {
		name     string
		old, new []domain.Cell
		edit     func(*domain.Result, *domain.Result)
		want     string
	}{
		{"address_subset", []domain.Cell{whole}, []domain.Cell{left}, nil, "improved"},
		{"port_subset", []domain.Cell{whole}, []domain.Cell{exactFlowCell("198.51.100.0", "198.51.100.255", 22, 22)}, nil, "improved"},
		{"both_dimensions", []domain.Cell{whole}, []domain.Cell{exactFlowCell("198.51.100.64", "198.51.100.95", 22, 23)}, nil, "improved"},
		{"old_overlapping_union", []domain.Cell{left, exactFlowCell("198.51.100.64", "198.51.100.255", 20, 30)}, []domain.Cell{left}, nil, "improved"},
		{"old_partition_union", []domain.Cell{left, exactFlowCell("198.51.100.128", "198.51.100.255", 20, 30)}, []domain.Cell{left}, nil, "improved"},
		{"same_set_repartitioned", []domain.Cell{whole}, []domain.Cell{left, exactFlowCell("198.51.100.128", "198.51.100.255", 20, 30)}, nil, "persistent"},
		{"duplicate_record_removed", []domain.Cell{whole, whole}, []domain.Cell{whole}, nil, "persistent"},
		{"smaller_but_different_addresses", []domain.Cell{whole}, []domain.Cell{other}, nil, "persistent"},
		{"shrink_and_new_flow", []domain.Cell{whole}, []domain.Cell{left, exactFlowCell("198.51.101.1", "198.51.101.1", 22, 22)}, nil, "persistent"},
		{"expand", []domain.Cell{left}, []domain.Cell{whole}, nil, "persistent"},
		{"policy_only_change", []domain.Cell{whole}, []domain.Cell{whole}, func(_, n *domain.Result) { n.Findings[0].Differences[0].PolicyRef = "replacement-policy" }, "persistent"},
		{"protocol_changed", []domain.Cell{whole}, []domain.Cell{left}, func(_, n *domain.Result) { n.Findings[0].QueryScope.Protocol = "udp" }, "incomparable"},
		{"query_domain_changed", []domain.Cell{whole}, []domain.Cell{left}, func(_, n *domain.Result) { n.Findings[0].QueryScope.Addresses = []string{"198.51.100.0/24"} }, "incomparable"},
		{"changed_action", []domain.Cell{whole}, []domain.Cell{left}, func(_, n *domain.Result) { n.Findings[0].Differences[0].Action = "drop" }, "persistent"},
		{"missing_difference", []domain.Cell{whole}, nil, nil, "persistent"},
		{"unknown_difference", []domain.Cell{whole}, []domain.Cell{left}, func(_, n *domain.Result) { n.Findings[0].Differences[0].Action = "unknown" }, "persistent"},
		{"non_flow_candidate", []domain.Cell{whole}, []domain.Cell{{Action: "candidate", PolicyRef: "duplicate"}}, nil, "persistent"},
		{"malformed_ipv4", []domain.Cell{whole}, []domain.Cell{left}, func(_, n *domain.Result) { n.Findings[0].Differences[0].AddressFrom = "unavailable" }, "persistent"},
		{"reversed_address", []domain.Cell{whole}, []domain.Cell{left}, func(_, n *domain.Result) { n.Findings[0].Differences[0].AddressFrom = "198.51.100.200" }, "persistent"},
		{"zero_port", []domain.Cell{whole}, []domain.Cell{left}, func(_, n *domain.Result) { n.Findings[0].Differences[0].PortFrom = 0 }, "persistent"},
		{"query_mismatch_subject", []domain.Cell{whole}, []domain.Cell{left}, func(_, n *domain.Result) { n.Findings[0].QueryScope.SubjectRef = "different-asset" }, "incomparable"},
		{"out_of_query_port", []domain.Cell{whole}, []domain.Cell{left}, func(o, n *domain.Result) {
			o.Findings[0].QueryScope.Ports = []domain.PortRange{{From: 22, To: 22}}
			n.Findings[0].QueryScope.Ports = []domain.PortRange{{From: 22, To: 22}}
		}, "persistent"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			i, old := exactFlowPair()
			next := copyInput(i)
			next.SnapshotVersion = "subset-comparison"
			n := copyResult(old)
			n.Manifest = manifest(next)
			old.Findings[0].Differences, n.Findings[0].Differences = append([]domain.Cell{}, test.old...), append([]domain.Cell{}, test.new...)
			if test.edit != nil {
				test.edit(&old, &n)
			}
			result, e := Compare(i, old, next, n)
			if test.want == "incomparable" {
				if e != nil || result.Comparable || len(result.Issues) != 0 || !containsReason(result.Reasons, "issue_not_examined") {
					t.Fatalf("changed query was treated as unchanged criteria: %+v %v", result, e)
				}
				return
			}
			if e != nil || !result.Comparable || len(result.Issues) != 1 || result.Issues[0].State != test.want {
				t.Fatalf("unexpected set comparison: want=%s got=%+v error=%v", test.want, result, e)
			}
		})
	}
}

func TestLinkedRuleDisappearanceDoesNotImplySmallerCanonicalViolation(t *testing.T) {
	i, old := exactFlowPair()
	old.Findings[0].Differences = []domain.Cell{exactFlowCell("198.51.100.0", "198.51.100.255", 20, 30)}
	linked := old.Findings[0]
	linked.ID, linked.RuleID = "linked-rule", "CHK-03"
	old.Findings = append(old.Findings, linked)
	n := copyResult(old)
	n.Findings[1].Verdict, n.Findings[1].Differences = "pass", []domain.Cell{}
	comparison, e := Compare(i, old, i, n)
	if e != nil || !comparison.Comparable || len(comparison.Issues) != 1 || comparison.Issues[0].State != "persistent" {
		t.Fatal("one correlated rule ceased while the same violating flow union persisted", e, comparison)
	}
	n.Findings[0].Differences = []domain.Cell{exactFlowCell("198.51.100.0", "198.51.100.127", 20, 30)}
	comparison, e = Compare(i, old, i, n)
	if e != nil || comparison.Issues[0].State != "improved" {
		t.Fatal("linked findings hid an actual canonical subset", e, comparison)
	}
}

func TestUnchangedInapplicableSiblingDoesNotEraseExaminedFailure(t *testing.T) {
	for _, replacement := range []string{"not_applicable", "needs_confirmation", "insufficient_data", "unsupported", "failed_check_inapplicable", "changed_inapplicable_query"} {
		t.Run(replacement, func(t *testing.T) {
			i, old := exactFlowPair()
			old.Findings[0].Differences = []domain.Cell{exactFlowCell("198.51.100.0", "198.51.100.255", 20, 30)}
			sibling := old.Findings[0]
			sibling.ID, sibling.RuleID, sibling.Verdict, sibling.QueryScope, sibling.Differences = "inapplicable-role-check", "CHK-08", "not_applicable", domain.Query{}, []domain.Cell{}
			old.Findings = append(old.Findings, sibling)
			n := copyResult(old)
			n.Findings[0].Differences = []domain.Cell{exactFlowCell("198.51.100.0", "198.51.100.127", 20, 30)}
			switch replacement {
			case "failed_check_inapplicable":
				n.Findings[0].Verdict = "not_applicable"
			case "changed_inapplicable_query":
				n.Findings[1].QueryScope.SubjectRef = "other-query"
			default:
				n.Findings[1].Verdict = replacement
			}
			comparison, e := Compare(i, old, i, n)
			if e != nil {
				t.Fatal(e)
			}
			if replacement == "not_applicable" {
				if !comparison.Comparable || len(comparison.Issues) != 1 || comparison.Issues[0].State != "improved" {
					t.Fatal("unchanged inapplicable sibling swallowed a proven subset", comparison)
				}
			} else if comparison.Comparable || !containsReason(comparison.Reasons, "issue_not_examined") || len(comparison.Issues) != 0 {
				t.Fatal("unknown, changed or missing predicate became improvement", comparison)
			}
		})
	}
}

func TestComparableRecheckRejectsChangedProviderIdentityAndAllowsMetadataRename(t *testing.T) {
	for _, field := range []string{"provider_id", "region", "resource_type", "name_and_tags"} {
		t.Run(field, func(t *testing.T) {
			i, old := fixedPair()
			i.Assets[0].ProviderResourceID, i.Assets[0].Region, i.Assets[0].ResourceType = "i-provider-before", "cn-hangzhou", "ecs"
			old.Manifest = manifest(i)
			next := copyInput(i)
			switch field {
			case "provider_id":
				next.Assets[0].ProviderResourceID = "i-other-provider-object"
			case "region":
				next.Assets[0].Region = "cn-beijing"
			case "resource_type":
				next.Assets[0].ResourceType = "nat_gateway"
			case "name_and_tags":
				next.Assets[0].Name, next.Assets[0].Tags = "renamed", map[string]string{"description": "new metadata"}
			}
			n := copyResult(old)
			n.Manifest = manifest(next)
			comparison, e := Compare(i, old, next, n)
			if e != nil {
				t.Fatal(e)
			}
			if field == "name_and_tags" {
				if !comparison.Comparable {
					t.Fatal("a display rename changed canonical identity", comparison)
				}
			} else if comparison.Comparable || !containsReason(comparison.Reasons, "object_identity_changed") {
				t.Fatal("another provider resource was treated as the original object", comparison)
			}
		})
	}
}

func containsReason(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func TestFlowUnionSweepMatchesIndependentSmallGridAndIPv4Boundary(t *testing.T) {
	random := rand.New(rand.NewSource(48013))
	for trial := 0; trial < 300; trial++ {
		rectangles := []flowRectangle{}
		grid := map[[2]uint64]bool{}
		for count := random.Intn(20); count > 0; count-- {
			x, y := uint64(random.Intn(16)), uint64(random.Intn(8)+1)
			xEnd, yEnd := x+uint64(random.Intn(int(16-x))+1), y+uint64(random.Intn(int(9-y))+1)
			rectangles = append(rectangles, flowRectangle{scalarRange{x, xEnd}, scalarRange{y, yEnd}})
			for a := x; a < xEnd; a++ {
				for p := y; p < yEnd; p++ {
					grid[[2]uint64{a, p}] = true
				}
			}
		}
		if got := rectangleUnionArea(rectangles); got != uint64(len(grid)) {
			t.Fatalf("sweep did not equal independent point-set union trial=%d got=%d want=%d", trial, got, len(grid))
		}
	}
	full := []flowRectangle{{scalarRange{0, uint64(1) << 32}, scalarRange{1, 65536}}}
	if got, want := rectangleUnionArea(full), (uint64(1)<<32)*65535; got != want {
		t.Fatalf("full IPv4/port boundary overflow: got=%d want=%d", got, want)
	}
}
