package audit

import (
	"context"
	"encoding/json"
	"testing"

	"cfw-policy-llm-agent/server/internal/domain"
)

func TestMissingWhitelistPreservesIndependentBaseline(t *testing.T) {
	in := fixture()
	in.Policies = []domain.Policy{rule("public-ssh", "0.0.0.0/0", "192.0.2.10", "accept", 1)}
	r, err := Run(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if find(t, r, "CHK-01").Verdict != "needs_confirmation" || find(t, r, "CHK-07").Verdict != "fail" {
		t.Fatalf("missing business suppressed baseline: %+v", r.Findings)
	}
	f := find(t, r, "CHK-07")
	if f.Origin != "rule" || f.AssessmentBasis != "synthetic_assumption" || len(f.EvidenceRefs) == 0 || len(f.Differences) == 0 {
		t.Fatalf("untraceable finding %+v", f)
	}
}

func TestBusinessDifferenceRequiredAccessAndIssueGrouping(t *testing.T) {
	in := fixture()
	in.Policies = []domain.Policy{rule("wide-ssh", "198.51.100.0/24", "192.0.2.10", "accept", 1)}
	s := &in.Assets[0].Services[0]
	s.AllowedConfirmed = true
	s.AllowedComplete = true
	s.Allowed = []domain.Access{{ScopeRef: in.Scope.Ref, Protocol: "tcp", Addresses: []string{"198.51.100.0/25"}, Ports: s.Ports, Applicability: "applicable"}}
	s.RequiredConfirmed = true
	s.Required = []domain.Access{{ScopeRef: in.Scope.Ref, Protocol: "tcp", Addresses: []string{"203.0.113.9"}, Ports: s.Ports, Applicability: "applicable"}}
	s.Allowed = append(s.Allowed, s.Required...)
	r, err := Run(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	f1, f3, f6 := find(t, r, "CHK-01"), find(t, r, "CHK-03"), find(t, r, "CHK-06")
	if f1.Verdict != "fail" || f3.Verdict != "fail" || f6.Verdict != "fail" {
		t.Fatalf("wrong business differences: %+v", r.Findings)
	}
	if f1.IssueKey != f3.IssueKey || f1.ID == f3.ID {
		t.Fatal("issue grouping lost original independent records")
	}
	if f1.Differences[0].AddressFrom != "198.51.100.128" || f1.Differences[0].AddressTo != "198.51.100.255" {
		t.Fatalf("inexact business difference: %+v", f1.Differences)
	}
}

func TestWholeAssetProtocolUpperBoundRequiresItsOwnConfirmation(t *testing.T) {
	in := fixture()
	p := rule("udp-open", "198.51.100.0/24", "192.0.2.10", "accept", 1)
	p.Protocol = "udp"
	p.Ports.Values = []domain.PortRange{{From: 53, To: 53}}
	in.Policies = []domain.Policy{p}
	r, err := Run(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if find(t, r, "CHK-02").Verdict != "needs_confirmation" {
		t.Fatal("service list inferred complete asset permission")
	}
	a := &in.Assets[0]
	a.AllowedConfirmed = true
	a.AllowedComplete = true
	a.Allowed = []domain.Access{{ScopeRef: in.Scope.Ref, Protocol: "tcp", Addresses: []string{"0.0.0.0/0"}, Ports: []domain.PortRange{{From: 443, To: 443}}, Applicability: "applicable"}}
	r, err = Run(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	udpFailure := false
	for _, finding := range r.Findings {
		if finding.RuleID == "CHK-02" && finding.QueryScope.Protocol == "udp" && finding.Verdict == "fail" {
			udpFailure = true
		}
	}
	if !udpFailure {
		t.Fatal("UDP silently omitted from full supported protocol comparison")
	}
}

func TestDatabaseRoleNeverInventsNoPublicRequirement(t *testing.T) {
	in := fixture()
	s := &in.Assets[0].Services[0]
	s.ID = "db"
	s.Role = "database"
	s.Ports = []domain.PortRange{{From: 15432, To: 15432}}
	p := rule("db-public", "0.0.0.0/0", "192.0.2.10", "accept", 1)
	p.Ports.Values = s.Ports
	in.Policies = []domain.Policy{p}
	r, err := Run(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if find(t, r, "CHK-08").Verdict != "needs_confirmation" {
		t.Fatal("database role invented access authorization")
	}
	s.PublicAccess = "not_required"
	r, err = Run(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	f := find(t, r, "CHK-08")
	if f.Verdict != "fail" || f.QueryScope.Ports[0].From != 15432 {
		t.Fatalf("actual database port not checked: %+v", f)
	}
	s.RoleStatus = "candidate"
	r, err = Run(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if find(t, r, "CHK-08").Verdict != "needs_confirmation" {
		t.Fatal("candidate promoted to confirmed role")
	}
}

func TestCoverageAndUnsupportedAreSeparateFromRisk(t *testing.T) {
	in := fixture()
	in.Protection.State = "off"
	in.Policies = []domain.Policy{rule("open", "0.0.0.0/0", "192.0.2.10", "accept", 1)}
	r, err := Run(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if find(t, r, "CHK-05").Verdict != "fail" || find(t, r, "CHK-07").Verdict != "insufficient_data" {
		t.Fatal("off layer treated as valid effective policy")
	}
	in.Protection.State = "on"
	in.Policies[0].Conditions = map[string]json.RawMessage{"application": json.RawMessage(`"uninterpreted"`)}
	r, err = Run(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if find(t, r, "CHK-07").Verdict != "unsupported" {
		t.Fatalf("unsupported semantic misclassified %+v", find(t, r, "CHK-07"))
	}
}

func TestDuplicateCandidateRequiresCompleteEqualConditions(t *testing.T) {
	in := fixture()
	a := rule("first", "198.51.100.0/24", "192.0.2.10", "drop", 1)
	b := a
	b.ID = "second"
	b.Priority = 2
	in.Policies = []domain.Policy{a, b}
	r, err := Run(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if find(t, r, "CHK-04").Verdict != "fail" {
		t.Fatal("exact duplicate candidate missing")
	}
	in.Policies[1].Conditions = map[string]json.RawMessage{"unknown": json.RawMessage(`true`)}
	r, err = Run(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if find(t, r, "CHK-04").Verdict == "fail" {
		t.Fatal("unknown condition dropped to manufacture duplicate")
	}
}

func TestExceptionIsEvaluatedAtFrozenTimeAndDoesNotEraseFact(t *testing.T) {
	in := fixture()
	in.Policies = []domain.Policy{rule("public", "0.0.0.0/0", "192.0.2.10", "accept", 1)}
	in.Baseline.Exceptions = []domain.Exception{{CheckID: "CHK-07", SubjectRef: "asset-a", ScopeRef: in.Scope.Ref, ExpiresAt: "2026-10-29T00:00:00Z", Reason: "人工临时测试", Confirmed: true}}
	r, err := Run(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	f := find(t, r, "CHK-07")
	if f.Verdict != "fail" || !f.AcceptedException {
		t.Fatal("exception erased risk fact or ignored fixed evaluation time")
	}
	in.EvaluationAt = "2026-10-30T00:00:00Z"
	r, err = Run(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if find(t, r, "CHK-07").AcceptedException {
		t.Fatal("expired exception remained accepted")
	}
}

func outbound(in domain.Input, nat bool) domain.Input {
	in.Scope.Direction = "out"
	in.Scope.Ref = "scope-out"
	in.Scope.Boundary = "internet"
	if nat {
		in.Scope.Boundary = "nat"
		in.Scope.GatewayRef = "nat-one"
	}
	in.Assets[0].ScopeRef = in.Scope.Ref
	in.Assets[0].Address = "192.0.2.10"
	in.Assets[0].Services[0].ID = "https-dependency"
	in.Assets[0].Services[0].Protocol = "tcp"
	in.Assets[0].Services[0].Ports = []domain.PortRange{{From: 443, To: 443}}
	in.Assets[0].Association = domain.Association{Status: "evidenced", GatewayRef: "nat-one", ObservationPoint: "before_translation", ObservedAt: in.EvaluationAt, EvidenceRef: "association-one"}
	in.Policies = []domain.Policy{rule("external", "192.0.2.10", "198.51.100.0/24", "accept", 1)}
	in.Policies[0].ScopeRef = in.Scope.Ref
	in.Policies[0].Ports.Values = []domain.PortRange{{From: 443, To: 443}}
	s := &in.Assets[0].Services[0]
	s.AllowedConfirmed = true
	s.AllowedComplete = true
	s.Allowed = []domain.Access{{ScopeRef: in.Scope.Ref, Protocol: "tcp", Addresses: []string{"198.51.100.0/25"}, Ports: s.Ports, Applicability: "applicable"}}
	s.RequiredConfirmed = true
	s.Required = []domain.Access{{ScopeRef: in.Scope.Ref, Protocol: "tcp", Addresses: []string{"203.0.113.9"}, Ports: s.Ports, Applicability: "applicable"}}
	s.Allowed = append(s.Allowed, s.Required...)
	return in
}
func TestOutboundUsesDestinationAxisWithoutInboundBaseline(t *testing.T) {
	in := outbound(fixture(), false)
	r, err := Run(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"OUT-01", "OUT-02"} {
		if find(t, r, id).Verdict != "fail" {
			t.Fatalf("%s missing: %+v", id, r.Findings)
		}
	}
	f := find(t, r, "OUT-01")
	if f.Differences[0].AddressFrom != "198.51.100.128" {
		t.Fatal("inbound source axis reused for outbound")
	}
	if find(t, r, "CHK-07").Verdict != "not_applicable" {
		t.Fatal("inbound high-risk source baseline applied to outbound target")
	}
	in.Assets[0].Services[0].Required[0].Applicability = "not_applicable"
	r, err = Run(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if find(t, r, "OUT-02").Verdict != "not_applicable" {
		t.Fatal("private/VPC access treated as internet refusal")
	}
}

func TestNATAssociationAndGatewayMode(t *testing.T) {
	in := outbound(fixture(), true)
	r, err := Run(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if find(t, r, "NAT-01").Verdict != "pass" {
		t.Fatal("explicit NAT association not recognized")
	}
	var required bool
	for _, f := range r.Findings {
		if f.RuleID == "NAT-02" && f.Subcheck == "OUT-02" {
			required = f.Verdict == "fail"
		}
	}
	if !required {
		t.Fatalf("NAT orchestration missing required-access subcheck: %+v", r.Findings)
	}
	in.Assets[0].Association.Status = "candidate"
	r, err = Run(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if find(t, r, "NAT-01").Verdict != "insufficient_data" {
		t.Fatal("VPC candidate promoted to NAT evidence")
	}
	for _, f := range r.Findings {
		if f.RuleID == "NAT-02" && f.Subcheck == "OUT-02" && f.Verdict == "fail" {
			t.Fatal("missing NAT association allowed asset-level certainty")
		}
	}
	in.Selection = domain.Selection{Kind: "gateway_policy", AssetIDs: []string{}}
	in.Assets = nil
	r, err = Run(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range r.Findings {
		if f.Subcheck == "OUT-01" && f.Verdict != "not_applicable" {
			t.Fatal("gateway policy invented asset matrix")
		}
	}
}
