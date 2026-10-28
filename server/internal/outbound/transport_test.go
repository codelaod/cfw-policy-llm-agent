package outbound

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type testLedger struct {
	mu           sync.Mutex
	maximum      int
	reserved     int
	completed    int
	fail         bool
	failComplete bool
}

func (l *testLedger) ReserveAttempt(ctx context.Context, _, _, _, _ string) (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if l.fail {
		return "", errors.New("PRIVATE_STORAGE_FAILURE")
	}
	if l.reserved >= l.maximum {
		return "", Failure("ATTEMPT_LIMIT")
	}
	l.reserved++
	return strings.Repeat("a", l.reserved), nil
}
func (l *testLedger) RecordAttemptResult(context.Context, string, string, string, int, int64) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.completed++
	if l.failComplete {
		return errors.New("PRIVATE_RECORD_FAILURE")
	}
	return nil
}

func TestE3OutboundResultPersistenceFailureClosesFurtherAdmission(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); io.WriteString(w, `{}`) }))
	defer upstream.Close()
	ledger := &testLedger{maximum: 5, failComplete: true}
	_, client := e3Manager(t, upstream.URL, ledger)
	for range 2 {
		_, err := client.Do(e3Request(t, context.Background(), "POST", upstream.URL+"/v1/chat/completions", `{}`))
		if ErrorCode(err) != "ATTEMPT_RECORD_FAILED" {
			t.Fatal("result persistence failure lost its controlled cause", ErrorCode(err))
		}
	}
	if calls.Load() != 1 || ledger.count() != 1 {
		t.Fatal("SDK could repeat HTTP after result persistence failed")
	}
}
func (l *testLedger) count() int { l.mu.Lock(); defer l.mu.Unlock(); return l.reserved }

func e3Manager(t *testing.T, endpoint string, ledger *testLedger, sources ...Source) (*Manager, *http.Client) {
	t.Helper()
	if len(sources) == 0 {
		sources = []Source{{Ref: "configured-model", Class: "model", Origin: endpoint, AllowLoopbackHTTP: true, Routes: []Route{{Method: "POST", Path: "/v1/chat/completions"}}}}
	}
	manager, err := NewManager(ledger, Config{Revision: "test-v1", Sources: sources})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.Close)
	client, err := manager.Client(context.Background(), "job-a", sources[0].Ref)
	if err != nil {
		t.Fatal(err)
	}
	return manager, client
}
func e3Request(t *testing.T, ctx context.Context, method, endpoint, body string) *http.Request {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, method, endpoint, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	return req
}

func TestE3OutboundConstructionAndRejectedDestinationsHaveNoEffects(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); io.WriteString(w, `{"ok":true}`) }))
	defer upstream.Close()
	ledger := &testLedger{maximum: 10}
	_, client := e3Manager(t, upstream.URL, ledger)
	if calls.Load() != 0 || ledger.count() != 0 {
		t.Fatal("client construction emitted a request")
	}
	for _, tc := range []struct{ method, url string }{{"GET", upstream.URL + "/v1/chat/completions"}, {"POST", upstream.URL + "/arbitrary-rpc"}, {"POST", strings.Replace(upstream.URL, "127.0.0.1", "localhost", 1) + "/v1/chat/completions"}} {
		_, err := client.Do(e3Request(t, context.Background(), tc.method, tc.url, `{}`))
		if ErrorCode(err) != "DESTINATION_REFUSED" {
			t.Fatalf("destination guard: %s", ErrorCode(err))
		}
	}
	if calls.Load() != 0 || ledger.count() != 0 {
		t.Fatal("rejected destination reserved or sent HTTP")
	}
}

