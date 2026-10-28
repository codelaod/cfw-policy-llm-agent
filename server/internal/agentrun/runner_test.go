package agentrun

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type scriptedModel struct {
	generate func(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error)
}

func (m *scriptedModel) Generate(ctx context.Context, messages []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	return m.generate(ctx, messages, opts...)
}
func (m *scriptedModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	panic("stream must never be called")
}
func answer(text string) *schema.Message {
	return &schema.Message{Role: schema.Assistant, Content: text, ResponseMeta: &schema.ResponseMeta{FinishReason: "stop"}}
}
func calls(items ...schema.ToolCall) *schema.Message {
	return &schema.Message{Role: schema.Assistant, ToolCalls: items, ReasoningContent: "transient-private-reasoning", Extra: map[string]any{"reasoning_details": "complete-transient-wire"}, ResponseMeta: &schema.ResponseMeta{FinishReason: "tool_calls"}}
}
func call(id, name, args string) schema.ToolCall {
	return schema.ToolCall{ID: id, Type: "function", Function: schema.FunctionCall{Name: name, Arguments: args}}
}

type fixtureQuery struct {
	mu                sync.Mutex
	scope             string
	missing           bool
	turns             int
	invocations       []string
	available, staged map[string]bool
	group             string
	invoke            func(context.Context, string, string) (string, error)
}

