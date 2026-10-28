package agentquery

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"cfw-policy-llm-agent/server/internal/audit"
)

func TestReviewE3PolicyChangeCannotBorrowAnotherServicesBusinessUpperBound(t *testing.T) {
	s, in, result, v := queryCase(t, "internet-in")
	asset := invoke(t, s, "get_asset_context", "asset_ref", v.Assets[0])
	web := data(asset)["services"].([]any)[0].(map[string]any)
	s.BeginTurn()
	webIntent := invoke(t, s, "get_business_intent", "business_ref", web["business_ref"].(string))
	fi, pi := -1, -1
	for i, f := range result.Findings {
		if f.ServiceRef != in.Assets[0].Services[1].ID || f.Verdict != "fail" {
			continue
		}
		for _, cell := range f.Differences {
			if cell.PolicyRef == "policy-management-wide" {
				fi = i
				pi = s.policyIndex(cell.PolicyRef)
				break
			}
		}
		if fi >= 0 {
			break
		}
	}
	if fi < 0 {
		t.Fatal("fixture has no management service violation")
	}
	finding := invoke(t, s, "get_finding_evidence", "finding_ref", v.Findings[fi].FindingRef)
	policy := s.policyRef(pi)
	invoke(t, s, "get_policy", "policy_ref", policy)
	s.BeginTurn()
	out := Output{ScopeRef: v.ScopeRef, RuleVersion: v.RuleVersion, SourceKind: v.SourceKind, AssessmentBasis: v.AssessmentBasis, Explanation: "以下为有条件建议。", Recommendations: []Recommendation{{
		Kind: "policy_change", TargetKind: "policy", TargetRef: policy, BasisKind: "known_gap", EvidenceRefs: stringItems(finding["evidence_refs"]),
		BusinessRef: web["business_ref"].(string), FindingRef: v.Findings[fi].FindingRef, Action: "conditional_restrict", AccessRef: data(webIntent)["allowed"].(map[string]any)["access_ref"].(string), Field: "source",
		Conditions: []string{"人工核对该服务的允许来源后再实施"}, Impact: "需保留必要管理访问", ManualNextStep: "在云控制台人工核对并修改",
	}}}
	if _, err := s.ValidateFinal(MarshalOutput(out)); !errors.Is(err, ErrFinal) {
		t.Errorf("management finding accepted the different Web service's public upper bound: %v", err)
	}
	if err := ValidateBoundOutput(in, &result, out, s.LocalBindings()); !errors.Is(err, ErrFinal) {
		t.Errorf("durable validation also accepted the cross-service business binding: %v", err)
	}
}

func TestReviewE3UnknownProtectionStateDoesNotExposeRawExternalText(t *testing.T) {
	in, err := audit.SyntheticExample("internet-in")
	if err != nil {
		t.Fatal(err)
	}
	in.SourceKind = "imported"
	private := "PRIVATE_PROTECTION_STATE https://private.example.invalid/secret"
	in.Protection.State = private
	s, err := New(in, nil)
	if err != nil {
		t.Fatal(err)
	}
	var initial InitialView
	if err = json.Unmarshal([]byte(s.InitialContext()), &initial); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]string{"scope_ref": initial.ScopeRef})
	response, err := s.Invoke(context.Background(), "get_partition_context", string(args))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(response, private) || strings.Contains(response, "private.example.invalid") {
		t.Fatal("raw unsupported protection state escaped the model-facing projection")
	}
}

func TestReviewE3FinalManualStepCannotContainRawShellOrCloudWriteMethod(t *testing.T) {
	s, _, _, v := queryCase(t, "internet-in")
	asset := invoke(t, s, "get_asset_context", "asset_ref", v.Assets[0])
	s.BeginTurn()
	for _, step := range []string{"执行 rm -rf /tmp/unrelated-files", "调用 AddControlPolicy"} {
		out := Output{ScopeRef: v.ScopeRef, RuleVersion: v.RuleVersion, SourceKind: v.SourceKind, AssessmentBasis: v.AssessmentBasis, Explanation: "以下为待确认建议。", Recommendations: []Recommendation{{Kind: "confirm_information", TargetKind: "asset", TargetRef: v.Assets[0], BasisKind: "unknown_followup", EvidenceRefs: stringItems(asset["evidence_refs"]), Action: "confirm_intent", Conditions: []string{"需要操作者确认业务资料"}, Impact: "业务资料影响审计判据", ManualNextStep: step}}}
		if _, err := s.ValidateFinal(MarshalOutput(out)); !errors.Is(err, ErrFinal) {
			t.Errorf("raw executable manual step accepted: %q, %v", step, err)
		}
	}
}
