package evaluation

import (
	"context"
	"testing"
)

func TestIndependentCompletedReplayDoesNotRequireModelFactory(t *testing.T) {
	f := newEvaluationFixture(t, "success", nil)
	a := f.accepted(t, FixedModel, "independent-offline-replay")
	first, err := f.runner.Run(context.Background(), f.protocol, f.caseData, a)
	if err != nil || first.Record.Status != "completed" {
		t.Fatal(err, first.Record)
	}
	before := f.requests.Load()
	f.runner.binding.Factory = nil
	replayed, err := f.runner.Run(context.Background(), f.protocol, f.caseData, a)
	if err != nil || !replayed.Record.Replayed || replayed.Record.ReportRef != first.Record.ReportRef || f.requests.Load() != before {
		t.Fatalf("completed local metadata replay still depends on enabled model runtime: err=%v replayed=%v old_report=%s new_report=%s HTTP=%d", err, replayed.Record.Replayed, first.Record.ReportRef, replayed.Record.ReportRef, f.requests.Load())
	}
}
