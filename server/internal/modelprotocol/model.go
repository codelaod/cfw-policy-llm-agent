// Package modelprotocol adapts the pinned non-streaming Eino component to a
// job-bound HTTP client. Wire messages remain transient; no provider error text
// or private connection setting is part of its public error contract.
package modelprotocol

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"

	"cfw-policy-llm-agent/server/internal/outbound"
	"cfw-policy-llm-agent/server/internal/workload"
	openai "github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

const MaxRequestBytes = 128 << 10
const MaxResponseBytes = 256 << 10

type Failure string

func (e Failure) Error() string     { return string(e) }
func (e Failure) ErrorCode() string { return string(e) }

// Config is constructed by the server from its explicit private configuration.
// AllowLoopbackFixture is available only to trusted local protocol tests.
type Config struct {
	BaseURL              string   `json:"-"`
	APIKey               string   `json:"-"`
	Model                string   `json:"-"`
	ProviderProfile      string   `json:"-"`
	MaxOutputTokens      int      `json:"-"`
	SensitiveValues      []string `json:"-"`
	AllowLoopbackFixture bool     `json:"-"`
}
type Factory struct {
	manager  *outbound.Manager
	config   Config
	endpoint string
}

func New(manager *outbound.Manager, cfg Config) (*Factory, error) {
	u, err := url.Parse(cfg.BaseURL)
	if manager == nil || err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawPath != "" || u.Opaque != "" || cfg.APIKey == "" || strings.TrimSpace(cfg.APIKey) != cfg.APIKey || strings.ContainsAny(cfg.APIKey, "\r\n\x00") || cfg.Model == "" || len(cfg.Model) > 128 || strings.ContainsAny(cfg.Model, "\r\n\x00") || cfg.MaxOutputTokens < 1 || cfg.MaxOutputTokens > workload.MaxCounter || (cfg.ProviderProfile != "openai-compatible" && cfg.ProviderProfile != "minimax") {
		return nil, Failure("MODEL_CONFIGURATION_INVALID")
	}
	if u.Scheme != "https" {
		ip := net.ParseIP(u.Hostname())
		if !cfg.AllowLoopbackFixture || u.Scheme != "http" || ip == nil || !ip.IsLoopback() {
			return nil, Failure("MODEL_CONFIGURATION_INVALID")
		}
	}
	if strings.Contains(u.Path, "..") || strings.Contains(u.Path, "//") || strings.HasSuffix(strings.TrimRight(u.Path, "/"), "/chat/completions") {
		return nil, Failure("MODEL_CONFIGURATION_INVALID")
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/chat/completions"
	cfg.SensitiveValues = append([]string{cfg.APIKey}, cfg.SensitiveValues...)
	return &Factory{manager: manager, config: cfg, endpoint: u.String()}, nil
}

// NewModel performs no HTTP, credential exchange, or attempt reservation.
// A new component instance belongs to each accepted task; the manager and its
// durable ledger and physical-request semaphores remain application shared.
func (f *Factory) NewModel(ctx context.Context, jobID string) (model.BaseChatModel, error) {
	client, err := f.manager.Client(ctx, jobID, "configured-model")
	if err != nil {
		return nil, safeError(err)
	}
	bounded := *client
	bounded.Transport = &protocolGuard{base: client.Transport, config: f.config, endpoint: f.endpoint}
	maximum := f.config.MaxOutputTokens
	cfg := &openai.ChatModelConfig{APIKey: f.config.APIKey, BaseURL: f.config.BaseURL, Model: f.config.Model, HTTPClient: &bounded, MaxCompletionTokens: &maximum}
	if f.config.ProviderProfile == "minimax" {
		cfg.ExtraFields = map[string]any{"reasoning_split": true}
	}
	inner, err := openai.NewChatModel(ctx, cfg)
	if err != nil {
		return nil, Failure("MODEL_INITIALIZATION_FAILED")
	}
	return &safeModel{inner: inner}, nil
}

// ErrorCode accepts only this adapter's controlled failures and known outbound
// admission codes. Arbitrary uppercase provider text is never a safe code.
func ErrorCode(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.Canceled) {
		return "REQUEST_CANCELLED"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "REQUEST_DEADLINE_EXCEEDED"
	}
	var controlled Failure
	if errors.As(err, &controlled) {
		return string(controlled)
	}
	switch code := outbound.ErrorCode(err); code {
	case "ATTEMPT_LIMIT", "EXECUTION_CLOSED", "BUDGET_NOT_CONFIGURED", "BUDGET_EXPIRED", "EFFECT_DENIED", "CONFIGURATION_STALE", "ATTEMPT_RECORD_FAILED", "ADMISSION_UNAVAILABLE", "OUTBOUND_STOPPING", "DESTINATION_REFUSED", "UPSTREAM_TRANSPORT", "UPSTREAM_HTTP_ERROR", "UPSTREAM_READ", "UPSTREAM_RESPONSE_TOO_LARGE", "UPSTREAM_ENCODING_UNSUPPORTED", "REDIRECT_REFUSED":
		return code
	}
	return "MODEL_REQUEST_FAILED"
}
func safeError(err error) error {
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return Failure(ErrorCode(err))
}

