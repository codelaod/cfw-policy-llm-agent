package agentrun

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"cfw-policy-llm-agent/server/internal/agentquery"
	"cfw-policy-llm-agent/server/internal/audit"
	"cfw-policy-llm-agent/server/internal/workload"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/eino-contrib/jsonschema"
)

type fixedDomainQuery struct {
	s       *agentquery.Service
	invoked []string
	pad     bool
}

func (q *fixedDomainQuery) InitialContext() string { return q.s.InitialContext() }
func (q *fixedDomainQuery) BeginTurn()             { q.s.BeginTurn() }
func (q *fixedDomainQuery) Tools() []*schema.ToolInfo {
	out := []*schema.ToolInfo{}
	for _, d := range q.s.Tools() {
		var p jsonschema.Schema
		_ = json.Unmarshal(d.Parameters, &p)
		out = append(out, &schema.ToolInfo{Name: d.Name, Desc: d.Description, ParamsOneOf: schema.NewParamsOneOfByJSONSchema(&p)})
	}
	return out
}
func (q *fixedDomainQuery) Invoke(ctx context.Context, name, args string) (string, error) {
	q.invoked = append(q.invoked, name)
	result, e := q.s.Invoke(ctx, name, args)
	if e == nil && q.pad {
		var value map[string]any
		_ = json.Unmarshal([]byte(result), &value)
		value["bounded_fixture_padding"] = strings.Repeat("x", 26<<10)
		raw, _ := json.Marshal(value)
		return string(raw), nil
	}
	return result, e
}

