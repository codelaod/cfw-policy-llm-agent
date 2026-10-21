package main

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestScriptedPathsAndRoundtrips(t *testing.T) {
	for _, tc := range []struct {
		name, status          string
		modelCalls, toolCalls int
	}{
		{"whitelist", "conditional_recommendation", 3, 2},
		{"missing-whitelist", "needs_confirmation", 2, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts := defaults()
			opts.Scenario = tc.name
			r, err := runProbe(context.Background(), opts)
			if err != nil {
				t.Fatal(err)
			}
			if r.Mode != "scripted_model" || r.Status != "completed" || r.Decision.Status != tc.status {
				t.Fatalf("unexpected report: %+v", r)
			}
			if r.ModelCalls != tc.modelCalls || r.ToolCalls != tc.toolCalls || r.ToolResultChecks != tc.toolCalls {
				t.Fatalf("incorrect loop or result roundtrips: %+v", r)
			}
			for i, event := range r.Events {
				if event.Sequence != i+1 {
					t.Fatal("event sequence is not contiguous")
				}
			}
			if tc.name == "missing-whitelist" && len(r.Decision.AdminSources) != 0 {
				t.Fatal("invented whitelist")
			}
		})
	}
}

func TestInjectedFailuresAreNotReportedAsSuccess(t *testing.T) {
	for _, fault := range []string{"unknown_tool", "bad_json", "unknown_asset", "unknown_field", "invented_evidence", "invented_sources", "missing_conditions", "bad_output"} {
		t.Run(fault, func(t *testing.T) {
			opts := defaults()
			opts.fault = fault
			r, err := runProbe(context.Background(), opts)
			if err == nil || r.Status != "failed" || r.Error == "" || r.Decision != nil {
				t.Fatalf("failure was accepted: %+v, %v", r, err)
			}
			markers := map[string]string{
				"unknown_tool": "delete_cloud_policy", "bad_json": "invalid tool arguments",
				"unknown_asset": "outside current task scope", "unknown_field": "invalid tool arguments",
				"invented_evidence": "evidence was not returned", "invented_sources": "recommended source differs",
				"missing_conditions": "conditions and evidence", "bad_output": "invalid final JSON",
			}
			if !strings.Contains(r.Error, markers[fault]) {
				t.Fatalf("failed for the wrong reason: %s", r.Error)
			}
			if fault == "unknown_tool" && r.ToolCalls != 0 {
				t.Fatal("unregistered tool was executed")
			}
		})
	}
}

func TestBudgets(t *testing.T) {
	cases := []struct {
		name   string
		change func(*Options)
	}{
		{"framework_iterations", func(o *Options) { o.fault = "loop"; o.MaxIterations = 1 }},
		{"model_calls", func(o *Options) { o.MaxModelCalls = 1 }},
		{"tool_calls", func(o *Options) { o.MaxToolCalls = 1 }},
		{"tool_output_bytes", func(o *Options) { o.MaxToolBytes = 1 }},
		{"final_output_bytes", func(o *Options) { o.MaxOutputBytes = 1 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := defaults()
			tc.change(&opts)
			r, err := runProbe(context.Background(), opts)
			if err == nil || r.Status != "failed" {
				t.Fatalf("limit was not enforced: %+v", r)
			}
			if tc.name == "framework_iterations" && r.ModelCalls != 1 {
				t.Fatalf("framework exceeded configured iteration limit: %+v", r)
			}
		})
	}
}

func TestToolFailurePreservesEvents(t *testing.T) {
	opts := defaults()
	opts.failTool = "get_asset"
	r, err := runProbe(context.Background(), opts)
	if err == nil || r.Status != "failed" {
		t.Fatal("tool failure accepted")
	}
	found := false
	for _, e := range r.Events {
		if e.Kind == "tool_failed" {
			found = true
		}
	}
	if !found {
		t.Fatal("tool error event was lost")
	}
}

func TestCancellationAndTimeout(t *testing.T) {
	t.Run("canceled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		r, err := runProbe(ctx, defaults())
		if err == nil || r.Status != "failed" {
			t.Fatal("cancellation was ignored")
		}
	})
	t.Run("deadline", func(t *testing.T) {
		opts := defaults()
		opts.waitForCancel = true
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
		defer cancel()
		r, err := runProbe(ctx, opts)
		if err == nil || r.Status != "failed" {
			t.Fatal("deadline was ignored")
		}
	})
}

func TestStrictJSON(t *testing.T) {
	for _, text := range []string{`{"asset_id":"demo-web-001"} {}`, `{"asset_id":"demo-web-001","extra":true}`, "null trailing"} {
		var request lookupRequest
		if strictJSON(text, &request) == nil {
			t.Fatalf("accepted malformed or extra data: %s", text)
		}
	}
}

func TestInvalidConfiguration(t *testing.T) {
	opts := defaults()
	opts.Scenario = "real-cloud"
	if _, err := runProbe(context.Background(), opts); err == nil {
		t.Fatal("accepted non-fixture scenario")
	}
	opts = defaults()
	opts.MaxModelCalls = 0
	if _, err := runProbe(context.Background(), opts); err == nil {
		t.Fatal("accepted unlimited model calls")
	}
}

func TestMissingIntentRejectsInventedSources(t *testing.T) {
	opts := defaults()
	opts.Scenario = "missing-whitelist"
	opts.fault = "invented_sources"
	r, err := runProbe(context.Background(), opts)
	if err == nil || !strings.Contains(r.Error, "missing whitelist") {
		t.Fatal("missing intent accepted invented sources")
	}
}
