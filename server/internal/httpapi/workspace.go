package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"

	"cfw-policy-llm-agent/server/internal/app"
	"cfw-policy-llm-agent/server/internal/audit"
	"cfw-policy-llm-agent/server/internal/domain"
	"cfw-policy-llm-agent/server/internal/recheck"
	"cfw-policy-llm-agent/server/internal/storage"
	"github.com/gin-gonic/gin"
)

type Capabilities struct {
	OfflineAvailable bool   `json:"offline_available"`
	WorkerSlots      int    `json:"worker_slots"`
	M1Enabled        bool   `json:"m1_enabled"`
	ModelConfigured  bool   `json:"model_configured"`
	ModelMode        string `json:"model_mode"`
	ModelAvailable   bool   `json:"model_available"`
}
type DataSource struct {
	ID         string `json:"id"`
	Label      string `json:"label"`
	Kind       string `json:"kind"`
	Available  bool   `json:"available"`
	CloudReads bool   `json:"cloud_reads"`
}
type DataSourcePage struct {
	Items []DataSource `json:"items"`
}
type ScopePage struct {
	Items  []domain.Scope `json:"items"`
	Offset int            `json:"offset"`
	Limit  int            `json:"limit"`
}
type SnapshotPage struct {
	Items  []storage.SnapshotRecord `json:"items"`
	Offset int                      `json:"offset"`
	Limit  int                      `json:"limit"`
}
type ConversationPage struct {
	Items  []storage.Conversation `json:"items"`
	Offset int                    `json:"offset"`
	Limit  int                    `json:"limit"`
}
type JobPage struct {
	Items  []storage.Job `json:"items"`
	Offset int           `json:"offset"`
	Limit  int           `json:"limit"`
}
type PolicyPage struct {
	Items  []domain.Policy `json:"items"`
	Offset int             `json:"offset"`
	Limit  int             `json:"limit"`
}
type FindingPage struct {
	Items  []storage.FindingView `json:"items"`
	Offset int                   `json:"offset"`
	Limit  int                   `json:"limit"`
}
type ReportPage struct {
	Items  []storage.ReportView `json:"items"`
	Offset int                  `json:"offset"`
	Limit  int                  `json:"limit"`
}
type ReviewPage struct {
	Items []storage.ReviewAction `json:"items"`
}
type ConversationRequest struct {
	Title          string `json:"title"`
	PolicyScopeRef string `json:"policy_scope_ref"`
}
type WorkspaceRequest struct {
	ConversationID   string `json:"conversation_id"`
	ExpectedRevision int64  `json:"expected_revision"`
}
type StartRequest struct {
	PlanID string `json:"plan_id"`
}
type ReportRequest struct {
	AuditID string `json:"audit_id"`
	Format  string `json:"format"`
}
type RejectRequest struct {
	ExpectedRevision int64 `json:"expected_draft_revision"`
}
type SendResponse struct {
	Job      storage.Job                 `json:"job"`
	Message  storage.ConversationMessage `json:"message"`
	Replayed bool                        `json:"replayed"`
}
type JobResult struct {
	Audit           *storage.AuditRecord `json:"audit"`
	IsFinal         bool                 `json:"is_final"`
	ViewSeq         int64                `json:"view_seq"`
	CompletedPhases []string             `json:"completed_phases"`
	Recheck         *RecheckView         `json:"recheck"`
	SnapshotID      string               `json:"snapshot_id"`
	AgentResult     *storage.AgentRecord `json:"agent_result"`
	AttemptUsage    storage.AttemptUsage `json:"attempt_usage"`
}
type RecheckView struct {
	ID         string             `json:"id"`
	OldAuditID string             `json:"old_audit_id"`
	NewAuditID string             `json:"new_audit_id"`
	CreatedAt  string             `json:"created_at"`
	Comparison recheck.Comparison `json:"comparison"`
}
type EvidenceValue struct {
	domain.Evidence
	Value any `json:"value"`
}
type FindingDetail struct {
	storage.FindingView
	Evidence []EvidenceValue `json:"evidence"`
}

