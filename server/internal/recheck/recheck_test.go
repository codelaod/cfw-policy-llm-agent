package recheck

import (
	"context"
	"encoding/json"
	"testing"

	"cfw-policy-llm-agent/server/internal/audit"
	"cfw-policy-llm-agent/server/internal/domain"
)

func fixedPair() (domain.Input, domain.Result) {
	i := domain.Input{SchemaVersion: 1, SnapshotVersion: "snapshot-old", ProfileVersion: "profiles-v1", IntentVersion: "intent-v1", NormalizerVersion: "normalize-v1", KernelVersion: "kernel-v1", SourceKind: "synthetic", EvaluationAt: "2026-10-28T04:00:00Z", CollectedAt: "2026-10-28T03:58:00Z",
		Scope:        domain.Scope{Ref: "scope", Provider: "aliyun", AccountRef: "account", ControlPlaneRef: "control-plane", IPFamily: "ipv4", Boundary: "internet", Direction: "in", PolicySetRef: "policies"},
		Semantics:    domain.Semantics{ProfileID: "internet-v1", Version: "v1", DefaultAction: "drop", SourceRef: "synthetic-semantics"},
		Completeness: domain.Completeness{Declared: true, EnumerationComplete: true, ObservationConsistency: "fixed_artifact"},
		Selection:    domain.Selection{Kind: "assets", AssetIDs: []string{"asset"}}, Baseline: domain.Baseline{Version: "baseline-v1", EnabledChecks: []string{"CHK-07"}},
		Assets: []domain.Asset{{ID: "asset", ScopeRef: "scope", Address: "192.0.2.1", AddressObserved: true, Services: []domain.Service{{ID: "service", Protocol: "tcp", Ports: []domain.PortRange{{From: 22, To: 22}}, Role: "management", RoleStatus: "confirmed"}}}},
	}
	r := domain.Result{SchemaVersion: 1, Manifest: manifest(i), Coverage: domain.Coverage{Complete: true}, Findings: []domain.Finding{{ID: "finding-old", RuleID: "CHK-07", RuleVersion: "v1", Origin: "rule", ScopeRef: "scope", SubjectRef: "asset", ServiceRef: "service", IssueKey: "family-asset", Verdict: "fail"}}}
	return i, r
}

func manifest(i domain.Input) domain.InputManifest {
	return domain.InputManifest{SnapshotVersion: i.SnapshotVersion, ProfileVersion: i.ProfileVersion, IntentVersion: i.IntentVersion, BaselineVersion: i.Baseline.Version, NormalizerVersion: i.NormalizerVersion, KernelVersion: i.KernelVersion, ProviderSemantics: i.Semantics, PolicyScope: i.Scope, EvaluationAt: i.EvaluationAt, CollectedAt: i.CollectedAt, SourceKind: i.SourceKind}
}
func copyInput(i domain.Input) domain.Input {
	b, _ := json.Marshal(i)
	var out domain.Input
	json.Unmarshal(b, &out)
	return out
}
func copyResult(r domain.Result) domain.Result {
	b, _ := json.Marshal(r)
	var out domain.Result
	json.Unmarshal(b, &out)
	return out
}

func TestCompareStatesUseIssueFamilyWithoutClaimingRepair(t *testing.T) {
	i, old := fixedPair()
	next := copyInput(i)
	next.SnapshotVersion = "snapshot-new"
	newResult := copyResult(old)
	newResult.Manifest = manifest(next)
	newResult.Findings[0].ID = "finding-new"
	newResult.Findings[0].Verdict = "pass"
	comparison, err := Compare(i, old, next, newResult)
	if err != nil {
		t.Fatal(err)
	}
	if !comparison.Comparable || len(comparison.Issues) != 1 || comparison.Issues[0].State != "not_detected" || len(comparison.Reasons) != 0 {
		t.Fatalf("bad comparison %+v", comparison)
	}
	if comparison.Issues[0].OldFindingIDs[0] != "finding-old" || comparison.Issues[0].NewFindingIDs[0] != "finding-new" {
		t.Fatal("original evidence linkage lost")
	}
	newResult.Findings[0].Verdict = "fail"
	comparison, err = Compare(i, old, next, newResult)
	if err != nil || comparison.Issues[0].State != "persistent" {
		t.Fatal("persistent issue lost")
	}
	old.Findings[0].Verdict = "pass"
	comparison, err = Compare(i, old, next, newResult)
	if err != nil || comparison.Issues[0].State != "new" {
		t.Fatal("new issue lost")
	}
}

