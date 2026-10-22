package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type wireMessage struct {
	Role             string           `json:"role"`
	Content          any              `json:"content"`
	ReasoningContent string           `json:"reasoning_content,omitempty"`
	ToolCalls        []map[string]any `json:"tool_calls,omitempty"`
	ToolCallID       string           `json:"tool_call_id,omitempty"`
}

type wireRequest struct {
	Messages            []wireMessage    `json:"messages"`
	Tools               []map[string]any `json:"tools"`
	MaxCompletionTokens int              `json:"max_completion_tokens"`
	ReasoningSplit      bool             `json:"reasoning_split"`
}

func liveTestConfig(t *testing.T, base string) modelConfig {
	t.Helper()
	values := configValues()
	values["CFW_MODEL_BASE_URL"] = base
	cfg, err := loadModelConfig("", lookupValues(values))
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func writeModelResponse(w http.ResponseWriter, message map[string]any, finish string, usage bool) {
	response := map[string]any{"id": "fixture-response", "object": "chat.completion", "created": 1, "model": "fixture-model",
		"choices": []any{map[string]any{"index": 0, "message": message, "finish_reason": finish}}}
	if usage {
		response["usage"] = map[string]int{"prompt_tokens": 10, "completion_tokens": 2, "total_tokens": 12}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(response)
}

func fixtureToolMessage(id, name string) map[string]any {
	return map[string]any{"role": "assistant", "content": "", "reasoning_content": "private-fixture-reasoning",
		"tool_calls": []any{map[string]any{"id": id, "type": "function", "function": map[string]any{"name": name, "arguments": `{"asset_id":"demo-web-001"}`}}}}
}

func TestLiveModelComponentMultiRoundtripAndReasoningPrivacy(t *testing.T) {
	var calls atomic.Int64
	var reasoningPreserved atomic.Bool
	var firstToolResult atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if r.URL.Path != "/custom/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer "+fixtureKey {
			t.Error("wrong SDK endpoint/auth")
		}
		var request wireRequest
		if json.NewDecoder(r.Body).Decode(&request) != nil {
			t.Error("bad request JSON")
		}
		if request.MaxCompletionTokens != 512 || !request.ReasoningSplit || len(request.Tools) != 2 {
			t.Error("budget/provider options or tools not forwarded")
		}
		for _, m := range request.Messages {
			if m.ReasoningContent == "private-fixture-reasoning" {
				reasoningPreserved.Store(true)
			}
			if m.Role == "tool" && m.ToolCallID == "call-asset" {
				firstToolResult.Store(true)
			}
		}
		switch n {
		case 1:
			writeModelResponse(w, fixtureToolMessage("call-asset", "get_asset"), "tool_calls", true)
		case 2:
			writeModelResponse(w, fixtureToolMessage("call-intent", "get_business_intent"), "tool_calls", true)
		default:
			d := Decision{Status: "conditional_recommendation", AssetID: assetID, EvidenceIDs: []string{"ev-asset", "ev-intent"}, AdminSources: []string{"198.51.100.10/32"}, Conditions: []string{"Check rule order and default behavior; not proof of reachability."}}
			body, _ := json.Marshal(d)
			writeModelResponse(w, map[string]any{"role": "assistant", "content": string(body), "reasoning_content": "private-fixture-reasoning"}, "stop", true)
		}
	}))
	defer server.Close()
	cfg := liveTestConfig(t, server.URL+"/custom/v1")
	report, err := runLiveProbe(context.Background(), cfg, "whitelist", "minimax")
	if err != nil {
		t.Fatal(err)
	}
	if report.Mode != "live_model" || report.Status != "completed" || report.NetworkRequests != 3 || report.ToolResultChecks != 2 || report.ToolCalls != 2 {
		t.Fatalf("incorrect integration counts: %+v", report)
	}
	if !reasoningPreserved.Load() || !firstToolResult.Load() {
		t.Fatal("SDK did not preserve reasoning or tool-call ID history")
	}
	if report.TokenUsage == nil || report.TokenUsage.TotalTokens != 36 || report.TokenUsage.ResponsesWithUsage != 3 {
		t.Fatal("actual provider usage was not accumulated")
	}
	encoded, _ := json.Marshal(report)
	for _, private := range []string{fixtureKey, server.URL, "fixture-model", "private-fixture-reasoning"} {
		if strings.Contains(string(encoded), private) {
			t.Fatal("report disclosed private data")
		}
	}
}

