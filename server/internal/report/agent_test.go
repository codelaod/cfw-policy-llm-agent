package report

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"cfw-policy-llm-agent/server/internal/agentquery"
	"cfw-policy-llm-agent/server/internal/audit"
	"cfw-policy-llm-agent/server/internal/domain"
)

func agentTool(t *testing.T, q *agentquery.Service, name, key, ref string) map[string]any {
	t.Helper()
	args, _ := json.Marshal(map[string]string{key: ref})
	raw, e := q.Invoke(context.Background(), name, string(args))
	if e != nil {
		t.Fatal(e)
	}
	var result map[string]any
	json.Unmarshal([]byte(raw), &result)
	return result
}
func agentData(value map[string]any) map[string]any { return value["data"].(map[string]any) }
func agentStrings(value any) []string {
	out := []string{}
	for _, v := range value.([]any) {
		out = append(out, v.(string))
	}
	return out
}
func supplementCase(t *testing.T) (domain.Input, domain.Result, AgentSupplement, string) {
	t.Helper()
	in, e := audit.SyntheticExample("internet-in")
	if e != nil {
		t.Fatal(e)
	}
	in.SourceKind = "imported"
	result, e := audit.Run(context.Background(), in)
	if e != nil {
		t.Fatal(e)
	}
	q, e := agentquery.New(in, &result)
	if e != nil {
		t.Fatal(e)
	}
	var initial agentquery.InitialView
	json.Unmarshal([]byte(q.InitialContext()), &initial)
	asset := agentTool(t, q, "get_asset_context", "asset_ref", initial.Assets[0])
	partition := agentTool(t, q, "get_partition_context", "scope_ref", initial.ScopeRef)
	services := agentData(asset)["services"].([]any)
	web := services[0].(map[string]any)
	manage := services[1].(map[string]any)
	q.BeginTurn()
	business := agentTool(t, q, "get_business_intent", "business_ref", manage["business_ref"].(string))
	rule := agentTool(t, q, "get_rule_definition", "rule_id", "CHK-07")
	fi := -1
	for i, f := range result.Findings {
		if f.ServiceRef == in.Assets[0].Services[1].ID && f.Verdict == "fail" {
			for _, c := range f.Differences {
				if c.PolicyRef == "policy-management-wide" {
					fi = i
					break
				}
			}
		}
		if fi >= 0 {
			break
		}
	}
	if fi < 0 {
		t.Fatal("fixture")
	}
	finding := agentTool(t, q, "get_finding_evidence", "finding_ref", initial.Findings[fi].FindingRef)
	policy := agentData(asset)["policy_refs"].([]any)[2].(string)
	agentTool(t, q, "get_policy", "policy_ref", policy)
	q.BeginTurn()
	evidence := agentStrings(asset["evidence_refs"])
	conditions := []string{"PRIVATE_MODEL_CONDITION"}
	impact, next := "PRIVATE_MODEL_IMPACT", "PRIVATE_MODEL_MANUAL_STEP"
	out := agentquery.Output{ScopeRef: initial.ScopeRef, RuleVersion: initial.RuleVersion, SourceKind: initial.SourceKind, AssessmentBasis: initial.AssessmentBasis, Explanation: "PRIVATE_MODEL_EXPLANATION", Roles: []agentquery.RoleCandidate{{AssetRef: initial.Assets[0], ServiceRef: web["service_ref"].(string), Role: "web", SupportingEvidence: evidence, ContradictingEvidence: []string{}, Unknowns: []string{"PRIVATE_ROLE_UNKNOWN"}, Questions: []string{"PRIVATE_ROLE_QUESTION"}}}, Questions: []string{"PRIVATE_FINAL_QUESTION"}, Recommendations: []agentquery.Recommendation{
		{Kind: "policy_change", TargetKind: "policy", TargetRef: policy, BasisKind: "known_gap", EvidenceRefs: agentStrings(finding["evidence_refs"]), BusinessRef: manage["business_ref"].(string), FindingRef: initial.Findings[fi].FindingRef, Action: "conditional_restrict", AccessRef: agentData(business)["allowed"].(map[string]any)["access_ref"].(string), Field: "source", Conditions: conditions, Impact: impact, ManualNextStep: next},
		{Kind: "coverage_followup", TargetKind: "protection_scope", TargetRef: initial.ScopeRef, BasisKind: "unknown_followup", EvidenceRefs: agentStrings(partition["evidence_refs"]), Action: "collect_missing_evidence", Conditions: conditions, Impact: impact, ManualNextStep: next},
		{Kind: "component_consideration", TargetKind: "asset_service", TargetRef: web["service_ref"].(string), BasisKind: "applicability_assessment", EvidenceRefs: append(append([]string{}, evidence...), agentStrings(rule["evidence_refs"])...), ComponentTopic: "waf", Action: "collect_missing_evidence", Conditions: conditions, Impact: impact, ManualNextStep: next},
		{Kind: "confirm_information", TargetKind: "asset", TargetRef: initial.Assets[0], BasisKind: "unknown_followup", EvidenceRefs: evidence, Action: "confirm_intent", Conditions: conditions, Impact: impact, ManualNextStep: next},
	}}
	if _, e = q.ValidateFinal(agentquery.MarshalOutput(out)); e != nil {
		t.Fatal("invalid test supplement", e)
	}
	return in, result, AgentSupplement{Output: &out, Bindings: q.LocalBindings(), ModelCalls: 3, ToolCalls: 6}, initial.ScopeRef
}
func TestAgentLayerUsesPublicTemplatesAndKeepsOriginalRules(t *testing.T) {
	in, result, supp, nonce := supplementCase(t)
	before, _ := json.Marshal(result)
	base, e := Build(context.Background(), result, nil, "json")
	if e != nil {
		t.Fatal(e)
	}
	var baseline Document
	json.Unmarshal(base, &baseline)
	for _, format := range []string{"json", "markdown"} {
		t.Run(format, func(t *testing.T) {
			body, e := BuildWithAgent(context.Background(), in, result, nil, format, Execution{Mode: "rules_and_agent", ModelStage: "completed"}, supp)
			if e != nil {
				t.Fatal(e)
			}
			for _, private := range []string{"PRIVATE_", in.Assets[0].ID, in.Assets[0].Address, in.Scope.Ref, in.Scope.AccountRef, in.Policies[2].ID, nonce, "/assets/", "/policies/"} {
				if bytes.Contains(body, []byte(private)) {
					t.Fatal("private model/identity/pointer escaped", private)
				}
			}
			if !bytes.Contains(body, []byte("详细模型解释")) {
				t.Fatal("omission not disclosed")
			}
			if format == "json" {
				var doc Document
				if e = json.Unmarshal(body, &doc); e != nil {
					t.Fatal(e)
				}
				if doc.Agent == nil || len(doc.Agent.Recommendations) != 4 || len(doc.Agent.Roles) != 1 || doc.Agent.Roles[0].Status != "candidate" {
					t.Fatal("agent union omitted", doc.Agent)
				}
				if !reflect.DeepEqual(doc.Findings, baseline.Findings) || !reflect.DeepEqual(doc.Evidence, baseline.Evidence) || !reflect.DeepEqual(doc.Coverage, baseline.Coverage) {
					t.Fatal("Agent changed rule facts or base aliases")
				}
				if doc.Agent.Usage.ReportedTokens != nil || doc.Agent.Usage.PhysicalAttempts != nil {
					t.Fatal("missing usage became zero")
				}
				for _, rec := range doc.Agent.Recommendations {
					if len(rec.EvidenceAliases) == 0 || len(rec.Conditions) == 0 || rec.ManualNextStep == "" || rec.Impact == "" {
						t.Fatal(rec)
					}
				}
			} else {
				for _, kind := range []string{"policy_change", "coverage_followup", "component_consideration", "confirm_information"} {
					if !bytes.Contains(body, []byte(kind)) {
						t.Fatal("markdown union missing", kind)
					}
				}
			}
		})
	}
	after, _ := json.Marshal(result)
	if !bytes.Equal(before, after) {
		t.Fatal("report mutated original input")
	}
}
func TestAgentSupplementRejectsForgedBindingAndSource(t *testing.T) {
	for _, mode := range []string{"input-manifest", "result-manifest", "source-upgrade", "wrong-binding", "model-zero", "tool-zero-returned", "stage-mismatch", "unknown-error", "oversized-output"} {
		t.Run(mode, func(t *testing.T) {
			in, result, supp, _ := supplementCase(t)
			execution := Execution{Mode: "rules_and_agent", ModelStage: "completed"}
			switch mode {
			case "input-manifest":
				in.SnapshotVersion = "different"
			case "result-manifest":
				result.Manifest.SourceKind = "live_readonly"
			case "source-upgrade":
				supp.Output.SourceKind = "live_readonly"
			case "wrong-binding":
				for key, b := range supp.Bindings {
					b.Field = "/missing/field"
					supp.Bindings[key] = b
					break
				}
			case "model-zero":
				supp.ModelCalls = 0
			case "tool-zero-returned":
				supp.ToolCalls = 0
			case "stage-mismatch":
				execution.ModelStage = "not_completed"
			case "unknown-error":
				supp.Output = nil
				supp.ErrorCode = "PRIVATE_UNKNOWN_ERROR"
				execution.ModelStage = "not_completed"
			case "oversized-output":
				supp.Output.Explanation = strings.Repeat("x", MaxBytes+1)
			}
			body, e := BuildWithAgent(context.Background(), in, result, nil, "json", execution, supp)
			if body != nil || !errors.Is(e, ErrInvalidResult) {
				t.Fatal("invalid supplement accepted", e)
			}
		})
	}
}
func TestAgentFailureKeepsRuleResultsAndControlledGap(t *testing.T) {
	in, result, supp, _ := supplementCase(t)
	supp.Output = nil
	supp.ErrorCode = "MODEL_PROTOCOL_INVALID"
	for _, format := range []string{"json", "markdown"} {
		body, e := BuildWithAgent(context.Background(), in, result, nil, format, Execution{Mode: "rules_and_agent", ModelStage: "not_completed"}, supp)
		if e != nil {
			t.Fatal(e)
		}
		if bytes.Contains(body, []byte("PRIVATE_")) || !bytes.Contains(body, []byte("MODEL_PROTOCOL_INVALID")) {
			t.Fatal("failure metadata leaked or vanished")
		}
		if format == "json" {
			var doc Document
			json.Unmarshal(body, &doc)
			if doc.Agent == nil || doc.Agent.Error == nil || len(doc.Agent.Roles) != 0 || len(doc.Agent.Recommendations) != 0 || len(doc.Findings) != len(result.Findings) {
				t.Fatal("failed model erased or claimed candidates", doc.Agent)
			}
		}
	}
	supp = AgentSupplement{ErrorCode: "MODEL_NOT_CONFIGURED"}
	body, e := BuildWithAgent(context.Background(), in, result, nil, "json", Execution{Mode: "rules_and_agent", ModelStage: "not_configured"}, supp)
	if e != nil {
		t.Fatal(e)
	}
	var doc Document
	json.Unmarshal(body, &doc)
	if doc.Agent.ModelCalls != 0 || doc.Agent.Usage.ReportedTokens != nil {
		t.Fatal("unconfigured stage invented usage")
	}
	in.Scope.AccountRef = "different-owner"
	if _, e = BuildWithAgent(context.Background(), in, result, nil, "json", Execution{Mode: "rules_and_agent", ModelStage: "not_configured"}, supp); !errors.Is(e, ErrInvalidResult) {
		t.Fatal("nil failure bindings skipped input/result binding", e)
	}
}
func TestAgentUsageKeepsUnknownPartialAndExplicitZeroDistinct(t *testing.T) {
	for _, mode := range []string{"missing", "unknown", "partial", "zero"} {
		t.Run(mode, func(t *testing.T) {
			in, result, supp, _ := supplementCase(t)
			switch mode {
			case "unknown":
				supp.Usage = &AgentUsage{PhysicalAttempts: 2, UnknownUsageAttempts: 2}
			case "partial":
				supp.Usage = &AgentUsage{PhysicalAttempts: 2, KnownUsageResponses: 1, UnknownUsageAttempts: 1, ReportedTokens: &domain.ModelUsage{PromptTokens: 3, CompletionTokens: 2, TotalTokens: 9}}
			case "zero":
				supp.Usage = &AgentUsage{PhysicalAttempts: 1, KnownUsageResponses: 1, ReportedTokens: &domain.ModelUsage{}}
			}
			body, e := BuildWithAgent(context.Background(), in, result, nil, "json", Execution{Mode: "rules_and_agent", ModelStage: "completed"}, supp)
			if e != nil {
				t.Fatal(e)
			}
			var doc Document
			json.Unmarshal(body, &doc)
			u := doc.Agent.Usage
			switch mode {
			case "missing":
				if u.Status != "unknown" || u.PhysicalAttempts != nil || u.ReportedTokens != nil {
					t.Fatal(u)
				}
			case "unknown":
				if u.Status != "unknown" || u.ReportedTokens != nil || *u.UnknownUsageAttempts != 2 {
					t.Fatal(u)
				}
			case "partial":
				if u.Status != "partial" || u.ReportedTokens == nil || u.ReportedTokens.TotalTokens != 9 || *u.UnknownUsageAttempts != 1 {
					t.Fatal("provider total recomputed or unknown erased", u)
				}
			case "zero":
				if u.Status != "reported" || u.ReportedTokens == nil || u.ReportedTokens.TotalTokens != 0 {
					t.Fatal("known zero became unknown", u)
				}
			}
		})
	}
	for _, usage := range []*AgentUsage{{PhysicalAttempts: 1, KnownUsageResponses: 1, UnknownUsageAttempts: 1}, {PhysicalAttempts: 1, KnownUsageResponses: 1}, {PhysicalAttempts: 1, UnknownUsageAttempts: 1, ReportedTokens: &domain.ModelUsage{}}, {PhysicalAttempts: 7, UnknownUsageAttempts: 7}, {PhysicalAttempts: 1, KnownUsageResponses: 1, ReportedTokens: &domain.ModelUsage{TotalTokens: -1}}} {
		in, result, supp, _ := supplementCase(t)
		supp.Usage = usage
		if _, e := BuildWithAgent(context.Background(), in, result, nil, "json", Execution{Mode: "rules_and_agent", ModelStage: "completed"}, supp); !errors.Is(e, ErrInvalidResult) {
			t.Fatal("invalid physical usage metadata accepted", e)
		}
	}
}
func TestAgentPublicAliasesIgnoreTemporaryNamespacesAndModelProse(t *testing.T) {
	in, result, supp, _ := supplementCase(t)
	first, e := BuildWithAgent(context.Background(), in, result, nil, "json", Execution{Mode: "rules_and_agent", ModelStage: "completed"}, supp)
	if e != nil {
		t.Fatal(e)
	}
	in2, result2, supp2, _ := supplementCase(t)
	supp2.Output.Explanation = "PRIVATE_CHANGED_EXPLANATION"
	supp2.Output.Recommendations[0].Conditions = []string{"PRIVATE_DIFFERENT_CONDITION"}
	second, e := BuildWithAgent(context.Background(), in2, result2, nil, "json", Execution{Mode: "rules_and_agent", ModelStage: "completed"}, supp2)
	if e != nil {
		t.Fatal(e)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("ephemeral namespace or omitted model prose changed fixed public projection")
	}
	for _, format := range []string{"json", "markdown"} {
		body, e := BuildWithAgent(context.Background(), in, result, nil, format, Execution{Mode: "rules_only", ModelStage: "not_requested"}, AgentSupplement{})
		if e != nil {
			t.Fatal(e)
		}
		if bytes.Contains(body, []byte(`"agent"`)) || bytes.Contains(body, []byte("Agent 补充")) {
			t.Fatal("rules-only invented an agent layer")
		}
	}
}
func TestAgentSameNamedServicesRemainBoundToDifferentAssets(t *testing.T) {
	in, e := audit.SyntheticExample("internet-in")
	if e != nil {
		t.Fatal(e)
	}
	extra := in.Assets[0]
	extra.ID = "PRIVATE_SECOND_ASSET"
	extra.Address = "192.0.2.11"
	in.Assets = append(in.Assets, extra)
	in.Selection.AssetIDs = append(in.Selection.AssetIDs, extra.ID)
	result, e := audit.Run(context.Background(), in)
	if e != nil {
		t.Fatal(e)
	}
	result.Findings = []domain.Finding{}
	result.Evidence = []domain.Evidence{}
	result.Coverage = domain.Coverage{Complete: false, Limitations: []string{"RULE_STAGE_INTERRUPTED"}}
	q, e := agentquery.New(in, &result)
	if e != nil {
		t.Fatal(e)
	}
	var v agentquery.InitialView
	json.Unmarshal([]byte(q.InitialContext()), &v)
	roles := []agentquery.RoleCandidate{}
	for _, assetRef := range v.Assets {
		a := agentTool(t, q, "get_asset_context", "asset_ref", assetRef)
		service := agentData(a)["services"].([]any)[0].(map[string]any)
		roles = append(roles, agentquery.RoleCandidate{AssetRef: assetRef, ServiceRef: service["service_ref"].(string), Role: "web", SupportingEvidence: agentStrings(a["evidence_refs"]), Unknowns: []string{"仍需核实实际用途"}, Questions: []string{"请操作者确认"}})
	}
	q.BeginTurn()
	out := agentquery.Output{ScopeRef: v.ScopeRef, RuleVersion: v.RuleVersion, SourceKind: v.SourceKind, AssessmentBasis: v.AssessmentBasis, Explanation: "PRIVATE_MODEL_EXPLANATION", Roles: roles}
	supp := AgentSupplement{Output: &out, Bindings: q.LocalBindings(), ModelCalls: 2, ToolCalls: 2}
	body, e := BuildWithAgent(context.Background(), in, result, nil, "json", Execution{Mode: "rules_and_agent", ModelStage: "completed"}, supp)
	if e != nil {
		t.Fatal(e)
	}
	var doc Document
	json.Unmarshal(body, &doc)
	first, second := doc.Agent.Roles[0].Target, doc.Agent.Roles[1].Target
	if first.AssetAlias == second.AssetAlias || first.ServiceAlias == second.ServiceAlias || first.Alias == "" || second.Alias == "" {
		t.Fatal("same service name confused local asset ownership", first, second)
	}
}
func TestAgentReportCancellationAndBounds(t *testing.T) {
	in, result, supp, _ := supplementCase(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e := BuildWithAgent(ctx, in, result, nil, "json", Execution{Mode: "rules_and_agent", ModelStage: "completed"}, supp); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	if _, e := BuildWithAgent(context.Background(), in, result, nil, "html", Execution{Mode: "rules_and_agent", ModelStage: "completed"}, supp); !errors.Is(e, ErrFormat) {
		t.Fatal(e)
	}
	supp.Output.Explanation = strings.Repeat("x", MaxBytes+1)
	body, e := BuildWithAgent(context.Background(), in, result, nil, "markdown", Execution{Mode: "rules_and_agent", ModelStage: "completed"}, supp)
	if body != nil || e == nil {
		t.Fatal("oversized model material entered report")
	}
}

func TestAgentSupplementCannotSerializeLocalBindingsOrModelProse(t *testing.T) {
	_, _, supp, _ := supplementCase(t)
	data, e := json.Marshal(supp)
	if e != nil {
		t.Fatal(e)
	}
	if bytes.Contains(data, []byte("PRIVATE_")) || bytes.Contains(data, []byte("/assets/")) || bytes.Contains(data, []byte(`"Bindings"`)) || bytes.Contains(data, []byte(`"Output"`)) {
		t.Fatal("server-only report input serialized private fields")
	}
}