func (a *API) workspaceRoutes(r *gin.Engine) {
	v := r.Group("/api/v1")
	v.GET("/capabilities", a.capabilities)
	v.GET("/data-sources", a.sources)
	v.GET("/policy-scopes", a.scopes)
	v.GET("/workspaces/current", a.workspace)
	v.PATCH("/workspaces/current", a.selectConversation)
	v.GET("/conversations", a.conversations)
	v.POST("/conversations", a.createConversation)
	v.GET("/conversations/:id", a.conversation)
	v.PATCH("/conversations/:id", a.patchConversation)
	v.GET("/conversations/:id/messages", a.messages)
	v.POST("/conversations/:id/messages", a.sendMessage)
	v.GET("/snapshots", a.snapshots)
	v.POST("/snapshots/import", a.importSnapshot)
	v.POST("/snapshots/examples/:name", a.example)
	v.GET("/snapshots/:id", a.snapshot)
	v.GET("/snapshots/:id/assets", a.assets)
	v.GET("/snapshots/:id/assets/:asset_id", a.asset)
	v.GET("/snapshots/:id/policies", a.policies)
	v.POST("/execution-plans", a.previewPlan)
	v.POST("/jobs", a.startJob)
	v.GET("/jobs", a.jobs)
	v.GET("/jobs/:id", a.checkJobKind, a.job)
	v.GET("/jobs/:id/result", a.checkJobKind, a.result)
	v.GET("/jobs/:id/input", a.checkJobKind, a.jobInput)
	v.GET("/jobs/:id/events", a.checkJobKind, a.events)
	v.GET("/jobs/:id/events/stream", a.checkJobKind, a.stream)
	v.POST("/jobs/:id/cancel", a.checkJobKind, a.cancel)
	v.GET("/audits/:id", a.audit)
	v.POST("/audits/:id/rechecks", a.recheck)
	v.GET("/findings", a.findings)
	v.GET("/findings/:id", a.finding)
	v.GET("/findings/:id/review-actions", a.reviews)
	v.POST("/findings/:id/review-actions", a.review)
	v.POST("/profile-drafts", a.createDraft)
	v.GET("/profile-drafts/:id", a.draft)
	v.PATCH("/profile-drafts/:id", a.updateDraft)
	v.POST("/profile-drafts/:id/preview", a.previewDraft)
	v.POST("/profile-drafts/:id/confirm", a.confirmDraft)
	v.POST("/profile-drafts/:id/reject", a.rejectDraft)
	v.GET("/reports", a.reports)
	v.POST("/reports", a.createReport)
	v.GET("/reports/:id", a.report)
	v.GET("/reports/:id/content", a.reportContent)
}

func decode(c *gin.Context, value any) bool {
	d := json.NewDecoder(c.Request.Body)
	d.DisallowUnknownFields()
	if d.Decode(value) != nil {
		fail(c, 422, "INVALID_REQUEST", "bounded JSON object required; unknown fields rejected")
		return false
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		fail(c, 422, "INVALID_REQUEST", "one JSON object required")
		return false
	}
	return true
}
func empty(c *gin.Context) bool {
	if c.Request.ContentLength == 0 {
		return true
	}
	return decode(c, &struct{}{})
}
func key(c *gin.Context) (string, bool) {
	k := c.GetHeader("Idempotency-Key")
	if strings.TrimSpace(k) == "" || len(k) > 128 {
		fail(c, 422, "IDEMPOTENCY_KEY_REQUIRED", "bounded Idempotency-Key header required")
		return "", false
	}
	return k, true
}
func queryInt(c *gin.Context, name string, def, max int) (int, bool) {
	raw := c.DefaultQuery(name, strconv.Itoa(def))
	if raw == "" || len(raw) > 10 {
		fail(c, 422, "INVALID_REQUEST", "invalid pagination")
		return 0, false
	}
	for _, v := range raw {
		if v < '0' || v > '9' {
			fail(c, 422, "INVALID_REQUEST", "invalid pagination")
			return 0, false
		}
	}
	n, e := strconv.Atoi(raw)
	if e != nil || n > max {
		fail(c, 422, "INVALID_REQUEST", "invalid pagination")
		return 0, false
	}
	return n, true
}
func pagination(c *gin.Context) (int, int, bool) {
	o, ok := queryInt(c, "offset", 0, 1000000)
	if !ok {
		return 0, 0, false
	}
	l, ok := queryInt(c, "limit", 100, 100)
	if !ok {
		return 0, 0, false
	}
	if l == 0 {
		fail(c, 422, "INVALID_REQUEST", "limit must be positive")
		return 0, 0, false
	}
	return o, l, true
}
func reply(c *gin.Context, status int, value any, err error) {
	if err != nil {
		storeError(c, err)
		return
	}
	c.JSON(status, value)
}

// capabilities godoc
// @Summary Read configured local capabilities without external calls
// @Tags Workspace
// @Produce json
// @Security AppToken
// @Success 200 {object} Capabilities
// @Failure 401,403 {object} Error
// @Router /capabilities [get]
func (a *API) capabilities(c *gin.Context) {
	c.JSON(200, Capabilities{true, 3, false, a.plans.ModelConfigured || a.plans.ModelReady, a.plans.ModelMode, a.plans.ModelReady})
}

