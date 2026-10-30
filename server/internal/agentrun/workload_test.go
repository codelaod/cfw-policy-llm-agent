package agentrun

import (
	"cfw-policy-llm-agent/server/internal/workload"
	"context"
	"fmt"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"testing"
	"time"
)

func TestExplicitWorkloadSeparatesLogicalCallsFromPhysicalAttemptCeiling(t *testing.T) {
	limits := workload.Default("audit")
	limits.ModelLogicalCalls = 8
	limits.ModelHTTPAttempts = 1
	limits.ToolCalls = 20
	limits.ToolBatch = 5
	limits.ModelRequestSeconds = 60
	step := 0
	out, err := Run(context.Background(), task(newQuery("explicit"), func(ctx context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
		step++
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) < 31*time.Second {
			t.Fatal("explicit response window was hidden behind thirty seconds")
		}
		if step <= 6 {
			return calls(call(fmt.Sprintf("explicit_%d", step), "get_business_intent", `{}`)), nil
		}
		return answer(final("explicit")), nil
	}), Config{Workload: workload.Resolved{ProfileRef: "explicit-model", Limits: limits}}, noSink)
	if err != nil || out.ModelCalls != 7 || out.ToolCalls != 6 {
		t.Fatalf("logical calls inherited physical or legacy cap: %+v %v", out, err)
	}
}
func TestExplicitToolBatchAndZeroModelCeilingAreEnforced(t *testing.T) {
	limits := workload.Default("audit")
	limits.ToolBatch = 5
	step := 0
	out, err := Run(context.Background(), task(newQuery("batch"), func(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
		step++
		if step == 1 {
			items := []schema.ToolCall{}
			for n := 0; n < 5; n++ {
				items = append(items, call(fmt.Sprintf("batch_%d", n), "get_business_intent", `{}`))
			}
			return calls(items...), nil
		}
		return answer(final("batch")), nil
	}), Config{Workload: workload.Resolved{ProfileRef: "explicit-batch", Limits: limits}}, noSink)
	if err != nil || out.ToolCalls != 5 {
		t.Fatalf("explicit batch still limited to four: %+v %v", out, err)
	}
	limits.ModelHTTPAttempts = 0
	called := false
	_, err = Run(context.Background(), task(newQuery("zero"), func(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
		called = true
		return answer(final("zero")), nil
	}), Config{Workload: workload.Resolved{ProfileRef: "explicit-zero", Limits: limits}}, noSink)
	if err == nil || called {
		t.Fatal("explicit zero acquired a default model request")
	}
}