func TestFixedPrefetchStopsAtCumulativeInputBoundaryBeforeModel(t *testing.T) {
	q := fixedDomain(t)
	q.pad = true
	called := false
	pipeline := FixedPipeline{Version: "fixed-padding-v1", Steps: []FixedStep{{Tool: "get_asset_context", Selector: "assets", MaxItems: 1}, {Tool: "get_business_intent", Selector: "business", MaxItems: 4}, {Tool: "get_partition_context", Selector: "scope", MaxItems: 1}}}
	out, e := RunFixed(context.Background(), Task{Kind: "audit", Queries: q, NewModel: func(context.Context) (model.BaseChatModel, error) { called = true; return nil, nil }}, Config{}, pipeline, SinkFunc(func(context.Context, Event) error { return nil }))
	if e == nil || out.ErrorCode != "MODEL_INPUT_LIMIT" || called || out.ModelCalls != 0 || out.ToolCalls > 5 || len(q.invoked) > 5 {
		t.Fatal(out, e, called, len(q.invoked))
	}
}
func (q *fixedDomainQuery) ValidateFinal(text string) (json.RawMessage, error) {
	v, e := q.s.ValidateFinal(text)
	if e != nil {
		return nil, e
	}
	return json.Marshal(v)
}
func fixedDomain(t *testing.T) *fixedDomainQuery {
	t.Helper()
	in, e := audit.SyntheticExample("internet-in")
	if e != nil {
		t.Fatal(e)
	}
	rules, e := audit.Run(context.Background(), in)
	if e != nil {
		t.Fatal(e)
	}
	service, e := agentquery.New(in, &rules)
	if e != nil {
		t.Fatal(e)
	}
	return &fixedDomainQuery{s: service}
}
func fixedAnswer(t *testing.T, q *fixedDomainQuery) string {
	t.Helper()
	var i agentquery.InitialView
	if e := json.Unmarshal([]byte(q.InitialContext()), &i); e != nil {
		t.Fatal(e)
	}
	out := agentquery.Output{ScopeRef: i.ScopeRef, RuleVersion: i.RuleVersion, SourceKind: i.SourceKind, AssessmentBasis: i.AssessmentBasis, Explanation: "人工模型夹具，仅保留待核对说明。", Roles: []agentquery.RoleCandidate{}, Recommendations: []agentquery.Recommendation{}, Questions: []string{}}
	raw, _ := json.Marshal(out)
	return string(raw)
}
func TestFixedPipelineUsesActualDomainToolsAndOneFinalModelWithoutForgedWire(t *testing.T) {
	q := fixedDomain(t)
	steps := FixedPipeline{Version: "public-fixed-v1", Steps: []FixedStep{{Tool: "get_asset_context", Selector: "assets", MaxItems: 1}, {Tool: "get_business_intent", Selector: "business", MaxItems: 4}}}
	calls := 0
	events := []Event{}
	out, e := RunFixed(context.Background(), Task{Kind: "audit", Queries: q, History: []HistoryMessage{{Role: "user", Text: "按同一固定任务核对证据和业务缺项。"}}, NewModel: func(context.Context) (model.BaseChatModel, error) {
		calls++
		if len(q.invoked) != 5 {
			t.Fatal("fixed reads were not completed before model creation", q.invoked)
		}
		return &scriptedModel{generate: func(ctx context.Context, messages []*schema.Message, _ ...model.Option) (*schema.Message, error) {
			deadline, ok := ctx.Deadline()
			if !ok || time.Until(deadline) <= 0 {
				t.Fatal("missing request deadline")
			}
			hasInitial, hasFixed := false, false
			for _, m := range messages {
				if m.Role == schema.Tool || len(m.ToolCalls) > 0 {
					t.Fatal("script pretended model selected tools")
				}
				hasInitial = hasInitial || strings.HasPrefix(m.Content, "固定公开任务资料")
				hasFixed = hasFixed || strings.Contains(m.Content, "predefined_pipeline")
			}
			if !hasInitial || !hasFixed {
				t.Fatal("fixed model lacked shared brief or visible prefetched source")
			}
			r := answer(fixedAnswer(t, q))
			r.ReasoningContent = "PRIVATE_FIXED_REASONING"
			return r, nil
		}}, nil
	}}, Config{}, steps, SinkFunc(func(_ context.Context, event Event) error { events = append(events, event); return nil }))
	if e != nil || out.ModelCalls != 1 || out.ToolCalls != 5 || calls != 1 || len(events) != 10 || strings.Contains(string(out.Output), "PRIVATE_FIXED_REASONING") {
		t.Fatal(out, e, calls, len(events))
	}
}
func TestFixedPipelineCannotChangeSelectionOrSpendDefaultWhenExplicitZero(t *testing.T) {
	for _, scenario := range []string{"bad-tool", "selection-limit", "tool-zero", "model-zero", "model-chooses-tool"} {
		t.Run(scenario, func(t *testing.T) {
			q := fixedDomain(t)
			pipeline := FixedPipeline{Version: "fixed-v1", Steps: []FixedStep{{Tool: "get_asset_context", Selector: "assets", MaxItems: 1}, {Tool: "get_business_intent", Selector: "business", MaxItems: 4}}}
			limits := workload.Default("audit")
			called := false
			switch scenario {
			case "bad-tool":
				pipeline.Steps[0].Tool = "DeleteControlPolicy"
			case "selection-limit":
				pipeline.Steps[1].MaxItems = 1
			case "tool-zero":
				limits.ToolCalls = 0
				limits.ToolBatch = 0
			case "model-zero":
				limits.ModelHTTPAttempts = 0
			}
			out, e := RunFixed(context.Background(), Task{Kind: "audit", Queries: q, NewModel: func(context.Context) (model.BaseChatModel, error) {
				called = true
				return &scriptedModel{generate: func(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
					if scenario == "model-chooses-tool" {
						return calls(call("attempt-extra", "get_asset_context", `{}`)), nil
					}
					return answer(fixedAnswer(t, q)), nil
				}}, nil
			}}, Config{Workload: workload.Resolved{ProfileRef: "fixed-boundary-test", Limits: limits}}, pipeline, noSink)
			if e == nil || len(out.Output) != 0 {
				t.Fatal("fixed boundary was ignored", out, e)
			}
			if scenario != "model-chooses-tool" && called {
				t.Fatal("failed prefetch or explicit zero initialized model")
			}
		})
	}
}
