package report

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"cfw-policy-llm-agent/server/internal/audit"
	"cfw-policy-llm-agent/server/internal/domain"
)

func privateResult() domain.Result {
	return domain.Result{
		SchemaVersion: 1,
		Manifest: domain.InputManifest{
			SnapshotVersion: "PRIVATE_SNAPSHOT", ProfileVersion: "PRIVATE_PROFILE", IntentVersion: "PRIVATE_INTENT",
			BaselineVersion: "PRIVATE_BASELINE", NormalizerVersion: "PRIVATE_NORMALIZER", KernelVersion: "PRIVATE_KERNEL",
			ProviderSemantics: domain.Semantics{ProfileID: "PRIVATE_SEMANTICS", Version: "PRIVATE_SEMANTICS_VERSION", DefaultAction: "drop", SourceRef: "PRIVATE_DEFAULT_SOURCE"},
			PolicyScope:       domain.Scope{Ref: "PRIVATE_SCOPE", Provider: "aliyun", AccountRef: "1234567890123456", ControlPlaneRef: "PRIVATE_CONTROL_PLANE", IPFamily: "ipv4", Boundary: "internet", Direction: "in", PolicySetRef: "PRIVATE_POLICY_SET"},
			EvaluationAt:      "2026-10-28T12:00:00+08:00", CollectedAt: "2026-10-28T04:00:00Z", SourceKind: "imported",
		},
		Coverage: domain.Coverage{Complete: false, EvaluatedQueries: 1, EvaluatedCells: 4, Limitations: []string{"PRIVATE_LIMITATION"}},
		Findings: []domain.Finding{{
			ID: "PRIVATE_FINDING", RuleID: "CHK-07", RuleVersion: "PRIVATE_RULE_VERSION", Origin: "rule", ScopeRef: "PRIVATE_SCOPE",
			SubjectRef: "i-private-cloud-resource", ServiceRef: "PRIVATE_SERVICE", IssueKey: "192.0.2.19|PRIVATE_ISSUE",
			Verdict: "fail", AssessmentBasis: "imported_assumption", Assumptions: []string{"PRIVATE_ASSUMPTION"}, Severity: "high", RemediationPriority: "R1",
			Summary: "SECRET_SUMMARY https://private.example.invalid?token=secret", QueryScope: domain.Query{FixedAddress: "192.0.2.19", Addresses: []string{"198.51.100.0/24"}},
			Differences:  []domain.Cell{{AddressFrom: "198.51.100.0", AddressTo: "198.51.100.255", PortFrom: 22, PortTo: 22, Action: "accept", PolicyRef: "PRIVATE_POLICY", Reason: "PRIVATE_REASON"}},
			EvidenceRefs: []string{"PRIVATE_EVIDENCE"}, Limitations: []string{"PRIVATE_LIMITATION"}, AcceptedException: true,
		}},
		Evidence: []domain.Evidence{{ID: "PRIVATE_EVIDENCE", SnapshotVersion: "PRIVATE_SNAPSHOT", ScopeRef: "PRIVATE_SCOPE", SubjectRef: "i-private-cloud-resource", PolicyRef: "PRIVATE_POLICY", Field: "PRIVATE_FIELD_PATH", Summary: "PRIVATE_EVIDENCE_SUMMARY", ObservedAt: "2026-10-28T04:00:00Z"}},
	}
}

func TestBuildPublicProjectionExcludesPrivateFields(t *testing.T) {
	result := privateResult()
	reviews := []ReviewHead{{FindingID: "PRIVATE_FINDING", Revision: 2, Plan: "deferred"}}
	for _, format := range []string{"json", "markdown"} {
		t.Run(format, func(t *testing.T) {
			body, err := Build(context.Background(), result, reviews, format)
			if err != nil {
				t.Fatal(err)
			}
			for _, secret := range []string{"PRIVATE_", "i-private-cloud-resource", "1234567890123456", "192.0.2.19", "198.51.100.", "SECRET_SUMMARY", "private.example.invalid"} {
				if bytes.Contains(body, []byte(secret)) {
					t.Fatalf("private value escaped into %s", format)
				}
			}
			for _, public := range []string{"CHK-07", "high", "R1", "imported_assumption", "deferred"} {
				if !bytes.Contains(body, []byte(public)) {
					t.Fatalf("missing validated dimension %s", public)
				}
			}
			if !bytes.Contains(body, []byte("导入声明条件下")) {
				t.Fatal("missing imported-assumption qualification")
			}
			if !bytes.Contains(body, []byte("unclassified_gap")) {
				t.Fatal("redacted gap must remain visible as a controlled code")
			}
			if bytes.Contains(body, []byte("已修复")) {
				t.Fatal("local review must not be presented as a real fix")
			}
		})
	}
}