func newQuery(scope string) *fixtureQuery {
	return &fixtureQuery{scope: scope, group: "opaque-group-" + scope, available: map[string]bool{}, staged: map[string]bool{}}
}
func (q *fixtureQuery) InitialContext() string {
	return `{"scope":"` + q.scope + `","asset_ref":"asset-` + q.scope + `"}`
}
func (q *fixtureQuery) BeginTurn() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.turns++
	for ref := range q.staged {
		q.available[ref] = true
	}
	q.staged = map[string]bool{}
}
func (q *fixtureQuery) Tools() []*schema.ToolInfo {
	return []*schema.ToolInfo{{Name: "get_asset_context", Desc: "Read the selected asset only"}, {Name: "get_security_group_rules", Desc: "Read a returned group reference only"}, {Name: "get_business_intent", Desc: "Read fixed business requirements"}}
}
func (q *fixtureQuery) Invoke(ctx context.Context, name, args string) (string, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.invocations = append(q.invocations, name)
	if q.invoke != nil {
		return q.invoke(ctx, name, args)
	}
	switch name {
	case "get_asset_context":
		q.staged[q.group] = true
		return `{"evidence_refs":["` + q.group + `"],"data":{"scope":"` + q.scope + `"}}`, nil
	case "get_security_group_rules":
		var value struct {
			Ref string `json:"ref"`
		}
		if json.Unmarshal([]byte(args), &value) != nil || !q.available[value.Ref] {
			return "", errors.New("private query scope denial")
		}
		ref := "evidence-" + q.scope
		q.staged[ref] = true
		return `{"evidence_refs":["` + ref + `"],"data":{"allowed":true}}`, nil
	case "get_business_intent":
		q.staged["intent-"+q.scope] = true
		return fmt.Sprintf(`{"evidence_refs":["intent-%s"],"missing":%t}`, q.scope, q.missing), nil
	}
	return "", errors.New("unregistered fixture tool")
}
func (q *fixtureQuery) ValidateFinal(text string) (json.RawMessage, error) {
	var value struct {
		Status string   `json:"status"`
		Scope  string   `json:"scope"`
		Refs   []string `json:"evidence_refs"`
	}
	if json.Unmarshal([]byte(text), &value) != nil || value.Scope != q.scope || value.Status == "" {
		return nil, errors.New("private invalid final")
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, ref := range value.Refs {
		if !q.available[ref] {
			return nil, errors.New("unknown final evidence")
		}
	}
	return json.RawMessage(text), nil
}
func final(scope string) string {
	return `{"status":"needs_confirmation","scope":"` + scope + `","evidence_refs":[]}`
}

var noSink = SinkFunc(func(context.Context, Event) error { return nil })

func task(q Query, generate func(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error)) Task {
	return Task{Kind: "audit", Queries: q, NewModel: func(context.Context) (model.BaseChatModel, error) { return &scriptedModel{generate: generate}, nil }}
}

func TestThreeIndependentRunnersReachConcurrentBarrier(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	started := make(chan string, 3)
	release := make(chan struct{})
	var factories atomic.Int32
	var wg sync.WaitGroup
	outcomes := make(chan Outcome, 3)
	failures := make(chan error, 3)
	for _, scope := range []string{"one", "two", "three"} {
		scope := scope
		q := newQuery(scope)
		wg.Add(1)
		go func() {
			defer wg.Done()
			runTask := task(q, func(ctx context.Context, input []*schema.Message, _ ...model.Option) (*schema.Message, error) {
				started <- scope
				select {
				case <-release:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
				for _, message := range input {
					if strings.Contains(message.Content, "asset-") && !strings.Contains(message.Content, "asset-"+scope) {
						return nil, errors.New("cross task context")
					}
				}
				result := answer(final(scope))
				result.ReasoningContent = "must-stay-transient-" + scope
				return result, nil
			})
			factory := runTask.NewModel
			runTask.NewModel = func(ctx context.Context) (model.BaseChatModel, error) { factories.Add(1); return factory(ctx) }
			out, err := Run(ctx, runTask, Config{}, noSink)
			outcomes <- out
			failures <- err
		}()
	}
	for i := 0; i < 3; i++ {
		select {
		case <-started:
		case <-ctx.Done():
			t.Fatal("three runner model calls did not overlap")
		}
	}
	close(release)
	wg.Wait()
	close(outcomes)
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	if factories.Load() != 3 {
		t.Fatalf("factory count %d", factories.Load())
	}
	for out := range outcomes {
		if out.ModelCalls != 1 || out.ToolCalls != 0 || out.UsageKnown {
			t.Fatalf("bad outcome %+v", out)
		}
		encoded, _ := json.Marshal(out)
		if strings.Contains(string(encoded), "transient") {
			t.Fatalf("reasoning persisted: %s", encoded)
		}
	}
}

func TestDependencyRequiresAnActualLaterModelTurn(t *testing.T) {
	q := newQuery("chain")
	var step int
	var events []Event
	out, err := Run(context.Background(), task(q, func(_ context.Context, input []*schema.Message, _ ...model.Option) (*schema.Message, error) {
		step++
		switch step {
		case 1:
			return calls(call("call_asset", "get_asset_context", `{"asset_ref":"asset-chain"}`)), nil
		case 2:
			var ref string
			for _, message := range input {
				if message.Role == schema.Assistant && len(message.ToolCalls) > 0 {
					if message.ReasoningContent != "transient-private-reasoning" || message.Extra["reasoning_details"] != "complete-transient-wire" {
						return nil, errors.New("wire reasoning continuity lost")
					}
				}
				if message.Role == schema.Tool {
					var result struct {
						Refs []string `json:"evidence_refs"`
					}
					_ = json.Unmarshal([]byte(message.Content), &result)
					if len(result.Refs) > 0 {
						ref = result.Refs[0]
					}
				}
			}
			if ref == "" {
				return nil, errors.New("missing prior result")
			}
			return calls(call("call_group", "get_security_group_rules", `{"ref":"`+ref+`"}`)), nil
		default:
			return answer(`{"status":"conditional","scope":"chain","evidence_refs":["evidence-chain"]}`), nil
		}
	}), Config{}, SinkFunc(func(_ context.Context, event Event) error { events = append(events, event); return nil }))
	if err != nil {
		t.Fatal(err)
	}
	if out.ModelCalls != 3 || out.ToolCalls != 2 || q.turns != 3 {
		t.Fatalf("calls %+v turns=%d", out, q.turns)
	}
	if len(events) != 4 || events[0].CallID != "call_asset" || events[2].CallID != "call_group" || events[3].EvidenceRefs[0] != "evidence-chain" {
		t.Fatalf("events %+v", events)
	}
	encoded, _ := json.Marshal(events)
	if strings.Contains(string(encoded), "reasoning") || strings.Contains(string(encoded), "asset_ref") || strings.Contains(string(encoded), "allowed") {
		t.Fatalf("unsafe event %s", encoded)
	}
}

func TestSameNameTwoToolCallsReturnTheirOwnIDs(t *testing.T) {
	q := newQuery("same")
	step := 0
	out, err := Run(context.Background(), task(q, func(_ context.Context, input []*schema.Message, _ ...model.Option) (*schema.Message, error) {
		step++
		if step == 1 {
			return calls(call("first_id", "get_business_intent", `{"part":"one"}`), call("second_id", "get_business_intent", `{"part":"two"}`)), nil
		}
		seen := map[string]bool{}
		for _, message := range input {
			if message.Role == schema.Tool {
				seen[message.ToolCallID] = true
			}
		}
		if len(seen) != 2 || !seen["first_id"] || !seen["second_id"] {
			return nil, fmt.Errorf("same name result IDs lost")
		}
		return answer(final("same")), nil
	}), Config{}, noSink)
	if err != nil {
		t.Fatal(err)
	}
	if out.ModelCalls != 2 || out.ToolCalls != 2 {
		t.Fatalf("outcome %+v", out)
	}
}

func TestSameBatchCannotUseANewReference(t *testing.T) {
	q := newQuery("batch")
	out, err := Run(context.Background(), task(q, func(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
		return calls(call("asset", "get_asset_context", `{}`), call("guess", "get_security_group_rules", `{"ref":"opaque-group-batch"}`)), nil
	}), Config{}, noSink)
	if err == nil || out.ErrorCode != "TOOL_QUERY_FAILED" || out.ModelCalls != 1 {
		t.Fatalf("same batch escalated %+v %v", out, err)
	}
}

func TestMissingBusinessDataStopsWithAConditionalQuestion(t *testing.T) {
	q := newQuery("missing")
	q.missing = true
	step := 0
	out, err := Run(context.Background(), task(q, func(_ context.Context, input []*schema.Message, _ ...model.Option) (*schema.Message, error) {
		step++
		if step == 1 {
			return calls(call("intent", "get_business_intent", `{}`)), nil
		}
		found := false
		for _, message := range input {
			if message.Role == schema.Tool && strings.Contains(message.Content, `"missing":true`) {
				found = true
			}
		}
		if !found {
			return nil, errors.New("missing status was not returned")
		}
		return answer(final("missing")), nil
	}), Config{}, noSink)
	if err != nil || out.ModelCalls != 2 || out.ToolCalls != 1 {
		t.Fatalf("outcome %+v %v", out, err)
	}
}

func TestProtocolRejectionStopsBeforeTools(t *testing.T) {
	oversizedArgs := `{"text":"` + strings.Repeat("界", 1500) + `"}`
	tests := []struct {
		name, code string
		response   *schema.Message
	}{
		{"unknown_tool", "UNKNOWN_TOOL", calls(call("one", "run_shell", `{}`))},
		{"duplicate_ids", "INVALID_TOOL_CALL_ID", calls(call("same", "get_business_intent", `{}`), call("same", "get_business_intent", `{}`))},
		{"illegal_id", "INVALID_TOOL_CALL_ID", calls(call("bad id", "get_business_intent", `{}`))},
		{"blank_id", "INVALID_TOOL_CALL_ID", calls(call("", "get_business_intent", `{}`))},
		{"batch_limit", "TOOL_BATCH_LIMIT", calls(call("a", "get_business_intent", `{}`), call("b", "get_business_intent", `{}`), call("c", "get_business_intent", `{}`), call("d", "get_business_intent", `{}`), call("e", "get_business_intent", `{}`))},
		{"byte_args_limit", "TOOL_ARGUMENT_LIMIT", calls(call("one", "get_business_intent", oversizedArgs))},
		{"array_args", "TOOL_ARGUMENT_INVALID", calls(call("one", "get_business_intent", `[]`))},
		{"duplicate_argument_key", "TOOL_ARGUMENT_INVALID", calls(call("one", "get_business_intent", `{"ref":"safe","ref":"other"}`))},
		{"truncated", "MODEL_OUTPUT_TRUNCATED", &schema.Message{Role: schema.Assistant, Content: final("reject"), ResponseMeta: &schema.ResponseMeta{FinishReason: "length"}}},
		{"filtered", "MODEL_CONTENT_FILTERED", &schema.Message{Role: schema.Assistant, Content: final("reject"), ResponseMeta: &schema.ResponseMeta{FinishReason: "content_filter"}}},
		{"role_tool", "MODEL_PROTOCOL_ERROR", &schema.Message{Role: schema.Tool, ToolCallID: "unknown", Content: `{}`, ResponseMeta: &schema.ResponseMeta{FinishReason: "stop"}}},
		{"unknown_finish", "INVALID_FINAL", &schema.Message{Role: schema.Assistant, Content: final("reject"), ResponseMeta: &schema.ResponseMeta{FinishReason: "unknown"}}},
		{"missing_finish", "MODEL_PROTOCOL_ERROR", &schema.Message{Role: schema.Assistant, Content: final("reject")}},
		{"final_limit", "INVALID_FINAL", answer(`{"text":"` + strings.Repeat("a", maxFinalBytes) + `"}`)},
		{"full_response_limit", "MODEL_OUTPUT_LIMIT", &schema.Message{Role: schema.Assistant, Content: final("reject"), ReasoningContent: strings.Repeat("a", maxModelBytes), ResponseMeta: &schema.ResponseMeta{FinishReason: "stop"}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			q := newQuery("reject")
			var events int
			out, err := Run(context.Background(), task(q, func(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
				return test.response, nil
			}), Config{}, SinkFunc(func(context.Context, Event) error { events++; return nil }))
			if err == nil || out.ErrorCode != test.code {
				t.Fatalf("want %s got %+v %v", test.code, out, err)
			}
			if len(q.invocations) != 0 || events != 0 {
				t.Fatalf("invalid batch executed query/events %v/%d", q.invocations, events)
			}
		})
	}
}

func TestLogicalHardLimitsCannotBeRaisedByConfig(t *testing.T) {
	for _, test := range []struct {
		kind          string
		models, tools int
	}{{"audit", 6, 12}, {"assistant_turn", 4, 8}} {
		t.Run(test.kind, func(t *testing.T) {
			q := newQuery(test.kind)
			step := 0
			runTask := task(q, func(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
				step++
				return calls(call(fmt.Sprintf("step_%d", step), "get_business_intent", `{}`)), nil
			})
			runTask.Kind = test.kind
			out, err := Run(context.Background(), runTask, Config{MaxModelCalls: 999, MaxToolCalls: 999, ModelTimeout: time.Hour}, noSink)
			if err == nil || out.ErrorCode != "MODEL_CALL_LIMIT" || out.ModelCalls != test.models || out.ToolCalls != test.models || q.turns != test.models {
				t.Fatalf("bad model bound %+v %v turns=%d", out, err, q.turns)
			}
			q = newQuery(test.kind)
			step = 0
			runTask = task(q, func(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
				step++
				batch := []schema.ToolCall{}
				for i := 0; i < 4; i++ {
					batch = append(batch, call(fmt.Sprintf("s_%d_%d", step, i), "get_business_intent", `{}`))
				}
				return calls(batch...), nil
			})
			runTask.Kind = test.kind
			out, err = Run(context.Background(), runTask, Config{}, noSink)
			if err == nil || out.ErrorCode != "TOOL_CALL_LIMIT" || out.ToolCalls != test.tools {
				t.Fatalf("bad tool bound %+v %v", out, err)
			}
		})
	}
}

func TestLowerLimitsAndModelTimeout(t *testing.T) {
	q := newQuery("short")
	out, err := Run(context.Background(), task(q, func(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
		return calls(call("one", "get_business_intent", `{}`)), nil
	}), Config{MaxModelCalls: 1}, noSink)
	if err == nil || out.ErrorCode != "MODEL_CALL_LIMIT" || out.ModelCalls != 1 {
		t.Fatalf("outcome %+v %v", out, err)
	}
	q = newQuery("timeout")
	start := time.Now()
	out, err = Run(context.Background(), task(q, func(ctx context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}), Config{ModelTimeout: 10 * time.Millisecond}, noSink)
	if err == nil || out.ErrorCode != "MODEL_TIMEOUT" || time.Since(start) > time.Second {
		t.Fatalf("timeout %+v %v", out, err)
	}
}

func TestModelInputByteLimitPreventsTheUnderlyingCall(t *testing.T) {
	q := newQuery(strings.Repeat("界", 45000))
	called := false
	out, err := Run(context.Background(), task(q, func(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
		called = true
		return answer(`{}`), nil
	}), Config{}, noSink)
	if err == nil || out.ErrorCode != "MODEL_INPUT_LIMIT" || called || out.ModelCalls != 0 {
		t.Fatalf("input bound %+v %v called=%t", out, err, called)
	}
}

func TestSinkFailureStopsSubsequentToolsAndModelCalls(t *testing.T) {
	for _, failAt := range []int{1, 2} {
		t.Run(fmt.Sprint(failAt), func(t *testing.T) {
			q := newQuery("sink")
			models, events := 0, 0
			out, err := Run(context.Background(), task(q, func(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
				models++
				return calls(call("one", "get_business_intent", `{}`), call("two", "get_business_intent", `{}`)), nil
			}), Config{}, SinkFunc(func(context.Context, Event) error {
				events++
				if events == failAt {
					return errors.New("raw secret storage failure")
				}
				return nil
			}))
			if err == nil || out.ErrorCode != "EVENT_SAVE_FAILED" || models != 1 || events != failAt || len(q.invocations) != failAt-1 {
				t.Fatalf("save did not stop %+v %v models=%d events=%d invoked=%v", out, err, models, events, q.invocations)
			}
			if strings.Contains(err.Error(), "secret") {
				t.Fatal("raw error leaked")
			}
		})
	}
}

func TestToolReplyBoundAndInvalidRefs(t *testing.T) {
	for _, test := range []struct{ name, value, code, status string }{
		{"large", `{"evidence_refs":[],"data":"` + strings.Repeat("a", maxToolBytes) + `"}`, "TOOL_RESULT_LIMIT", "truncated"},
		{"missing_refs", `{"data":true}`, "TOOL_RESULT_INVALID", "rejected"},
		{"bad_refs", `{"evidence_refs":["ok","ok"]}`, "TOOL_RESULT_INVALID", "rejected"},
		{"array", `[]`, "TOOL_RESULT_INVALID", "rejected"},
	} {
		t.Run(test.name, func(t *testing.T) {
			q := newQuery("result")
			q.invoke = func(context.Context, string, string) (string, error) { return test.value, nil }
			var events []Event
			out, err := Run(context.Background(), task(q, func(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
				return calls(call("one", "get_business_intent", `{}`)), nil
			}), Config{}, SinkFunc(func(_ context.Context, event Event) error { events = append(events, event); return nil }))
			if err == nil || out.ErrorCode != test.code || len(events) != 2 || events[1].Status != test.status || len(events[1].EvidenceRefs) != 0 {
				t.Fatalf("reply %+v %v events=%+v", out, err, events)
			}
		})
	}
}

func TestSensitiveValuesNeverReachOutcomeEventsOrNextTurn(t *testing.T) {
	const secret = "private-fixture-credential"
	for _, where := range []string{"initial", "history", "model", "tool", "call_id"} {
		t.Run(where, func(t *testing.T) {
			q := newQuery("sensitive")
			if where == "initial" {
				q.scope = secret
			}
			q.invoke = func(context.Context, string, string) (string, error) {
				return `{"evidence_refs":["safe"],"data":"` + secret + `"}`, nil
			}
			runTask := task(q, func(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
				if where == "model" {
					result := answer(final("sensitive"))
					result.ReasoningContent = secret
					return result, nil
				}
				id := "one"
				if where == "call_id" {
					id = secret
				}
				return calls(call(id, "get_business_intent", `{}`)), nil
			})
			if where == "history" {
				runTask.History = []HistoryMessage{{Role: "user", Text: secret}}
			}
			var events []Event
			out, err := Run(context.Background(), runTask, Config{SensitiveValues: []string{secret}}, SinkFunc(func(_ context.Context, event Event) error { events = append(events, event); return nil }))
			if err == nil {
				t.Fatal("sensitive value accepted")
			}
			data, _ := json.Marshal(struct {
				Outcome Outcome
				Events  []Event
				Error   string
			}{out, events, err.Error()})
			if strings.Contains(string(data), secret) {
				t.Fatalf("sensitive persisted %s", data)
			}
			if out.ModelCalls > 1 {
				t.Fatalf("continued after sensitive output %+v", out)
			}
		})
	}
}

func TestPublicHistoryCannotReplayPrivilegedOrToolMessages(t *testing.T) {
	for _, role := range []string{"tool", "system", "developer", "reasoning"} {
		t.Run(role, func(t *testing.T) {
			q := newQuery("history")
			runTask := task(q, func(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
				t.Fatal("model called for invalid history")
				return nil, nil
			})
			runTask.History = []HistoryMessage{{Role: role, Text: "old wire value"}}
			out, err := Run(context.Background(), runTask, Config{}, noSink)
			if err == nil || out.ErrorCode != "INVALID_HISTORY" {
				t.Fatalf("history accepted %+v %v", out, err)
			}
		})
	}
}

func TestFinalRequiresQueryValidationWithNoTextFallback(t *testing.T) {
	q := newQuery("final")
	out, err := Run(context.Background(), task(q, func(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
		return answer("plain answer"), nil
	}), Config{}, noSink)
	if err == nil || out.ErrorCode != "FINAL_VALIDATION_FAILED" || len(out.Output) != 0 {
		t.Fatalf("text fallback %+v %v", out, err)
	}
}

func TestCancellationStopsOnlyItsRunner(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	finished := make(chan Outcome, 1)
	go func() {
		out, _ := Run(ctx, task(newQuery("cancel"), func(ctx context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
			close(started)
			<-ctx.Done()
			return nil, ctx.Err()
		}), Config{}, noSink)
		finished <- out
	}()
	<-started
	cancel()
	select {
	case out := <-finished:
		if out.ErrorCode != "CANCELLED" {
			t.Fatalf("cancel outcome %+v", out)
		}
	case <-time.After(time.Second):
		t.Fatal("cancel did not stop")
	}
	out, err := Run(context.Background(), task(newQuery("independent"), func(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
		return answer(final("independent")), nil
	}), Config{}, noSink)
	if err != nil || len(out.Output) == 0 {
		t.Fatalf("other runner failed %+v %v", out, err)
	}
}

func TestTranscriptRejectsExtraMissingDuplicateAndWrongToolResults(t *testing.T) {
	assistant := calls(call("one", "get_business_intent", `{}`))
	result := `{"evidence_refs":[]}`
	for _, test := range []struct {
		name     string
		messages []*schema.Message
	}{
		{"missing", []*schema.Message{assistant}},
		{"extra", []*schema.Message{assistant, {Role: schema.Tool, ToolCallID: "one", Content: result}, {Role: schema.Tool, ToolCallID: "unknown", Content: result}}},
		{"duplicate", []*schema.Message{assistant, {Role: schema.Tool, ToolCallID: "one", Content: result}, {Role: schema.Tool, ToolCallID: "one", Content: result}}},
		{"wrong_value", []*schema.Message{assistant, {Role: schema.Tool, ToolCallID: "one", Content: `{"evidence_refs":["other"]}`}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := &state{calls: map[string]*callRecord{"one": {name: "get_business_intent", args: `{}`, assistant: assistantSignature(assistant), result: result, completed: true}}}
			if code := s.validateTranscript(test.messages); code == "" {
				t.Fatal("bad transcript accepted")
			}
		})
	}
	copy := *assistant
	copy.ReasoningContent = "tampered"
	s := &state{calls: map[string]*callRecord{"one": {name: "get_business_intent", args: `{}`, assistant: assistantSignature(assistant), result: result, completed: true}}}
	if s.validateTranscript([]*schema.Message{&copy, {Role: schema.Tool, ToolCallID: "one", Content: result}}) != "MODEL_HISTORY_INVALID" {
		t.Fatal("changed transient wire accepted")
	}
}

type finalQuery struct {
	Query
	validate func(string) (json.RawMessage, error)
}

func (q finalQuery) ValidateFinal(text string) (json.RawMessage, error) { return q.validate(text) }
func TestCancellationDuringFinalValidationCannotPublishAnOutput(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	q := finalQuery{Query: newQuery("late"), validate: func(text string) (json.RawMessage, error) { cancel(); return json.RawMessage(text), nil }}
	out, err := Run(ctx, task(q, func(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
		return answer(final("late")), nil
	}), Config{}, noSink)
	if err == nil || out.ErrorCode != "CANCELLED" || len(out.Output) != 0 {
		t.Fatalf("cancelled final published %+v %v", out, err)
	}
}
func TestDefaultTimeoutIsThirtySecondsAndSDKUsageStaysUnknown(t *testing.T) {
	out, err := Run(context.Background(), task(newQuery("usage"), func(ctx context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 30*time.Second {
			return nil, errors.New("no bounded model deadline")
		}
		result := answer(final("usage"))
		result.ResponseMeta.Usage = &schema.TokenUsage{PromptTokens: 0, CompletionTokens: 0, TotalTokens: 0}
		return result, nil
	}), Config{ModelTimeout: time.Hour}, noSink)
	if err != nil || out.UsageKnown {
		t.Fatalf("usage/deadline %+v %v", out, err)
	}
	encoded, _ := json.Marshal(out)
	if strings.Contains(string(encoded), "prompt_tokens") || strings.Contains(string(encoded), "total_tokens") {
		t.Fatalf("SDK usage persisted %s", encoded)
	}
}
func TestNormalizedFinalIsBoundedAndSensitiveChecked(t *testing.T) {
	for _, test := range []struct {
		name       string
		normalized json.RawMessage
	}{{"expanded", json.RawMessage(`{"value":"` + strings.Repeat("a", maxFinalBytes) + `"}`)}, {"private", json.RawMessage(`{"value":"private-final-fixture"}`)}, {"ambiguous", json.RawMessage(`{"status":"a","status":"b"}`)}} {
		t.Run(test.name, func(t *testing.T) {
			q := finalQuery{Query: newQuery("normalize"), validate: func(string) (json.RawMessage, error) { return test.normalized, nil }}
			out, err := Run(context.Background(), task(q, func(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
				return answer(final("normalize")), nil
			}), Config{SensitiveValues: []string{"private-final-fixture"}}, noSink)
			if err == nil || len(out.Output) != 0 {
				t.Fatalf("invalid normalized output %+v %v", out, err)
			}
		})
	}
}
func TestReusedIDAcrossLaterTurnsIsRejected(t *testing.T) {
	q := newQuery("reuse")
	out, err := Run(context.Background(), task(q, func(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
		return calls(call("same_id", "get_business_intent", `{}`)), nil
	}), Config{}, noSink)
	if err == nil || out.ErrorCode != "INVALID_TOOL_CALL_ID" || out.ModelCalls != 2 || out.ToolCalls != 1 {
		t.Fatalf("reused ID accepted %+v %v", out, err)
	}
}
func TestControlledAdapterErrorSurvivesWithoutRawError(t *testing.T) {
	out, err := Run(context.Background(), task(newQuery("adapter"), func(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
		return nil, Controlled("MODEL_HTTP_LIMIT")
	}), Config{}, noSink)
	if err == nil || out.ErrorCode != "MODEL_HTTP_LIMIT" || err.Error() != "MODEL_HTTP_LIMIT" {
		t.Fatalf("controlled code lost %+v %v", out, err)
	}
}

func TestProjectedTaskDataCannotBecomeASystemInstruction(t *testing.T) {
	q := newQuery("data-role")
	out, err := Run(context.Background(), task(q, func(_ context.Context, input []*schema.Message, _ ...model.Option) (*schema.Message, error) {
		found := false
		for _, message := range input {
			if strings.Contains(message.Content, "asset-data-role") {
				if message.Role == schema.System {
					return nil, errors.New("external task facts entered system instruction")
				}
				found = true
			}
		}
		if !found {
			return nil, errors.New("projected task context missing")
		}
		return answer(final("data-role")), nil
	}), Config{}, noSink)
	if err != nil || len(out.Output) == 0 {
		t.Fatalf("task data role %+v %v", out, err)
	}
}
func TestQueryPanicStillFinishesItsSafeToolEvent(t *testing.T) {
	q := newQuery("panic")
	q.invoke = func(context.Context, string, string) (string, error) { panic("raw private fixture panic") }
	var events []Event
	out, err := Run(context.Background(), task(q, func(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
		return calls(call("one", "get_business_intent", `{}`)), nil
	}), Config{}, SinkFunc(func(_ context.Context, event Event) error { events = append(events, event); return nil }))
	if err == nil || out.ErrorCode != "TOOL_QUERY_FAILED" || len(events) != 2 || events[1].Type != "tool_finished" || events[1].Status != "failed" {
		t.Fatalf("panic event %+v %v events=%+v", out, err, events)
	}
}
func TestPersistencePanicIsAControlledSaveFailure(t *testing.T) {
	q := newQuery("save-panic")
	out, err := Run(context.Background(), task(q, func(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
		return calls(call("one", "get_business_intent", `{}`)), nil
	}), Config{}, SinkFunc(func(context.Context, Event) error { panic("raw private save panic") }))
	if err == nil || out.ErrorCode != "EVENT_SAVE_FAILED" || len(q.invocations) != 0 {
		t.Fatalf("panic save %+v %v", out, err)
	}
}

func TestCancellationDuringToolPreservesCancellationCode(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	q := newQuery("cancel-tool")
	started := make(chan struct{})
	q.invoke = func(ctx context.Context, _, _ string) (string, error) {
		close(started)
		<-ctx.Done()
		return "", ctx.Err()
	}
	done := make(chan Outcome, 1)
	go func() {
		out, _ := Run(ctx, task(q, func(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
			return calls(call("one", "get_business_intent", `{}`)), nil
		}), Config{}, noSink)
		done <- out
	}()
	<-started
	cancel()
	select {
	case out := <-done:
		if out.ErrorCode != "CANCELLED" {
			t.Fatalf("tool cancel code %+v", out)
		}
	case <-time.After(time.Second):
		t.Fatal("tool cancel did not finish")
	}
}

type beginQuery struct {
	Query
	begin func()
}

func (q beginQuery) BeginTurn() { q.begin() }
func TestCancellationAtTurnBoundaryDoesNotCallModel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	q := beginQuery{Query: newQuery("begin-cancel"), begin: cancel}
	called := false
	out, err := Run(ctx, task(q, func(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
		called = true
		return answer(final("begin-cancel")), nil
	}), Config{}, noSink)
	if err == nil || out.ErrorCode != "CANCELLED" || called || out.ModelCalls != 0 {
		t.Fatalf("model started after turn cancellation %+v %v called=%t", out, err, called)
	}
}
func TestCancellationAfterStartedCommitDoesNotInvokeTool(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	q := newQuery("start-cancel")
	out, err := Run(ctx, task(q, func(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
		return calls(call("one", "get_business_intent", `{}`)), nil
	}), Config{}, SinkFunc(func(_ context.Context, event Event) error {
		if event.Type == "tool_started" {
			cancel()
		}
		return nil
	}))
	if err == nil || out.ErrorCode != "CANCELLED" || len(q.invocations) != 0 {
		t.Fatalf("tool invoked after started cancellation %+v %v queries=%d", out, err, len(q.invocations))
	}
}