// sources godoc
// @Summary List local configured sources without credential discovery
// @Tags Workspace
// @Produce json
// @Security AppToken
// @Success 200 {object} DataSourcePage
// @Router /data-sources [get]
func (a *API) sources(c *gin.Context) {
	c.JSON(200, DataSourcePage{[]DataSource{{"local-offline", "固定离线资料", "offline", true, false}}})
}

// scopes godoc
// @Summary List locally known policy scopes
// @Tags Workspace
// @Produce json
// @Security AppToken
// @Param offset query int false "Offset" default(0)
// @Param limit query int false "Page size 1..100" default(100)
// @Success 200 {object} ScopePage
// @Router /policy-scopes [get]
func (a *API) scopes(c *gin.Context) {
	o, l, ok := pagination(c)
	if !ok {
		return
	}
	items, e := a.engine.Store.PolicyScopes(c.Request.Context(), o, l)
	reply(c, 200, ScopePage{items, o, l}, e)
}

// workspace godoc
// @Summary Read persistent current conversation
// @Tags Workspace
// @Produce json
// @Security AppToken
// @Success 200 {object} storage.WorkspaceState
// @Router /workspaces/current [get]
func (a *API) workspace(c *gin.Context) {
	v, e := a.engine.Store.CurrentWorkspace(c.Request.Context())
	reply(c, 200, v, e)
}

// selectConversation godoc
// @Summary Select the current conversation with revision check
// @Tags Workspace
// @Accept json
// @Produce json
// @Security AppToken
// @Param request body WorkspaceRequest true "Explicit selection"
// @Success 200 {object} storage.WorkspaceState
// @Failure 409,422 {object} Error
// @Router /workspaces/current [patch]
func (a *API) selectConversation(c *gin.Context) {
	var req WorkspaceRequest
	if !decode(c, &req) {
		return
	}
	v, e := a.engine.Store.SelectCurrentConversation(c.Request.Context(), req.ConversationID, req.ExpectedRevision)
	reply(c, 200, v, e)
}

// conversations godoc
// @Summary List durable conversations
// @Tags Conversations
// @Produce json
// @Security AppToken
// @Param offset query int false "Offset" default(0)
// @Param limit query int false "Page size 1..100" default(100)
// @Success 200 {object} ConversationPage
// @Router /conversations [get]
func (a *API) conversations(c *gin.Context) {
	o, l, ok := pagination(c)
	if !ok {
		return
	}
	v, e := a.engine.Store.ListConversations(c.Request.Context(), o, l)
	reply(c, 200, ConversationPage{v, o, l}, e)
}

// createConversation godoc
// @Summary Create and select a conversation without starting a task
// @Tags Conversations
// @Accept json
// @Produce json
// @Security AppToken
// @Param request body ConversationRequest true "Title and known scope, or empty scope"
// @Success 201 {object} storage.Conversation
// @Failure 422 {object} Error
// @Router /conversations [post]
func (a *API) createConversation(c *gin.Context) {
	var req ConversationRequest
	if !decode(c, &req) {
		return
	}
	v, e := a.engine.Store.CreateConversation(c.Request.Context(), req.Title, req.PolicyScopeRef)
	reply(c, 201, v, e)
}

// conversation godoc
// @Summary Reopen a durable conversation
// @Tags Conversations
// @Produce json
// @Security AppToken
// @Param id path string true "Conversation ID"
// @Success 200 {object} storage.Conversation
// @Router /conversations/{id} [get]
func (a *API) conversation(c *gin.Context) {
	v, e := a.engine.Store.GetConversation(c.Request.Context(), c.Param("id"))
	reply(c, 200, v, e)
}

// patchConversation godoc
// @Summary Edit title, scope or archive state with revision check
// @Tags Conversations
// @Accept json
// @Produce json
// @Security AppToken
// @Param id path string true "Conversation ID"
// @Param request body storage.ConversationPatch true "Revision and changed fields"
// @Success 200 {object} storage.Conversation
// @Failure 409,422 {object} Error
// @Router /conversations/{id} [patch]
func (a *API) patchConversation(c *gin.Context) {
	var req storage.ConversationPatch
	if !decode(c, &req) {
		return
	}
	v, e := a.engine.Store.PatchConversation(c.Request.Context(), c.Param("id"), req)
	reply(c, 200, v, e)
}

