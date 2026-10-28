package audit

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"cfw-policy-llm-agent/server/internal/domain"
)

func TestReviewCompleteBusinessUpperBoundContainsRequiredAccess(t *testing.T) {
	in := fixture()
	s := &in.Assets[0].Services[0]
	s.AllowedConfirmed = true
	s.AllowedComplete = true
	s.Allowed = []domain.Access{{ScopeRef: in.Scope.Ref, Protocol: "tcp", Addresses: []string{"198.51.100.0/25"}, Ports: s.Ports, Applicability: "applicable"}}
	s.RequiredConfirmed = true
	s.Required = []domain.Access{{ScopeRef: in.Scope.Ref, Protocol: "tcp", Addresses: []string{"203.0.113.9"}, Ports: s.Ports, Applicability: "applicable"}}
	if Validate(in) == nil {
		t.Fatal("required access outside explicitly complete upper bound accepted")
	}
}

func TestReviewEvidenceJSONPointersAndIndependentTimes(t *testing.T) {
	in := fixture()
	in.CollectedAt = "2026-10-29T00:00:00Z"
	in.Policies = []domain.Policy{rule("exposure", "0.0.0.0/0", "192.0.2.10", "accept", 1)}
	r, err := Run(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if r.Manifest.CollectedAt != in.CollectedAt || r.Manifest.EvaluationAt != in.EvaluationAt {
		t.Fatal("collection and evaluation time conflated")
	}
	b, _ := json.Marshal(in)
	var tree any
	json.Unmarshal(b, &tree)
	for _, e := range r.Evidence {
		if e.ObservedAt != in.CollectedAt || !strings.HasPrefix(e.Field, "/") {
			t.Fatalf("untraceable source time/path %+v", e)
		}
		value := tree
		for _, part := range strings.Split(e.Field[1:], "/") {
			switch current := value.(type) {
			case map[string]any:
				var ok bool
				value, ok = current[part]
				if !ok {
					t.Fatalf("missing field %s", e.Field)
				}
			case []any:
				i, err := strconv.Atoi(part)
				if err != nil || i < 0 || i >= len(current) {
					t.Fatalf("bad index %s", e.Field)
				}
				value = current[i]
			default:
				t.Fatalf("non-container pointer %s", e.Field)
			}
		}
		if e.PolicyRef != "" {
			p, ok := value.(map[string]any)
			if !ok || p["id"] != e.PolicyRef {
				t.Fatal("policy ref is not the source pointed to")
			}
		}
	}
}

func TestReviewReturnedQueryDoesNotAliasFrozenInput(t *testing.T) {
	in := fixture()
	r, err := Run(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	f := find(t, r, "CHK-03")
	f.QueryScope.Ports[0].From = 12345
	if in.Assets[0].Services[0].Ports[0].From != 22 {
		t.Fatal("returned finding mutates the frozen input through a shared slice")
	}
	q := query()
	path, err := Evaluate(context.Background(), in, q)
	if err != nil {
		t.Fatal(err)
	}
	path.Query.Ports[0].From = 12345
	if q.Ports[0].From != 22 {
		t.Fatal("returned path aliases caller query")
	}
}

func TestReviewMissingEvidenceIsInformationGapNotUnsupportedSemantics(t *testing.T) {
	for _, kind := range []string{"reference", "order", "enabled"} {
		t.Run(kind, func(t *testing.T) {
			in := fixture()
			p := rule("uncertain", "0.0.0.0/0", "192.0.2.10", "accept", 1)
			switch kind {
			case "reference":
				p.Source = domain.AddressSet{Ref: "not-collected"}
			case "order":
				p.OrderKnown = false
			case "enabled":
				p.Enabled = "unknown"
			}
			in.Policies = []domain.Policy{p}
			r, err := Run(context.Background(), in)
			if err != nil {
				t.Fatal(err)
			}
			if find(t, r, "CHK-07").Verdict != "insufficient_data" {
				t.Fatal("missing observation was reported as unsupported provider semantics")
			}
			if find(t, r, "CHK-04").Verdict != "insufficient_data" {
				t.Fatal("duplicate comparison also needs to distinguish missing observations")
			}
		})
	}
}

func TestReviewMultipleRequiredDeclarationsKeepUniqueFindingsAndEvidence(t *testing.T) {
	in := fixture()
	s := &in.Assets[0].Services[0]
	s.RequiredConfirmed = true
	s.Required = []domain.Access{
		{ScopeRef: in.Scope.Ref, Protocol: "tcp", Addresses: []string{"0.0.0.0/0"}, Ports: s.Ports, Applicability: "unknown"},
		{ScopeRef: in.Scope.Ref, Protocol: "tcp", Addresses: []string{"198.51.100.9"}, Ports: s.Ports, Applicability: "unknown"},
		{ScopeRef: in.Scope.Ref, Protocol: "tcp", Addresses: []string{"203.0.113.9"}, Ports: s.Ports, Applicability: "applicable"},
	}
	s.Required = append(s.Required, s.Required[2])
	r, err := Run(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, f := range r.Findings {
		if seen[f.ID] {
			t.Fatal("multiple required records produced the same finding ID")
		}
		seen[f.ID] = true
	}
	seen = map[string]bool{}
	for _, e := range r.Evidence {
		if seen[e.ID] {
			t.Fatal("multiple required records produced the same evidence ID")
		}
		seen[e.ID] = true
	}
}

func TestReviewDuplicateCanonicalizationAndDisabledConfigurations(t *testing.T) {
	in := fixture()
	a := rule("one", "198.51.100.0/24", "192.0.2.10", "accept", 1)
	b := a
	b.ID = "two"
	b.Priority = 2
	b.Source.Values = []string{"203.0.113.0/24"}
	in.Policies = []domain.Policy{a, b}
	r, err := Run(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if find(t, r, "CHK-04").Verdict != "pass" {
		t.Fatal("different actual interval values collapsed into duplicate")
	}
	b.Source = a.Source
	b.Enabled = "disabled"
	a.Enabled = "disabled"
	in.Policies = []domain.Policy{a, b}
	r, err = Run(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if find(t, r, "CHK-04").Verdict != "fail" {
		t.Fatal("disabled configuration duplicates lost by effective-path filter")
	}
}

func TestReviewUnrecognizedVersionsAndBaselineDoNotSilentlyPass(t *testing.T) {
	for _, change := range []func(*domain.Input){
		func(i *domain.Input) { i.NormalizerVersion = "999" },
		func(i *domain.Input) { i.Scope.ControlPlaneRef = "" },
		func(i *domain.Input) { i.Scope.IPFamily = "ipv6" },
		func(i *domain.Input) { i.Semantics.Version = "999" },
		func(i *domain.Input) { i.Baseline.EnabledChecks = []string{"CHK-09"} },
		func(i *domain.Input) { i.Baseline.RiskPorts = nil },
		func(i *domain.Input) {
			s := &i.Assets[0].Services[0]
			s.DenyAll = true
			s.Allowed = []domain.Access{{ScopeRef: i.Scope.Ref, Protocol: "tcp", Addresses: []string{"198.51.100.0/24"}, Ports: s.Ports, Applicability: "applicable"}}
		},
		func(i *domain.Input) {
			i.Assets[0].Allowed = []domain.Access{{ScopeRef: i.Scope.Ref, Protocol: "tcp", Applicability: "applicable"}}
		},
	} {
		in := fixture()
		change(&in)
		if Validate(in) == nil {
			t.Fatal("unknown version, missing adopted baseline, or missing explicit access dimensions accepted")
		}
	}
}

func TestReviewPartialKnownViolationSurvivesUnknownSubdomain(t *testing.T) {
	in := fixture()
	unknown := rule("unknown-part", "198.51.100.0/24", "192.0.2.10", "accept", 1)
	unknown.Enabled = "unknown"
	in.Policies = []domain.Policy{unknown, rule("known-public-tail", "0.0.0.0/0", "192.0.2.10", "accept", 2)}
	r, err := Run(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	f := find(t, r, "CHK-07")
	if f.Verdict != "fail" || len(f.Differences) == 0 || r.Coverage.Complete {
		t.Fatal("proven violation dropped or unknown subdomain promoted to complete")
	}
}
