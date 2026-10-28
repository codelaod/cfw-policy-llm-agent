package httpapi_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"cfw-policy-llm-agent/server/internal/app"
	"cfw-policy-llm-agent/server/internal/httpapi"
	"cfw-policy-llm-agent/server/internal/storage"
	"github.com/gin-gonic/gin"
)

const testToken = "synthetic-e0-test-token"

type mockArrival struct{ ID, Conversation string }
type mockGate struct {
	once    sync.Once
	release chan struct{}
}
type localMock struct {
	server  *httptest.Server
	arrived chan mockArrival
	mu      sync.Mutex
	gates   map[string]*mockGate
	calls   map[string]int
}

// The handler cannot finish until this test explicitly releases its request.
// Thus three arrivals prove overlapping physical HTTP calls, not status labels.
func newLocalMock(t *testing.T) *localMock {
	t.Helper()
	m := &localMock{arrived: make(chan mockArrival, 20), gates: map[string]*mockGate{}, calls: map[string]int{}}
	m.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var arrival mockArrival
		if err := json.NewDecoder(r.Body).Decode(&arrival); err != nil {
			http.Error(w, "invalid synthetic request", http.StatusBadRequest)
			return
		}
		gate := &mockGate{release: make(chan struct{})}
		m.mu.Lock()
		m.calls[arrival.ID]++
		m.gates[arrival.ID] = gate
		m.mu.Unlock()
		m.arrived <- arrival
		select {
		case <-r.Context().Done():
			return
		case <-gate.release:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(arrival)
		}
	}))
	t.Cleanup(m.server.Close)
	return m
}

func (m *localMock) run(ctx context.Context, job storage.Job) error {
	body, err := json.Marshal(mockArrival{ID: job.ID, Conversation: job.ConversationID})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.server.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	resp, err := m.server.Client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var got mockArrival
	if err = json.NewDecoder(resp.Body).Decode(&got); err != nil {
		return err
	}
	if got.ID != job.ID || got.Conversation != job.ConversationID {
		return fmt.Errorf("synthetic response crossed conversation")
	}
	return nil
}
func (m *localMock) release(id string) {
	m.mu.Lock()
	gate := m.gates[id]
	m.mu.Unlock()
	if gate != nil {
		gate.once.Do(func() { close(gate.release) })
	}
}
func (m *localMock) count(id string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls[id]
}
func (m *localMock) next(t *testing.T) mockArrival {
	t.Helper()
	select {
	case got := <-m.arrived:
		return got
	case <-time.After(5 * time.Second):
		t.Fatal("local mock did not receive the expected physical HTTP request")
	}
	return mockArrival{}
}

type harness struct {
	engine *app.Engine
	server *httptest.Server
	router *gin.Engine
	client *http.Client
}