// messages godoc
// @Summary Read public messages without replaying model calls
// @Tags Conversations
// @Produce json
// @Security AppToken
// @Param id path string true "Conversation ID"
// @Param after_seq query int false "Last contiguous message seq" default(0)
// @Param limit query int false "Page size 1..100" default(100)
// @Success 200 {object} storage.ConversationMessagePage
// @Router /conversations/{id}/messages [get]
func (a *API) messages(c *gin.Context) {
	n, l, e := cursor(c)
	if e != nil {
		storeError(c, e)
		return
	}
	v, e := a.engine.Store.ConversationMessages(c.Request.Context(), c.Param("id"), n, l)
	reply(c, 200, v, e)
}

// sendMessage godoc
// @Summary Explicitly send a bounded assistant turn, with no cloud reads
// @Tags Conversations
// @Accept json
// @Produce json
// @Security AppToken
// @Param id path string true "Conversation ID"
// @Param request body app.AssistantRequest true "Explicit model acceptance and fixed input"
// @Success 202 {object} SendResponse
// @Success 200 {object} SendResponse "Idempotent replay"
// @Failure 409,422,429,503 {object} Error
// @Router /conversations/{id}/messages [post]
func (a *API) sendMessage(c *gin.Context) {
	var req app.AssistantRequest
	if !decode(c, &req) {
		return
	}
	j, m, replay, e := a.plans.StartAssistant(c.Request.Context(), a.engine, c.Param("id"), req)
	status := 202
	if replay {
		status = 200
	}
	reply(c, status, SendResponse{j, m, replay}, e)
}

// snapshots godoc
// @Summary List immutable local inputs
// @Tags Inputs
// @Produce json
// @Security AppToken
// @Param offset query int false "Offset" default(0)
// @Param limit query int false "Page size 1..100" default(100)
// @Success 200 {object} SnapshotPage
// @Router /snapshots [get]
func (a *API) snapshots(c *gin.Context) {
	o, l, ok := pagination(c)
	if !ok {
		return
	}
	v, e := a.engine.Store.ListSnapshots(c.Request.Context(), o, l)
	reply(c, 200, SnapshotPage{v, o, l}, e)
}

// importSnapshot godoc
// @Summary Import a bounded JSON artifact with unverified imported provenance
// @Tags Inputs
// @Accept json
// @Produce json
// @Security AppToken
// @Param request body domain.Input true "Fixed input, maximum 5 MiB; source is server assigned"
// @Success 201 {object} storage.SnapshotRecord
// @Failure 413,422 {object} Error
// @Router /snapshots/import [post]
func (a *API) importSnapshot(c *gin.Context) {
	data, e := io.ReadAll(c.Request.Body)
	if e != nil {
		fail(c, 422, "INVALID_REQUEST", "input body unavailable")
		return
	}
	v, e := a.audits.Import(c.Request.Context(), data)
	if e != nil {
		if errors.Is(e, audit.ErrInput) || errors.Is(e, audit.ErrUnsupported) || errors.Is(e, storage.ErrInputInvalid) {
			fail(c, 422, "INVALID_IMPORT", "invalid or unsupported input artifact")
		} else {
			storeError(c, e)
		}
		return
	}
	reply(c, 201, v, nil)
}

// example godoc
// @Summary Load a server-owned public synthetic example
// @Tags Inputs
// @Accept json
// @Produce json
// @Security AppToken
// @Param name path string true "internet-in, internet-out or nat-out"
// @Success 201 {object} storage.SnapshotRecord
// @Failure 422 {object} Error
// @Router /snapshots/examples/{name} [post]
func (a *API) example(c *gin.Context) {
	if !empty(c) {
		return
	}
	switch c.Param("name") {
	case "internet-in", "internet-out", "nat-out":
	default:
		fail(c, 422, "INVALID_REQUEST", "unknown public example")
		return
	}
	v, e := a.audits.LoadExample(c.Request.Context(), c.Param("name"))
	reply(c, 201, v, e)
}

// snapshot godoc
// @Summary Read one immutable input
// @Tags Inputs
// @Produce json
// @Security AppToken
// @Param id path string true "Snapshot ID"
// @Success 200 {object} storage.SnapshotRecord
// @Router /snapshots/{id} [get]
func (a *API) snapshot(c *gin.Context) {
	v, e := a.engine.Store.GetSnapshot(c.Request.Context(), c.Param("id"))
	reply(c, 200, v, e)
}

// assets godoc
// @Summary Read assets with declared and locally confirmed business context separated
// @Tags Inputs
// @Produce json
// @Security AppToken
// @Param id path string true "Snapshot ID"
// @Param offset query int false "Offset" default(0)
// @Param limit query int false "Page size 1..100" default(100)
// @Success 200 {object} app.AssetViewPage
// @Router /snapshots/{id}/assets [get]
func (a *API) assets(c *gin.Context) {
	o, l, ok := pagination(c)
	if !ok {
		return
	}
	v, e := a.confirmations.ReadAssets(c.Request.Context(), c.Param("id"), o, l)
	reply(c, 200, v, e)
}

