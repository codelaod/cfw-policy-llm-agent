package httpapi

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"cfw-policy-llm-agent/server/internal/app"
	"cfw-policy-llm-agent/server/internal/storage"
	"github.com/gin-gonic/gin"
)

type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
type Health struct {
	Stage  string `json:"stage"`
	Source string `json:"source"`
	Status string `json:"status"`
}
type CreateJob struct {
	ConversationID  string `json:"conversation_id" binding:"required"`
	ClientRequestID string `json:"client_request_id" binding:"required"`
}

type API struct {
	engine          *app.Engine
	token           [32]byte
	sensitiveValues []string
	webDir          string
	plans           *app.PlanService
	audits          *app.AuditService
	confirmations   *app.ConfirmationService
	streams         chan struct{}
}

func New(engine *app.Engine, token string, options ...Options) (*gin.Engine, error) {
	if len(token) < 16 {
		return nil, errors.New("CFW_APP_TOKEN must contain at least 16 characters")
	}
	if engine == nil || engine.Store == nil {
		return nil, errors.New("execution store required")
	}
	a := &API{engine: engine, token: sha256.Sum256([]byte(token)), sensitiveValues: []string{token}, streams: make(chan struct{}, 8), plans: app.NewPlanService(engine.Store), audits: app.NewAuditService(engine.Store), confirmations: app.NewConfirmationService(engine.Store)}
	if err := a.configureStatic(options); err != nil {
		return nil, err
	}
	if len(options) > 0 {
		if options[0].Plans != nil {
			if options[0].Plans.Store != engine.Store {
				return nil, errors.New("plan service must use the execution store")
			}
			a.plans = options[0].Plans
		}
		for _, value := range options[0].SensitiveValues {
			if value != "" {
				a.sensitiveValues = append(a.sensitiveValues, value)
			}
		}
	}
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.CustomRecoveryWithWriter(io.Discard, func(c *gin.Context, _ any) { fail(c, 500, "INTERNAL_ERROR", "request failed") }), a.protect)
	r.GET("/api/v1/health", a.health)
	r.POST("/api/v1/e0/jobs", a.create)
	r.GET("/api/v1/e0/jobs/:id", a.checkJobKind, a.job)
	r.GET("/api/v1/e0/jobs/:id/events", a.checkJobKind, a.events)
	r.GET("/api/v1/e0/jobs/:id/events/stream", a.checkJobKind, a.stream)
	r.POST("/api/v1/e0/jobs/:id/cancel", a.checkJobKind, a.cancel)
	a.workspaceRoutes(r)
	r.NoRoute(a.noRoute)
	return r, nil
}

func (a *API) checkJobKind(c *gin.Context) {
	j, err := a.engine.Store.Get(c.Request.Context(), c.Param("id"))
	if err != nil {
		storeError(c, err)
		return
	}
	probePath := strings.HasPrefix(c.Request.URL.Path, "/api/v1/e0/")
	if probePath != (j.Kind == "e0_probe") {
		fail(c, 404, "NOT_FOUND", "task is unavailable at this route")
		return
	}
	c.Next()
}

func fail(c *gin.Context, status int, code, message string) {
	c.AbortWithStatusJSON(status, Error{code, message})
}

func localHost(host string) bool {
	h := host
	if strings.Contains(h, ":") {
		if v, _, err := net.SplitHostPort(h); err == nil {
			h = v
		} else {
			return false
		}
	}
	return h == "localhost" || h == "127.0.0.1" || h == "::1"
}

