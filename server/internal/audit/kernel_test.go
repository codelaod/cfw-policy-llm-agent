package audit

import (
	"context"
	"encoding/json"
	"math/rand"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"cfw-policy-llm-agent/server/internal/domain"
)

func itoa(i int) string { return strconv.Itoa(i) }

func fixture() domain.Input {
	return domain.Input{SchemaVersion: 1, SnapshotVersion: "snapshot-1", ProfileVersion: "profile-1", IntentVersion: "intent-1", NormalizerVersion: "1", KernelVersion: "1", SourceKind: "synthetic", EvaluationAt: "2026-10-28T00:00:00Z", CollectedAt: "2026-10-27T23:59:00Z",
		Scope:        domain.Scope{Ref: "scope-in", Provider: "aliyun", AccountRef: "account-demo", ControlPlaneRef: "control-plane-demo", IPFamily: "ipv4", Boundary: "internet", Direction: "in", PolicySetRef: "set-demo"},
		Semantics:    domain.Semantics{ProfileID: "synthetic-v1", Version: "1", DefaultAction: "drop", SourceRef: "synthetic-case"},
		Completeness: domain.Completeness{Declared: true, EnumerationComplete: true, ObservationConsistency: "fixed_artifact"},
		Protection:   domain.Protection{Applicable: true, Required: true, State: "on", EvidenceRef: "protection-demo"},
		Selection:    domain.Selection{Kind: "assets", AssetIDs: []string{"asset-a"}},
		Assets:       []domain.Asset{{ID: "asset-a", ScopeRef: "scope-in", Address: "192.0.2.10", AddressObserved: true, Services: []domain.Service{{ID: "ssh", Protocol: "tcp", Ports: []domain.PortRange{{From: 22, To: 22}}, Role: "management", RoleStatus: "confirmed", PublicAccess: "unknown"}}}},
		AddressRefs:  map[string]domain.AddressReference{}, PortRefs: map[string]domain.PortReference{},
		Baseline: domain.Baseline{Version: "B2-v1", EnabledChecks: []string{"CHK-01", "CHK-02", "CHK-03", "CHK-04", "CHK-05", "CHK-06", "CHK-07", "CHK-08", "OUT-01", "OUT-02", "NAT-01", "NAT-02"}, RiskPorts: []domain.RiskPort{{Protocol: "tcp", Port: 22, Topic: "remote-administration"}, {Protocol: "tcp", Port: 3389, Topic: "remote-administration"}, {Protocol: "tcp", Port: 3306, Topic: "database-port"}, {Protocol: "tcp", Port: 5432, Topic: "database-port"}, {Protocol: "tcp", Port: 1433, Topic: "database-port"}}}}
}
func rule(id, source, destination, action string, priority int) domain.Policy {
	return domain.Policy{ID: id, ScopeRef: "scope-in", Source: domain.AddressSet{Values: []string{source}}, Destination: domain.AddressSet{Values: []string{destination}}, Protocol: "tcp", Ports: domain.PortSet{Values: []domain.PortRange{{From: 22, To: 22}}}, Action: action, Priority: priority, OrderKnown: true, Enabled: "enabled"}
}
func query() domain.Query {
	return domain.Query{SubjectRef: "asset-a", ServiceRef: "ssh", FixedAddress: "192.0.2.10", Protocol: "tcp", Addresses: []string{"0.0.0.0/0"}, Ports: []domain.PortRange{{From: 22, To: 22}}}
}
func find(t *testing.T, r domain.Result, id string) domain.Finding {
	t.Helper()
	for _, f := range r.Findings {
		if f.RuleID == id {
			return f
		}
	}
	t.Fatalf("missing %s: %+v", id, r.Findings)
	return domain.Finding{}
}

func TestFirstMatchWildcardAndDefaults(t *testing.T) {
	for _, tc := range []struct {
		name     string
		policies []domain.Policy
		def      string
		want     string
		broad    bool
	}{
		{"fully-covered", []domain.Policy{rule("drop", "0.0.0.0/0", "192.0.2.10", "drop", 1), rule("allow", "0.0.0.0/0", "192.0.2.10", "accept", 2)}, "accept", "drop", false},
		{"wildcard", []domain.Policy{rule("allow", "0.0.0.0/0", "192.0.2.10", "accept", 1)}, "drop", "accept", true},
		{"default-accept", nil, "accept", "accept", true},
		{"default-drop", nil, "drop", "drop", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := fixture()
			in.Policies = tc.policies
			in.Semantics.DefaultAction = tc.def
			got, err := Evaluate(context.Background(), in, query())
			if err != nil {
				t.Fatal(err)
			}
			if !got.Complete || len(got.Cells) != 1 || got.Cells[0].Action != tc.want || got.Cells[0].Broad != tc.broad {
				t.Fatalf("got %+v", got)
			}
		})
	}
}