// asset godoc
// @Summary Read a selected asset and current local confirmation versions
// @Tags Inputs
// @Produce json
// @Security AppToken
// @Param id path string true "Snapshot ID"
// @Param asset_id path string true "Asset ID"
// @Success 200 {object} app.AssetView
// @Router /snapshots/{id}/assets/{asset_id} [get]
func (a *API) asset(c *gin.Context) {
	v, e := a.confirmations.ReadAssetContext(c.Request.Context(), c.Param("id"), c.Param("asset_id"))
	reply(c, 200, v, e)
}

// policies godoc
// @Summary Read fixed policy configuration evidence
// @Tags Inputs
// @Produce json
// @Security AppToken
// @Param id path string true "Snapshot ID"
// @Param offset query int false "Offset" default(0)
// @Param limit query int false "Page size 1..100" default(100)
// @Success 200 {object} PolicyPage
// @Router /snapshots/{id}/policies [get]
func (a *API) policies(c *gin.Context) {
	o, l, ok := pagination(c)
	if !ok {
		return
	}
	v, e := a.engine.Store.GetSnapshot(c.Request.Context(), c.Param("id"))
	if e != nil {
		storeError(c, e)
		return
	}
	items := []domain.Policy{}
	if o < len(v.Input.Policies) {
		end := min(o+l, len(v.Input.Policies))
		items = append(items, v.Input.Policies[o:end]...)
	}
	c.JSON(200, PolicyPage{items, o, l})
}

// previewPlan godoc
// @Summary Preview and freeze bounded effects without starting execution
// @Tags Execution
// @Accept json
// @Produce json
// @Security AppToken
// @Param request body app.PlanRequest true "Fixed offline scope and selection"
// @Success 201 {object} app.PlanPreview
// @Failure 409,422 {object} Error
// @Router /execution-plans [post]
func (a *API) previewPlan(c *gin.Context) {
	var req app.PlanRequest
	if !decode(c, &req) {
		return
	}
	v, e := a.plans.Preview(c.Request.Context(), req)
	reply(c, 201, v, e)
}

// startJob godoc
// @Summary Accept a previously previewed plan
// @Tags Execution
// @Accept json
// @Produce json
// @Security AppToken
// @Param Idempotency-Key header string true "Stable key retained on unknown response"
// @Param request body StartRequest true "Only plan_id is accepted"
// @Success 202 {object} storage.Job
// @Success 200 {object} storage.Job "Idempotent replay"
// @Failure 409,422,429,503 {object} Error
// @Router /jobs [post]
func (a *API) startJob(c *gin.Context) {
	var req StartRequest
	if !decode(c, &req) {
		return
	}
	k, ok := key(c)
	if !ok {
		return
	}
	j, replay, e := a.engine.EnqueuePlan(c.Request.Context(), req.PlanID, k, a.plans.ConfigRevision)
	status := 202
	if replay {
		status = 200
	}
	reply(c, status, j, e)
}

// jobs godoc
// @Summary List bounded persisted tasks
// @Tags Execution
// @Produce json
// @Security AppToken
// @Param conversation_id query string false "Conversation filter"
// @Param offset query int false "Offset" default(0)
// @Param limit query int false "Page size 1..100" default(100)
// @Success 200 {object} JobPage
// @Router /jobs [get]
func (a *API) jobs(c *gin.Context) {
	o, l, ok := pagination(c)
	if !ok {
		return
	}
	v, e := a.engine.Store.ListBusinessJobs(c.Request.Context(), c.Query("conversation_id"), o, l)
	reply(c, 200, JobPage{v, o, l}, e)
}

// result godoc
// @Summary Read saved rule/Agent results, attempt usage and finality from one database snapshot
// @Tags Execution
// @Produce json
// @Security AppToken
// @Param id path string true "Job ID"
// @Success 200 {object} JobResult
// @Router /jobs/{id}/result [get]
func (a *API) result(c *gin.Context) {
	view, e := a.engine.Store.ReadJobWorkspace(c.Request.Context(), c.Param("id"))
	phases := []string{}
	if view.InputSealed {
		phases = append(phases, "L1")
	}
	if view.Audit != nil {
		phases = append(phases, "L2")
	}
	if view.AgentResult != nil && view.AgentResult.Content.Output != nil && view.AgentResult.Content.ErrorCode == "" {
		phases = append(phases, "L3")
	}
	if view.ReportPublished {
		phases = append(phases, "L4")
	}
	var comparison *RecheckView
	if e == nil && view.Recheck != nil {
		comparison = &RecheckView{ID: view.Recheck.ID, OldAuditID: view.Recheck.OldAuditID, NewAuditID: view.Recheck.NewAuditID, CreatedAt: view.Recheck.CreatedAt}
		e = json.Unmarshal(view.Recheck.Comparison, &comparison.Comparison)
	}
	reply(c, 200, JobResult{Audit: view.Audit, IsFinal: storage.Terminal(view.Job.Status), ViewSeq: view.Job.ViewSeq, CompletedPhases: phases, Recheck: comparison, SnapshotID: view.SnapshotID, AgentResult: view.AgentResult, AttemptUsage: view.AttemptUsage}, e)
}