func TestLiveParallelToolIDsAndMissingUsage(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			message := fixtureToolMessage("parallel-asset", "get_asset")
			other := fixtureToolMessage("parallel-intent", "get_business_intent")
			message["tool_calls"] = append(message["tool_calls"].([]any), other["tool_calls"].([]any)...)
			writeModelResponse(w, message, "tool_calls", false)
			return
		}
		d := Decision{Status: "conditional_recommendation", AssetID: assetID, EvidenceIDs: []string{"ev-asset", "ev-intent"}, AdminSources: []string{"198.51.100.10/32"}, Conditions: []string{"scope remains conditional"}}
		b, _ := json.Marshal(d)
		writeModelResponse(w, map[string]any{"role": "assistant", "content": string(b)}, "stop", false)
	}))
	defer server.Close()
	report, err := runLiveProbe(context.Background(), liveTestConfig(t, server.URL+"/v1"), "whitelist", "openai-compatible")
	if err != nil || report.ToolResultChecks != 2 || report.TokenUsage != nil {
		t.Fatalf("parallel IDs or unavailable usage mishandled: %v, %+v", err, report)
	}
	ids := map[string]bool{}
	for _, event := range report.Events {
		if event.Kind == "tool_started" {
			ids[event.CallID] = true
		}
	}
	if !ids["parallel-asset"] || !ids["parallel-intent"] {
		t.Fatal("tool events did not use Eino's actual context IDs")
	}
}

func TestLiveTruncationStopsWithoutRetry(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		writeModelResponse(w, map[string]any{"role": "assistant", "content": "", "reasoning_content": "do-not-persist"}, "length", true)
	}))
	defer server.Close()
	report, err := runLiveProbe(context.Background(), liveTestConfig(t, server.URL+"/v1"), "whitelist", "minimax")
	if err == nil || report.ErrorCode != "output_truncated_by_limit" || calls.Load() != 1 || report.TokenUsage == nil {
		t.Fatalf("truncation retried, accepted or usage lost: %+v", report)
	}
}

func TestLiveHTTPFailuresAreRedactedAndNotRetried(t *testing.T) {
	for _, status := range []int{302, 400, 401, 403, 404, 429, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var calls atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Location", "https://must-not-follow.invalid")
				w.WriteHeader(status)
				fmt.Fprint(w, fixtureKey+" raw provider details")
			}))
			defer server.Close()
			report, err := runLiveProbe(context.Background(), liveTestConfig(t, server.URL+"/v1"), "whitelist", "minimax")
			if err == nil || report.Status != "failed" || calls.Load() != 1 || report.HTTPStatus != status {
				t.Fatal("HTTP failure was accepted, followed or retried")
			}
			b, _ := json.Marshal(report)
			if strings.Contains(string(b), fixtureKey) || strings.Contains(string(b), server.URL) || strings.Contains(err.Error(), fixtureKey) {
				t.Fatal("provider error leaked private data")
			}
		})
	}
}

func TestLiveRequestBudgetAndProtocolFailures(t *testing.T) {
	cases := []struct {
		name     string
		message  func(int64) map[string]any
		expected string
		budget   int
	}{
		{"request_budget", func(n int64) map[string]any { return fixtureToolMessage(fmt.Sprintf("call-%d", n), "get_asset") }, "iteration_budget_exhausted", 1},
		{"unknown_tool", func(int64) map[string]any { return fixtureToolMessage("call-bad", "delete_cloud_policy") }, "unregistered_tool_requested", 4},
		{"bad_call_id", func(int64) map[string]any { return fixtureToolMessage("has spaces", "get_asset") }, "invalid_or_reused_tool_call_id", 4},
		{"malformed_final", func(int64) map[string]any { return map[string]any{"role": "assistant", "content": "not JSON"} }, "output_or_tool_validation_failed", 4},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := calls.Add(1)
				message := tc.message(n)
				finish := "tool_calls"
				if message["tool_calls"] == nil {
					finish = "stop"
				}
				writeModelResponse(w, message, finish, true)
			}))
			defer server.Close()
			cfg := liveTestConfig(t, server.URL+"/v1")
			cfg.maxCalls = tc.budget
			report, err := runLiveProbe(context.Background(), cfg, "whitelist", "minimax")
			if err == nil || report.Status != "failed" || calls.Load() > int64(tc.budget) {
				t.Fatal("error accepted or HTTP budget exceeded")
			}
			if tc.expected != "" && report.ErrorCode != tc.expected {
				t.Fatalf("wrong error category: %s", report.ErrorCode)
			}
		})
	}
}