func TestE3OutboundRecordsBeforeSendAndNeverRefundsHTTPFailure(t *testing.T) {
	var calls atomic.Int32
	ledger := &testLedger{maximum: 2}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ledger.count() == 0 {
			t.Error("physical request preceded durable admission")
		}
		calls.Add(1)
		w.WriteHeader(500)
		io.WriteString(w, `{"error":{"message":"PRIVATE_PROVIDER_SECRET"}}`)
	}))
	defer upstream.Close()
	_, client := e3Manager(t, upstream.URL, ledger)
	for range 2 {
		response, err := client.Do(e3Request(t, context.Background(), "POST", upstream.URL+"/v1/chat/completions", `{}`))
		if err != nil {
			t.Fatal(ErrorCode(err))
		}
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		if strings.Contains(string(body), "PRIVATE") || !strings.Contains(string(body), "UPSTREAM_HTTP_ERROR") {
			t.Fatal("raw upstream error body escaped")
		}
	}
	if _, err := client.Do(e3Request(t, context.Background(), "POST", upstream.URL+"/v1/chat/completions", `{}`)); ErrorCode(err) != "ATTEMPT_LIMIT" {
		t.Fatalf("failed attempts refunded: %s", ErrorCode(err))
	}
	if calls.Load() != 2 || ledger.count() != 2 {
		t.Fatal("attempt count diverged from physical sends")
	}
}

func TestE3OutboundRedirectAndOversizedResponseStayBounded(t *testing.T) {
	var targetCalls atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { targetCalls.Add(1) }))
	defer target.Close()
	for _, scenario := range []string{"redirect", "oversize"} {
		t.Run(scenario, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if scenario == "redirect" {
					http.Redirect(w, r, target.URL, 307)
					return
				}
				io.WriteString(w, strings.Repeat("x", 65))
			}))
			defer upstream.Close()
			ledger := &testLedger{maximum: 2}
			_, client := e3Manager(t, upstream.URL, ledger, Source{Ref: "configured-model", Class: "model", Origin: upstream.URL, AllowLoopbackHTTP: true, MaxResponseBytes: 64, Routes: []Route{{Method: "POST", Path: "/v1/chat/completions"}}})
			_, err := client.Do(e3Request(t, context.Background(), "POST", upstream.URL+"/v1/chat/completions", `{}`))
			want := "REDIRECT_REFUSED"
			if scenario == "oversize" {
				want = "UPSTREAM_RESPONSE_TOO_LARGE"
			}
			if ErrorCode(err) != want || ledger.count() != 1 {
				t.Fatalf("response guard: %s", ErrorCode(err))
			}
		})
	}
	if targetCalls.Load() != 0 {
		t.Fatal("redirect expanded endpoint authority")
	}
}

func TestE3OutboundStorageRejectionSendsNothing(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer upstream.Close()
	_, client := e3Manager(t, upstream.URL, &testLedger{maximum: 3, fail: true})
	_, err := client.Do(e3Request(t, context.Background(), "POST", upstream.URL+"/v1/chat/completions", `{}`))
	if ErrorCode(err) != "ADMISSION_UNAVAILABLE" || calls.Load() != 0 {
		t.Fatal("storage rejection still emitted HTTP")
	}
}

func TestE3OutboundModelSharedThreeAndPerTaskOne(t *testing.T) {
	arrived := make(chan string, 10)
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		arrived <- r.Header.Get("X-Test-Job")
		select {
		case <-release:
			io.WriteString(w, `{}`)
		case <-r.Context().Done():
		}
	}))
	defer upstream.Close()
	ledger := &testLedger{maximum: 10}
	manager, _ := e3Manager(t, upstream.URL, ledger)
	done := make(chan error, 5)
	for _, job := range []string{"a", "b", "c", "d", "a"} {
		client, err := manager.Client(context.Background(), job, "configured-model")
		if err != nil {
			t.Fatal(err)
		}
		req := e3Request(t, context.Background(), "POST", upstream.URL+"/v1/chat/completions", `{}`)
		req.Header.Set("X-Test-Job", job)
		go func() {
			response, err := client.Do(req)
			if response != nil {
				response.Body.Close()
			}
			done <- err
		}()
	}
	seen := map[string]bool{}
	for range 3 {
		select {
		case job := <-arrived:
			if seen[job] {
				t.Fatal("two model physical requests from same task overlapped")
			}
			seen[job] = true
		case <-time.After(time.Second):
			t.Fatal("three tasks did not overlap")
		}
	}
	select {
	case <-arrived:
		t.Fatal("model concurrency exceeded 3")
	case <-time.After(40 * time.Millisecond):
	}
	close(release)
	for range 5 {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(ErrorCode(err))
			}
		case <-time.After(time.Second):
			t.Fatal("model queue failed to drain")
		}
	}
}