// jobInput godoc
// @Summary Read the immutable effective input sealed for this task
// @Description Protected local view only. Never substitutes current snapshot declarations or business heads. Unsealed input returns INPUT_NOT_SEALED.
// @Tags Execution
// @Produce json
// @Security AppToken
// @Param id path string true "Formal job ID"
// @Success 200 {object} domain.Input
// @Failure 401,403,404,409,503 {object} Error
// @Router /jobs/{id}/input [get]
func (a *API) jobInput(c *gin.Context) {
	input, err := a.engine.Store.GetJobInput(c.Request.Context(), c.Param("id"))
	reply(c, 200, input, err)
}

// audit godoc
// @Summary Read an immutable audit result and its sealed effective input
// @Tags Findings
// @Produce json
// @Security AppToken
// @Param id path string true "Audit result ID"
// @Success 200 {object} storage.AuditRecord
// @Router /audits/{id} [get]
func (a *API) audit(c *gin.Context) {
	v, e := a.engine.Store.GetAudit(c.Request.Context(), c.Param("id"))
	reply(c, 200, v, e)
}

// recheck godoc
// @Summary Preview a new audit linked to the old job; explicit plan acceptance still required
// @Tags Execution
// @Accept json
// @Produce json
// @Security AppToken
// @Param id path string true "Old audit job ID"
// @Param request body app.RecheckRequest true "New fixed snapshot and evaluation time policy"
// @Success 201 {object} app.PlanPreview
// @Failure 409,422 {object} Error
// @Router /audits/{id}/rechecks [post]
func (a *API) recheck(c *gin.Context) {
	var req app.RecheckRequest
	if !decode(c, &req) {
		return
	}
	v, e := a.plans.RecheckPreview(c.Request.Context(), c.Param("id"), req)
	reply(c, 201, v, e)
}

// findings godoc
// @Summary List original findings with durable review heads
// @Tags Findings
// @Produce json
// @Security AppToken
// @Param audit_id query string false "Audit result filter"
// @Param offset query int false "Offset" default(0)
// @Param limit query int false "Page size 1..100" default(100)
// @Success 200 {object} FindingPage
// @Router /findings [get]
func (a *API) findings(c *gin.Context) {
	o, l, ok := pagination(c)
	if !ok {
		return
	}
	v, e := a.engine.Store.ListFindingViews(c.Request.Context(), c.Query("audit_id"), o, l)
	reply(c, 200, FindingPage{v, o, l}, e)
}

// finding godoc
// @Summary Read evidence values from this audit's fixed effective input
// @Tags Findings
// @Produce json
// @Security AppToken
// @Param id path string true "Finding record ID"
// @Success 200 {object} FindingDetail
// @Router /findings/{id} [get]
func (a *API) finding(c *gin.Context) {
	v, run, e := a.engine.Store.ReadFindingView(c.Request.Context(), c.Param("id"))
	if e != nil {
		storeError(c, e)
		return
	}
	data, e := json.Marshal(run.Input)
	if e != nil {
		storeError(c, e)
		return
	}
	var tree any
	if e = json.Unmarshal(data, &tree); e != nil {
		storeError(c, e)
		return
	}
	refs := map[string]bool{}
	for _, id := range v.Finding.EvidenceRefs {
		refs[id] = true
	}
	values := []EvidenceValue{}
	for _, evidence := range run.Result.Evidence {
		if refs[evidence.ID] {
			value, ok := jsonPointer(tree, evidence.Field)
			if !ok {
				fail(c, 503, "EVIDENCE_UNAVAILABLE", "fixed evidence value is unavailable")
				return
			}
			values = append(values, EvidenceValue{evidence, value})
		}
	}
	c.JSON(200, FindingDetail{v, values})
}
func jsonPointer(value any, pointer string) (any, bool) {
	if pointer == "" {
		return value, true
	}
	if !strings.HasPrefix(pointer, "/") {
		return nil, false
	}
	for _, raw := range strings.Split(pointer[1:], "/") {
		var b strings.Builder
		for i := 0; i < len(raw); i++ {
			if raw[i] != '~' {
				b.WriteByte(raw[i])
				continue
			}
			i++
			if i >= len(raw) {
				return nil, false
			}
			switch raw[i] {
			case '0':
				b.WriteByte('~')
			case '1':
				b.WriteByte('/')
			default:
				return nil, false
			}
		}
		part := b.String()
		switch node := value.(type) {
		case map[string]any:
			var ok bool
			value, ok = node[part]
			if !ok {
				return nil, false
			}
		case []any:
			if len(part) == 0 || (len(part) > 1 && part[0] == '0') {
				return nil, false
			}
			n, e := strconv.Atoi(part)
			if e != nil || n < 0 || n >= len(node) {
				return nil, false
			}
			value = node[n]
		default:
			return nil, false
		}
	}
	return value, true
}