func TestJSONProjectionPreservesReferencesAndCapturedReview(t *testing.T) {
	result := privateResult()
	first, err := Build(context.Background(), result, []ReviewHead{{FindingID: "PRIVATE_FINDING", Revision: 2, Plan: "todo"}}, "json")
	if err != nil {
		t.Fatal(err)
	}
	var doc Document
	if err = json.Unmarshal(first, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.SchemaVersion != 1 || doc.SourceKind != "imported" || len(doc.Findings) != 1 || len(doc.Evidence) != 1 {
		t.Fatalf("invalid document %+v", doc)
	}
	f := doc.Findings[0]
	if f.ReviewRevision != 2 || f.Plan != "todo" || len(f.EvidenceAliases) != 1 || f.EvidenceAliases[0] != doc.Evidence[0].Alias {
		t.Fatal("review or evidence linkage lost")
	}
	if doc.Coverage.Complete || doc.Coverage.EvaluatedCells != 4 || len(doc.Coverage.Limitations) == 0 {
		t.Fatal("coverage was upgraded or hidden")
	}
	result.Findings[0].Summary = "later private edit"
	second, err := Build(context.Background(), result, []ReviewHead{{FindingID: "PRIVATE_FINDING", Revision: 3, Plan: "deferred"}}, "json")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(first, second) || !bytes.Contains(first, []byte(`"review_revision": 2`)) {
		t.Fatal("captured review was not a fixed projection")
	}
}

func TestBuildIsDeterministicAndDoesNotMutateResult(t *testing.T) {
	r := privateResult()
	before, _ := json.Marshal(r)
	a, err := Build(context.Background(), r, nil, "json")
	if err != nil {
		t.Fatal(err)
	}
	b, err := Build(context.Background(), r, nil, "json")
	if err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(r)
	if !bytes.Equal(a, b) || !bytes.Equal(before, after) {
		t.Fatal("report is nondeterministic or mutated the original result")
	}
}

func TestBuildRejectsInvalidReferencesAndUntrustedDimensions(t *testing.T) {
	tests := []struct {
		name   string
		change func(*domain.Result)
	}{
		{"basis_upgrade", func(r *domain.Result) { r.Findings[0].AssessmentBasis = "live_observed" }},
		{"raw_rule", func(r *domain.Result) { r.Findings[0].RuleID = "SECRET_RULE" }},
		{"raw_verdict", func(r *domain.Result) { r.Findings[0].Verdict = "SECRET_VERDICT" }},
		{"raw_priority", func(r *domain.Result) { r.Findings[0].RemediationPriority = "SECRET_PRIORITY" }},
		{"missing_evidence", func(r *domain.Result) { r.Findings[0].EvidenceRefs = []string{"unknown"} }},
		{"cross_scope", func(r *domain.Result) { r.Evidence[0].ScopeRef = "other" }},
		{"cross_snapshot", func(r *domain.Result) { r.Evidence[0].SnapshotVersion = "other" }},
		{"duplicate_finding", func(r *domain.Result) { r.Findings = append(r.Findings, r.Findings[0]) }},
		{"invalid_time", func(r *domain.Result) { r.Manifest.EvaluationAt = "PRIVATE_DATE" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			r := privateResult()
			test.change(&r)
			body, err := Build(context.Background(), r, nil, "json")
			if !errors.Is(err, ErrInvalidResult) || body != nil {
				t.Fatalf("invalid projection accepted: %v", err)
			}
			if strings.Contains(err.Error(), "SECRET") {
				t.Fatal("validation error leaks input")
			}
		})
	}
	for _, heads := range [][]ReviewHead{
		{{FindingID: "missing", Revision: 1, Plan: "todo"}},
		{{FindingID: "PRIVATE_FINDING", Revision: 1, Plan: "SECRET_PLAN"}},
		{{FindingID: "PRIVATE_FINDING", Revision: -1}},
		{{FindingID: "PRIVATE_FINDING", Revision: 0, Plan: "todo"}},
		{{FindingID: "PRIVATE_FINDING", Revision: 1}, {FindingID: "PRIVATE_FINDING", Revision: 2}},
	} {
		if _, err := Build(context.Background(), privateResult(), heads, "json"); !errors.Is(err, ErrInvalidResult) {
			t.Fatal("invalid captured review accepted")
		}
	}
}

func TestBuildCancellationAndFormats(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Build(ctx, privateResult(), nil, "json"); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled report still generated")
	}
	if _, err := Build(context.Background(), privateResult(), nil, "html"); !errors.Is(err, ErrFormat) {
		t.Fatal("unsupported report format accepted")
	}
}

