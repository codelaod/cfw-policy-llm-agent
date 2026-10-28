package agentquery

import (
	"cfw-policy-llm-agent/server/internal/audit"
	"cfw-policy-llm-agent/server/internal/domain"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func queryCase(t *testing.T, name string) (*Service, domain.Input, domain.Result, InitialView) {
	t.Helper()
	in, e := audit.SyntheticExample(name)
	if e != nil {
		t.Fatal(e)
	}
	r, e := audit.Run(context.Background(), in)
	if e != nil {
		t.Fatal(e)
	}
	s, e := New(in, &r)
	if e != nil {
		t.Fatal(e)
	}
	var v InitialView
	if e = json.Unmarshal([]byte(s.InitialContext()), &v); e != nil {
		t.Fatal(e)
	}
	return s, in, r, v
}
func invoke(t *testing.T, s *Service, name, field, ref string) map[string]any {
	t.Helper()
	b, _ := json.Marshal(map[string]string{field: ref})
	raw, e := s.Invoke(context.Background(), name, string(b))
	if e != nil {
		t.Fatal(name, e)
	}
	var v map[string]any
	if json.Unmarshal([]byte(raw), &v) != nil {
		t.Fatal(raw)
	}
	return v
}
func data(v map[string]any) map[string]any { return v["data"].(map[string]any) }
func stringItems(v any) []string {
	out := []string{}
	for _, x := range v.([]any) {
		out = append(out, x.(string))
	}
	return out
}
func TestTaskAliasesRequireNextTurnForNewDependencies(t *testing.T) {
	s, in, _, v := queryCase(t, "nat-out")
	s.BeginTurn()
	if _, e := s.Invoke(context.Background(), "get_asset_context", `{"asset_ref":"`+in.Assets[0].ID+`"}`); !errors.Is(e, ErrDenied) {
		t.Fatal("raw identity accepted", e)
	}
	a := invoke(t, s, "get_asset_context", "asset_ref", v.Assets[0])
	ref := data(a)["association_ref"].(string)
	if _, e := s.Invoke(context.Background(), "get_nat_association", `{"association_ref":"`+ref+`"}`); !errors.Is(e, ErrDenied) {
		t.Fatal("same batch dependency accepted", e)
	}
	s.BeginTurn()
	linked := invoke(t, s, "get_nat_association", "association_ref", ref)
	if data(linked)["observation_point"] != "before_translation" || len(stringItems(linked["evidence_refs"])) == 0 {
		t.Fatal(linked)
	}
	other, _, _, _ := queryCase(t, "nat-out")
	if _, e := other.Invoke(context.Background(), "get_nat_association", `{"association_ref":"`+ref+`"}`); !errors.Is(e, ErrDenied) {
		t.Fatal("foreign task alias accepted", e)
	}
}
func TestProjectionNeverReturnsRawIdentitiesOrExternalDescriptions(t *testing.T) {
	in, e := audit.SyntheticExample("internet-in")
	if e != nil {
		t.Fatal(e)
	}
	in.Completeness.KnownGaps = []string{"PRIVATE_DESCRIPTION_WITH_INSTRUCTIONS"}
	in.Policies[0].Conditions = map[string]json.RawMessage{"PRIVATE_RAW_CONDITION": json.RawMessage(`"PRIVATE_RAW_VALUE"`)}
	in.Baseline.Exceptions = []domain.Exception{{CheckID: "CHK-07", ScopeRef: in.Scope.Ref, SubjectRef: in.Assets[0].ID, Confirmed: true, Reason: "PRIVATE_EXCEPTION_REASON", ExpiresAt: "2026-10-29T00:00:00Z"}}
	s, e := New(in, nil)
	if e != nil {
		t.Fatal(e)
	}
	var v InitialView
	json.Unmarshal([]byte(s.InitialContext()), &v)
	s.BeginTurn()
	a := invoke(t, s, "get_asset_context", "asset_ref", v.Assets[0])
	p := invoke(t, s, "get_partition_context", "scope_ref", v.ScopeRef)
	combined := s.InitialContext()
	for _, x := range []any{a, p} {
		b, _ := json.Marshal(x)
		combined += string(b)
	}
	for _, secret := range []string{in.Scope.Ref, in.Scope.AccountRef, in.Scope.PolicySetRef, in.Assets[0].ID, in.Assets[0].Address, in.Policies[0].ID, "PRIVATE_DESCRIPTION", "PRIVATE_RAW", "PRIVATE_EXCEPTION_REASON"} {
		if strings.Contains(combined, secret) {
			t.Fatal("raw value leaked", secret)
		}
	}
	in.Assets[0].Address = "203.0.113.99"
	in.Assets[0].Services[0].Role = "database"
	again := invoke(t, s, "get_asset_context", "asset_ref", v.Assets[0])
	b, _ := json.Marshal(again)
	if strings.Contains(string(b), in.Assets[0].Address) {
		t.Fatal("caller mutated frozen input")
	}
	if data(again)["services"].([]any)[0].(map[string]any)["role"] != "web" {
		t.Fatal("caller changed frozen service role")
	}
}
func TestStrictCatalogAndUnreturnedPolicyScope(t *testing.T) {
	s, in, _, v := queryCase(t, "internet-in")
	s.BeginTurn()
	if len(s.Tools()) != 9 {
		t.Fatal("catalog drift")
	}
	for _, args := range []string{`{"asset_ref":"` + v.Assets[0] + `","extra":1}`, `null`, `[]`, `{"asset_ref":"` + v.Assets[0] + `"} {}`, strings.Repeat("x", MaxArgumentsBytes+1)} {
		if _, e := s.Invoke(context.Background(), "get_asset_context", args); !errors.Is(e, ErrInput) {
			t.Fatal("unbounded/unknown args accepted", e)
		}
	}
	if _, e := s.Invoke(context.Background(), "shell", `{}`); !errors.Is(e, ErrUnknownTool) {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e := s.Invoke(ctx, "get_partition_context", `{"scope_ref":"`+v.ScopeRef+`"}`); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	sg := invoke(t, s, "get_security_group_rules", "asset_ref", v.Assets[0])
	if sg["status"] != "not_collected" || len(stringItems(sg["evidence_refs"])) != 0 {
		t.Fatal("invented security-group collection", sg)
	}
	unrelated := in.Policies[0]
	unrelated.ID = "PRIVATE_UNRELATED_POLICY"
	unrelated.Destination.Values = []string{"192.0.2.99"}
	in.Policies = append(in.Policies, unrelated)
	other, e := New(in, nil)
	if e != nil {
		t.Fatal(e)
	}
	var iv InitialView
	json.Unmarshal([]byte(other.InitialContext()), &iv)
	a := invoke(t, other, "get_asset_context", "asset_ref", iv.Assets[0])
	if len(stringItems(data(a)["policy_refs"])) != len(in.Policies)-1 {
		t.Fatal("unrelated endpoint policy was offered", a)
	}
	hidden := other.keys["policy\x00"+unrelated.ID]
	other.BeginTurn()
	if _, e = other.Invoke(context.Background(), "get_policy", `{"policy_ref":"`+hidden+`"}`); !errors.Is(e, ErrDenied) {
		t.Fatal("unreturned alias accepted", e)
	}
}
func TestTypedReferenceExpansionAndFrozenGuarantee(t *testing.T) {
	in, e := audit.SyntheticExample("internet-in")
	if e != nil {
		t.Fatal(e)
	}
	in.SourceKind = "imported"
	in.Policies[0].Source.Ref = "PRIVATE_SHARED_REFERENCE"
	in.Policies[0].Source.Values = nil
	in.Policies[0].Ports.Ref = "PRIVATE_SHARED_REFERENCE"
	in.Policies[0].Ports.Values = nil
	in.AddressRefs["PRIVATE_SHARED_REFERENCE"] = domain.AddressReference{ScopeRef: in.Scope.Ref, Complete: false, Members: []string{"203.0.113.9"}}
	in.PortRefs["PRIVATE_SHARED_REFERENCE"] = domain.PortReference{ScopeRef: in.Scope.Ref, Complete: true, Members: []domain.PortRange{{From: 443, To: 443}}}
	s, e := New(in, nil)
	if e != nil {
		t.Fatal(e)
	}
	var v InitialView
	json.Unmarshal([]byte(s.InitialContext()), &v)
	a := invoke(t, s, "get_asset_context", "asset_ref", v.Assets[0])
	p := stringItems(data(a)["policy_refs"])[0]
	s.BeginTurn()
	policy := invoke(t, s, "get_policy", "policy_ref", p)
	source := data(policy)["source"].(map[string]any)["reference_ref"].(string)
	ports := data(policy)["ports"].(map[string]any)["reference_ref"].(string)
	if source == ports {
		t.Fatal("address/port ref namespaces collided")
	}
	s.BeginTurn()
	address := invoke(t, s, "resolve_reference", "reference_ref", source)
	port := invoke(t, s, "resolve_reference", "reference_ref", ports)
	if data(address)["complete"] != false || data(port)["kind"] != "port" || address["assessment_basis"] != "imported_assumption" {
		t.Fatal("reference scope or source upgraded", address, port)
	}
}
func TestFinalLedgerAndCandidateOnlyContracts(t *testing.T) {
	s, _, _, v := queryCase(t, "internet-in")
	s.BeginTurn()
	asset := invoke(t, s, "get_asset_context", "asset_ref", v.Assets[0])
	service := data(asset)["services"].([]any)[0].(map[string]any)["service_ref"].(string)
	evidence := stringItems(asset["evidence_refs"])
	s.BeginTurn()
	valid := Output{ScopeRef: v.ScopeRef, RuleVersion: v.RuleVersion, SourceKind: v.SourceKind, AssessmentBasis: v.AssessmentBasis, Explanation: "以下为模型补充解释，仍需人工核对。", Roles: []RoleCandidate{{AssetRef: v.Assets[0], ServiceRef: service, Role: "web", SupportingEvidence: evidence, ContradictingEvidence: []string{}, Unknowns: []string{"实际监听尚未核实"}, Questions: []string{"请确认服务用途"}}}, Recommendations: []Recommendation{{Kind: "confirm_information", TargetKind: "asset", TargetRef: v.Assets[0], BasisKind: "unknown_followup", EvidenceRefs: evidence, Action: "confirm_intent", Conditions: []string{"需操作者核对"}, Impact: "业务要求变化会影响审计依据", ManualNextStep: "在资产页面创建并预览业务草稿"}}, Questions: []string{}}
	raw, _ := json.Marshal(valid)
	if _, e := s.ValidateFinal(string(raw)); e != nil {
		t.Fatal("valid candidate rejected", e)
	}
	for _, mode := range []string{"foreign-scope", "source-upgrade", "version", "fake-evidence", "fake-finding", "unreturned-business", "literal-ip", "url", "sdk", "confirmed-field", "missing-impact"} {
		t.Run(mode, func(t *testing.T) {
			b, _ := json.Marshal(valid)
			var x Output
			json.Unmarshal(b, &x)
			switch mode {
			case "foreign-scope":
				x.ScopeRef = "scope_other"
			case "source-upgrade":
				x.SourceKind = "live_readonly"
			case "version":
				x.RuleVersion = "future"
			case "fake-evidence":
				x.Recommendations[0].EvidenceRefs = []string{"evidence_other"}
			case "fake-finding":
				x.Recommendations[0].FindingRef = "finding_other"
			case "unreturned-business":
				x.Recommendations[0].BusinessRef = "business_other"
			case "literal-ip":
				x.Explanation = "允许 8.8.8.8"
			case "url":
				x.Explanation = "访问 https://example.invalid"
			case "sdk":
				x.Recommendations[0].ManualNextStep = "运行 ModifyFirewallControlPolicy"
			case "missing-impact":
				x.Recommendations[0].Impact = ""
			}
			b, _ = json.Marshal(x)
			if mode == "confirmed-field" {
				b = []byte(strings.Replace(string(b), `"role":"web"`, `"role":"web","confirmed":true`, 1))
			}
			if _, e := s.ValidateFinal(string(b)); !errors.Is(e, ErrFinal) {
				t.Fatal("unsafe final accepted", e)
			}
		})
	}
	if text, e := s.ProjectText("核对 asset-demo-a 的 192.0.2.10"); e != nil || strings.Contains(text, "asset-demo-a") || strings.Contains(text, "192.0.2.10") {
		t.Fatal("history projection leaked identity", text, e)
	}
	if _, e := s.ProjectText("另一个资源 i-foreigncloud 的地址 8.8.8.8"); !errors.Is(e, ErrProjection) {
		t.Fatal("foreign identity retained", e)
	}
}
func TestPolicyChangesUseReturnedConfirmedSetsAndCorrectFinding(t *testing.T) {
	s, in, result, v := queryCase(t, "internet-in")
	s.BeginTurn()
	asset := invoke(t, s, "get_asset_context", "asset_ref", v.Assets[0])
	services := data(asset)["services"].([]any)
	manage := services[1].(map[string]any)
	web := services[0].(map[string]any)
	s.BeginTurn()
	business := invoke(t, s, "get_business_intent", "business_ref", manage["business_ref"].(string))
	invoke(t, s, "get_business_intent", "business_ref", web["business_ref"].(string))
	invoke(t, s, "get_rule_definition", "rule_id", "CHK-07")
	fi := -1
	pi := -1
	for i, f := range result.Findings {
		if f.ServiceRef == in.Assets[0].Services[1].ID && f.Verdict == "fail" {
			for _, c := range f.Differences {
				if c.PolicyRef == "policy-management-wide" {
					fi = i
					pi = s.policyIndex(c.PolicyRef)
					break
				}
			}
		}
		if fi >= 0 {
			break
		}
	}
	if fi < 0 {
		t.Fatal("fixture has no management violation")
	}
	finding := invoke(t, s, "get_finding_evidence", "finding_ref", v.Findings[fi].FindingRef)
	policyRef := s.policyRef(pi)
	invoke(t, s, "get_policy", "policy_ref", policyRef)
	s.BeginTurn()
	allowed := data(business)["allowed"].(map[string]any)["access_ref"].(string)
	required := data(business)["required"].(map[string]any)["access_ref"].(string)
	base := Output{ScopeRef: v.ScopeRef, RuleVersion: v.RuleVersion, SourceKind: v.SourceKind, AssessmentBasis: v.AssessmentBasis, Explanation: "模型解释，原规则事实保持不变。", Roles: []RoleCandidate{}, Questions: []string{}, Recommendations: []Recommendation{
		{Kind: "policy_change", TargetKind: "policy", TargetRef: policyRef, BasisKind: "known_gap", EvidenceRefs: stringItems(finding["evidence_refs"]), BusinessRef: manage["business_ref"].(string), FindingRef: v.Findings[fi].FindingRef, Action: "conditional_restrict", AccessRef: allowed, Field: "source", Conditions: []string{"已确认业务允许上界适用于此服务"}, Impact: "收窄来源前需保留必要管理访问", ManualNextStep: "人工核对影响后在云控制台修改"},
		{Kind: "coverage_followup", TargetKind: "protection_scope", TargetRef: v.ScopeRef, BasisKind: "unknown_followup", EvidenceRefs: stringItems(asset["evidence_refs"]), Action: "collect_missing_evidence", Conditions: []string{"仅核对已提供的保护层"}, Impact: "其他层状态仍需人工核实", ManualNextStep: "核对当前保护范围"},
		{Kind: "component_consideration", TargetKind: "asset_service", TargetRef: web["service_ref"].(string), BasisKind: "applicability_assessment", EvidenceRefs: stringItems(asset["evidence_refs"]), ComponentTopic: "waf", Action: "collect_missing_evidence", Conditions: []string{"确认实际为公网应用服务后再评估"}, Impact: "部署位置与现有覆盖需核对", ManualNextStep: "人工评估应用层防护适用性"},
	}}
	if _, e := s.ValidateFinal(MarshalOutput(base)); e != nil {
		t.Fatal("bounded four-kind union rejected", e)
	}
	if e := ValidateBoundOutput(in, &result, base, s.LocalBindings()); e != nil {
		t.Fatal("fixed binding replay rejected policy/component union", e)
	}
	for _, mode := range []string{"wrong-direction", "wrong-finding-policy", "wrong-access-kind", "unknown-component", "database-waf", "false-known-gap"} {
		t.Run(mode, func(t *testing.T) {
			var x Output
			json.Unmarshal([]byte(MarshalOutput(base)), &x)
			switch mode {
			case "wrong-direction":
				x.Recommendations[0].Field = "destination"
			case "wrong-finding-policy":
				x.Recommendations[0].TargetRef = s.policyRef(0)
			case "wrong-access-kind":
				x.Recommendations[0].AccessRef = required
			case "unknown-component":
				x.Recommendations[2].ComponentTopic = "security_center"
			case "database-waf":
				x.Recommendations[2].TargetRef = services[2].(map[string]any)["service_ref"].(string)
			case "false-known-gap":
				x.Recommendations[1].BasisKind = "known_gap"
			}
			if _, e := s.ValidateFinal(MarshalOutput(x)); !errors.Is(e, ErrFinal) {
				t.Fatal("unsafe structured recommendation accepted", e)
			}
		})
	}
	base.Recommendations = base.Recommendations[:1]
	base.Recommendations[0].Action = "conditional_allow_required"
	base.Recommendations[0].AccessRef = required
	if _, e := s.ValidateFinal(MarshalOutput(base)); e != nil {
		t.Fatal("confirmed required-set reference rejected", e)
	}
}
func TestOversizedToolsPublishNoRefsAndProjectionHasExactIdentityBoundaries(t *testing.T) {
	in, e := audit.SyntheticExample("internet-in")
	if e != nil {
		t.Fatal(e)
	}
	ports := make([]domain.PortRange, 100)
	for i := range ports {
		ports[i] = domain.PortRange{From: i + 1, To: i + 1}
	}
	in.Assets[0].Services = nil
	for i := 0; i < 100; i++ {
		in.Assets[0].Services = append(in.Assets[0].Services, domain.Service{ID: fmt.Sprintf("bounded-service-%d", i), Protocol: "tcp", Role: "unknown", RoleStatus: "unknown", PublicAccess: "unknown", Ports: ports})
	}
	s, e := New(in, nil)
	if e != nil {
		t.Fatal(e)
	}
	var v InitialView
	json.Unmarshal([]byte(s.InitialContext()), &v)
	if _, e = s.Invoke(context.Background(), "get_asset_context", `{"asset_ref":"`+v.Assets[0]+`"}`); !errors.Is(e, ErrTooLarge) {
		t.Fatal("oversized tool response accepted", e)
	}
	if len(s.returned) != 0 || len(s.pending) != 0 {
		t.Fatal("failed oversized response published refs")
	}
	small, _, _, _ := queryCase(t, "internet-in")
	if _, e = small.ProjectText("另一个端点 192.0.2.100"); !errors.Is(e, ErrProjection) {
		t.Fatal("partial identity replacement changed a foreign endpoint into this task alias", e)
	}
}
func TestHistoryProjectsAllKnownReferencesAndRejectsExternalFreeText(t *testing.T) {
	in, e := audit.SyntheticExample("nat-out")
	if e != nil {
		t.Fatal(e)
	}
	in.AddressRefs["PRIVATE_ADDRESS_REFERENCE"] = domain.AddressReference{ScopeRef: in.Scope.Ref, Complete: true, Members: []string{"203.0.113.77"}}
	in.Baseline.Exceptions = []domain.Exception{{CheckID: "NAT-02", Subcheck: "OUT-01", ScopeRef: in.Scope.Ref, SubjectRef: in.Assets[0].ID, Confirmed: true, Reason: "PRIVATE_EXTERNAL_EXCEPTION_REASON", ExpiresAt: "2026-10-29T00:00:00Z"}}
	in.Policies[0].Conditions = map[string]json.RawMessage{"PRIVATE_DESCRIPTION_KEY": json.RawMessage(`"PRIVATE_DESCRIPTION_VALUE"`)}
	s, e := New(in, nil)
	if e != nil {
		t.Fatal(e)
	}
	for _, raw := range []string{"PRIVATE_ADDRESS_REFERENCE", "203.0.113.77", in.Assets[0].Services[0].Required[0].Addresses[0], in.Assets[0].Association.EvidenceRef, in.Protection.EvidenceRef} {
		projected, e := s.ProjectText("核对 " + raw + " 的证据")
		if e != nil || strings.Contains(projected, raw) {
			t.Fatal("known frozen reference did not project", e)
		}
	}
	for _, raw := range []string{"PRIVATE_EXTERNAL_EXCEPTION_REASON", "PRIVATE_DESCRIPTION_KEY", "PRIVATE_DESCRIPTION_VALUE"} {
		if _, e = s.ProjectText("外部内容 " + raw); !errors.Is(e, ErrProjection) {
			t.Fatal("external description entered history projection", e)
		}
	}
}

func TestResultBindingAndSameBatchFinalEvidence(t *testing.T) {
	in, e := audit.SyntheticExample("internet-in")
	if e != nil {
		t.Fatal(e)
	}
	result, e := audit.Run(context.Background(), in)
	if e != nil {
		t.Fatal(e)
	}
	for _, mode := range []string{"manifest", "unknown-rule", "foreign-evidence", "invalid-field", "fake-policy"} {
		t.Run(mode, func(t *testing.T) {
			b, _ := json.Marshal(result)
			var bad domain.Result
			json.Unmarshal(b, &bad)
			switch mode {
			case "manifest":
				bad.Manifest.SourceKind = "live_readonly"
			case "unknown-rule":
				bad.Findings[0].RuleID = "INVENTED-CHECK"
			case "foreign-evidence":
				bad.Evidence[0].ScopeRef = "other-scope"
			case "invalid-field":
				bad.Evidence[0].Field = "/missing/field"
			case "fake-policy":
				bad.Findings[0].Differences = []domain.Cell{{PolicyRef: "policy_missing"}}
			}
			if _, e := New(in, &bad); !errors.Is(e, ErrInput) {
				t.Fatal("forged rule result accepted", e)
			}
		})
	}
	s, _, _, v := queryCase(t, "internet-in")
	s.BeginTurn()
	asset := invoke(t, s, "get_asset_context", "asset_ref", v.Assets[0])
	refs := stringItems(asset["evidence_refs"])
	out := Output{ScopeRef: v.ScopeRef, RuleVersion: v.RuleVersion, SourceKind: v.SourceKind, AssessmentBasis: v.AssessmentBasis, Explanation: "模型补充说明。", Recommendations: []Recommendation{{Kind: "confirm_information", TargetKind: "asset", TargetRef: v.Assets[0], BasisKind: "unknown_followup", EvidenceRefs: refs, Action: "confirm_intent", Conditions: []string{"需要人工确认"}, Impact: "确认资料影响审计依据", ManualNextStep: "核对资料并保存草稿"}}}
	if _, e = s.ValidateFinal(MarshalOutput(out)); !errors.Is(e, ErrFinal) {
		t.Fatal("same-batch tool refs entered final output", e)
	}
	s.BeginTurn()
	if _, e = s.ValidateFinal(MarshalOutput(out)); e != nil {
		t.Fatal("next-turn evidence not available", e)
	}
}