func TestE3OutboundCloudAndCredentialShareTwoSlots(t *testing.T) {
	arrived := make(chan struct{}, 3)
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		arrived <- struct{}{}
		select {
		case <-release:
			io.WriteString(w, `{}`)
		case <-r.Context().Done():
		}
	}))
	defer upstream.Close()
	ledger := &testLedger{maximum: 5}
	manager, _ := e3Manager(t, upstream.URL, ledger,
		Source{Ref: "cloud-cfw", Class: "cloud", Origin: upstream.URL, AllowLoopbackHTTP: true, Routes: []Route{{Method: "POST", Path: "/", Actions: []string{"DescribeNatFirewallList"}}}},
		Source{Ref: "credential-role", Class: "credential", Origin: upstream.URL, AllowLoopbackHTTP: true, Routes: []Route{{Method: "POST", Path: "/credential"}}})
	done := make(chan error, 3)
	for n, ref := range []string{"cloud-cfw", "credential-role", "cloud-cfw"} {
		client, err := manager.Client(context.Background(), "job-"+strings.Repeat("a", n+1), ref)
		if err != nil {
			t.Fatal(err)
		}
		path := "/?Action=DescribeNatFirewallList"
		if ref == "credential-role" {
			path = "/credential"
		}
		req := e3Request(t, context.Background(), "POST", upstream.URL+path, `{}`)
		go func() {
			response, err := client.Do(req)
			if response != nil {
				response.Body.Close()
			}
			done <- err
		}()
	}
	for range 2 {
		select {
		case <-arrived:
		case <-time.After(time.Second):
			t.Fatal("two cloud/credential calls did not overlap")
		}
	}
	select {
	case <-arrived:
		t.Fatal("cloud and credential pools were independently doubled")
	case <-time.After(40 * time.Millisecond):
	}
	close(release)
	for range 3 {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(ErrorCode(err))
			}
		case <-time.After(time.Second):
			t.Fatal("shared cloud pool failed to drain")
		}
	}
}

func TestE3OutboundWaitingCancellationDoesNotAcquireAnotherAttempt(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		select {
		case <-release:
			io.WriteString(w, `{}`)
		case <-r.Context().Done():
		}
	}))
	defer upstream.Close()
	ledger := &testLedger{maximum: 5}
	manager, first := e3Manager(t, upstream.URL, ledger)
	done := make(chan error, 1)
	go func() {
		response, err := first.Do(e3Request(t, context.Background(), "POST", upstream.URL+"/v1/chat/completions", `{}`))
		if response != nil {
			response.Body.Close()
		}
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("first call did not enter")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	second, err := manager.Client(ctx, "job-a", "configured-model")
	if err != nil {
		t.Fatal(err)
	}
	_, err = second.Do(e3Request(t, ctx, "POST", upstream.URL+"/v1/chat/completions", `{}`))
	if err == nil || ledger.count() != 1 {
		t.Fatal("cancelled per-task wait reserved or emitted a second request")
	}
	close(release)
	if err = <-done; err != nil {
		t.Fatal(ErrorCode(err))
	}
}

