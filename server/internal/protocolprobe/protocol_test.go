// Package protocolprobe verifies fixed SDK control points with loopback-only
// httptest servers. These helpers are intentionally test-only: the ledger here
// is not the application's persistent authorization or budget implementation.
package protocolprobe

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	cloudfw "github.com/alibabacloud-go/cloudfw-20171207/v11/client"
	openapi "github.com/alibabacloud-go/darabonba-openapi/v2/client"
	"github.com/alibabacloud-go/tea/dara"
	credential "github.com/aliyun/credentials-go/credentials"
	openai "github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/schema"
)

type safeFailure string

func (e safeFailure) Error() string { return string(e) }

// sharedLedger is deliberately in-memory for the dependency probe. Production
// requires the separately designed durable multi-level attempt ledger.
type sharedLedger struct {
	mu                sync.Mutex
	maximum, attempts int
}

func (b *sharedLedger) reserve() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.attempts >= b.maximum {
		return safeFailure("ATTEMPT_LIMIT")
	}
	b.attempts++ // Count before any physical send; unknown delivery is not refunded.
	return nil
}
func (b *sharedLedger) count() int { b.mu.Lock(); defer b.mu.Unlock(); return b.attempts }

type guardedTransport struct {
	base   http.RoundTripper
	host   string
	ledger *sharedLedger
	limit  int64
}