type safeModel struct{ inner model.BaseChatModel }

func (m *safeModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, Failure("MODEL_STREAM_UNSUPPORTED")
}
func (m *safeModel) Generate(ctx context.Context, messages []*schema.Message, opts ...model.Option) (reply *schema.Message, err error) {
	defer func() {
		if recover() != nil {
			reply = nil
			err = Failure("MODEL_ADAPTER_FAILED")
		}
	}()
	reply, err = m.inner.Generate(ctx, messages, opts...)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, safeError(err)
	}
	if reply == nil || reply.Role != schema.Assistant {
		return nil, Failure("MODEL_PROTOCOL_INVALID")
	}
	return reply, nil
}

type protocolGuard struct {
	base     http.RoundTripper
	config   Config
	endpoint string
}

func containsSensitive(value any, values []string) bool {
	switch node := value.(type) {
	case string:
		for _, sensitive := range values {
			if sensitive != "" && strings.Contains(node, sensitive) {
				return true
			}
		}
	case []any:
		for _, item := range node {
			if containsSensitive(item, values) {
				return true
			}
		}
	case map[string]any:
		for key, item := range node {
			if containsSensitive(key, values) || containsSensitive(item, values) {
				return true
			}
		}
	}
	return false
}

func (g *protocolGuard) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL == nil || req.URL.String() != g.endpoint || req.Method != "POST" || req.Header.Get("Authorization") != "Bearer "+g.config.APIKey || req.Header.Get("X-App-Token") != "" || req.Body == nil {
		return nil, Failure("MODEL_REQUEST_INVALID")
	}
	body, err := io.ReadAll(io.LimitReader(req.Body, MaxRequestBytes+1))
	req.Body.Close()
	if err != nil || len(body) > MaxRequestBytes {
		return nil, Failure("MODEL_REQUEST_TOO_LARGE")
	}
	var tree any
	if json.Unmarshal(body, &tree) != nil || containsSensitive(tree, g.config.SensitiveValues) {
		return nil, Failure("MODEL_REQUEST_INVALID")
	}
	var envelope struct {
		Model               string `json:"model"`
		Stream              bool   `json:"stream"`
		MaxCompletionTokens int    `json:"max_completion_tokens"`
		MaxTokens           int    `json:"max_tokens"`
	}
	if json.Unmarshal(body, &envelope) != nil || envelope.Model != g.config.Model || envelope.Stream || envelope.MaxCompletionTokens != g.config.MaxOutputTokens || envelope.MaxTokens != 0 {
		return nil, Failure("MODEL_REQUEST_INVALID")
	}
	copy := req.Clone(req.Context())
	copy.Body = io.NopCloser(bytes.NewReader(body))
	copy.ContentLength = int64(len(body))
	copy.GetBody = nil
	response, err := g.base.RoundTrip(copy)
	if err != nil {
		return nil, safeError(err)
	}
	if response == nil || response.Body == nil {
		return nil, Failure("MODEL_PROTOCOL_INVALID")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, Failure("UPSTREAM_HTTP_ERROR")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, MaxResponseBytes+1))
	if err != nil {
		if req.Context().Err() != nil {
			return nil, req.Context().Err()
		}
		return nil, Failure("MODEL_RESPONSE_READ_FAILED")
	}
	if len(data) > MaxResponseBytes {
		return nil, Failure("MODEL_RESPONSE_TOO_LARGE")
	}
	if json.Unmarshal(data, &tree) != nil || containsSensitive(tree, g.config.SensitiveValues) {
		return nil, Failure("MODEL_PROTOCOL_INVALID")
	}
	var result struct {
		BaseResp *struct {
			Code int `json:"status_code"`
		} `json:"base_resp"`
		Choices []struct {
			Index        int    `json:"index"`
			FinishReason string `json:"finish_reason"`
			Message      struct {
				Role      string            `json:"role"`
				Content   string            `json:"content"`
				ToolCalls []json.RawMessage `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	if json.Unmarshal(data, &result) != nil || result.BaseResp != nil && result.BaseResp.Code != 0 || len(result.Choices) != 1 || result.Choices[0].Index != 0 || result.Choices[0].Message.Role != "assistant" {
		return nil, Failure("MODEL_PROTOCOL_INVALID")
	}
	choice := result.Choices[0]
	switch choice.FinishReason {
	case "length":
		return nil, Failure("MODEL_OUTPUT_TRUNCATED")
	case "content_filter":
		return nil, Failure("MODEL_OUTPUT_FILTERED")
	case "stop":
		if len(choice.Message.ToolCalls) != 0 || strings.TrimSpace(choice.Message.Content) == "" {
			return nil, Failure("MODEL_PROTOCOL_INVALID")
		}
	case "tool_calls":
		if len(choice.Message.ToolCalls) == 0 {
			return nil, Failure("MODEL_PROTOCOL_INVALID")
		}
	default:
		return nil, Failure("MODEL_PROTOCOL_INVALID")
	}
	response.Body = io.NopCloser(bytes.NewReader(data))
	response.ContentLength = int64(len(data))
	return response, nil
}
