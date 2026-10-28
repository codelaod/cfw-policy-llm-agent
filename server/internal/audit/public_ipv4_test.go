package audit

import (
	"context"
	"testing"

	"cfw-policy-llm-agent/server/internal/domain"
)

func TestDatabasePublicClassificationUsesSpecialPurposeRegistry(t *testing.T) {
	for _, tc := range []struct {
		ip     string
		public bool
	}{
		{"100.64.0.1", false}, {"100.127.255.255", false}, {"100.63.255.255", true}, {"100.128.0.0", true},
		{"198.18.0.1", false}, {"198.19.255.255", false}, {"198.17.255.255", true}, {"198.20.0.0", true},
		{"192.0.2.10", false}, {"198.51.100.10", false}, {"203.0.113.10", false},
		{"192.0.0.8", false}, {"192.0.0.9", true}, {"192.0.0.10", true}, {"192.0.0.170", false},
		{"192.31.196.1", true}, {"192.52.193.1", true}, {"192.175.48.1", true},
		{"192.88.99.2", false}, {"224.0.0.1", false}, {"240.0.0.1", false}, {"8.8.8.8", true},
	} {
		t.Run(tc.ip, func(t *testing.T) {
			in := fixture()
			service := &in.Assets[0].Services[0]
			service.Role = "database"
			service.RoleStatus = "confirmed"
			service.PublicAccess = "not_required"
			in.Policies = []domain.Policy{rule("only-this-source", tc.ip, "192.0.2.10", "accept", 1)}
			result, err := Run(context.Background(), in)
			if err != nil {
				t.Fatal(err)
			}
			finding := find(t, result, "CHK-08")
			want := "pass"
			if tc.public {
				want = "fail"
			}
			if finding.Verdict != want {
				t.Fatalf("source %s: public configuration verdict %s, want %s", tc.ip, finding.Verdict, want)
			}
		})
	}
}

func TestDeprecatedRegistryStatusRemainsUnknown(t *testing.T) {
	in := fixture()
	service := &in.Assets[0].Services[0]
	service.Role = "database"
	service.PublicAccess = "not_required"
	in.Policies = []domain.Policy{rule("deprecated-range", "192.88.99.1", "192.0.2.10", "accept", 1)}
	result, err := Run(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	finding := find(t, result, "CHK-08")
	if finding.Verdict != "insufficient_data" {
		t.Fatalf("empty IANA status must not become known safe/non-public: %+v", finding)
	}
}

func TestWildcardDatabaseQueryRetainsSpecialGlobalExceptions(t *testing.T) {
	in := fixture()
	service := &in.Assets[0].Services[0]
	service.Role = "database"
	service.PublicAccess = "not_required"
	in.Policies = []domain.Policy{rule("special-range", "192.0.0.0/24", "192.0.2.10", "accept", 1)}
	result, err := Run(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	finding := find(t, result, "CHK-08")
	if finding.Verdict != "fail" {
		t.Fatal("globally-reachable special exceptions were accidentally excluded")
	}
	for _, cell := range finding.Differences {
		if ipv4number(cell.AddressFrom) < ipv4number("192.0.0.9") || ipv4number(cell.AddressTo) > ipv4number("192.0.0.10") {
			t.Fatalf("non-global address leaked into the public configuration query: %+v", cell)
		}
	}
}

func TestRiskPortWildcardProofIsLimitedToPublicConfigurationDomain(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		dropKnown, dropUnknown bool
		want                   string
	}{
		{"only-non-public-remainder", true, true, "pass"},
		{"only-unclassified-remainder", true, false, "insufficient_data"},
		{"known-public-remainder", false, false, "fail"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := fixture()
			in.Baseline.EnabledChecks = []string{"CHK-07"}
			in.Baseline.RiskPorts = []domain.RiskPort{{Protocol: "tcp", Port: 22, Topic: "remote-administration"}}
			blocked := []string{}
			if tc.dropKnown {
				blocked = append(blocked, publicAddresses()...)
			}
			if tc.dropUnknown {
				blocked = append(blocked, uncertainPublicAddresses()...)
			}
			for i, cidr := range blocked {
				in.Policies = append(in.Policies, rule("public-drop-"+itoa(i), cidr, "192.0.2.10", "drop", i+1))
			}
			in.Policies = append(in.Policies, rule("wildcard-tail", "0.0.0.0/0", "192.0.2.10", "accept", 900000))
			result, err := Run(context.Background(), in)
			if err != nil {
				t.Fatal(err)
			}
			finding := find(t, result, "CHK-07")
			if finding.Verdict != tc.want {
				t.Fatalf("%s: wildcard proof verdict=%s want=%s", tc.name, finding.Verdict, tc.want)
			}
			if tc.want == "insufficient_data" && finding.RemediationPriority != "V0" {
				t.Fatal("unknown address classification must request verification")
			}
			queryRanges, err := parseAddresses(finding.QueryScope.Addresses)
			if err != nil || contains(queryRanges, ipv4number("100.64.0.1")) || !contains(queryRanges, ipv4number("192.0.0.9")) {
				t.Fatal("finding query does not state its actual public configuration domain")
			}
		})
	}
}