func TestCompareRejectsChangedMeaningOrIncompleteCoverage(t *testing.T) {
	cases := []struct {
		name string
		edit func(*domain.Input, *domain.Result)
	}{
		{"boundary", func(i *domain.Input, r *domain.Result) {
			i.Scope.Boundary = "nat"
			i.Scope.Direction = "out"
			i.Scope.GatewayRef = "gateway"
		}},
		{"direction", func(i *domain.Input, r *domain.Result) { i.Scope.Direction = "out" }},
		{"control_plane", func(i *domain.Input, r *domain.Result) { i.Scope.ControlPlaneRef = "different-control-plane" }},
		{"ip_family", func(i *domain.Input, r *domain.Result) { i.Scope.IPFamily = "ipv6" }},
		{"gateway", func(i *domain.Input, r *domain.Result) { i.Scope.GatewayRef = "another-gateway" }},
		{"intent", func(i *domain.Input, r *domain.Result) { i.IntentVersion = "intent-v2" }},
		{"profile", func(i *domain.Input, r *domain.Result) { i.ProfileVersion = "profile-v2" }},
		{"baseline", func(i *domain.Input, r *domain.Result) { i.Baseline.Version = "baseline-v2" }},
		{"kernel", func(i *domain.Input, r *domain.Result) { i.KernelVersion = "kernel-v2" }},
		{"semantics", func(i *domain.Input, r *domain.Result) { i.Semantics.Version = "v2" }},
		{"evaluation_time", func(i *domain.Input, r *domain.Result) { i.EvaluationAt = "2026-10-29T04:00:00Z" }},
		{"missing_asset", func(i *domain.Input, r *domain.Result) { i.Assets = nil }},
		{"observation_point", func(i *domain.Input, r *domain.Result) { i.Assets[0].Association.ObservationPoint = "after-nat" }},
		{"missing_page", func(i *domain.Input, r *domain.Result) { i.Completeness.EnumerationComplete = false }},
		{"known_gap", func(i *domain.Input, r *domain.Result) { i.Completeness.KnownGaps = []string{"missing_page"} }},
		{"unverified", func(i *domain.Input, r *domain.Result) { i.Completeness.ObservationConsistency = "unverified" }},
		{"incomplete_result", func(i *domain.Input, r *domain.Result) { r.Coverage.Complete = false }},
		{"missing_check", func(i *domain.Input, r *domain.Result) { r.Findings = nil }},
		{"unexamined_check", func(i *domain.Input, r *domain.Result) { r.Findings[0].Verdict = "needs_confirmation" }},
		{"unmatched_rule_version", func(i *domain.Input, r *domain.Result) { r.Findings[0].RuleVersion = "new-rule" }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			i, old := fixedPair()
			next := copyInput(i)
			r := copyResult(old)
			test.edit(&next, &r)
			r.Manifest = manifest(next)
			c, err := Compare(i, old, next, r)
			if err != nil {
				t.Fatal(err)
			}
			if c.Comparable || len(c.Reasons) == 0 || len(c.Issues) != 0 {
				t.Fatalf("incomparable data treated as repair: %+v", c)
			}
		})
	}
}

func TestCompareRejectsMismatchedResultAndKeepsInputsUnchanged(t *testing.T) {
	i, r := fixedPair()
	next := copyInput(i)
	other := copyResult(r)
	other.Manifest.IntentVersion = "unbound-version"
	if _, err := Compare(i, r, next, other); err == nil {
		t.Fatal("result from another input accepted")
	}
	beforeI, _ := json.Marshal(i)
	beforeR, _ := json.Marshal(r)
	if _, err := Compare(i, r, next, r); err != nil {
		t.Fatal(err)
	}
	afterI, _ := json.Marshal(i)
	afterR, _ := json.Marshal(r)
	if string(beforeI) != string(afterI) || string(beforeR) != string(afterR) {
		t.Fatal("comparison mutated original immutable data")
	}
}

func TestCompareCoalescesLinkedChecksAndPreservesAllRecords(t *testing.T) {
	i, r := fixedPair()
	other := r.Findings[0]
	other.ID = "linked"
	other.RuleID = "CHK-01"
	r.Findings = append(r.Findings, other)
	next := copyInput(i)
	next.SnapshotVersion = "snapshot-new"
	nr := copyResult(r)
	nr.Manifest = manifest(next)
	for n := range nr.Findings {
		nr.Findings[n].ID += "-new"
	}
	c, err := Compare(i, r, next, nr)
	if err != nil {
		t.Fatal(err)
	}
	if !c.Comparable || len(c.Issues) != 1 || len(c.Issues[0].OldFindingIDs) != 2 || len(c.Issues[0].NewFindingIDs) != 2 {
		t.Fatalf("linked checks counted separately or lost: %+v", c)
	}
}

