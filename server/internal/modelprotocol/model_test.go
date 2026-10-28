package modelprotocol_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cfw-policy-llm-agent/server/internal/app"
	"cfw-policy-llm-agent/server/internal/modelprotocol"
	"cfw-policy-llm-agent/server/internal/outbound"
	"cfw-policy-llm-agent/server/internal/storage"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func protocolCase(t *testing.T, handler http.HandlerFunc, maximum ...time.Duration) (*modelprotocol.Factory, *storage.Store, storage.Job) {
	t.Helper()
	ctx := context.Background()
	upstream := httptest.NewServer(handler)
	t.Cleanup(upstream.Close)
	store, err := storage.Open(filepath.Join(t.TempDir(), "protocol.sqlite"), true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	snapshot, err := app.NewAuditService(store).LoadExample(ctx, "internet-in")
	if err != nil {
		t.Fatal(err)
	}
	conv, err := store.CreateConversation(ctx, "本机协议人工测试", snapshot.ScopeRef)
	if err != nil {
		t.Fatal(err)
	}
	plans := app.NewPlanService(store)
	plans.ModelReady = true
	plans.ConfigRevision = "protocol-fixture-v1"
	plans.BudgetRef = "protocol-fixture-budget"
	if err = store.ConfigureBudgetPolicy(ctx, storage.BudgetPolicy{Ref: plans.BudgetRef, ApplicationModelAttempts: 10, SessionModelAttempts: 10, ExpiresAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)}); err != nil {
		t.Fatal(err)
	}
	p, err := plans.Preview(ctx, app.PlanRequest{Kind: "audit", ConversationID: conv.ID, DataSourceID: "local-offline", PolicyScopeRef: snapshot.ScopeRef, Selection: app.PlanSelection{Kind: "assets", AssetIDs: snapshot.Input.Selection.AssetIDs}, DataPolicy: app.DataPolicy{Mode: "pinned_snapshot", SnapshotID: snapshot.ID}, BaselineVersionID: "B2-v1", Mode: "rules_and_agent"})
	if err != nil {
		t.Fatal(err)
	}
	j, _, err := store.EnqueuePlan(ctx, p.ID, "protocol-fixture-job", plans.ConfigRevision)
	if err != nil {
		t.Fatal(err)
	}
	j, found, err := store.Claim(ctx)
	if err != nil || !found {
		t.Fatal(err)
	}
	timeout := 30 * time.Second
	if len(maximum) > 0 {
		timeout = maximum[0]
	}
	manager, err := outbound.NewManager(store, outbound.Config{Revision: plans.ConfigRevision, Sources: []outbound.Source{{Ref: "configured-model", Class: "model", Origin: upstream.URL, AllowLoopbackHTTP: true, Routes: []outbound.Route{{Method: "POST", Path: "/v1/chat/completions"}}, MaxResponseBytes: 256 << 10, Timeout: timeout}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.Close)
	factory, err := modelprotocol.New(manager, modelprotocol.Config{BaseURL: upstream.URL + "/v1", APIKey: "synthetic-protocol-key", Model: "synthetic-configured-model", ProviderProfile: "openai-compatible", MaxOutputTokens: 512, AllowLoopbackFixture: true})
	if err != nil {
		t.Fatal(err)
	}
	return factory, store, j
}

func TestE3IndependentHTTPTimeoutDoesNotExposeWrappedEndpoint(t *testing.T) {
	factory, _, job := protocolCase(t, func(w http.ResponseWriter, r *http.Request) { io.Copy(io.Discard, r.Body); <-r.Context().Done() }, 30*time.Millisecond)
	m, err := factory.NewModel(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = m.Generate(context.Background(), []*schema.Message{schema.UserMessage("query")})
	if err == nil || strings.Contains(err.Error(), "127.0.0.1") || strings.Contains(err.Error(), "http:") {
		t.Fatal("independent transport timeout leaked a wrapped private endpoint")
	}
}

func TestE3ModelAdapterIsLazyAndPreservesNonStreamingAssistantContinuity(t *testing.T) {
	var calls atomic.Int32
	var invalid atomic.Bool
	factory, store, job := protocolCase(t, func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		var body struct {
			Model               string `json:"model"`
			Stream              bool   `json:"stream"`
			MaxCompletionTokens int    `json:"max_completion_tokens"`
			Messages            []struct {
				Role       string `json:"role"`
				Reasoning  string `json:"reasoning_content"`
				ToolCallID string `json:"tool_call_id"`
			} `json:"messages"`
		}
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer synthetic-protocol-key" || json.NewDecoder(r.Body).Decode(&body) != nil || body.Stream || body.Model != "synthetic-configured-model" || body.MaxCompletionTokens != 512 {
			invalid.Store(true)
		}
		w.Header().Set("Content-Type", "application/json")
		if n == 1 {
			io.WriteString(w, `{"id":"reply-one","choices":[{"index":0,"message":{"role":"assistant","content":"","reasoning_content":"transient-continuity-only","tool_calls":[{"id":"lookup-one","type":"function","function":{"name":"get_partition_context","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`)
			return
		}
		if len(body.Messages) != 3 || body.Messages[1].Reasoning != "transient-continuity-only" || body.Messages[2].ToolCallID != "lookup-one" {
			invalid.Store(true)
		}
		io.WriteString(w, `{"id":"reply-two","choices":[{"index":0,"message":{"role":"assistant","content":"{\"status\":\"test-only\"}"},"finish_reason":"stop"}]}`)
	})
	m, err := factory.NewModel(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	usage, err := store.BudgetUsage(context.Background(), job.BudgetRef, "", job.ID)
	if err != nil || usage.ModelAttempts != 0 || calls.Load() != 0 {
		t.Fatal("construction sent a request or reserved budget")
	}
	first, err := m.Generate(context.Background(), []*schema.Message{schema.UserMessage("只验证人工协议")})
	if err != nil {
		t.Fatal(err)
	}
	if first.ReasoningContent != "transient-continuity-only" || len(first.ToolCalls) != 1 {
		t.Fatal("transient assistant response was discarded")
	}
	_, err = m.Generate(context.Background(), []*schema.Message{schema.UserMessage("只验证人工协议"), first, schema.ToolMessage(`{"status":"known"}`, "lookup-one")})
	if err != nil || invalid.Load() {
		t.Fatal("nonstreaming protocol or tool correlation changed", err)
	}
	usage, err = store.BudgetUsage(context.Background(), job.BudgetRef, "", job.ID)
	if err != nil || usage.ModelAttempts != 2 {
		t.Fatal("physical request accounting differs from HTTP")
	}
	attempts, err := store.JobAttempts(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(attempts)
	for _, private := range []string{"transient-continuity-only", "synthetic-protocol-key", "127.0.0.1"} {
		if strings.Contains(string(encoded), private) {
			t.Fatal("wire contents entered durable attempt metadata")
		}
	}
}

func TestE3ModelAdapterBlocksCredentialReflectionAndModelOverrideBeforeHTTP(t *testing.T) {
	var calls atomic.Int32
	factory, store, job := protocolCase(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(500) })
	m, err := factory.NewModel(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		text string
		opts []model.Option
	}{
		{"synthetic-protocol-key", nil}, {"normal query", []model.Option{model.WithModel("other-model")}},
	} {
		if _, err = m.Generate(context.Background(), []*schema.Message{schema.UserMessage(tc.text)}, tc.opts...); err == nil {
			t.Fatal("request changed configured model authority")
		}
	}
	usage, err := store.BudgetUsage(context.Background(), job.BudgetRef, "", job.ID)
	if err != nil || calls.Load() != 0 || usage.ModelAttempts != 0 {
		t.Fatal("invalid request reached network admission")
	}
}

func TestE3ModelAdapterRejectsPrivateErrorBodyTruncationAndOversizedInput(t *testing.T) {
	for _, kind := range []string{"http-error", "length", "provider-error", "reflected-secret", "malformed"} {
		t.Run(kind, func(t *testing.T) {
			factory, _, job := protocolCase(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch kind {
				case "http-error":
					w.WriteHeader(401)
					io.WriteString(w, `{"error":{"message":"PRIVATE_ERROR synthetic-protocol-key"}}`)
				case "length":
					io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"incomplete"},"finish_reason":"length"}]}`)
				case "provider-error":
					io.WriteString(w, `{"base_resp":{"status_code":1008,"status_msg":"PRIVATE_ERROR"},"choices":[]}`)
				case "reflected-secret":
					io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"synthetic-protocol-key"},"finish_reason":"stop"}]}`)
				case "malformed":
					io.WriteString(w, `{"choices":"PRIVATE_ERROR"}`)
				}
			})
			m, err := factory.NewModel(context.Background(), job.ID)
			if err != nil {
				t.Fatal(err)
			}
			_, err = m.Generate(context.Background(), []*schema.Message{schema.UserMessage("query")})
			if err == nil {
				t.Fatal("invalid upstream response was accepted")
			}
			for _, private := range []string{"PRIVATE_ERROR", "synthetic-protocol-key", "127.0.0.1"} {
				if strings.Contains(err.Error(), private) {
					t.Fatal("adapter leaked upstream or private configuration")
				}
			}
		})
	}
	factory, store, job := protocolCase(t, func(w http.ResponseWriter, r *http.Request) { t.Error("oversized prompt reached upstream") })
	m, err := factory.NewModel(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.Generate(context.Background(), []*schema.Message{schema.UserMessage(strings.Repeat("x", 129<<10))}); err == nil {
		t.Fatal("oversized request accepted")
	}
	usage, _ := store.BudgetUsage(context.Background(), job.BudgetRef, "", job.ID)
	if usage.ModelAttempts != 0 {
		t.Fatal("oversized request consumed an external attempt")
	}
}
