package agentquery

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"cfw-policy-llm-agent/server/internal/audit"
	"cfw-policy-llm-agent/server/internal/domain"
)

func TestIndependentAssetProtectionProjectionAndScopedCoverageRecommendation(t *testing.T) {
	in, err := audit.SyntheticExample("internet-in")
	if err != nil {
		t.Fatal(err)
	}
	in.Protection.State = "unknown"
	in.Assets[0].Protection = &domain.Protection{Applicable: true, Required: true, State: "off", EvidenceRef: "private-per-asset-protection-record"}
	result, err := audit.Run(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(in, &result)
	if err != nil {
		t.Fatal(err)
	}
	var initial InitialView
	if err := json.Unmarshal([]byte(s.InitialContext()), &initial); err != nil {
		t.Fatal(err)
	}
	s.BeginTurn()
	asset := invoke(t, s, "get_asset_context", "asset_ref", initial.Assets[0])
	if data(asset)["protection_state"] != "off" {
		t.Fatal("individual state lost", asset)
	}
	projection, _ := json.Marshal(asset)
	if strings.Contains(string(projection), in.Assets[0].Protection.EvidenceRef) {
		t.Fatal("private protection record leaked")
	}
	protectionRef := data(asset)["protection_evidence_ref"].(string)
	s.BeginTurn()
	binding, ok := s.LocalBindings()[protectionRef]
	if !ok || binding.Document != "input" || binding.Field != "/assets/0/protection" || binding.AssetRef != in.Assets[0].ID || !binding.Returned {
		t.Fatalf("asset protection evidence lost binding: %+v", binding)
	}
	findingRef := ""
	for _, finding := range initial.Findings {
		if finding.RuleID == "CHK-05" && finding.Verdict == "fail" {
			findingRef = finding.FindingRef
		}
	}
	if findingRef == "" {
		t.Fatal("missing asset coverage finding")
	}
	finding := invoke(t, s, "get_finding_evidence", "finding_ref", findingRef)
	s.BeginTurn()
	rec := Recommendation{Kind: "coverage_followup", TargetKind: "protection_scope", TargetRef: initial.ScopeRef, BasisKind: "known_gap", FindingRef: findingRef, EvidenceRefs: stringItems(finding["evidence_refs"]), Action: "review_rule", Conditions: []string{"依据所选对象的固定保护观察"}, Impact: "只涉及该对象当前防护层", ManualNextStep: "人工核对该对象的保护范围"}
	if !s.validateRecommendation(rec) {
		t.Fatal("known asset coverage gap rejected due to mixed scope aggregate")
	}
	rec.FindingRef = ""
	if s.validateRecommendation(rec) {
		t.Fatal("asset gap promoted to whole partition without its finding")
	}
}