func (a *API) protect(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.Header("X-Content-Type-Options", "nosniff")
	if !localHost(c.Request.Host) {
		fail(c, 403, "INVALID_HOST", "loopback host required")
		return
	}
	if origin := c.GetHeader("Origin"); origin != "" {
		u, err := url.Parse(origin)
		// Vite's local proxy preserves the browser Origin; both ports are explicit.
		allowed := err == nil && u.Scheme == "http" && u.User == nil && u.Path == "" && (u.Host == c.Request.Host || u.Host == "127.0.0.1:5173" || u.Host == "localhost:5173")
		if !allowed {
			fail(c, 403, "INVALID_ORIGIN", "origin is not allowed")
			return
		}
	}
	if a.webDir != "" && !strings.HasPrefix(c.Request.URL.Path, "/api/") {
		c.Next()
		return
	}
	actual := sha256.Sum256([]byte(c.GetHeader("X-App-Token")))
	if subtle.ConstantTimeCompare(actual[:], a.token[:]) != 1 {
		fail(c, 401, "AUTH_REQUIRED", "unlock with application token")
		return
	}
	// Reject the known application credential before business writes, including
	// JSON escapes. Neither errors nor logs contain the rejected content.
	for _, value := range a.sensitiveValues {
		if strings.Contains(c.Request.URL.String(), value) || strings.Contains(c.GetHeader("Idempotency-Key"), value) {
			fail(c, 422, "CREDENTIAL_IN_PAYLOAD", "server credentials or private connection values cannot enter business data")
			return
		}
	}
	if c.Request.Body != nil && c.Request.ContentLength != 0 {
		maximum := int64(32 << 10)
		if c.Request.URL.Path == "/api/v1/snapshots/import" {
			maximum = 5 << 20
		}
		body, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, maximum))
		if err != nil {
			fail(c, 413, "PAYLOAD_TOO_LARGE", "request body exceeds its bound")
			return
		}
		if len(body) > 0 {
			var tree any
			decoder := json.NewDecoder(bytes.NewReader(body))
			decoder.UseNumber()
			var extra any
			if decoder.Decode(&tree) != nil || decoder.Decode(&extra) != io.EOF {
				fail(c, 422, "INVALID_REQUEST", "one JSON value required")
				return
			}
			for _, value := range a.sensitiveValues {
				if hasSecret(tree, value) {
					fail(c, 422, "CREDENTIAL_IN_PAYLOAD", "server credentials or private connection values cannot enter business data")
					return
				}
			}
		}
		c.Request.Body = io.NopCloser(bytes.NewReader(body))
	}
	c.Next()
}

func hasSecret(value any, secret string) bool {
	switch v := value.(type) {
	case json.Number:
		return strings.Contains(v.String(), secret)
	case string:
		return strings.Contains(v, secret)
	case []any:
		for _, item := range v {
			if hasSecret(item, secret) {
				return true
			}
		}
	case map[string]any:
		for key, item := range v {
			if strings.Contains(key, secret) || hasSecret(item, secret) {
				return true
			}
		}
	}
	return false
}

func storeError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, storage.ErrNotFound):
		fail(c, 404, "NOT_FOUND", "record not found")
	case errors.Is(err, storage.ErrInputInvalid):
		fail(c, 422, "INVALID_REQUEST", "invalid or unsupported request")
	case errors.Is(err, storage.ErrVersionConflict):
		fail(c, 409, "VERSION_CONFLICT", "saved version changed; reload before confirming")
	case errors.Is(err, storage.ErrPlanStale):
		fail(c, 409, "PLAN_STALE", "preview expired or input changed; preview again")
	case errors.Is(err, storage.ErrPlanConsumed):
		fail(c, 409, "PLAN_CONSUMED", "plan was already consumed")
	case errors.Is(err, storage.ErrConversationArchived):
		fail(c, 409, "CONVERSATION_ARCHIVED", "conversation is archived")
	case errors.Is(err, storage.ErrJobTerminal):
		fail(c, 409, "JOB_STATE_CONFLICT", "operation is not available in this task state")
	case errors.Is(err, storage.ErrInputNotSealed):
		fail(c, 409, "INPUT_NOT_SEALED", "the accepted task has not sealed its effective input yet")
	case errors.Is(err, app.ErrSourceNotConfigured):
		fail(c, 409, "SOURCE_NOT_CONFIGURED", "read-only source is not configured")
	case errors.Is(err, app.ErrModelNotConfigured):
		fail(c, 409, "MODEL_NOT_CONFIGURED", "model is not configured or authorized")
	case errors.Is(err, app.ErrScopeDenied):
		fail(c, 422, "SCOPE_DENIED", "object or scope is outside the fixed input")
	case errors.Is(err, app.ErrRecheckNotComparable):
		fail(c, 409, "RECHECK_NOT_COMPARABLE", "object_missing: an original audit object is absent from the new input")
	case errors.Is(err, app.ErrStopping):
		fail(c, 503, "SERVER_STOPPING", "server is stopping; execution admission is closed")
	case errors.Is(err, storage.ErrCursor):
		fail(c, 422, "INVALID_EVENT_CURSOR", "cursor must be within this job's persisted events")
	case errors.Is(err, storage.ErrBusy):
		fail(c, 409, "CONVERSATION_BUSY", "conversation already has an unfinished task")
	case errors.Is(err, storage.ErrConflict):
		fail(c, 409, "IDEMPOTENCY_CONFLICT", "client_request_id was used for different input")
	case errors.Is(err, storage.ErrCapacity):
		fail(c, 429, "QUEUE_FULL", "waiting queue is full")
	default:
		fail(c, 503, "STORAGE_UNAVAILABLE", "durable operation failed")
	}
}

