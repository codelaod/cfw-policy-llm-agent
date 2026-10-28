// Package outbound admits every physical HTTP attempt using a durable ledger.
// Constructing a manager or a bound client performs no network or ledger work.
package outbound

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"cfw-policy-llm-agent/server/internal/domain"
)

type Failure string

func (e Failure) Error() string     { return string(e) }
func (e Failure) ErrorCode() string { return string(e) }

// ErrorCode is the only error projection suitable for durable/public records.
// http.Client and third-party SDKs can otherwise include private endpoint URLs
// when wrapping a transport error, even though its inner cause is controlled.
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
	var coded interface{ ErrorCode() string }
	if errors.As(err, &coded) {
		value := coded.ErrorCode()
		if len(value) > 0 && len(value) <= 80 {
			valid := true
			for _, r := range value {
				if !(r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_') {
					valid = false
				}
			}
			if valid {
				return value
			}
		}
	}
	return "UPSTREAM_FAILURE"
}

type Ledger interface {
	ReserveAttempt(context.Context, string, string, string, string) (string, error)
	RecordAttemptResult(context.Context, string, string, string, int, int64) error
}

type Route struct {
	Method  string
	Path    string
	Actions []string
}
type Source struct {
	Ref               string
	Class             string
	Origin            string
	Routes            []Route
	AllowLoopbackHTTP bool
	Timeout           time.Duration
	MaxResponseBytes  int64
}
type Config struct {
	Revision string
	Sources  []Source
}
type source struct {
	Source
	origin *url.URL
}
type taskGate struct {
	semaphore  chan struct{}
	references int
}
type Manager struct {
	ledger    Ledger
	revision  string
	sources   map[string]source
	cloud     chan struct{}
	model     chan struct{}
	mu        sync.Mutex
	tasks     map[string]*taskGate
	ctx       context.Context
	stop      context.CancelFunc
	transport *http.Transport
	blocked   atomic.Bool
}

func safeAlias(value string) bool {
	if len(value) == 0 || len(value) > 128 {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

var readonlyCloudActions = map[string]bool{
	"DescribeAssetList": true, "DescribeControlPolicy": true, "DescribeNatFirewallList": true, "DescribeNatFirewallControlPolicy": true, "DescribeAddressBook": true, "DescribeInstances": true, "DescribeSecurityGroupAttribute": true, "GetCallerIdentity": true,
}

func NewManager(ledger Ledger, config Config) (*Manager, error) {
	if ledger == nil || !safeAlias(config.Revision) || len(config.Sources) == 0 || len(config.Sources) > 16 {
		return nil, Failure("OUTBOUND_CONFIGURATION_INVALID")
	}
	ctx, stop := context.WithCancel(context.Background())
	m := &Manager{ledger: ledger, revision: config.Revision, sources: map[string]source{}, cloud: make(chan struct{}, 2), model: make(chan struct{}, 3), tasks: map[string]*taskGate{}, ctx: ctx, stop: stop}
	for _, item := range config.Sources {
		u, err := url.Parse(item.Origin)
		if err != nil || len(item.Origin) > 2048 || !safeAlias(item.Ref) || item.Class != "cloud" && item.Class != "credential" && item.Class != "model" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" || u.Path != "" && u.Path != "/" || len(item.Routes) == 0 || len(item.Routes) > 16 {
			return nil, Failure("OUTBOUND_CONFIGURATION_INVALID")
		}
		if _, duplicate := m.sources[item.Ref]; duplicate {
			return nil, Failure("OUTBOUND_CONFIGURATION_INVALID")
		}
		if u.Scheme != "https" {
			ip := net.ParseIP(u.Hostname())
			if u.Scheme != "http" || !item.AllowLoopbackHTTP || ip == nil || !ip.IsLoopback() {
				return nil, Failure("OUTBOUND_CONFIGURATION_INVALID")
			}
		}
		if item.Timeout == 0 {
			item.Timeout = 30 * time.Second
		}
		if item.Timeout <= 0 || item.Timeout > 30*time.Second {
			return nil, Failure("OUTBOUND_CONFIGURATION_INVALID")
		}
		if item.MaxResponseBytes == 0 {
			item.MaxResponseBytes = 2 << 20
		}
		if item.MaxResponseBytes < 1 || item.MaxResponseBytes > 2<<20 {
			return nil, Failure("OUTBOUND_CONFIGURATION_INVALID")
		}
		routes := make([]Route, len(item.Routes))
		seen := map[string]bool{}
		for n, route := range item.Routes {
			if route.Method != "GET" && route.Method != "POST" || !strings.HasPrefix(route.Path, "/") || strings.ContainsAny(route.Path, "%?#\\") || strings.Contains(route.Path, "..") || seen[route.Method+route.Path] {
				return nil, Failure("OUTBOUND_CONFIGURATION_INVALID")
			}
			seen[route.Method+route.Path] = true
			if item.Class == "cloud" && len(route.Actions) == 0 || item.Class == "model" && len(route.Actions) != 0 {
				return nil, Failure("OUTBOUND_CONFIGURATION_INVALID")
			}
			if item.Class == "credential" && len(route.Actions) == 0 {
				ip := net.ParseIP(u.Hostname())
				// The action-free exchange endpoint exists only for explicit local
				// protocol tests. Production credential RPCs need a fixed action.
				if u.Scheme != "http" || !item.AllowLoopbackHTTP || ip == nil || !ip.IsLoopback() {
					return nil, Failure("OUTBOUND_CONFIGURATION_INVALID")
				}
			}
			for _, action := range route.Actions {
				if item.Class == "cloud" && !readonlyCloudActions[action] || item.Class == "credential" && action != "AssumeRole" && action != "GetCallerIdentity" {
					return nil, Failure("OUTBOUND_CONFIGURATION_INVALID")
				}
			}
			routes[n] = Route{Method: route.Method, Path: route.Path, Actions: append([]string(nil), route.Actions...)}
		}
		item.Routes = routes
		m.sources[item.Ref] = source{Source: item, origin: u}
	}
	// A fresh HTTP/1 connection cannot be internally replayed by Go's Transport.
	// Disabling reuse and HTTP/2 makes one RoundTrip one physical HTTP attempt;
	// any SDK/application retry must enter this guard and reserve again.
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	m.transport = &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: -1}).DialContext, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}, TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 30 * time.Second, MaxResponseHeaderBytes: 32 << 10, DisableKeepAlives: true, DisableCompression: true, Protocols: protocols}
	return m, nil
}

