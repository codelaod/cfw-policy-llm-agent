package audit

import (
	"fmt"

	"cfw-policy-llm-agent/server/internal/domain"
)

// ProjectBaseline is the single source for the adopted B2 M0 catalogue. Every
// call returns independent slices; callers cannot mutate a process-wide policy.
func ProjectBaseline() domain.Baseline {
	return domain.Baseline{Version: "B2-v1", EnabledChecks: []string{"CHK-01", "CHK-02", "CHK-03", "CHK-04", "CHK-05", "CHK-06", "CHK-07", "CHK-08", "OUT-01", "OUT-02", "NAT-01", "NAT-02"}, RiskPorts: []domain.RiskPort{{Protocol: "tcp", Port: 22, Topic: "remote-administration"}, {Protocol: "tcp", Port: 3389, Topic: "remote-administration"}, {Protocol: "tcp", Port: 3306, Topic: "database-port"}, {Protocol: "tcp", Port: 5432, Topic: "database-port"}, {Protocol: "tcp", Port: 1433, Topic: "database-port"}}, Exceptions: []domain.Exception{}}
}

// SyntheticExample is a fixed public construction, not a cloud snapshot or a
// formal evaluation dataset. It permits a credential-free offline first run.
func SyntheticExample(id string) (domain.Input, error) {
	if id != "internet-in" && id != "internet-out" && id != "nat-out" {
		return domain.Input{}, fmt.Errorf("%w: unknown public example", ErrInput)
	}
	in := domain.Input{SchemaVersion: 1, SnapshotVersion: "synthetic-" + id + "-v1", ProfileVersion: "profile-demo-v1", IntentVersion: "intent-demo-v1", NormalizerVersion: NormalizerVersion, KernelVersion: KernelVersion, SourceKind: "synthetic", CollectedAt: "2026-10-28T00:00:00Z", EvaluationAt: "2026-10-28T01:00:00Z",
		Scope:        domain.Scope{Ref: "scope-demo-" + id, Provider: "aliyun", AccountRef: "synthetic-account", ControlPlaneRef: "synthetic-control-plane", IPFamily: "ipv4", Boundary: "internet", Direction: "in", PolicySetRef: "policy-set-demo-" + id},
		Semantics:    domain.Semantics{ProfileID: "aliyun-cfw-internet-v1", Version: ProviderSemanticsVersion, DefaultAction: "accept", SourceRef: "T-06"},
		Completeness: domain.Completeness{Declared: true, EnumerationComplete: true, ObservationConsistency: "fixed_artifact", KnownGaps: []string{}},
		Protection:   domain.Protection{Applicable: true, Required: true, State: "on", EvidenceRef: "synthetic-protection-assertion"},
		Selection:    domain.Selection{Kind: "assets", AssetIDs: []string{"asset-demo-a"}}, AddressRefs: map[string]domain.AddressReference{}, PortRefs: map[string]domain.PortReference{},
		Baseline: ProjectBaseline(),
	}
	asset := domain.Asset{ID: "asset-demo-a", ScopeRef: in.Scope.Ref, Address: "192.0.2.10", AddressObserved: true, Services: []domain.Service{}}
	access := func(addresses []string, port int) domain.Access {
		return domain.Access{ScopeRef: in.Scope.Ref, Protocol: "tcp", Addresses: addresses, Ports: []domain.PortRange{{From: port, To: port}}, Applicability: "applicable"}
	}
	policy := func(name, source, destination, protocol string, ports []domain.PortRange, action string, priority int) domain.Policy {
		return domain.Policy{ID: name, ScopeRef: in.Scope.Ref, Source: domain.AddressSet{Values: []string{source}}, Destination: domain.AddressSet{Values: []string{destination}}, Protocol: protocol, Ports: domain.PortSet{Values: ports}, Action: action, Priority: priority, OrderKnown: true, Enabled: "enabled"}
	}
	if id == "internet-in" {
		https := access([]string{"0.0.0.0/0"}, 443)
		management := access([]string{"198.51.100.0/25"}, 22)
		asset.AllowedConfirmed = true
		asset.AllowedComplete = true
		asset.Allowed = []domain.Access{https, management}
		asset.Services = []domain.Service{
			{ID: "svc-https", Protocol: "tcp", Ports: https.Ports, Role: "web", RoleStatus: "confirmed", PublicAccess: "required", AllowedConfirmed: true, AllowedComplete: true, Allowed: []domain.Access{https}, RequiredConfirmed: true, Required: []domain.Access{https}},
			{ID: "svc-management", Protocol: "tcp", Ports: management.Ports, Role: "management", RoleStatus: "confirmed", PublicAccess: "required", AllowedConfirmed: true, AllowedComplete: true, Allowed: []domain.Access{management}, RequiredConfirmed: true, Required: []domain.Access{management}},
			{ID: "svc-database", Protocol: "tcp", Ports: []domain.PortRange{{From: 15432, To: 15432}}, Role: "database", RoleStatus: "confirmed", PublicAccess: "not_required", AllowedConfirmed: true, AllowedComplete: true, DenyAll: true, Allowed: []domain.Access{}, RequiredConfirmed: true, Required: []domain.Access{}},
		}
		in.Policies = []domain.Policy{
			policy("policy-https-a", "0.0.0.0/0", asset.Address, "tcp", https.Ports, "accept", 10),
			policy("policy-https-duplicate", "0.0.0.0/0", asset.Address, "tcp", https.Ports, "accept", 20),
			policy("policy-management-wide", "0.0.0.0/0", asset.Address, "tcp", management.Ports, "accept", 30),
			policy("policy-database-public", "0.0.0.0/0", asset.Address, "tcp", asset.Services[2].Ports, "accept", 40),
			policy("policy-explicit-deny-remainder", "0.0.0.0/0", asset.Address, "any", []domain.PortRange{{From: 1, To: 65535}}, "drop", 100),
		}
	} else {
		in.Scope.Direction = "out"
		if id == "nat-out" {
			in.Scope.Boundary = "nat"
			in.Scope.GatewayRef = "gateway-demo-one"
			in.Semantics.ProfileID = "aliyun-cfw-nat-v1"
			asset.Association = domain.Association{Status: "evidenced", GatewayRef: in.Scope.GatewayRef, ObservationPoint: "before_translation", ObservedAt: in.CollectedAt, EvidenceRef: "synthetic-nat-association-assertion"}
		}
		allowed := access([]string{"198.51.100.0/25", "203.0.113.9"}, 443)
		required := access([]string{"203.0.113.9"}, 443)
		asset.Services = []domain.Service{{ID: "svc-external-https", Protocol: "tcp", Ports: allowed.Ports, Role: "background", RoleStatus: "confirmed", PublicAccess: "unknown", AllowedConfirmed: true, AllowedComplete: true, Allowed: []domain.Access{allowed}, RequiredConfirmed: true, Required: []domain.Access{required}}}
		in.Policies = []domain.Policy{
			policy("policy-external-wide", asset.Address, "198.51.100.0/24", "tcp", allowed.Ports, "accept", 10),
			policy("policy-external-deny-remainder", asset.Address, "0.0.0.0/0", "any", []domain.PortRange{{From: 1, To: 65535}}, "drop", 100),
		}
	}
	in.Assets = []domain.Asset{asset}
	return in, Validate(in)
}