// health godoc
// @Summary Read local offline service status (no outbound requests)
// @Tags E0
// @Produce json
// @Security AppToken
// @Success 200 {object} Health
// @Failure 401,403 {object} Error
// @Router /health [get]
func (a *API) health(c *gin.Context) { c.JSON(200, Health{"E4", "local-offline", "ok"}) }

// create godoc
// @Summary Start a synthetic local E0 probe; never a business audit
// @Tags E0
// @Accept json
// @Produce json
// @Security AppToken
// @Param request body CreateJob true "Synthetic conversation and idempotency identifiers"
// @Success 202 {object} storage.Job
// @Success 200 {object} storage.Job "Same input replay"
// @Failure 401,403,409,422,429,503 {object} Error
// @Router /e0/jobs [post]
func (a *API) create(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4096)
	var req CreateJob
	d := json.NewDecoder(c.Request.Body)
	d.DisallowUnknownFields()
	if d.Decode(&req) != nil || !validID(req.ConversationID) || !validID(req.ClientRequestID) {
		fail(c, 422, "INVALID_REQUEST", "bounded identifiers required; unknown fields rejected")
		return
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		fail(c, 422, "INVALID_REQUEST", "one JSON object required")
		return
	}
	j, replay, err := a.engine.Create(c.Request.Context(), req.ConversationID, req.ClientRequestID)
	if errors.Is(err, app.ErrStopping) {
		fail(c, 503, "SERVER_STOPPING", "server is stopping; new execution is closed")
		return
	}
	if err != nil {
		storeError(c, err)
		return
	}
	status := 202
	if replay {
		status = 200
	}
	c.JSON(status, j)
}

func validID(s string) bool {
	if len(s) == 0 || len(s) > 80 {
		return false
	}
	for _, v := range s {
		if !((v >= 'a' && v <= 'z') || (v >= 'A' && v <= 'Z') || (v >= '0' && v <= '9') || v == '_' || v == '-') {
			return false
		}
	}
	return true
}

// job godoc
// @Summary Get a persisted task and its view_seq
// @Tags E0
// @Produce json
// @Security AppToken
// @Param id path string true "Task ID"
// @Success 200 {object} storage.Job
// @Failure 401,403,404,503 {object} Error
// @Router /e0/jobs/{id} [get]
// @Router /jobs/{id} [get]
func (a *API) job(c *gin.Context) {
	j, err := a.engine.Store.Get(c.Request.Context(), c.Param("id"))
	if err != nil {
		storeError(c, err)
		return
	}
	c.JSON(200, j)
}