func (m *Manager) Close() { m.stop(); m.transport.CloseIdleConnections() }

// Client is bound by the server to one accepted job and one configured source.
// It must not be exposed as an arbitrary-URL or source-selection tool.
func (m *Manager) Client(ctx context.Context, jobID, sourceRef string) (*http.Client, error) {
	src, known := m.sources[sourceRef]
	if !known || !safeAlias(jobID) || ctx == nil {
		return nil, Failure("DESTINATION_REFUSED")
	}
	if m.ctx.Err() != nil {
		return nil, Failure("OUTBOUND_STOPPING")
	}
	if m.blocked.Load() {
		return nil, Failure("ATTEMPT_RECORD_FAILED")
	}
	return &http.Client{Transport: &guard{manager: m, jobID: jobID, source: src, taskContext: ctx}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, nil
}

func acquire(ctx context.Context, semaphore chan struct{}) error {
	select {
	case semaphore <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (m *Manager) slot(ctx context.Context, jobID, class string) (func(), error) {
	if class != "model" {
		if err := acquire(ctx, m.cloud); err != nil {
			return nil, err
		}
		return func() { <-m.cloud }, nil
	}
	m.mu.Lock()
	gate := m.tasks[jobID]
	if gate == nil {
		gate = &taskGate{semaphore: make(chan struct{}, 1)}
		m.tasks[jobID] = gate
	}
	gate.references++
	m.mu.Unlock()
	drop := func() {
		m.mu.Lock()
		gate.references--
		if gate.references == 0 {
			delete(m.tasks, jobID)
		}
		m.mu.Unlock()
	}
	if err := acquire(ctx, gate.semaphore); err != nil {
		drop()
		return nil, err
	}
	if err := acquire(ctx, m.model); err != nil {
		<-gate.semaphore
		drop()
		return nil, err
	}
	return func() { <-m.model; <-gate.semaphore; drop() }, nil
}

type guard struct {
	manager     *Manager
	jobID       string
	source      source
	taskContext context.Context
}

func (g *guard) requestAllowed(req *http.Request) ([]byte, error) {
	u := req.URL
	if u == nil || u.Scheme != g.source.origin.Scheme || !strings.EqualFold(u.Host, g.source.origin.Host) || u.User != nil || u.Opaque != "" || u.Fragment != "" || u.RawPath != "" && u.RawPath != u.Path || req.Host != "" && !strings.EqualFold(req.Host, u.Host) {
		return nil, Failure("DESTINATION_REFUSED")
	}
	var matched *Route
	for n := range g.source.Routes {
		route := &g.source.Routes[n]
		if req.Method == route.Method && u.Path == route.Path {
			matched = route
			break
		}
	}
	if matched == nil {
		return nil, Failure("DESTINATION_REFUSED")
	}
	var body []byte
	if req.Body != nil && req.Body != http.NoBody {
		if req.ContentLength < 0 || req.ContentLength > 2<<20 {
			return nil, Failure("OUTBOUND_REQUEST_INVALID")
		}
		var err error
		body, err = io.ReadAll(io.LimitReader(req.Body, 2<<20+1))
		req.Body.Close()
		if err != nil || len(body) > 2<<20 || req.ContentLength > 0 && int64(len(body)) != req.ContentLength {
			return nil, Failure("OUTBOUND_REQUEST_INVALID")
		}
	}
	if len(matched.Actions) != 0 {
		// Some fixed SDK versions fill Header maps directly using lowercase keys.
		// Enumerate all case variants rather than relying on canonical map keys.
		actions := []string{}
		for key, values := range req.Header {
			if strings.EqualFold(key, "X-Acs-Action") {
				actions = append(actions, values...)
			}
		}
		query, err := url.ParseQuery(u.RawQuery)
		if err != nil {
			return nil, Failure("DESTINATION_REFUSED")
		}
		collect := func(values url.Values) {
			for key, items := range values {
				if strings.EqualFold(key, "Action") {
					actions = append(actions, items...)
				}
			}
		}
		collect(query)
		if len(body) != 0 {
			contentType := ""
			for key, values := range req.Header {
				if strings.EqualFold(key, "Content-Type") {
					if len(values) != 1 || contentType != "" {
						return nil, Failure("DESTINATION_REFUSED")
					}
					contentType = values[0]
				}
			}
			media, _, err := mime.ParseMediaType(contentType)
			if err != nil {
				return nil, Failure("DESTINATION_REFUSED")
			}
			switch media {
			case "application/x-www-form-urlencoded":
				values, err := url.ParseQuery(string(body))
				if err != nil {
					return nil, Failure("DESTINATION_REFUSED")
				}
				collect(values)
			case "application/json":
				jsonActions, err := rpcJSONActions(body)
				if err != nil {
					return nil, Failure("DESTINATION_REFUSED")
				}
				actions = append(actions, jsonActions...)
			default:
				return nil, Failure("DESTINATION_REFUSED")
			}
		}
		if len(actions) == 0 {
			return nil, Failure("DESTINATION_REFUSED")
		}
		for _, action := range actions {
			// Repeated selectors must agree across headers, query and body.
			// A provider's first/last-key parsing must not change our intent.
			if action != actions[0] {
				return nil, Failure("DESTINATION_REFUSED")
			}
			allowed := false
			for _, known := range matched.Actions {
				if action == known {
					allowed = true
				}
			}
			if !allowed {
				return nil, Failure("DESTINATION_REFUSED")
			}
		}
	}
	return body, nil
}

// rpcJSONActions visits fields in wire order, preserving duplicate/case-variant
// Action keys. Decoding into a map would discard a conflicting earlier value.
func rpcJSONActions(body []byte) ([]string, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') {
		return nil, Failure("DESTINATION_REFUSED")
	}
	actions := []string{}
	for decoder.More() {
		field, err := decoder.Token()
		if err != nil {
			return nil, Failure("DESTINATION_REFUSED")
		}
		name, ok := field.(string)
		if !ok {
			return nil, Failure("DESTINATION_REFUSED")
		}
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			return nil, Failure("DESTINATION_REFUSED")
		}
		if strings.EqualFold(name, "Action") {
			var action string
			if json.Unmarshal(value, &action) != nil {
				return nil, Failure("DESTINATION_REFUSED")
			}
			actions = append(actions, action)
		}
	}
	end, err := decoder.Token()
	if err != nil || end != json.Delim('}') {
		return nil, Failure("DESTINATION_REFUSED")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return nil, Failure("DESTINATION_REFUSED")
	}
	return actions, nil
}

func (g *guard) RoundTrip(req *http.Request) (response *http.Response, err error) {
	if req.Body != nil {
		defer req.Body.Close()
	}
	if g.manager.ctx.Err() != nil {
		return nil, Failure("OUTBOUND_STOPPING")
	}
	if g.manager.blocked.Load() {
		return nil, Failure("ATTEMPT_RECORD_FAILED")
	}
	body, err := g.requestAllowed(req)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(g.source.Timeout)
	if taskDeadline, known := g.taskContext.Deadline(); known && taskDeadline.Before(deadline) {
		deadline = taskDeadline
	}
	ctx, cancel := context.WithDeadline(req.Context(), deadline)
	stopTask := context.AfterFunc(g.taskContext, cancel)
	stopManager := context.AfterFunc(g.manager.ctx, cancel)
	defer func() { stopTask(); stopManager(); cancel() }()
	contextError := func() error {
		if taskErr := g.taskContext.Err(); taskErr != nil {
			return taskErr
		}
		return ctx.Err()
	}
	if err = g.taskContext.Err(); err != nil {
		return nil, err
	}
	release, err := g.manager.slot(ctx, g.jobID, g.source.Class)
	if err != nil {
		return nil, contextError()
	}
	defer release()
	if err = ctx.Err(); err != nil {
		return nil, contextError()
	}
	if g.manager.blocked.Load() {
		return nil, Failure("ATTEMPT_RECORD_FAILED")
	}
	attemptID, err := g.manager.ledger.ReserveAttempt(ctx, g.jobID, g.source.Class, g.source.Ref, g.manager.revision)
	if err != nil {
		code := ErrorCode(err)
		if code == "UPSTREAM_FAILURE" {
			code = "ADMISSION_UNAVAILABLE"
		}
		return nil, Failure(code)
	}
	// Reservation may wait on the durable store while another attempt fails
	// to record. Recheck every gate after the wait and before physical send.
	// A reserved-but-unsent attempt stays conservatively charged; it is not
	// refunded or silently reassigned after admission has closed.
	if g.manager.blocked.Load() {
		return nil, Failure("ATTEMPT_RECORD_FAILED")
	}
	if g.manager.ctx.Err() != nil {
		return nil, Failure("OUTBOUND_STOPPING")
	}
	if err = g.taskContext.Err(); err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, contextError()
	}
	started := time.Now()
	state, code, status := "unknown", "UPSTREAM_TRANSPORT", 0
	var usage *domain.ModelUsage
	defer func() {
		recordCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer stop()
		if recordErr := recordResult(recordCtx, g.manager.ledger, attemptID, state, code, status, time.Since(started).Milliseconds(), usage); recordErr != nil {
			g.manager.blocked.Store(true)
			response = nil
			err = Failure("ATTEMPT_RECORD_FAILED")
		}
	}()
	if err = ctx.Err(); err != nil {
		err = contextError()
		code = ErrorCode(err)
		return nil, err
	}
	physical := req.Clone(ctx)
	physical.Body = io.NopCloser(bytes.NewReader(body))
	physical.ContentLength = int64(len(body))
	physical.GetBody = nil
	response, err = g.manager.transport.RoundTrip(physical)
	if err != nil {
		if ctx.Err() != nil {
			err = contextError()
			code = ErrorCode(err)
			return nil, err
		}
		return nil, Failure("UPSTREAM_TRANSPORT")
	}
	defer response.Body.Close()
	status = response.StatusCode
	state = "response"
	code = ""
	if status < 100 || status > 599 {
		state, code, status = "unknown", "UPSTREAM_PROTOCOL", 0
		return nil, Failure(code)
	}
	if status >= 300 && status < 400 {
		code = "REDIRECT_REFUSED"
		return nil, Failure(code)
	}
	if encoding := response.Header.Get("Content-Encoding"); encoding != "" && encoding != "identity" {
		code = "UPSTREAM_ENCODING_UNSUPPORTED"
		return nil, Failure(code)
	}
	content, readErr := io.ReadAll(io.LimitReader(response.Body, g.source.MaxResponseBytes+1))
	if readErr != nil {
		code = "UPSTREAM_READ"
		if ctx.Err() != nil {
			err = contextError()
			code = ErrorCode(err)
			return nil, err
		}
		return nil, Failure(code)
	}
	if int64(len(content)) > g.source.MaxResponseBytes {
		code = "UPSTREAM_RESPONSE_TOO_LARGE"
		return nil, Failure(code)
	}
	if status >= 400 {
		code = "UPSTREAM_HTTP_ERROR"
		content = []byte(`{"Code":"InternalError","Message":"UPSTREAM_HTTP_ERROR","error":{"message":"UPSTREAM_HTTP_ERROR","type":"upstream_error","code":"upstream_error"}}`)
	} else if g.source.Class == "model" && status >= 200 && status < 300 {
		usage = ParseModelUsage(content)
	}
	response.Header = http.Header{"Content-Type": []string{"application/json"}}
	response.Status = fmt.Sprintf("%d %s", status, http.StatusText(status))
	response.Body = io.NopCloser(bytes.NewReader(content))
	response.ContentLength = int64(len(content))
	return response, nil
}
