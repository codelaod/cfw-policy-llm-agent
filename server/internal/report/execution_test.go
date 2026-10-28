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
