package report

import (
	"cfw-policy-llm-agent/server/internal/agentquery"
	"cfw-policy-llm-agent/server/internal/audit"
	"context"
	"encoding/json"
	"testing"
)

func TestAgentReportRetainsControlledUnavailableContextFailure(t *testing.T) {
	in, result, _, _ := supplementCase(t)
	for _, format := range []string{"json", "markdown"} {
		body, e := BuildWithAgent(context.Background(), in, result, nil, format, Execution{Mode: "rules_and_agent", ModelStage: "not_completed", JobStatus: "partial"}, AgentSupplement{ErrorCode: "AGENT_CONTEXT_UNAVAILABLE"})
		if e != nil || len(body) == 0 {
			t.Fatal("valid context failure could not produce fixed gap report", e)
		}
	}
}
func TestAgentReportUsageOverflowRemainsUnknownWithoutLosingCounts(t *testing.T) {
	in, result, supp, _ := supplementCase(t)
	supp.Usage = &AgentUsage{PhysicalAttempts: 2, KnownUsageResponses: 2, UsageTotalsOverflow: true}
	body, e := BuildWithAgent(context.Background(), in, result, nil, "json", Execution{Mode: "rules_and_agent", ModelStage: "completed"}, supp)
	if e != nil {
		t.Fatal("valid overflow summary rejected", e)
	}
	var doc Document
	json.Unmarshal(body, &doc)
	if !doc.Agent.Usage.UsageTotalsOverflow || doc.Agent.Usage.Status != "overflow" || doc.Agent.Usage.ReportedTokens != nil || *doc.Agent.Usage.KnownUsageResponses != 2 || *doc.Agent.Usage.PhysicalAttempts != 2 {
		t.Fatal("overflow totals were invented or response counts lost", doc.Agent.Usage)
	}
}
func TestAgentInstanceAliasesStayUniqueWithExistingSameNamedServices(t *testing.T) {
	in, e := audit.SyntheticExample("internet-in")
	if e != nil {
		t.Fatal(e)
	}
	extra := in.Assets[0]
	extra.ID = "second-instance"
	extra.Address = "192.0.2.11"
	in.Assets = append(in.Assets, extra)
	in.Selection.AssetIDs = append(in.Selection.AssetIDs, extra.ID)
	result, e := audit.Run(context.Background(), in)
	if e != nil {
		t.Fatal(e)
	}
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
		roles = append(roles, agentquery.RoleCandidate{AssetRef: assetRef, ServiceRef: service["service_ref"].(string), Role: "web", SupportingEvidence: agentStrings(a["evidence_refs"]), Unknowns: []string{"需核实用途"}, Questions: []string{"请确认角色"}})
	}
	q.BeginTurn()
	out := agentquery.Output{ScopeRef: v.ScopeRef, RuleVersion: v.RuleVersion, SourceKind: v.SourceKind, AssessmentBasis: v.AssessmentBasis, Explanation: "模型角色候选。", Roles: roles}
	body, e := BuildWithAgent(context.Background(), in, result, nil, "json", Execution{Mode: "rules_and_agent", ModelStage: "completed"}, AgentSupplement{Output: &out, Bindings: q.LocalBindings(), ModelCalls: 2, ToolCalls: 2})
	if e != nil {
		t.Fatal(e)
	}
	var doc Document
	json.Unmarshal(body, &doc)
	if doc.Agent.Roles[0].Target.ServiceAlias == doc.Agent.Roles[1].Target.ServiceAlias {
		t.Fatal("separate instances reused an ambiguous service instance alias")
	}
}