func TestPartialShadowPreservesExactRemainder(t *testing.T) {
	in := fixture()
	in.Policies = []domain.Policy{rule("drop", "0.0.0.0/1", "192.0.2.10", "drop", 1), rule("allow", "0.0.0.0/0", "192.0.2.10", "accept", 2)}
	r, err := Evaluate(context.Background(), in, query())
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Cells) != 2 || r.Cells[1].AddressFrom != "128.0.0.0" || r.Cells[1].AddressTo != "255.255.255.255" || r.Cells[1].Action != "accept" || !r.Cells[1].Broad {
		t.Fatalf("remaining range lost: %+v", r)
	}
}

func TestUnknownNeverSilentlySkipped(t *testing.T) {
	for _, kind := range []string{"condition", "enabled", "order", "reference", "duplicate-order", "log-action", "default", "enumeration", "changed", "protection"} {
		t.Run(kind, func(t *testing.T) {
			in := fixture()
			in.Policies = []domain.Policy{rule("first", "0.0.0.0/0", "192.0.2.10", "accept", 1), rule("last", "0.0.0.0/0", "192.0.2.10", "drop", 2)}
			switch kind {
			case "condition":
				in.Policies[0].Conditions = map[string]json.RawMessage{"application": json.RawMessage(`"HTTPS"`)}
			case "enabled":
				in.Policies[0].Enabled = "unknown"
			case "order":
				in.Policies[0].OrderKnown = false
			case "reference":
				in.Policies[0].Source = domain.AddressSet{Ref: "missing"}
			case "duplicate-order":
				in.Policies[1].Priority = 1
			case "log-action":
				in.Policies[0].Action = "log"
			case "default":
				in.Policies = nil
				in.Semantics.DefaultAction = "unknown"
			case "enumeration":
				in.Completeness.EnumerationComplete = false
			case "changed":
				in.Completeness.ObservationConsistency = "changed"
			case "protection":
				in.Protection.State = "unknown"
			}
			r, err := Evaluate(context.Background(), in, query())
			if err != nil {
				t.Fatal(err)
			}
			if r.Complete || len(r.Reasons) == 0 {
				t.Fatalf("unknown promoted: %+v", r)
			}
			for _, c := range r.Cells {
				if c.Action != "unknown" {
					t.Fatalf("known action after unknown: %+v", c)
				}
			}
		})
	}
}

func TestIrrelevantUnsupportedAndDisabledRulesDoNotPoisonQuery(t *testing.T) {
	in := fixture()
	a := rule("unrelated", "0.0.0.0/0", "192.0.2.11", "accept", 1)
	a.Conditions = map[string]json.RawMessage{"domain": json.RawMessage(`"not-read.example"`)}
	a.OrderKnown = false
	d := rule("disabled", "0.0.0.0/0", "192.0.2.10", "accept", 2)
	d.Enabled = "disabled"
	in.Policies = []domain.Policy{a, d, rule("answer", "0.0.0.0/0", "192.0.2.10", "drop", 3)}
	r, err := Evaluate(context.Background(), in, query())
	if err != nil || !r.Complete || r.Cells[0].Action != "drop" {
		t.Fatalf("irrelevant rule poisoned result: %+v %v", r, err)
	}
}

func TestStaticReferencesANYAndPortSplitting(t *testing.T) {
	in := fixture()
	in.AddressRefs["everyone"] = domain.AddressReference{ScopeRef: in.Scope.Ref, Complete: true, Members: []string{"0.0.0.0/1", "128.0.0.0/1"}}
	in.PortRefs["admin"] = domain.PortReference{ScopeRef: in.Scope.Ref, Complete: true, Members: []domain.PortRange{{From: 22, To: 22}}}
	p := rule("allow", "0.0.0.0/0", "192.0.2.10", "accept", 1)
	p.Source = domain.AddressSet{Ref: "everyone"}
	p.Ports = domain.PortSet{Ref: "admin"}
	p.Protocol = "any"
	in.Policies = []domain.Policy{p}
	q := query()
	q.Ports = []domain.PortRange{{From: 21, To: 23}}
	r, err := Evaluate(context.Background(), in, q)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Complete || len(r.Cells) != 3 || r.Cells[1].Action != "accept" || !r.Cells[1].Broad {
		t.Fatalf("static refs: %+v", r)
	}
	q.Protocol = "udp"
	r, err = Evaluate(context.Background(), in, q)
	if err != nil || r.Cells[1].Action != "accept" {
		t.Fatalf("ANY was silently TCP-only: %+v %v", r, err)
	}
}