// reviews godoc
// @Summary Read immutable review actions
// @Tags Findings
// @Produce json
// @Security AppToken
// @Param id path string true "Finding record ID"
// @Param after_revision query int false "Revision cursor" default(0)
// @Param limit query int false "Page size 1..100" default(100)
// @Success 200 {object} ReviewPage
// @Router /findings/{id}/review-actions [get]
func (a *API) reviews(c *gin.Context) {
	n, ok := queryInt(c, "after_revision", 0, 1000000)
	if !ok {
		return
	}
	l, ok := queryInt(c, "limit", 100, 100)
	if !ok || l == 0 {
		if ok {
			fail(c, 422, "INVALID_REQUEST", "limit must be positive")
		}
		return
	}
	v, e := a.engine.Store.ReviewHistory(c.Request.Context(), c.Param("id"), int64(n), l)
	reply(c, 200, ReviewPage{v}, e)
}

// review godoc
// @Summary Append a review with CAS and idempotency
// @Tags Findings
// @Accept json
// @Produce json
// @Security AppToken
// @Param id path string true "Finding record ID"
// @Param Idempotency-Key header string true "Stable review operation key"
// @Param request body storage.ReviewRequest true "Expected revision and action"
// @Success 201 {object} storage.ReviewAction
// @Failure 409,422 {object} Error
// @Router /findings/{id}/review-actions [post]
func (a *API) review(c *gin.Context) {
	var req storage.ReviewRequest
	if !decode(c, &req) {
		return
	}
	k, ok := key(c)
	if !ok {
		return
	}
	req.ClientRequestID = k
	v, e := a.audits.Review(c.Request.Context(), c.Param("id"), req)
	reply(c, 201, v, e)
}

// createDraft godoc
// @Summary Create a business confirmation draft for explicit assets
// @Tags Confirmation
// @Accept json
// @Produce json
// @Security AppToken
// @Param request body storage.CreateDraftRequest true "Fixed snapshot and asset IDs"
// @Success 201 {object} storage.ProfileDraft
// @Router /profile-drafts [post]
func (a *API) createDraft(c *gin.Context) {
	var req storage.CreateDraftRequest
	if !decode(c, &req) {
		return
	}
	v, e := a.confirmations.CreateDraft(c.Request.Context(), req)
	reply(c, 201, v, e)
}

// draft godoc
// @Summary Reload a durable business draft
// @Tags Confirmation
// @Produce json
// @Security AppToken
// @Param id path string true "Draft ID"
// @Success 200 {object} storage.ProfileDraft
// @Router /profile-drafts/{id} [get]
func (a *API) draft(c *gin.Context) {
	v, e := a.confirmations.GetDraft(c.Request.Context(), c.Param("id"))
	reply(c, 200, v, e)
}

// updateDraft godoc
// @Summary Save draft changes with revision check
// @Tags Confirmation
// @Accept json
// @Produce json
// @Security AppToken
// @Param id path string true "Draft ID"
// @Param request body storage.UpdateDraftRequest true "Expected revision and explicit changes"
// @Success 200 {object} storage.ProfileDraft
// @Failure 409,422 {object} Error
// @Router /profile-drafts/{id} [patch]
func (a *API) updateDraft(c *gin.Context) {
	var req storage.UpdateDraftRequest
	if !decode(c, &req) {
		return
	}
	v, e := a.confirmations.UpdateDraft(c.Request.Context(), c.Param("id"), req)
	reply(c, 200, v, e)
}

// previewDraft godoc
// @Summary Preview normalized differences without confirming any section
// @Tags Confirmation
// @Produce json
// @Security AppToken
// @Param id path string true "Draft ID"
// @Success 200 {object} storage.ConfirmationPreview
// @Router /profile-drafts/{id}/preview [post]
func (a *API) previewDraft(c *gin.Context) {
	if !empty(c) {
		return
	}
	v, e := a.confirmations.Preview(c.Request.Context(), c.Param("id"))
	reply(c, 200, v, e)
}