func newHarness(t *testing.T, run app.RunFunc) *harness {
	t.Helper()
	store, err := storage.Open(filepath.Join(t.TempDir(), "synthetic.sqlite"), true)
	if err != nil {
		t.Fatal(err)
	}
	engine := app.New(store, run)
	router, err := httpapi.New(engine, testToken)
	if err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	server := httptest.NewServer(router)
	engine.Start()
	h := &harness{engine: engine, server: server, router: router, client: &http.Client{Timeout: 5 * time.Second}}
	t.Cleanup(func() {
		engine.Stop()
		server.Close()
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	return h
}

func TestShutdownRejectsNewExecutionAdmission(t *testing.T) {
	h := newHarness(t, func(context.Context, storage.Job) error { return nil })
	h.engine.Stop()
	status, body := h.request(t, http.MethodPost, "/api/v1/e0/jobs", `{"conversation_id":"after-stop","client_request_id":"after-stop"}`)
	if status != http.StatusServiceUnavailable || !bytes.Contains(body, []byte("SERVER_STOPPING")) {
		t.Fatalf("shutdown accepted a task with no worker: %d %s", status, body)
	}
}
func (h *harness) request(t *testing.T, method, path, body string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, h.server.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-App-Token", testToken)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, data
}
func decodeJob(t *testing.T, data []byte) storage.Job {
	t.Helper()
	var job storage.Job
	if err := json.Unmarshal(data, &job); err != nil {
		t.Fatal(err)
	}
	if job.ID == "" || job.ConversationID == "" {
		t.Fatalf("not a job: %s", data)
	}
	return job
}
func (h *harness) create(t *testing.T, conversation, key string) storage.Job {
	t.Helper()
	body, _ := json.Marshal(httpapi.CreateJob{ConversationID: conversation, ClientRequestID: key})
	status, data := h.request(t, http.MethodPost, "/api/v1/e0/jobs", string(body))
	if status != http.StatusAccepted {
		t.Fatalf("create status %d: %s", status, data)
	}
	return decodeJob(t, data)
}
func (h *harness) job(t *testing.T, id string) storage.Job {
	t.Helper()
	status, data := h.request(t, http.MethodGet, "/api/v1/e0/jobs/"+id, "")
	if status != http.StatusOK {
		t.Fatalf("job status %d: %s", status, data)
	}
	return decodeJob(t, data)
}
func (h *harness) awaitStatus(t *testing.T, id, want string) storage.Job {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		job := h.job(t, id)
		if job.Status == want {
			return job
		}
		if time.Now().After(deadline) {
			t.Fatalf("job %s status %s, want %s", id, job.Status, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
func (h *harness) page(t *testing.T, id string, after int64, limit int) storage.EventPage {
	t.Helper()
	path := fmt.Sprintf("/api/v1/e0/jobs/%s/events?after_seq=%d&limit=%d", id, after, limit)
	status, data := h.request(t, http.MethodGet, path, "")
	if status != http.StatusOK {
		t.Fatalf("event page status %d: %s", status, data)
	}
	var page storage.EventPage
	if err := json.Unmarshal(data, &page); err != nil {
		t.Fatal(err)
	}
	return page
}
func assertError(t *testing.T, status int, body []byte, wantStatus int, wantCode string) {
	t.Helper()
	var got httpapi.Error
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("error is not JSON: %s", body)
	}
	if status != wantStatus || got.Code != wantCode {
		t.Fatalf("error %d %s, want %d %s", status, body, wantStatus, wantCode)
	}
	if bytes.Contains(body, []byte(testToken)) {
		t.Fatal("error leaked the application token")
	}
}

func TestThreeConversationsOverlapAndFourthWaits(t *testing.T) {
	mock := newLocalMock(t)
	h := newHarness(t, mock.run)
	jobs := make([]storage.Job, 0, 4)
	for i := range 3 {
		jobs = append(jobs, h.create(t, fmt.Sprintf("conversation_%d", i), fmt.Sprintf("request_%d", i)))
	}
	arrivals := map[string]string{}
	for range 3 {
		got := mock.next(t)
		arrivals[got.ID] = got.Conversation
	}
	for _, job := range jobs {
		if arrivals[job.ID] != job.ConversationID {
			t.Fatalf("request crossed conversation: %#v", arrivals)
		}
		if got := h.job(t, job.ID); got.Status != "running" {
			t.Fatalf("blocked HTTP call is %s", got.Status)
		}
	}
	fourth := h.create(t, "conversation_fourth", "request_fourth")
	if got := h.job(t, fourth.ID); got.Status != "queued" {
		t.Fatalf("fourth job status %s, want queued", got.Status)
	}
	select {
	case got := <-mock.arrived:
		t.Fatalf("fourth request arrived before any slot was released: %#v", got)
	case <-time.After(100 * time.Millisecond):
	}
	queuedCancel := h.create(t, "conversation_queued_cancel", "request_queued_cancel")
	status, body := h.request(t, http.MethodPost, "/api/v1/e0/jobs/"+queuedCancel.ID+"/cancel", "")
	if status != http.StatusOK || decodeJob(t, body).Status != "cancelled" {
		t.Fatalf("queued cancel did not immediately reach terminal state: %d %s", status, body)
	}
	// Release a middle slot: the next request must not wait for the first job.
	mock.release(jobs[1].ID)
	if got := mock.next(t); got.ID != fourth.ID || got.Conversation != fourth.ConversationID {
		t.Fatalf("unexpected queued arrival: %#v", got)
	}
	jobs = append(jobs, fourth)
	for _, job := range jobs {
		mock.release(job.ID)
	}
	for _, job := range jobs {
		finished := h.awaitStatus(t, job.ID, "completed")
		if mock.count(job.ID) != 1 {
			t.Fatalf("job %s made %d physical requests", job.ID, mock.count(job.ID))
		}
		page := h.page(t, job.ID, 0, 100)
		if page.HasMore || page.LastSeq != finished.ViewSeq || int64(len(page.Events)) != finished.ViewSeq {
			t.Fatalf("inconsistent persisted event tail: %#v", page)
		}
		for i, event := range page.Events {
			if event.JobID != job.ID || event.Seq != int64(i+1) || event.SchemaVersion != 1 {
				t.Fatalf("crossed or discontinuous event: %#v", event)
			}
		}
		last := page.Events[len(page.Events)-1]
		if last.Type != "job_terminal" || last.Payload["status"] != "completed" {
			t.Fatalf("invalid terminal event: %#v", last)
		}
	}
	if mock.count(queuedCancel.ID) != 0 {
		t.Fatal("queued cancellation still issued a physical HTTP call")
	}
}

func TestConcurrentIdempotencyAndConversationConflict(t *testing.T) {
	mock := newLocalMock(t)
	h := newHarness(t, mock.run)
	type result struct {
		status int
		body   []byte
		err    error
	}
	results := make(chan result, 8)
	start := make(chan struct{})
	for range 8 {
		go func() {
			<-start
			req, err := http.NewRequest(http.MethodPost, h.server.URL+"/api/v1/e0/jobs", strings.NewReader(`{"conversation_id":"same_conversation","client_request_id":"same_request"}`))
			if err != nil {
				results <- result{err: err}
				return
			}
			req.Header.Set("X-App-Token", testToken)
			req.Header.Set("Content-Type", "application/json")
			resp, err := h.client.Do(req)
			if err != nil {
				results <- result{err: err}
				return
			}
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			results <- result{status: resp.StatusCode, body: body, err: err}
		}()
	}
	close(start)
	accepted := 0
	id := ""
	for range 8 {
		got := <-results
		if got.err != nil {
			t.Fatal(got.err)
		}
		if got.status == http.StatusAccepted {
			accepted++
		} else if got.status != http.StatusOK {
			t.Fatalf("duplicate status %d: %s", got.status, got.body)
		}
		job := decodeJob(t, got.body)
		if id == "" {
			id = job.ID
		} else if id != job.ID {
			t.Fatal("concurrent same-key requests created different jobs")
		}
	}
	if accepted != 1 {
		t.Fatalf("accepted %d new jobs, want exactly 1", accepted)
	}
	if got := mock.next(t); got.ID != id {
		t.Fatalf("unexpected physical request: %#v", got)
	}
	status, body := h.request(t, http.MethodPost, "/api/v1/e0/jobs", `{"conversation_id":"other_conversation","client_request_id":"same_request"}`)
	assertError(t, status, body, http.StatusConflict, "IDEMPOTENCY_CONFLICT")
	status, body = h.request(t, http.MethodPost, "/api/v1/e0/jobs", `{"conversation_id":"same_conversation","client_request_id":"new_request"}`)
	assertError(t, status, body, http.StatusConflict, "CONVERSATION_BUSY")
	mock.release(id)
	h.awaitStatus(t, id, "completed")
	if mock.count(id) != 1 {
		t.Fatalf("same key made %d physical calls", mock.count(id))
	}
}

func TestSSEFlushDisconnectAndPersistedReplay(t *testing.T) {
	mock := newLocalMock(t)
	h := newHarness(t, mock.run)
	job := h.create(t, "stream_conversation", "stream_request")
	if got := mock.next(t); got.ID != job.ID {
		t.Fatal("unexpected physical call")
	}
	job = h.awaitStatus(t, job.ID, "running")
	path := fmt.Sprintf("/api/v1/e0/jobs/%s/events/stream?after_seq=%d", job.ID, job.ViewSeq)
	req, _ := http.NewRequest(http.MethodGet, h.server.URL+path, nil)
	req.Header.Set("X-App-Token", testToken)
	req.Header.Set("Accept", "text/event-stream")
	// There are no new events while the mock is gated. Returning headers here
	// proves that Gin/net/http actually flushed before task completion.
	response, err := h.client.Do(req)
	if err != nil {
		t.Fatalf("SSE headers did not flush during an active task: %v", err)
	}
	t.Cleanup(func() { _ = response.Body.Close() })
	if response.StatusCode != http.StatusOK || !strings.HasPrefix(response.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("SSE response: %d %v", response.StatusCode, response.Header)
	}
	if response.Header.Get("Cache-Control") != "no-store" || response.Header.Get("X-Subscription-Lifetime-Seconds") != "120" {
		t.Fatalf("missing SSE response controls: %v", response.Header)
	}
	_ = response.Body.Close()
	if current := h.job(t, job.ID); current.Status != "running" {
		t.Fatalf("disconnect changed task to %s", current.Status)
	}
	mock.release(job.ID)
	finished := h.awaitStatus(t, job.ID, "completed")
	if mock.count(job.ID) != 1 {
		t.Fatal("observation reconnect replayed execution")
	}

	page := h.page(t, job.ID, job.ViewSeq, 1)
	if len(page.Events) != 1 || page.Events[0].Seq != job.ViewSeq+1 || page.Events[0].Type != "job_terminal" || page.NextSeq != finished.ViewSeq || page.HasMore {
		t.Fatalf("incorrect reconnect page: %#v", page)
	}
	all := h.page(t, job.ID, 0, 100)
	firstPage := h.page(t, job.ID, 0, 2)
	if len(firstPage.Events) != 2 || firstPage.NextSeq != 2 || !firstPage.HasMore || firstPage.LastSeq != finished.ViewSeq {
		t.Fatalf("incorrect bounded first page: %#v", firstPage)
	}
	secondPage := h.page(t, job.ID, firstPage.NextSeq, 100)
	if secondPage.HasMore || secondPage.NextSeq != finished.ViewSeq || len(secondPage.Events) != len(all.Events)-2 {
		t.Fatalf("incorrect cursor continuation: %#v", secondPage)
	}
	status, body := h.request(t, http.MethodGet, "/api/v1/e0/jobs/"+job.ID+"/events/stream?after_seq=0", "")
	if status != http.StatusOK {
		t.Fatalf("replay stream status %d: %s", status, body)
	}
	frames := parseSSE(t, body)
	if len(frames) != len(all.Events) {
		t.Fatalf("stream has %d events, persisted page has %d", len(frames), len(all.Events))
	}
	for i, event := range frames {
		want := all.Events[i]
		if event.JobID != want.JobID || event.Seq != want.Seq || event.Type != want.Type || event.Payload["status"] != want.Payload["status"] {
			t.Fatalf("stream differs from persisted event: %#v != %#v", event, want)
		}
	}
	status, body = h.request(t, http.MethodGet, fmt.Sprintf("/api/v1/e0/jobs/%s/events/stream?after_seq=%d", job.ID, finished.ViewSeq), "")
	if status != http.StatusNoContent || len(body) != 0 {
		t.Fatalf("terminal tail: %d %s", status, body)
	}
	if mock.count(job.ID) != 1 {
		t.Fatal("event replay caused an additional physical call")
	}

	for _, suffix := range []string{"events", "events/stream"} {
		for _, cursor := range []string{"", "-1", "+1", "1.5", "9007199254740992", "99999"} {
			status, body := h.request(t, http.MethodGet, "/api/v1/e0/jobs/"+job.ID+"/"+suffix+"?after_seq="+cursor, "")
			assertError(t, status, body, http.StatusUnprocessableEntity, "INVALID_EVENT_CURSOR")
		}
	}
}

func parseSSE(t *testing.T, body []byte) []storage.Event {
	t.Helper()
	var events []storage.Event
	var id, kind, data string
	scanner := bufio.NewScanner(bytes.NewReader(body))
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if data == "" {
				continue
			}
			var event storage.Event
			if err := json.Unmarshal([]byte(data), &event); err != nil {
				t.Fatal(err)
			}
			if id != strconv.FormatInt(event.Seq, 10) || kind != event.Type {
				t.Fatalf("SSE envelope differs from JSON: id=%s event=%s %#v", id, kind, event)
			}
			events = append(events, event)
			id, kind, data = "", "", ""
			continue
		}
		switch {
		case strings.HasPrefix(line, "id: "):
			id = strings.TrimPrefix(line, "id: ")
		case strings.HasPrefix(line, "event: "):
			kind = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			data = strings.TrimPrefix(line, "data: ")
		case strings.HasPrefix(line, ":"):
		default:
			t.Fatalf("unexpected SSE line %q", line)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if data != "" {
		t.Fatal("stream ended inside a persisted event frame")
	}
	return events
}

func TestCancellationIsIsolatedAndTerminal(t *testing.T) {
	mock := newLocalMock(t)
	h := newHarness(t, mock.run)
	first := h.create(t, "cancel_first", "cancel_request_first")
	second := h.create(t, "cancel_second", "cancel_request_second")
	for range 2 {
		mock.next(t)
	}
	status, body := h.request(t, http.MethodPost, "/api/v1/e0/jobs/"+first.ID+"/cancel", "")
	if status != http.StatusOK {
		t.Fatalf("cancel status %d: %s", status, body)
	}
	cancelled := h.awaitStatus(t, first.ID, "cancelled")
	if got := h.job(t, second.ID); got.Status != "running" {
		t.Fatalf("cancelling first changed second to %s", got.Status)
	}
	mock.release(first.ID)
	mock.release(second.ID)
	h.awaitStatus(t, second.ID, "completed")
	if got := h.job(t, first.ID); got.Status != "cancelled" || got.ViewSeq != cancelled.ViewSeq {
		t.Fatalf("late callback changed cancelled task: %#v", got)
	}
	for _, job := range []storage.Job{first, second} {
		if mock.count(job.ID) != 1 {
			t.Fatalf("job %s made %d physical calls", job.ID, mock.count(job.ID))
		}
		page := h.page(t, job.ID, 0, 100)
		terminal := page.Events[len(page.Events)-1]
		want := "completed"
		if job.ID == first.ID {
			want = "cancelled"
		}
		if terminal.Type != "job_terminal" || terminal.Payload["status"] != want {
			t.Fatalf("wrong terminal event %#v", terminal)
		}
	}
}

func TestApplicationProtectionAndJSONNotFound(t *testing.T) {
	h := newHarness(t, func(context.Context, storage.Job) error { return nil })
	for _, tc := range []struct {
		name, token, host, origin string
		status                    int
		code                      string
	}{
		{name: "missing application token", status: 401, code: "AUTH_REQUIRED"},
		{name: "wrong application token", token: "wrong-token", status: 401, code: "AUTH_REQUIRED"},
		{name: "foreign host", token: testToken, host: "attacker.example", status: 403, code: "INVALID_HOST"},
		{name: "foreign origin", token: testToken, origin: "https://attacker.example", status: 403, code: "INVALID_ORIGIN"},
		{name: "opaque origin", token: testToken, origin: "null", status: 403, code: "INVALID_ORIGIN"},
		{name: "same origin", token: testToken, origin: h.server.URL, status: 200},
		{name: "local development proxy origin", token: testToken, origin: "http://127.0.0.1:5173", status: 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, _ := http.NewRequest(http.MethodGet, h.server.URL+"/api/v1/health", nil)
			if tc.token != "" {
				req.Header.Set("X-App-Token", tc.token)
			}
			if tc.host != "" {
				req.Host = tc.host
			}
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			resp, err := h.client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatal(err)
			}
			if tc.code != "" {
				assertError(t, resp.StatusCode, body, tc.status, tc.code)
			} else if resp.StatusCode != tc.status {
				t.Fatalf("status %d: %s", resp.StatusCode, body)
			}
		})
	}
	status, body := h.request(t, http.MethodGet, "/api/v1/does-not-exist", "")
	assertError(t, status, body, http.StatusNotFound, "NOT_FOUND")
	if _, err := httpapi.New(nil, "too-short"); err == nil {
		t.Fatal("server accepted an insufficient application token")
	}
}

func TestGeneratedSwaggerMatchesActualRoutes(t *testing.T) {
	h := newHarness(t, func(context.Context, storage.Job) error { return nil })
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "docs", "api", "swagger.json"))
	if err != nil {
		t.Fatalf("generated Swagger must be retained: %v", err)
	}
	var spec struct {
		Swagger             string                                     `json:"swagger"`
		BasePath            string                                     `json:"basePath"`
		Paths               map[string]map[string]json.RawMessage      `json:"paths"`
		SecurityDefinitions map[string]struct{ Type, Name, In string } `json:"securityDefinitions"`
		Definitions         map[string]struct {
			Properties map[string]json.RawMessage `json:"properties"`
		} `json:"definitions"`
	}
	if err := json.Unmarshal(data, &spec); err != nil {
		t.Fatal(err)
	}
	if spec.Swagger != "2.0" || spec.BasePath != "/api/v1" {
		t.Fatalf("wrong generated specification version/base: %s %s", spec.Swagger, spec.BasePath)
	}
	security := spec.SecurityDefinitions["AppToken"]
	if security.Type != "apiKey" || security.In != "header" || security.Name != "X-App-Token" {
		t.Fatalf("missing application header security: %#v", security)
	}
	if _, ok := spec.Definitions["httpapi.JobResult"].Properties["recheck"]; !ok {
		t.Error("generated job result omits the persisted recheck comparison")
	}
	actual := map[string]bool{}
	pathParam := regexp.MustCompile(`:([A-Za-z_][A-Za-z0-9_]*)`)
	for _, route := range h.router.Routes() {
		path := pathParam.ReplaceAllString(strings.TrimPrefix(route.Path, spec.BasePath), "{$1}")
		method := strings.ToLower(route.Method)
		key := method + " " + path
		actual[key] = true
		if _, ok := spec.Paths[path][method]; !ok {
			t.Errorf("actual route absent from generated Swagger: %s", key)
		}
	}
	for path, methods := range spec.Paths {
		for method := range methods {
			switch method {
			case "get", "post", "put", "patch", "delete", "head", "options":
				if !actual[method+" "+path] {
					t.Errorf("documented route absent from server: %s %s", method, path)
				}
			}
		}
	}
}