func TestComparableIdentityUsesCanonicalScopeAndEquivalentInstant(t *testing.T) {
	i, r := fixedPair()
	next := copyInput(i)
	next.Scope.Ref = "different-local-scope-reference"
	next.Assets[0].ScopeRef = next.Scope.Ref
	next.EvaluationAt = "2026-10-28T12:00:00+08:00"
	nr := copyResult(r)
	nr.Manifest = manifest(next)
	nr.Findings[0].ScopeRef = next.Scope.Ref
	c, err := Compare(i, r, next, nr)
	if err != nil || !c.Comparable {
		t.Fatalf("same partition/instant was treated as a different meaning: %+v %v", c, err)
	}
}

func actualKernelInput() domain.Input {
	i, _ := fixedPair()
	i.KernelVersion = audit.KernelVersion
	i.NormalizerVersion = audit.NormalizerVersion
	i.CollectedAt = "2026-10-28T03:58:00Z"
	i.Semantics = domain.Semantics{ProfileID: "synthetic-v1", Version: audit.ProviderSemanticsVersion, DefaultAction: "accept", SourceRef: "synthetic-case"}
	i.Protection = domain.Protection{Applicable: true, Required: true, State: "on", EvidenceRef: "synthetic-protection"}
	i.Baseline.RiskPorts = []domain.RiskPort{{Protocol: "tcp", Port: 22}}
	return i
}

func TestActualKernelManifestsAreBoundWithCollectionTime(t *testing.T) {
	i := actualKernelInput()
	i.Policies = []domain.Policy{{ID: "deny-unknown-public-classification", ScopeRef: i.Scope.Ref, Source: domain.AddressSet{Values: []string{"192.88.99.0/24"}}, Destination: domain.AddressSet{Values: []string{"192.0.2.1/32"}}, Protocol: "tcp", Ports: domain.PortSet{Values: []domain.PortRange{{From: 22, To: 22}}}, Action: "drop", Priority: 1, OrderKnown: true, Enabled: "enabled"}}
	old, err := audit.Run(context.Background(), i)
	if err != nil {
		t.Fatal(err)
	}
	next := copyInput(i)
	next.SnapshotVersion = "snapshot-next"
	next.CollectedAt = "2026-10-28T03:59:00Z"
	next.Policies = append(next.Policies, domain.Policy{ID: "restrict", ScopeRef: i.Scope.Ref, Source: domain.AddressSet{Values: []string{"0.0.0.0/0"}}, Destination: domain.AddressSet{Values: []string{"192.0.2.1/32"}}, Protocol: "tcp", Ports: domain.PortSet{Values: []domain.PortRange{{From: 22, To: 22}}}, Action: "drop", Priority: 10, OrderKnown: true, Enabled: "enabled"})
	nr, err := audit.Run(context.Background(), next)
	if err != nil {
		t.Fatal(err)
	}
	c, err := Compare(i, old, next, nr)
	if err != nil {
		t.Fatal(err)
	}
	if !c.Comparable || len(c.Issues) != 1 || c.Issues[0].State != "not_detected" {
		t.Fatalf("real fixed input manifests did not compare: %+v", c)
	}
}

func TestUnknownPublicClassificationCannotBeCountedAsComparableRecheck(t *testing.T) {
	i := actualKernelInput()
	old, err := audit.Run(context.Background(), i)
	if err != nil {
		t.Fatal(err)
	}
	if old.Coverage.Complete {
		t.Fatal("fixture failed to retain unknown public classification")
	}
	next := copyInput(i)
	next.SnapshotVersion = "next-with-known-restriction"
	next.CollectedAt = "2026-10-28T03:59:00Z"
	next.Policies = []domain.Policy{{ID: "restrict-all", ScopeRef: i.Scope.Ref, Source: domain.AddressSet{Values: []string{"0.0.0.0/0"}}, Destination: domain.AddressSet{Values: []string{"192.0.2.1/32"}}, Protocol: "tcp", Ports: domain.PortSet{Values: []domain.PortRange{{From: 22, To: 22}}}, Action: "drop", Priority: 1, OrderKnown: true, Enabled: "enabled"}}
	nr, err := audit.Run(context.Background(), next)
	if err != nil {
		t.Fatal(err)
	}
	c, err := Compare(i, old, next, nr)
	if err != nil {
		t.Fatal(err)
	}
	if c.Comparable || len(c.Issues) != 0 {
		t.Fatal("incomplete original public classification was counted as disappearance")
	}
	found := false
	for _, reason := range c.Reasons {
		if reason == "incomplete_coverage" {
			found = true
		}
	}
	if !found {
		t.Fatalf("classification coverage reason lost: %+v", c)
	}
}