func cursor(c *gin.Context) (int64, int, error) {
	s := c.DefaultQuery("after_seq", "0")
	if s == "" || len(s) > 16 {
		return 0, 0, storage.ErrCursor
	}
	for _, v := range s {
		if v < '0' || v > '9' {
			return 0, 0, storage.ErrCursor
		}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n > 9007199254740991 {
		return 0, 0, storage.ErrCursor
	}
	l, err := strconv.Atoi(c.DefaultQuery("limit", "100"))
	if err != nil || l < 1 || l > 100 {
		return 0, 0, storage.ErrCursor
	}
	return n, l, nil
}

// events godoc
// @Summary Read a consistent persisted event page; no execution side effects
// @Tags E0
// @Produce json
// @Security AppToken
// @Param id path string true "Task ID"
// @Param after_seq query integer false "Nonnegative decimal safe integer, no greater than last_seq" default(0)
// @Param limit query integer false "Page size 1..100" default(100)
// @Success 200 {object} storage.EventPage
// @Failure 401,403,404,422,503 {object} Error
// @Router /e0/jobs/{id}/events [get]
// @Router /jobs/{id}/events [get]
func (a *API) events(c *gin.Context) {
	n, l, err := cursor(c)
	if err != nil {
		storeError(c, err)
		return
	}
	p, _, err := a.engine.Store.Page(c.Request.Context(), c.Param("id"), n, l)
	if err != nil {
		storeError(c, err)
		return
	}
	c.JSON(200, p)
}

// cancel godoc
// @Summary Explicitly cancel a task (disconnecting observation does not cancel)
// @Tags E0
// @Produce json
// @Security AppToken
// @Param id path string true "Task ID"
// @Success 200 {object} storage.Job
// @Failure 401,403,404,503 {object} Error
// @Router /e0/jobs/{id}/cancel [post]
// @Router /jobs/{id}/cancel [post]
func (a *API) cancel(c *gin.Context) {
	j, err := a.engine.Cancel(c.Request.Context(), c.Param("id"))
	if err != nil {
		storeError(c, err)
		return
	}
	c.JSON(200, j)
}

// stream godoc
// @Summary Observe committed task events; reconnect only reads persisted history
// @Description See events.schema.json and the SSE contract. Lifetime 120s, heartbeat 15s, per-write deadline 10s. No model token streaming.
// @Tags E0
// @Produce text/event-stream
// @Security AppToken
// @Param id path string true "Task ID"
// @Param after_seq query integer false "Last contiguous seq, nonnegative safe integer" default(0)
// @Success 200 {string} string "SSE frames (id=seq, event=type, data=complete event JSON)"
// @Header 200 {integer} X-Subscription-Lifetime-Seconds "120"
// @Success 204 "Terminal job cursor already at event tail"
// @Failure 401,403,404,422,429,503 {object} Error
// @Router /e0/jobs/{id}/events/stream [get]
// @Router /jobs/{id}/events/stream [get]
func (a *API) stream(c *gin.Context) {
	n, _, err := cursor(c)
	if err != nil {
		storeError(c, err)
		return
	}
	p, j, err := a.engine.Store.Page(c.Request.Context(), c.Param("id"), n, 100)
	if err != nil {
		storeError(c, err)
		return
	}
	if storage.Terminal(j.Status) && n == p.LastSeq {
		c.Status(204)
		return
	}
	select {
	case a.streams <- struct{}{}:
		defer func() { <-a.streams }()
	default:
		fail(c, 429, "SUBSCRIPTION_LIMIT", "too many subscriptions")
		return
	}
	controller := http.NewResponseController(c.Writer)
	if err = controller.SetWriteDeadline(time.Now().Add(10 * time.Second)); err != nil {
		fail(c, 503, "STREAM_UNSUPPORTED", "write deadline unsupported")
		return
	}
	c.Header("Content-Type", "text/event-stream; charset=utf-8")
	c.Header("X-Accel-Buffering", "no")
	c.Header("X-Subscription-Lifetime-Seconds", "120")
	c.Status(200)
	if controller.Flush() != nil {
		return
	}
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	poll := time.NewTicker(time.Second)
	defer poll.Stop()
	lifetime := time.NewTimer(120 * time.Second)
	defer lifetime.Stop()
	write := func(frame string) bool {
		if controller.SetWriteDeadline(time.Now().Add(10*time.Second)) != nil {
			return false
		}
		if _, err := fmt.Fprint(c.Writer, frame); err != nil {
			return false
		}
		return controller.Flush() == nil
	}
	for {
		for _, event := range p.Events {
			b, err := json.Marshal(event)
			if err != nil {
				return
			}
			if !write(fmt.Sprintf("id: %d\nevent: %s\ndata: %s\n\n", event.Seq, event.Type, b)) {
				return
			}
			n = event.Seq
		}
		if storage.Terminal(j.Status) && n == p.LastSeq {
			return
		}
		if p.HasMore {
			p, j, err = a.engine.Store.Page(c.Request.Context(), j.ID, n, 100)
			if err != nil {
				return
			}
			continue
		}
		select {
		case <-c.Request.Context().Done():
			return
		case <-lifetime.C:
			return
		case <-heartbeat.C:
			if !write(": heartbeat\n\n") {
				return
			}
		case <-poll.C:
		}
		// All DB rows/transactions are closed before touching the response writer.
		p, j, err = a.engine.Store.Page(c.Request.Context(), j.ID, n, 100)
		if err != nil {
			return
		}
	}
}
