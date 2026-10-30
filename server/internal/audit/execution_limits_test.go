package audit

import (
	"cfw-policy-llm-agent/server/internal/domain"
	"context"
	"fmt"
	"testing"
	"time"
)

func TestConfiguredCellLimitAppliesToBusinessSubtractionAsWellAsPath(t *testing.T) {
	for _, tc := range []struct {
		members, limit int
		wantLimited    bool
	}{{113, 60000, false}, {2, 1, true}} {
		in, e := SyntheticExample("internet-in")
		if e != nil {
			t.Fatal(e)
		}
		in.Assets = in.Assets[:1]
		a := &in.Assets[0]
		in.Selection.AssetIDs = []string{a.ID}
		a.Allowed = []domain.Access{}
		a.AllowedConfirmed = false
		a.AllowedComplete = false
		addresses := []string{}
		ports := []domain.PortRange{}
		for i := 0; i < tc.members; i++ {
			addresses = append(addresses, fmt.Sprintf("10.0.%d.1", i))
			ports = append(ports, domain.PortRange{From: i*2 + 1, To: i*2 + 1})
		}
		a.Services = []domain.Service{{ID: "service-explicit", Role: "web", Protocol: "tcp", Ports: []domain.PortRange{{From: 1, To: 1000}}, AllowedConfirmed: true, AllowedComplete: true, Allowed: []domain.Access{{ScopeRef: in.Scope.Ref, Protocol: "tcp", Applicability: "applicable", Addresses: addresses, Ports: ports}}}}
		in.Policies = []domain.Policy{{ID: "p-explicit", ScopeRef: in.Scope.Ref, Priority: 1, OrderKnown: true, Enabled: "enabled", Protocol: "tcp", Action: "accept", Source: domain.AddressSet{Values: []string{"0.0.0.0/0"}}, Destination: domain.AddressSet{Values: []string{a.Address}}, Ports: domain.PortSet{Values: []domain.PortRange{{From: 1, To: 1000}}}}}
		in.Baseline.EnabledChecks = []string{"CHK-03"}
		in.Baseline.Exceptions = []domain.Exception{}
		ctx, e := WithExecutionLimits(context.Background(), ExecutionLimits{MaxAssets: 60, MaxPolicies: 1200, MaxQueryCells: tc.limit, Timeout: 45 * time.Second})
		if e != nil {
			t.Fatal(e)
		}
		r, e := Run(ctx, in)
		if e != nil {
			t.Fatal(e)
		}
		if len(r.Findings) != 1 {
			t.Fatal("missing business comparison")
		}
		limited := false
		for _, v := range r.Findings[0].Limitations {
			if v == "QUERY_CELL_LIMIT" {
				limited = true
			}
		}
		if limited != tc.wantLimited {
			t.Fatalf("subtraction ignored profile: limit=%d path=%d verdict=%s limitations=%v", tc.limit, r.Coverage.EvaluatedCells, r.Findings[0].Verdict, r.Findings[0].Limitations)
		}
		if !tc.wantLimited && (r.Findings[0].Verdict != "fail" || len(r.Findings[0].Differences) == 0) {
			t.Fatal("larger profile did not finish deterministic difference")
		}
	}
}