func TestLiveBodyLimitAndProviderApplicationError(t *testing.T) {
	for _, mode := range []string{"oversize", "application_error"} {
		t.Run(mode, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if mode == "oversize" {
					fmt.Fprint(w, strings.Repeat("x", maxProviderBody+1))
				} else {
					fmt.Fprint(w, `{"base_resp":{"status_code":1004,"status_msg":"`+fixtureKey+`"}}`)
				}
			}))
			defer server.Close()
			report, err := runLiveProbe(context.Background(), liveTestConfig(t, server.URL+"/v1"), "whitelist", "minimax")
			if err == nil || report.NetworkRequests != 1 || strings.Contains(report.Error, fixtureKey) {
				t.Fatal("unsafe provider body accepted or leaked")
			}
		})
	}
}

func TestLiveMissingIntentAndFinalReflectionRedaction(t *testing.T) {
	var calls atomic.Int64
	var base string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			writeModelResponse(w, fixtureToolMessage("call-intent", "get_business_intent"), "tool_calls", true)
			return
		}
		d := Decision{Status: "needs_confirmation", AssetID: assetID, EvidenceIDs: []string{"ev-intent"}, AdminSources: []string{}, Conditions: []string{"Need whitelist. " + fixtureKey + " http://" + r.Host + "/v1 origin=" + r.Host + " fixture-model"}}
		b, _ := json.Marshal(d)
		writeModelResponse(w, map[string]any{"role": "assistant", "content": string(b)}, "stop", true)
	}))
	defer server.Close()
	base = server.URL + "/v1"
	report, err := runLiveProbe(context.Background(), liveTestConfig(t, base), "missing-whitelist", "minimax")
	if err != nil || report.Decision.Status != "needs_confirmation" || report.ToolResultChecks != 1 {
		t.Fatal("missing intent integration failed")
	}
	b, _ := json.Marshal(report)
	if strings.Contains(string(b), fixtureKey) || strings.Contains(string(b), base) || strings.Contains(string(b), "fixture-model") || strings.Contains(string(b), "127.0.0.1") {
		t.Fatal("reflected private configuration leaked")
	}
}

func TestLiveTimeoutAndUnsupportedConfiguration(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(200 * time.Millisecond):
		}
	}))
	defer server.Close()
	cfg := liveTestConfig(t, server.URL+"/v1")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	report, err := runLiveProbe(ctx, cfg, "whitelist", "minimax")
	if err == nil || report.Status != "failed" || report.NetworkRequests > 1 {
		t.Fatal("deadline ignored")
	}
	cfg.apiStyle = "responses"
	if _, err := runLiveProbe(context.Background(), cfg, "whitelist", "minimax"); err == nil {
		t.Fatal("unsupported API style silently downgraded")
	}
	if finalContentWithoutThinking("<think>private</think> {\"status\":1}") != "{\"status\":1}" {
		t.Fatal("final thinking prefix not separated")
	}
}

func TestLiveProviderReportedOutputCannotExceedRequestedLimit(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"test","choices":[{"index":0,"message":{"role":"assistant","content":"{}"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":513,"total_tokens":523}}`)
	}))
	defer server.Close()
	report, err := runLiveProbe(context.Background(), liveTestConfig(t, server.URL+"/v1"), "whitelist", "minimax")
	if err == nil || report.ErrorCode != "provider_output_limit_violated" || calls.Load() != 1 || report.TokenUsage == nil {
		t.Fatal("provider output-limit violation was accepted or usage lost")
	}
}