func (g *guardedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	ip := net.ParseIP(req.URL.Hostname())
	if req.URL.Scheme != "http" || req.URL.Host != g.host || ip == nil || !ip.IsLoopback() || req.URL.User != nil {
		return nil, safeFailure("DESTINATION_REFUSED")
	}
	if err := req.Context().Err(); err != nil {
		return nil, err
	}
	if err := g.ledger.reserve(); err != nil {
		return nil, err
	}
	resp, err := g.base.RoundTrip(req)
	if err != nil {
		if ctxErr := req.Context().Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, safeFailure("UPSTREAM_TRANSPORT")
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, g.limit+1))
	resp.Body.Close()
	if err != nil {
		if ctxErr := req.Context().Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, safeFailure("UPSTREAM_READ")
	}
	if int64(len(body)) > g.limit {
		return nil, safeFailure("UPSTREAM_RESPONSE_TOO_LARGE")
	}
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		return nil, safeFailure("REDIRECT_REFUSED")
	}
	// Keep only synthetic safe codes before handing error bodies to either SDK.
	// This avoids SDK errors retaining raw provider Message/RequestId fields.
	if resp.StatusCode >= 400 {
		body = []byte(`{"Code":"InternalError","Message":"UPSTREAM_HTTP_ERROR","error":{"message":"UPSTREAM_HTTP_ERROR","type":"upstream_error","code":"upstream_error"}}`)
	}
	resp.Body = io.NopCloser(bytes.NewReader(body))
	resp.ContentLength = int64(len(body))
	return resp, nil
}
func localClient(t *testing.T, endpoint string, b *sharedLedger, limit int64) *http.Client {
	t.Helper()
	u, err := url.Parse(endpoint)
	if err != nil {
		t.Fatal("invalid test endpoint")
	}
	transport := &http.Transport{Proxy: nil, ResponseHeaderTimeout: time.Second}
	t.Cleanup(transport.CloseIdleConnections)
	return &http.Client{Transport: &guardedTransport{base: transport, host: u.Host, ledger: b, limit: limit}, Timeout: 2 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// SDK injection uses a per-instance client, never tea globals/default transport.
type sdkHTTP struct{ client *http.Client }

func (c sdkHTTP) Call(r *http.Request, _ *http.Transport) (*http.Response, error) {
	return c.client.Do(r)
}

// exchangeCredential explicitly binds the already accepted task context to a
// synthetic credential exchange. credentials-go's interface lacks a ctx argument;
// no default profile, instance metadata or real STS endpoint is used.
type exchangeCredential struct {
	credential.Credential
	mu       sync.Mutex
	ctx      context.Context
	client   *http.Client
	endpoint string
	cached   *credential.CredentialModel
}

func (p *exchangeCredential) GetCredential() (*credential.CredentialModel, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cached != nil {
		return p.cached, nil
	}
	req, err := http.NewRequestWithContext(p.ctx, http.MethodPost, p.endpoint, nil)
	if err != nil {
		return nil, safeFailure("CREDENTIAL_REQUEST")
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, safeFailure("CREDENTIAL_EXCHANGE")
	}
	var model credential.CredentialModel
	if json.NewDecoder(resp.Body).Decode(&model) != nil {
		return nil, safeFailure("CREDENTIAL_DECODE")
	}
	if dara.StringValue(model.AccessKeyId) == "" || dara.StringValue(model.AccessKeySecret) == "" {
		return nil, safeFailure("CREDENTIAL_MISSING")
	}
	p.cached = &model
	return p.cached, nil
}
func staticCredential(t *testing.T) credential.Credential {
	t.Helper()
	c, err := credential.NewCredential(&credential.Config{Type: dara.String("access_key"), AccessKeyId: dara.String("synthetic-access-key"), AccessKeySecret: dara.String("synthetic-access-secret")})
	if err != nil {
		t.Fatal("synthetic credential setup failed")
	}
	return c
}
func sdkClient(t *testing.T, endpoint string, h *http.Client, c credential.Credential, retries int) *cloudfw.Client {
	t.Helper()
	u, _ := url.Parse(endpoint)
	client, err := cloudfw.NewClient(&openapi.Config{Endpoint: dara.String(u.Host), Protocol: dara.String("http"), RegionId: dara.String("cn-hangzhou"), Credential: c, UserAgent: dara.String("cfw-e0-loopback-probe"), HttpClient: sdkHTTP{client: h}, RetryOptions: &dara.RetryOptions{Retryable: retries > 1, MaxAttempts: retries}})
	if err != nil {
		t.Fatal("synthetic SDK initialization failed")
	}
	return client
}
func readNAT(ctx context.Context, c *cloudfw.Client) error {
	_, err := c.DescribeNatFirewallListWithContext(ctx, &cloudfw.DescribeNatFirewallListRequest{PageNo: dara.Int64(1), PageSize: dara.Int64(10), NatGatewayId: dara.String("synthetic-nat")}, &dara.RuntimeOptions{})
	return err
}

func TestEinoNonStreamingLoopback(t *testing.T) {
	var calls atomic.Int32
	var invalid atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var body struct {
			Model    string `json:"model"`
			Stream   bool   `json:"stream"`
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if r.URL.Path != "/v1/chat/completions" || r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer synthetic-model-token" || json.NewDecoder(r.Body).Decode(&body) != nil || body.Stream || body.Model != "synthetic-model" {
			invalid.Store(true)
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"synthetic-message","object":"chat.completion","model":"synthetic-model","choices":[{"index":0,"message":{"role":"assistant","content":"人工资料协议验证"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`)
	}))
	defer upstream.Close()
	ledger := &sharedLedger{maximum: 1}
	h := localClient(t, upstream.URL, ledger, 2<<20)
	model, err := openai.NewChatModel(context.Background(), &openai.ChatModelConfig{APIKey: "synthetic-model-token", BaseURL: upstream.URL + "/v1", Model: "synthetic-model", HTTPClient: h})
	if err != nil {
		t.Fatal("Eino init failed")
	}
	if ledger.count() != 0 {
		t.Fatal("initialization sent HTTP")
	}
	reply, err := model.Generate(context.Background(), []*schema.Message{schema.UserMessage("只验证本机人工协议")})
	if err != nil || reply == nil || reply.Content != "人工资料协议验证" || invalid.Load() {
		t.Fatal("non-streaming Eino protocol failed")
	}
	if ledger.count() != 1 || calls.Load() != 1 {
		t.Fatal("physical attempts mismatch")
	}
}

func TestSDKCredentialAndInternalRetryShareAttempts(t *testing.T) {
	var cloudCalls, credentialCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/credential" {
			credentialCalls.Add(1)
			io.WriteString(w, `{"accessKeyId":"synthetic-access-key","accessKeySecret":"synthetic-access-secret","type":"access_key"}`)
			return
		}
		if cloudCalls.Add(1) == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			io.WriteString(w, `{"Code":"InternalError","Message":"synthetic-private-upstream-body"}`)
			return
		}
		io.WriteString(w, `{"RequestId":"synthetic","TotalCount":0,"NatFirewallList":[]}`)
	}))
	defer upstream.Close()
	ledger := &sharedLedger{maximum: 3}
	h := localClient(t, upstream.URL, ledger, 2<<20)
	provider := &exchangeCredential{Credential: staticCredential(t), ctx: context.Background(), client: h, endpoint: upstream.URL + "/credential"}
	client := sdkClient(t, upstream.URL, h, provider, 2)
	if ledger.count() != 0 {
		t.Fatal("SDK or credential init sent HTTP")
	}
	if err := readNAT(context.Background(), client); err != nil {
		t.Fatal("SDK local retry did not complete")
	}
	if credentialCalls.Load() != 1 || cloudCalls.Load() != 2 || ledger.count() != 3 {
		t.Fatalf("attempts credential=%d cloud=%d total=%d", credentialCalls.Load(), cloudCalls.Load(), ledger.count())
	}
	// No refund/new allowance is obtained by making a further logical call.
	if err := readNAT(context.Background(), client); err == nil {
		t.Fatal("shared budget did not stop later physical call")
	}
	if cloudCalls.Load() != 2 || ledger.count() != 3 {
		t.Fatal("budget overrun")
	}
}

func TestSDKWithContextCancelsPhysicalHTTP(t *testing.T) {
	entered := make(chan struct{})
	cancelled := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(entered); <-r.Context().Done(); close(cancelled) }))
	defer upstream.Close()
	ledger := &sharedLedger{maximum: 1}
	h := localClient(t, upstream.URL, ledger, 2<<20)
	client := sdkClient(t, upstream.URL, h, staticCredential(t), 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- readNAT(ctx, client) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("SDK physical request never arrived")
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled SDK returned success")
		}
	case <-time.After(time.Second):
		t.Fatal("SDK did not return after task cancellation")
	}
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("HTTP server did not observe request cancellation")
	}
	if ledger.count() != 1 {
		t.Fatal("cancelled physical call must still count")
	}
}

func TestCredentialExchangeCancelsPhysicalHTTP(t *testing.T) {
	entered := make(chan struct{})
	cancelled := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(entered); <-r.Context().Done(); close(cancelled) }))
	defer upstream.Close()
	ledger := &sharedLedger{maximum: 1}
	h := localClient(t, upstream.URL, ledger, 2<<20)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	provider := &exchangeCredential{Credential: staticCredential(t), ctx: ctx, client: h, endpoint: upstream.URL + "/credential"}
	client := sdkClient(t, upstream.URL, h, provider, 1)
	done := make(chan error, 1)
	go func() { done <- readNAT(ctx, client) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("credential exchange never arrived")
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled exchange returned success")
		}
	case <-time.After(time.Second):
		t.Fatal("credential exchange did not stop")
	}
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("credential HTTP did not observe cancellation")
	}
	if ledger.count() != 1 {
		t.Fatal("cancelled exchange must still count")
	}
}

func TestSDKBoundsBeforeDecodeAndRedactsErrors(t *testing.T) {
	for _, mode := range []string{"oversize", "error", "redirect"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				switch mode {
				case "oversize":
					io.WriteString(w, strings.Repeat("x", 2048))
				case "error":
					w.WriteHeader(500)
					io.WriteString(w, `{"Code":"InternalError","Message":"synthetic-private-upstream-body"}`)
				case "redirect":
					w.Header().Set("Location", "http://192.0.2.1/forbidden")
					w.WriteHeader(307)
				}
			}))
			defer upstream.Close()
			ledger := &sharedLedger{maximum: 1}
			h := localClient(t, upstream.URL, ledger, 1024)
			client := sdkClient(t, upstream.URL, h, staticCredential(t), 1)
			err := readNAT(context.Background(), client)
			if err == nil {
				t.Fatal("unsafe upstream response returned success")
			}
			if strings.Contains(err.Error(), "synthetic-private-upstream-body") || strings.Contains(err.Error(), "synthetic-access-secret") {
				t.Fatal("raw upstream body/credential leaked into SDK error")
			}
			if calls.Load() != 1 || ledger.count() != 1 {
				t.Fatal("unexpected redirect or retry request")
			}
		})
	}
}

func TestGuardRejectsNonLoopbackWithoutSend(t *testing.T) {
	ledger := &sharedLedger{maximum: 1}
	h := localClient(t, "http://127.0.0.1:1", ledger, 1024)
	_, err := h.Get("http://192.0.2.1/forbidden")
	if err == nil || ledger.count() != 0 {
		t.Fatal("non-loopback request reached physical send")
	}
	var safe safeFailure
	if !errors.As(err, &safe) || safe != "DESTINATION_REFUSED" {
		t.Fatal("destination error was not stable")
	}
}

func TestEinoRejectsOversizedBodyBeforeDecode(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, strings.Repeat("synthetic-private-upstream-body", 200))
	}))
	defer upstream.Close()
	ledger := &sharedLedger{maximum: 1}
	h := localClient(t, upstream.URL, ledger, 1024)
	model, err := openai.NewChatModel(context.Background(), &openai.ChatModelConfig{APIKey: "synthetic-model-token", BaseURL: upstream.URL + "/v1", Model: "synthetic-model", HTTPClient: h})
	if err != nil {
		t.Fatal("Eino init failed")
	}
	_, err = model.Generate(context.Background(), []*schema.Message{schema.UserMessage("人工协议")})
	if err == nil || strings.Contains(err.Error(), "synthetic-private-upstream-body") || !strings.Contains(err.Error(), "UPSTREAM_RESPONSE_TOO_LARGE") {
		t.Fatal("model response must be bounded before SDK decode")
	}
	if ledger.count() != 1 {
		t.Fatal("model physical attempt mismatch")
	}
}