func TestKnownGapsAndUnsupportedProtocolRemainControlled(t *testing.T) {
	r := privateResult()
	r.Coverage.Limitations = []string{"DATA_INCOMPLETE", "QUERY_CELL_LIMIT"}
	r.Findings[0].Limitations = []string{"REFERENCE_OR_MATCH_UNKNOWN"}
	r.Findings[0].Severity = "unassessed"
	r.Findings[0].Verdict = "unsupported"
	r.Findings[0].QueryScope.Protocol = "PRIVATE_UNSUPPORTED_PROTOCOL"
	body, err := Build(context.Background(), r, nil, "json")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(body, []byte("PRIVATE_")) {
		t.Fatal("unsupported protocol leaked raw input")
	}
	for _, code := range []string{"data_incomplete", "query_cell_limit", "reference_or_match_unknown"} {
		if !bytes.Contains(body, []byte(code)) {
			t.Fatalf("known gap code lost: %s", code)
		}
	}
}

func TestNATTargetAliasesExistInBothFormats(t *testing.T) {
	r := privateResult()
	r.Manifest.PolicyScope.Boundary = "nat"
	r.Manifest.PolicyScope.Direction = "out"
	r.Manifest.PolicyScope.GatewayRef = "PRIVATE_GATEWAY"
	r.Findings[0].RuleID = "NAT-02"
	for _, format := range []string{"json", "markdown"} {
		body, err := Build(context.Background(), r, nil, format)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(body, []byte("gateway-001")) || !bytes.Contains(body, []byte("policy-set-001")) || bytes.Contains(body, []byte("PRIVATE_")) {
			t.Fatal("NAT target lost or leaked")
		}
	}
}

func TestProjectionRejectsAggregateOutputBeforeAllocatingDetails(t *testing.T) {
	r := privateResult()
	details := make([]domain.Cell, 10000)
	for n := range details {
		details[n] = domain.Cell{AddressFrom: "PRIVATE_FROM", AddressTo: "PRIVATE_TO", PortFrom: 22, PortTo: 22, Action: "accept", PolicyRef: "PRIVATE_POLICY"}
	}
	r.Findings[0].Differences = details
	for _, id := range []string{"PRIVATE_SECOND", "PRIVATE_THIRD"} {
		f := r.Findings[0]
		f.ID = id
		r.Findings = append(r.Findings, f)
	}
	if body, err := Build(context.Background(), r, nil, "json"); !errors.Is(err, ErrTooLarge) || body != nil {
		t.Fatal("large aggregate projection was not rejected")
	}
}

func publicKernelInput() domain.Input {
	m := privateResult().Manifest
	return domain.Input{SchemaVersion: 1, SnapshotVersion: m.SnapshotVersion, ProfileVersion: m.ProfileVersion, IntentVersion: m.IntentVersion, NormalizerVersion: audit.NormalizerVersion, KernelVersion: audit.KernelVersion, SourceKind: "synthetic", EvaluationAt: m.EvaluationAt, CollectedAt: "2026-10-28T03:59:00Z", Scope: m.PolicyScope,
		Semantics: domain.Semantics{ProfileID: "synthetic-v1", Version: audit.ProviderSemanticsVersion, DefaultAction: "accept", SourceRef: "synthetic-case"}, Completeness: domain.Completeness{Declared: true, EnumerationComplete: true, ObservationConsistency: "fixed_artifact"}, Protection: domain.Protection{Applicable: true, Required: true, State: "on", EvidenceRef: "synthetic-protection"}, Selection: domain.Selection{Kind: "assets", AssetIDs: []string{"asset"}}, Assets: []domain.Asset{{ID: "asset", ScopeRef: m.PolicyScope.Ref, Address: "192.0.2.1", AddressObserved: true}}, Baseline: domain.Baseline{Version: m.BaselineVersion, EnabledChecks: []string{"CHK-07"}, RiskPorts: []domain.RiskPort{{Protocol: "tcp", Port: 22}}}}
}