func TestLimitsCancellationAndImmutability(t *testing.T) {
	in := fixture()
	in.Policies = []domain.Policy{rule("a", "0.0.0.0/0", "192.0.2.10", "accept", 1)}
	before, _ := json.Marshal(in)
	_, err := Run(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(in)
	if string(before) != string(after) {
		t.Fatal("Run mutated frozen input")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r, err := Evaluate(ctx, in, query())
	if err == nil && r.Complete {
		t.Fatal("cancelled query passed")
	}
	in.Policies = nil
	for i := 0; i < 250; i++ {
		p := rule(string(rune('a'+i)), "0.0.0.0/0", "192.0.2.10", "accept", i)
		p.Source.Values = []string{"198.51." + itoa(i%250) + ".0/24"}
		p.Ports.Values = []domain.PortRange{{From: 1 + i*100, To: 1 + i*100}}
		in.Policies = append(in.Policies, p)
	}
	q := query()
	q.Ports = []domain.PortRange{{From: 1, To: 65535}}
	r, err = Evaluate(context.Background(), in, q)
	if err != nil {
		t.Fatal(err)
	}
	if r.Complete || r.EvaluatedCells > 50000 || !strings.Contains(strings.Join(r.Reasons, ","), "LIMIT") {
		t.Fatalf("cell limit late/missing: %+v", r)
	}
}

func TestSmallUniverseMatchesIndependentExhaustiveReference(t *testing.T) {
	rng := rand.New(rand.NewSource(8421))
	for example := 0; example < 80; example++ {
		in := fixture()
		in.Policies = nil
		for i := 0; i < 8; i++ {
			last := rng.Intn(8)
			p := rule(itoa(i), "198.51.100."+itoa(last)+"/32", "192.0.2.10", "accept", i)
			if rng.Intn(2) == 0 {
				p.Action = "drop"
			}
			a, b := rng.Intn(8)+1, rng.Intn(8)+1
			if a > b {
				a, b = b, a
			}
			p.Ports.Values = []domain.PortRange{{From: a, To: b}}
			in.Policies = append(in.Policies, p)
		}
		q := query()
		q.Addresses = []string{"198.51.100.0/29"}
		q.Ports = []domain.PortRange{{From: 1, To: 8}}
		got, err := Evaluate(context.Background(), in, q)
		if err != nil || !got.Complete {
			t.Fatalf("evaluate: %+v %v", got, err)
		}
		for ip := 0; ip < 8; ip++ {
			for port := 1; port <= 8; port++ {
				want := "drop"
				for _, p := range in.Policies {
					if p.Source.Values[0] == "198.51.100."+itoa(ip)+"/32" && port >= p.Ports.Values[0].From && port <= p.Ports.Values[0].To {
						want = p.Action
						break
					}
				}
				actual := ""
				for _, c := range got.Cells {
					if ipv4number("198.51.100."+itoa(ip)) >= ipv4number(c.AddressFrom) && ipv4number("198.51.100."+itoa(ip)) <= ipv4number(c.AddressTo) && port >= c.PortFrom && port <= c.PortTo {
						actual = c.Action
						break
					}
				}
				if actual != want {
					t.Fatalf("case=%d ip=%d port=%d want=%s got=%s", example, ip, port, want, actual)
				}
			}
		}
	}
}

func TestInputValidationImportGuaranteesAndVersions(t *testing.T) {
	in := fixture()
	in.Semantics = domain.Semantics{ProfileID: "aliyun-cfw-internet-v1", Version: "1", DefaultAction: "accept", SourceRef: "T-06"}
	b, _ := json.Marshal(in)
	imported, err := DecodeImport(b)
	if err != nil || imported.SourceKind != "imported" {
		t.Fatalf("import guarantee: %+v %v", imported, err)
	}
	r, err := Run(context.Background(), imported)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range r.Findings {
		if f.AssessmentBasis != "imported_assumption" || len(f.Assumptions) == 0 {
			t.Fatal("import promoted or assumptions lost")
		}
	}
	for _, change := range []func(*domain.Input){func(i *domain.Input) { i.Semantics.DefaultAction = "drop" }, func(i *domain.Input) { i.Selection.AssetIDs = []string{} }, func(i *domain.Input) { i.Selection.AssetIDs = []string{"asset-a", "asset-a"} }, func(i *domain.Input) { i.Policies = []domain.Policy{rule("a", "::/0", "192.0.2.10", "accept", 1)} }, func(i *domain.Input) { i.Assets[0].ScopeRef = "other" }, func(i *domain.Input) {
		i.Assets[0].Services[0].AllowedConfirmed = true
		i.Assets[0].Services[0].AllowedComplete = true
	}, func(i *domain.Input) { i.Assets[0].Services[0].Ports = []domain.PortRange{{From: 5, To: 4}} }} {
		var copy domain.Input
		json.Unmarshal(b, &copy)
		change(&copy)
		if Validate(copy) == nil {
			t.Fatalf("invalid input accepted: %+v", copy)
		}
	}
	if _, err = DecodeImport([]byte(`{"schema_version":1,"$ref":"file:///not-read"}`)); err == nil {
		t.Fatal("external reference accepted")
	}
	if _, err = DecodeImport([]byte(strings.Repeat("[", 40) + strings.Repeat("]", 40))); err == nil {
		t.Fatal("unbounded nesting accepted")
	}
	if _, err = DecodeImport(append(b, b...)); err == nil {
		t.Fatal("multiple JSON objects accepted")
	}
	if !reflect.DeepEqual(r.Manifest.ProviderSemantics, imported.Semantics) || r.Manifest.EvaluationAt != imported.EvaluationAt {
		t.Fatal("manifest does not freeze input")
	}
}