// confirmDraft godoc
// @Summary Atomically confirm only explicitly selected business sections
// @Tags Confirmation
// @Accept json
// @Produce json
// @Security AppToken
// @Param id path string true "Draft ID"
// @Param Idempotency-Key header string true "Stable confirmation key"
// @Param request body storage.ConfirmDraftRequest true "Preview identity, revision and selected sections"
// @Success 200 {object} storage.ConfirmationResult
// @Failure 409,422 {object} Error
// @Router /profile-drafts/{id}/confirm [post]
func (a *API) confirmDraft(c *gin.Context) {
	var req storage.ConfirmDraftRequest
	if !decode(c, &req) {
		return
	}
	k, ok := key(c)
	if !ok {
		return
	}
	v, e := a.confirmations.Confirm(c.Request.Context(), c.Param("id"), req, k)
	reply(c, 200, v, e)
}

// rejectDraft godoc
// @Summary Reject a candidate while preserving old confirmed versions
// @Tags Confirmation
// @Accept json
// @Produce json
// @Security AppToken
// @Param id path string true "Draft ID"
// @Param Idempotency-Key header string true "Stable rejection key"
// @Param request body RejectRequest true "Expected draft revision"
// @Success 200 {object} storage.ConfirmationResult
// @Failure 409,422 {object} Error
// @Router /profile-drafts/{id}/reject [post]
func (a *API) rejectDraft(c *gin.Context) {
	var req RejectRequest
	if !decode(c, &req) {
		return
	}
	k, ok := key(c)
	if !ok {
		return
	}
	v, e := a.confirmations.Reject(c.Request.Context(), c.Param("id"), req.ExpectedRevision, k)
	reply(c, 200, v, e)
}

// reports godoc
// @Summary List fixed public report versions
// @Tags Reports
// @Produce json
// @Security AppToken
// @Param audit_id query string false "Audit result filter"
// @Param offset query int false "Offset" default(0)
// @Param limit query int false "Page size 1..100" default(100)
// @Success 200 {object} ReportPage
// @Router /reports [get]
func (a *API) reports(c *gin.Context) {
	o, l, ok := pagination(c)
	if !ok {
		return
	}
	v, e := a.engine.Store.ReportPage(c.Request.Context(), c.Query("audit_id"), o, l)
	reply(c, 200, ReportPage{v, o, l}, e)
}

// createReport godoc
// @Summary Create a fixed public projection from saved results and review heads
// @Tags Reports
// @Accept json
// @Produce json
// @Security AppToken
// @Param Idempotency-Key header string true "Stable report operation key"
// @Param request body ReportRequest true "Audit ID and json or markdown format"
// @Success 201 {object} storage.ReportView
// @Failure 409,422 {object} Error
// @Router /reports [post]
func (a *API) createReport(c *gin.Context) {
	var req ReportRequest
	if !decode(c, &req) {
		return
	}
	if req.Format != "json" && req.Format != "markdown" {
		fail(c, 422, "INVALID_REQUEST", "unsupported report format")
		return
	}
	k, ok := key(c)
	if !ok {
		return
	}
	saved, e := a.audits.Report(c.Request.Context(), req.AuditID, req.Format, k)
	if e != nil {
		storeError(c, e)
		return
	}
	v, e := a.engine.Store.ReportView(c.Request.Context(), saved.ID)
	reply(c, 201, v, e)
}

// report godoc
// @Summary Read fixed report metadata and its originating job if present
// @Tags Reports
// @Produce json
// @Security AppToken
// @Param id path string true "Report ID"
// @Success 200 {object} storage.ReportView
// @Router /reports/{id} [get]
func (a *API) report(c *gin.Context) {
	v, e := a.engine.Store.ReportView(c.Request.Context(), c.Param("id"))
	reply(c, 200, v, e)
}

// reportContent godoc
// @Summary Download a fixed public report with application access protection
// @Tags Reports
// @Produce application/json,text/markdown
// @Security AppToken
// @Param id path string true "Report ID"
// @Success 200 {file} file
// @Router /reports/{id}/content [get]
func (a *API) reportContent(c *gin.Context) {
	v, e := a.engine.Store.GetReport(c.Request.Context(), c.Param("id"))
	if e != nil {
		storeError(c, e)
		return
	}
	contentType, ext := "application/json; charset=utf-8", "json"
	if v.Format == "markdown" {
		contentType, ext = "text/markdown; charset=utf-8", "md"
	}
	c.Header("Content-Disposition", `attachment; filename="audit-report.`+ext+`"`)
	c.Data(200, contentType, v.Content)
}