func TestRealKernelResultKeepsCollectedTimeAndScopeQualification(t *testing.T) {
	in := publicKernelInput()
	// The IANA registry leaves the deprecated relay aggregate's public status
	// unknown. An intended complete case must explicitly deny that subdomain;
	// the remaining default allow still proves known public management exposure.
	in.Policies = []domain.Policy{{ID: "deny-unknown-public-classification", ScopeRef: in.Scope.Ref, Source: domain.AddressSet{Values: []string{"192.88.99.0/24"}}, Destination: domain.AddressSet{Values: []string{"192.0.2.1/32"}}, Protocol: "tcp", Ports: domain.PortSet{Values: []domain.PortRange{{From: 22, To: 22}}}, Action: "drop", Priority: 1, OrderKnown: true, Enabled: "enabled"}}
	r, err := audit.Run(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Coverage.Complete || len(r.Findings) == 0 || r.Findings[0].Verdict != "fail" {
		t.Fatalf("fixture did not exercise a complete actual risk: %+v", r.Coverage)
	}
	body, err := Build(context.Background(), r, nil, "json")
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err = json.Unmarshal(body, &doc); err != nil {
		t.Fatal(err)
	}
	if doc["collected_at"] != "2026-10-28T03:59:00Z" {
		t.Fatal("actual collection time missing from public manifest")
	}
	if bytes.Contains(body, []byte("unclassified_gap")) {
		t.Fatal("fixed configuration-scope qualification was misrepresented as missing data")
	}
}

func TestDefaultAllowUnknownPublicClassificationStaysVisibleInReports(t *testing.T) {
	r, err := audit.Run(context.Background(), publicKernelInput())
	if err != nil {
		t.Fatal(err)
	}
	if r.Coverage.Complete {
		t.Fatal("default allow across unknown public classification claimed complete coverage")
	}
	failure := false
	for _, f := range r.Findings {
		if f.RuleID == "CHK-07" && f.Verdict == "fail" {
			failure = true
		}
	}
	if !failure {
		t.Fatal("known public violation was erased by the unknown subdomain")
	}
	for _, format := range []string{"json", "markdown"} {
		body, err := Build(context.Background(), r, nil, format)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(body, []byte("public_address_classification_unknown")) {
			t.Fatal("public-classification gap was hidden or flattened")
		}
		if format == "json" {
			var doc Document
			if json.Unmarshal(body, &doc) != nil || doc.Coverage.Complete {
				t.Fatal("public report upgraded incomplete coverage")
			}
		}
	}
}

func TestNewKernelGapCodesUseFixedPublicNotices(t *testing.T) {
	codes := []string{"REFERENCE_INCOMPLETE", "STATE_UNSUPPORTED", "ACTION_UNCONFIRMED", "QUERY_DIMENSION_UNKNOWN", "DUPLICATE_FIELDS_INCOMPLETE", "PUBLIC_ADDRESS_CLASSIFICATION_UNKNOWN"}
	r := privateResult()
	r.Coverage.Limitations = append(append([]string(nil), codes...), "PRIVATE_REASON_AND_URL")
	r.Findings[0].Limitations = codes
	for _, format := range []string{"json", "markdown"} {
		body, err := Build(context.Background(), r, nil, format)
		if err != nil {
			t.Fatal(err)
		}
		for _, code := range codes {
			if !bytes.Contains(body, []byte(strings.ToLower(code))) {
				t.Fatalf("new controlled code hidden: %s", code)
			}
		}
		if bytes.Contains(body, []byte("PRIVATE_")) {
			t.Fatal("free text escaped controlled gap projection")
		}
	}
}

func TestBuiltinDuplicateCandidatesProjectPolicyReferences(t *testing.T) {
	for _, id := range []string{"internet-in", "nat-out"} {
		t.Run(id, func(t *testing.T) {
			in, err := audit.SyntheticExample(id)
			if err != nil {
				t.Fatal(err)
			}
			if id == "nat-out" {
				duplicate := in.Policies[0]
				duplicate.ID = "policy-nat-duplicate"
				duplicate.Priority = 20
				in.Policies = append(in.Policies, duplicate)
			}
			result, err := audit.Run(context.Background(), in)
			if err != nil {
				t.Fatal(err)
			}
			candidateFound := false
			for _, finding := range result.Findings {
				for _, d := range finding.Differences {
					if d.Action == "candidate" {
						candidateFound = true
					}
				}
			}
			if !candidateFound {
				t.Fatal("fixture did not exercise actual exact-duplicate candidates")
			}
			body, err := Build(context.Background(), result, nil, "json")
			if err != nil {
				t.Fatal(err)
			}
			var doc struct {
				Findings []struct {
					RuleID      string                       `json:"rule_id"`
					Differences []map[string]json.RawMessage `json:"differences"`
				} `json:"findings"`
			}
			if err = json.Unmarshal(body, &doc); err != nil {
				t.Fatal(err)
			}
			aliases := []string{}
			for _, finding := range doc.Findings {
				for _, difference := range finding.Differences {
					var kind string
					json.Unmarshal(difference["kind"], &kind)
					if kind != "duplicate_policy_candidate" {
						continue
					}
					if finding.RuleID != "CHK-04" && finding.RuleID != "NAT-02" {
						t.Fatal("candidate projected for unrelated check")
					}
					for _, field := range []string{"address_from_alias", "address_to_alias", "port_from", "port_to", "action"} {
						if _, exists := difference[field]; exists {
							t.Fatalf("maintenance candidate invented flow field %s", field)
						}
					}
					var alias string
					if json.Unmarshal(difference["policy_alias"], &alias) != nil || alias == "" {
						t.Fatal("candidate policy reference missing")
					}
					aliases = append(aliases, alias)
				}
			}
			if len(aliases) < 2 {
				t.Fatal("duplicate policy pair was not preserved")
			}
			markdown, err := Build(context.Background(), result, nil, "markdown")
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Contains(markdown, []byte("duplicate_policy_candidate")) || bytes.Contains(markdown, []byte("端口 0–0")) {
				t.Fatal("Markdown must describe maintenance references without synthetic traffic")
			}
			for _, alias := range aliases {
				if !bytes.Contains(markdown, []byte(alias)) {
					t.Fatal("formats disagree on policy aliases")
				}
			}
			for _, policy := range in.Policies {
				if bytes.Contains(body, []byte(policy.ID)) || bytes.Contains(markdown, []byte(policy.ID)) {
					t.Fatal("raw policy identity escaped public projection")
				}
			}
		})
	}
}

func TestMaintenanceCandidateShapeCannotBypassActionValidation(t *testing.T) {
	for _, mutation := range []struct {
		name string
		edit func(*domain.Finding)
	}{
		{"unrelated_check", func(f *domain.Finding) { f.RuleID = "CHK-07" }},
		{"wrong_nat_subcheck", func(f *domain.Finding) { f.RuleID = "NAT-02"; f.Subcheck = "CHK-05" }},
		{"wrong_reason", func(f *domain.Finding) { f.Differences[0].Reason = "PRIVATE_MADE_UP_REASON" }},
		{"missing_policy", func(f *domain.Finding) { f.Differences[0].PolicyRef = "" }},
		{"invented_address", func(f *domain.Finding) { f.Differences[0].AddressFrom = "192.0.2.1" }},
		{"invented_port", func(f *domain.Finding) { f.Differences[0].PortTo = 22 }},
		{"invalid_action", func(f *domain.Finding) { f.Differences[0].Action = "PRIVATE_WRITE_ACTION" }},
		{"missing_policy_evidence", func(f *domain.Finding) { f.Differences[0].PolicyRef = "PRIVATE_UNEVIDENCED_POLICY" }},
		{"invented_broadness", func(f *domain.Finding) { f.Differences[0].Broad = true }},
		{"model_candidate", func(f *domain.Finding) { f.Origin = "model"; f.Verdict = "needs_confirmation" }},
		{"empty_flow_action", func(f *domain.Finding) { f.RuleID = "CHK-07"; f.Differences[0].Action = "" }},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			result := privateResult()
			finding := &result.Findings[0]
			finding.RuleID = "CHK-04"
			finding.Differences = []domain.Cell{{Action: "candidate", PolicyRef: "PRIVATE_POLICY", Reason: "EXACT_DUPLICATE_CANDIDATE"}}
			mutation.edit(finding)
			if body, err := Build(context.Background(), result, nil, "json"); !errors.Is(err, ErrInvalidResult) || body != nil {
				t.Fatal("malformed candidate bypassed action validation")
			}
		})
	}
}

func TestFlowDifferenceStillKeepsItsTypedRange(t *testing.T) {
	r := privateResult()
	body, err := Build(context.Background(), r, nil, "json")
	if err != nil {
		t.Fatal(err)
	}
	var doc Document
	if err = json.Unmarshal(body, &doc); err != nil {
		t.Fatal(err)
	}
	d := doc.Findings[0].Differences[0]
	if d.Kind != "flow" || d.PortFrom == nil || d.PortTo == nil || *d.PortFrom != 22 || *d.PortTo != 22 || d.Action != "accept" || d.AddressFromAlias == "" {
		t.Fatal("flow range lost while separating maintenance candidates")
	}
	md, err := Build(context.Background(), r, nil, "markdown")
	if err != nil || !bytes.Contains(md, []byte("端口 22–22")) {
		t.Fatal("formats disagree on flow range")
	}
}
