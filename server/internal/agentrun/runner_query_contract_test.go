package agentrun

import (
	"context"
	"encoding/json"
	"testing"

	"cfw-policy-llm-agent/server/internal/agentquery"
	"cfw-policy-llm-agent/server/internal/audit"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/eino-contrib/jsonschema"
)

// This adapter keeps the real query result JSON unchanged. In particular, it
// must not rename evidence_refs just to satisfy the runner's event projection.
type contractQuery struct{ inner *agentquery.Service }

func (q contractQuery) InitialContext() string { return q.inner.InitialContext() }
func (q contractQuery) BeginTurn()             { q.inner.BeginTurn() }
func (q contractQuery) Invoke(ctx context.Context, name, args string) (string, error) {
	return q.inner.Invoke(ctx, name, args)
}
func (q contractQuery) ValidateFinal(text string) (json.RawMessage, error) {
	out, err := q.inner.ValidateFinal(text)
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(out)
	return data, err
}
func (q contractQuery) Tools() []*schema.ToolInfo {
	infos := []*schema.ToolInfo{}
	for _, definition := range q.inner.Tools() {
		var params jsonschema.Schema
		if json.Unmarshal(definition.Parameters, &params) != nil {
			panic("invalid server-owned tool schema")
		}
		infos = append(infos, &schema.ToolInfo{Name: definition.Name, Desc: definition.Description, ParamsOneOf: schema.NewParamsOneOfByJSONSchema(&params)})
	}
	return infos
}

func TestRealQueryServiceEvidenceContractReachesFinalOutputAndEvents(t *testing.T) {
	in, err := audit.SyntheticExample("internet-in")
	if err != nil {
		t.Fatal(err)
	}
	queries, err := agentquery.New(in, nil)
	if err != nil {
		t.Fatal(err)
	}
	var initial agentquery.InitialView
	if err = json.Unmarshal([]byte(queries.InitialContext()), &initial); err != nil {
		t.Fatal(err)
	}
	var emitted []Event
	var returnedEvidence []string
	step := 0
	configured := Task{Kind: "assistant_turn", Queries: contractQuery{queries}, NewModel: func(context.Context) (model.BaseChatModel, error) {
		return &scriptedModel{generate: func(_ context.Context, messages []*schema.Message, _ ...model.Option) (*schema.Message, error) {
			step++
			if step == 1 {
				return calls(call("real-query-asset", "get_asset_context", `{"asset_ref":"`+initial.Assets[0]+`"}`)), nil
			}
			for _, message := range messages {
				if message.Role == schema.Tool && message.ToolCallID == "real-query-asset" {
					var result agentquery.ToolResult
					if err := json.Unmarshal([]byte(message.Content), &result); err != nil {
						return nil, err
					}
					returnedEvidence = result.EvidenceRefs
				}
			}
			out := agentquery.Output{ScopeRef: initial.ScopeRef, RuleVersion: initial.RuleVersion, SourceKind: initial.SourceKind, AssessmentBasis: initial.AssessmentBasis, Explanation: "这些是固定资料下的待确认建议。", Recommendations: []agentquery.Recommendation{{Kind: "confirm_information", TargetKind: "asset", TargetRef: initial.Assets[0], BasisKind: "unknown_followup", EvidenceRefs: returnedEvidence, Action: "confirm_intent", Conditions: []string{"需要操作者核对业务资料"}, Impact: "业务要求会影响审计判据", ManualNextStep: "在资产页面预览并确认业务资料"}}}
			return answer(agentquery.MarshalOutput(out)), nil
		}}, nil
	}}
	outcome, err := Run(context.Background(), configured, Config{}, SinkFunc(func(_ context.Context, event Event) error { emitted = append(emitted, event); return nil }))
	if err != nil {
		t.Fatalf("real query result was rejected: %v", err)
	}
	if outcome.ModelCalls != 2 || outcome.ToolCalls != 1 || len(outcome.Output) == 0 || len(returnedEvidence) == 0 {
		t.Fatal("real query result did not reach final domain validation")
	}
	if len(emitted) != 2 || emitted[1].Type != "tool_finished" || emitted[1].Status != "succeeded" || len(emitted[1].EvidenceRefs) != len(returnedEvidence) || emitted[1].EvidenceRefs[0] != returnedEvidence[0] {
		t.Fatal("tool event did not retain actual returned evidence references")
	}
}
