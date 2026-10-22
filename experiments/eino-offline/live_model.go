package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	openai "github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

const maxProviderBody = 256 * 1024

type liveError struct{ code string }

func (e *liveError) Error() string {
	return "真实模型验证未完成（" + e.code + "）；原始服务响应与私有配置未输出"
}
func liveFailure(code string) error { return &liveError{code: code} }
func sanitizedLiveError(err error) *liveError {
	var safe *liveError
	if errors.As(err, &safe) {
		return safe
	}
	if errors.Is(err, context.Canceled) {
		return &liveError{code: "canceled"}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &liveError{code: "timeout"}
	}
	if errors.Is(err, adk.ErrExceedMaxIterations) {
		return &liveError{code: "iteration_budget_exhausted"}
	}
	return &liveError{code: "output_or_tool_validation_failed"}
}

// requestGuard limits actual HTTP attempts, including any SDK retry. It rejects
// redirects at Client level and never retains provider bodies or credentials in errors.
type requestGuard struct {
	mu          sync.Mutex
	base        http.RoundTripper
	endpoint    string
	modelName   string
	key         string
	maxRequests int
	maxTokens   int
	attempts    int
	lastStatus  int
	lastFailure string
	lastUsage   *ReportedTokenUsage
}

func (g *requestGuard) fail(code string) error {
	g.mu.Lock()
	g.lastFailure = code
	g.mu.Unlock()
	return liveFailure(code)
}

