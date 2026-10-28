package report

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestExecutionProjectionUsesControlledStageMetadata(t *testing.T) {
	result := privateResult()
	for _, format := range []string{"json", "markdown"} {
		body, err := BuildWithExecution(context.Background(), result, nil, format, Execution{Mode: "rules_and_agent", ModelStage: "not_configured"})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(body), "PRIVATE_") {
			t.Fatal("execution metadata bypassed public projection")
		}
		if format == "json" {
			var doc Document
			if json.Unmarshal(body, &doc) != nil || doc.Execution == nil || doc.Execution.ModelStage != "not_configured" {
				t.Fatal("missing stage gap")
			}
		} else if !strings.Contains(string(body), "模型未配置") {
			t.Fatal("markdown omitted stage gap")
		}
	}
	for _, bad := range []Execution{{Mode: "PRIVATE_MODE", ModelStage: "not_configured"}, {Mode: "rules_only", ModelStage: "completed"}, {Mode: "rules_and_agent", ModelStage: "PRIVATE_ERROR"}} {
		if _, err := BuildWithExecution(context.Background(), result, nil, "json", bad); !errors.Is(err, ErrInvalidResult) {
			t.Fatal("free execution metadata entered public report")
		}
	}
}
func TestExecutionSeparatesSuccessfulModelStageFromInterruptedJob(t *testing.T) {
	for _, format := range []string{"json", "markdown"} {
		in, result, supp, _ := supplementCase(t)
		body, err := BuildWithAgent(context.Background(), in, result, nil, format, Execution{Mode: "rules_and_agent", ModelStage: "completed", JobStatus: "interrupted"}, supp)
		if err != nil {
			t.Fatal(err)
		}
		if format == "json" {
			var doc Document
			if json.Unmarshal(body, &doc) != nil || doc.Execution == nil || doc.Execution.ModelStage != "completed" || doc.Execution.JobStatus != "interrupted" || doc.Agent == nil || doc.Agent.ModelStage != "completed" {
				t.Fatal("L3 and job terminal state merged")
			}
		} else {
			if !strings.Contains(string(body), "模型阶段流程已结束") || !strings.Contains(string(body), "整体任务中断") {
				t.Fatal("markdown hid independent stage/job outcome")
			}
		}
	}
	for _, status := range []string{"queued", "running", "cancelling", "PRIVATE_JOB_STATUS"} {
		body, err := BuildWithExecution(context.Background(), privateResult(), nil, "json", Execution{Mode: "rules_and_agent", ModelStage: "completed", JobStatus: status})
		if body != nil || !errors.Is(err, ErrInvalidResult) {
			t.Fatal("nonterminal/free job status entered report", err)
		}
	}
}