func TestE3OutboundCancellationReachesPhysicalHTTPWithoutRefund(t *testing.T) {
	entered := make(chan struct{})
	cancelled := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		close(entered)
		<-r.Context().Done()
		close(cancelled)
	}))
	defer upstream.Close()
	ledger := &testLedger{maximum: 5}
	manager, _ := e3Manager(t, upstream.URL, ledger)
	ctx, cancel := context.WithCancel(context.Background())
	client, err := manager.Client(ctx, "cancel-job", "configured-model")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := client.Do(e3Request(t, context.Background(), "POST", upstream.URL+"/v1/chat/completions", `{}`))
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("physical request did not enter")
	}
	cancel()
	select {
	case err = <-done:
		if ErrorCode(err) != "REQUEST_CANCELLED" {
			t.Fatal("task cancellation was lost", ErrorCode(err))
		}
	case <-time.After(time.Second):
		t.Fatal("client did not cancel")
	}
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("physical handler did not observe cancellation")
	}
	if ledger.count() != 1 {
		t.Fatal("cancelled physical attempt was refunded")
	}
}

func TestE3OutboundCloudActionAndMutableConfigCannotExpandPermission(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); io.WriteString(w, `{}`) }))
	defer upstream.Close()
	routes := []Route{{Method: "POST", Path: "/", Actions: []string{"DescribeNatFirewallList"}}}
	ledger := &testLedger{maximum: 5}
	_, client := e3Manager(t, upstream.URL, ledger, Source{Ref: "cloud-cfw", Class: "cloud", Origin: upstream.URL, AllowLoopbackHTTP: true, Routes: routes})
	routes[0].Actions[0] = "DeleteNatFirewall"
	for _, request := range []*http.Request{
		e3Request(t, context.Background(), "POST", upstream.URL+"/?Action=DeleteNatFirewall", `{}`),
		e3Request(t, context.Background(), "POST", upstream.URL+"/?Action=DescribeNatFirewallList", `{"Action":"DeleteNatFirewall"}`),
		e3Request(t, context.Background(), "POST", upstream.URL+"/", `{}`),
	} {
		if _, err := client.Do(request); ErrorCode(err) != "DESTINATION_REFUSED" {
			t.Fatal("RPC action escaped server allowlist", ErrorCode(err))
		}
	}
	if ledger.count() != 0 || calls.Load() != 0 {
		t.Fatal("rejected RPC sent physical HTTP")
	}
	response, err := client.Do(e3Request(t, context.Background(), "POST", upstream.URL+"/?Action=DescribeNatFirewallList", `{}`))
	if err != nil {
		t.Fatal(ErrorCode(err))
	}
	response.Body.Close()
	if calls.Load() != 1 {
		t.Fatal("immutable read allowlist was damaged by caller mutation")
	}
}

func TestE3OutboundProductionCredentialSourceRequiresExplicitOperation(t *testing.T) {
	ledger := &testLedger{maximum: 5}
	manager, err := NewManager(ledger, Config{Revision: "test-v1", Sources: []Source{{Ref: "credential-role", Class: "credential", Origin: "https://credential.example.invalid", Routes: []Route{{Method: "POST", Path: "/"}}}}})
	if manager != nil {
		manager.Close()
	}
	if ErrorCode(err) != "OUTBOUND_CONFIGURATION_INVALID" || ledger.count() != 0 {
		t.Fatal("production credential effect accepted arbitrary RPC operations")
	}
}

func TestE3OutboundBoundDeadlinePreservesItsCause(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.Copy(io.Discard, r.Body); <-r.Context().Done() }))
	defer upstream.Close()
	ledger := &testLedger{maximum: 3}
	manager, _ := e3Manager(t, upstream.URL, ledger)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	client, err := manager.Client(ctx, "deadline-job", "configured-model")
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Do(e3Request(t, context.Background(), "POST", upstream.URL+"/v1/chat/completions", `{}`))
	if ErrorCode(err) != "REQUEST_DEADLINE_EXCEEDED" {
		t.Fatal("bound task deadline became a generic cancellation", ErrorCode(err))
	}
	if ledger.count() != 1 {
		t.Fatal("expired physical attempt was refunded")
	}
}