func (g *requestGuard) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method != http.MethodPost || req.URL.String() != g.endpoint {
		return nil, g.fail("unexpected_request_destination")
	}
	if req.Header.Get("Authorization") != "Bearer "+g.key {
		return nil, g.fail("authorization_header_invalid")
	}
	if req.Body == nil {
		return nil, g.fail("request_body_missing")
	}
	body, err := io.ReadAll(io.LimitReader(req.Body, maxProviderBody+1))
	req.Body.Close()
	if err != nil || len(body) > maxProviderBody {
		return nil, g.fail("request_body_limit")
	}
	var envelope struct {
		Model               string `json:"model"`
		MaxCompletionTokens int    `json:"max_completion_tokens"`
		MaxTokens           int    `json:"max_tokens"`
		Stream              bool   `json:"stream"`
	}
	if json.Unmarshal(body, &envelope) != nil || envelope.Model != g.modelName || envelope.MaxCompletionTokens != g.maxTokens || envelope.MaxTokens != 0 || envelope.Stream {
		return nil, g.fail("request_budget_or_mode_invalid")
	}
	if len(g.key) >= 8 && bytes.Contains(body, []byte(g.key)) {
		return nil, g.fail("credential_in_model_payload")
	}
	req.Body = io.NopCloser(bytes.NewReader(body))
	req.ContentLength = int64(len(body))
	g.mu.Lock()
	if g.attempts >= g.maxRequests {
		g.lastFailure = "http_request_budget_exhausted"
		g.mu.Unlock()
		return nil, liveFailure("http_request_budget_exhausted")
	}
	g.attempts++
	g.lastUsage = nil
	g.mu.Unlock()
	response, err := g.base.RoundTrip(req)
	if err != nil {
		return nil, g.fail("network_or_tls_failure")
	}
	g.mu.Lock()
	g.lastStatus = response.StatusCode
	g.mu.Unlock()
	data, readErr := io.ReadAll(io.LimitReader(response.Body, maxProviderBody+1))
	response.Body.Close()
	if readErr != nil || len(data) > maxProviderBody {
		return nil, g.fail("response_body_limit")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		code := "provider_http_error"
		switch response.StatusCode {
		case 301, 302, 303, 307, 308:
			code = "redirect_refused"
		case 401, 403:
			code = "authentication_or_permission_rejected"
		case 404:
			code = "endpoint_or_model_not_found"
		case 429:
			code = "rate_or_quota_limit"
		}
		return nil, g.fail(code)
	}
	var providerError struct {
		BaseResp *struct {
			StatusCode int `json:"status_code"`
		} `json:"base_resp"`
	}
	if json.Unmarshal(data, &providerError) == nil && providerError.BaseResp != nil && providerError.BaseResp.StatusCode != 0 {
		return nil, g.fail("provider_application_error")
	}
	var usageEnvelope struct {
		Usage *struct {
			PromptTokens     *int `json:"prompt_tokens"`
			CompletionTokens *int `json:"completion_tokens"`
			TotalTokens      *int `json:"total_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(data, &usageEnvelope) == nil && usageEnvelope.Usage != nil {
		u := usageEnvelope.Usage
		if u.PromptTokens != nil && u.CompletionTokens != nil && u.TotalTokens != nil && *u.PromptTokens >= 0 && *u.CompletionTokens >= 0 && *u.TotalTokens >= 0 {
			g.mu.Lock()
			g.lastUsage = &ReportedTokenUsage{PromptTokens: *u.PromptTokens, CompletionTokens: *u.CompletionTokens, TotalTokens: *u.TotalTokens, ResponsesWithUsage: 1}
			g.mu.Unlock()
		}
	}
	response.Body = io.NopCloser(bytes.NewReader(data))
	response.ContentLength = int64(len(data))
	return response, nil
}

func (g *requestGuard) snapshot() (int, int, string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.attempts, g.lastStatus, g.lastFailure
}

type observedLiveModel struct {
	inner model.BaseChatModel
	state *runState
	guard *requestGuard
}

var safeToolCallID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,160}$`)

func (m *observedLiveModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, liveFailure("streaming_not_validated")
}
func (m *observedLiveModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (result *schema.Message, err error) {
	defer func() {
		if recover() != nil {
			result = nil
			err = liveFailure("provider_adapter_panic")
		}
	}()
	s := m.state
	s.mu.Lock()
	if ctx.Err() != nil {
		s.mu.Unlock()
		return nil, ctx.Err()
	}
	if s.report.ModelCalls >= s.opts.MaxModelCalls {
		s.mu.Unlock()
		return nil, liveFailure("model_call_budget_exhausted")
	}
	if len(s.livePending) > 0 {
		for _, message := range input {
			name, ok := s.livePending[message.ToolCallID]
			if message.Role != schema.Tool || !ok {
				continue
			}
			var response lookupResult
			if strictJSON(message.Content, &response) != nil || s.evidence[response.EvidenceID] != name {
				s.mu.Unlock()
				return nil, liveFailure("tool_result_provenance_invalid")
			}
			s.report.ToolResultChecks++
			s.recordLocked("tool_result_received", name, message.ToolCallID)
			delete(s.livePending, message.ToolCallID)
		}
		if len(s.livePending) > 0 {
			s.mu.Unlock()
			return nil, liveFailure("tool_result_id_not_returned")
		}
	}
	s.report.ModelCalls++
	s.recordLocked("model_requested", "configured_model", "")
	s.mu.Unlock()
	result, err = m.inner.Generate(ctx, input, opts...)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		_, _, code := m.guard.snapshot()
		if code == "" {
			code = "provider_protocol_error"
		}
		return nil, liveFailure(code)
	}
	if result == nil || result.Role != schema.Assistant {
		return nil, liveFailure("invalid_assistant_message")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if result.ResponseMeta != nil {
		reason := result.ResponseMeta.FinishReason
		switch reason {
		case "stop", "tool_calls", "length", "content_filter":
		default:
			reason = "unknown"
		}
		s.report.FinishReasons = append(s.report.FinishReasons, reason)
		usage := m.guard.reportedUsage()
		if usage != nil {
			if s.report.TokenUsage == nil {
				s.report.TokenUsage = &ReportedTokenUsage{}
			}
			s.report.TokenUsage.PromptTokens += usage.PromptTokens
			s.report.TokenUsage.CompletionTokens += usage.CompletionTokens
			s.report.TokenUsage.TotalTokens += usage.TotalTokens
			s.report.TokenUsage.ResponsesWithUsage++
			if usage.CompletionTokens > m.guard.maxTokens {
				return nil, liveFailure("provider_output_limit_violated")
			}
		}
		if reason == "length" {
			return nil, liveFailure("output_truncated_by_limit")
		}
		if reason == "content_filter" {
			return nil, liveFailure("provider_content_filter")
		}
	}
	if len(result.ToolCalls) > 4 {
		return nil, liveFailure("too_many_parallel_tool_calls")
	}
	if s.livePending == nil {
		s.livePending = make(map[string]string)
		s.liveCallIDs = make(map[string]bool)
	}
	for _, call := range result.ToolCalls {
		if call.Function.Name != "get_asset" && call.Function.Name != "get_business_intent" {
			return nil, liveFailure("unregistered_tool_requested")
		}
		if !safeToolCallID.MatchString(call.ID) || s.liveCallIDs[call.ID] {
			return nil, liveFailure("invalid_or_reused_tool_call_id")
		}
		s.liveCallIDs[call.ID] = true
		s.livePending[call.ID] = call.Function.Name
		s.recordLocked("tool_requested", call.Function.Name, call.ID)
	}
	if len(result.ToolCalls) == 0 {
		s.recordLocked("model_completed", "configured_model", "")
	}
	// Reasoning remains transient in schema.Message for provider-required history
	// continuity. It is never copied into Report or persisted by this experiment.
	return result, nil
}

func finalContentWithoutThinking(content string) string {
	text := strings.TrimSpace(content)
	if strings.HasPrefix(text, "<think>") {
		if end := strings.Index(text, "</think>"); end >= 0 {
			return strings.TrimSpace(text[end+len("</think>"):])
		}
		return ""
	}
	return text
}

func runLiveProbe(ctx context.Context, config modelConfig, scenario, provider string) (Report, error) {
	if config.apiStyle != "chat_completions" {
		return Report{}, liveFailure("configured_api_style_not_implemented")
	}
	if provider != "minimax" && provider != "openai-compatible" {
		return Report{}, liveFailure("unknown_provider_profile")
	}
	if scenario != "whitelist" && scenario != "missing-whitelist" {
		return Report{}, liveFailure("select_one_synthetic_scenario")
	}
	baseURL, err := url.Parse(config.baseURL)
	if err != nil {
		return Report{}, liveFailure("invalid_configuration")
	}
	baseURL.Path = strings.TrimRight(baseURL.Path, "/") + "/chat/completions"
	transport := &http.Transport{Proxy: http.ProxyFromEnvironment}
	if current, ok := http.DefaultTransport.(*http.Transport); ok {
		transport = current.Clone()
	}
	transport.ResponseHeaderTimeout = time.Duration(config.timeoutSeconds) * time.Second
	defer transport.CloseIdleConnections()
	guard := &requestGuard{base: transport, endpoint: baseURL.String(), key: config.apiKey, modelName: config.modelName, maxRequests: config.maxCalls, maxTokens: config.maxOutputTokens}
	client := &http.Client{Transport: guard, Timeout: time.Duration(config.timeoutSeconds) * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	cfg := &openai.ChatModelConfig{APIKey: config.apiKey, BaseURL: config.baseURL, Model: config.modelName, HTTPClient: client, MaxCompletionTokens: &config.maxOutputTokens}
	if provider == "minimax" {
		cfg.ExtraFields = map[string]any{"reasoning_split": true}
	}
	inner, err := openai.NewChatModel(ctx, cfg)
	if err != nil {
		return Report{}, liveFailure("provider_initialization_failed")
	}
	opts := defaults()
	opts.Scenario = scenario
	opts.MaxIterations = config.maxCalls
	opts.MaxModelCalls = config.maxCalls
	report, err := runProbeWithFactory(ctx, opts, "live_model", func(s *runState) model.BaseChatModel { return &observedLiveModel{inner: inner, state: s, guard: guard} })
	report.NetworkRequests, report.HTTPStatus, _ = guard.snapshot()
	redact := func(value string) string {
		for _, private := range []string{config.apiKey, config.baseURL, baseURL.Host, baseURL.Hostname(), config.modelName} {
			if private != "" {
				value = strings.ReplaceAll(value, private, "[redacted]")
			}
		}
		return value
	}
	report.Error = redact(report.Error)
	for i := range report.Events {
		report.Events[i].CallID = redact(report.Events[i].CallID)
	}
	if report.Decision != nil {
		for i := range report.Decision.Conditions {
			report.Decision.Conditions[i] = redact(report.Decision.Conditions[i])
		}
	}
	return report, err
}

func (g *requestGuard) reportedUsage() *ReportedTokenUsage {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.lastUsage == nil {
		return nil
	}
	result := *g.lastUsage
	return &result
}
