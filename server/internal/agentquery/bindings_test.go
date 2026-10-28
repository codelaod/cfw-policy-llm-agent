package agentquery

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestLocalBindingsOnlyExposeIssuedFixedPointersToServer(t *testing.T) {
	s, in, result, v := queryCase(t, "nat-out")
	before := s.LocalBindings()
	if before[v.ScopeRef].Document != "input" || before[v.ScopeRef].Field != "/scope" || before[v.Assets[0]].AssetRef != in.Assets[0].ID {
		t.Fatal(before)
	}
	asset := invoke(t, s, "get_asset_context", "asset_ref", v.Assets[0])
	service := data(asset)["services"].([]any)[0].(map[string]any)["service_ref"].(string)
	association := data(asset)["association_ref"].(string)
	if _, ok := s.LocalBindings()[service]; ok {
		t.Fatal("same-batch service binding escaped")
	}
	if _, ok := s.LocalBindings()[association]; ok {
		t.Fatal("same-batch association binding escaped")
	}
	s.BeginTurn()
	bindings := s.LocalBindings()
	if bindings[service].Field != "/assets/0/services/0" || bindings[service].ServiceRef != in.Assets[0].Services[0].ID || bindings[association].Field != "/assets/0/association" {
		t.Fatal(bindings)
	}
	if result.Findings[0].ID != "" && bindings[v.Findings[0].FindingRef].Document != "result" {
		t.Fatal("finding document lost")
	}
	bindings[service] = LocalBinding{Kind: "policy", Document: "input", Field: "/policies/0"}
	if s.LocalBindings()[service].Kind != "service" {
		t.Fatal("returned map mutated service")
	}
	raw := s.InitialContext()
	again := invoke(t, s, "get_asset_context", "asset_ref", v.Assets[0])
	b, _ := json.Marshal(again)
	raw += string(b)
	for _, value := range []string{in.Assets[0].ID, in.Assets[0].Services[0].ID, in.Assets[0].Address} {
		if strings.Contains(raw, value) {
			t.Fatal("local bindings changed model projection", value)
		}
	}
}
func TestBoundOutputRevalidatesLocalBindingAndReturnedLedger(t *testing.T) {
	s, in, result, v := queryCase(t, "internet-in")
	asset := invoke(t, s, "get_asset_context", "asset_ref", v.Assets[0])
	s.BeginTurn()
	out := Output{ScopeRef: v.ScopeRef, RuleVersion: v.RuleVersion, SourceKind: v.SourceKind, AssessmentBasis: v.AssessmentBasis, Explanation: "这是模型补充解释。", Recommendations: []Recommendation{{Kind: "confirm_information", TargetKind: "asset", TargetRef: v.Assets[0], BasisKind: "unknown_followup", EvidenceRefs: stringItems(asset["evidence_refs"]), Action: "confirm_intent", Conditions: []string{"需要操作者核对"}, Impact: "业务资料会影响判据", ManualNextStep: "在本地业务草稿中核对"}}}
	if e := ValidateBoundOutput(in, &result, out, s.LocalBindings()); e != nil {
		t.Fatal("valid fixed binding rejected", e)
	}
	for _, mode := range []string{"bad-field", "wrong-owner", "wrong-document", "not-returned", "missing-target", "wrong-kind"} {
		t.Run(mode, func(t *testing.T) {
			bindings := s.LocalBindings()
			key := out.Recommendations[0].EvidenceRefs[0]
			binding := bindings[key]
			switch mode {
			case "bad-field":
				binding.Field = "/missing/field"
			case "wrong-owner":
				binding.AssetRef = "asset_foreign"
			case "wrong-document":
				binding.Document = "model"
			case "not-returned":
				binding.Returned = false
			case "missing-target":
				delete(bindings, v.Assets[0])
			case "wrong-kind":
				binding.Kind = "policy"
			}
			bindings[key] = binding
			if e := ValidateBoundOutput(in, &result, out, bindings); !errors.Is(e, ErrFinal) {
				t.Fatal("forged local binding accepted", e)
			}
		})
	}
}
func TestPendingComponentCatalogCannotAuthorizeFinalOutput(t *testing.T) {
	s, _, _, v := queryCase(t, "internet-in")
	asset := invoke(t, s, "get_asset_context", "asset_ref", v.Assets[0])
	service := data(asset)["services"].([]any)[0].(map[string]any)["service_ref"].(string)
	s.BeginTurn()
	invoke(t, s, "get_rule_definition", "rule_id", "CHK-07")
	out := Output{ScopeRef: v.ScopeRef, RuleVersion: v.RuleVersion, SourceKind: v.SourceKind, AssessmentBasis: v.AssessmentBasis, Explanation: "模型给出适用性评估。", Recommendations: []Recommendation{{Kind: "component_consideration", TargetKind: "asset_service", TargetRef: service, BasisKind: "applicability_assessment", EvidenceRefs: stringItems(asset["evidence_refs"]), ComponentTopic: "waf", Action: "collect_missing_evidence", Conditions: []string{"需核对实际应用协议和入口"}, Impact: "现有保护覆盖仍需人工核实", ManualNextStep: "核对应用层防护适用性"}}}
	if _, e := s.ValidateFinal(MarshalOutput(out)); !errors.Is(e, ErrFinal) {
		t.Fatal("pending catalog authorized final", e)
	}
	s.BeginTurn()
	if _, e := s.ValidateFinal(MarshalOutput(out)); e != nil {
		t.Fatal(e)
	}
}
