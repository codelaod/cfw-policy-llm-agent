package httpapi

import (
	"strconv"

	"cfw-policy-llm-agent/server/internal/app"
	"cfw-policy-llm-agent/server/internal/domain"
	"cfw-policy-llm-agent/server/internal/storage"
	"github.com/gin-gonic/gin"
)

type IntentResponse struct {
	Group *storage.ResourceGroupResult `json:"group,omitempty"`

	Business *storage.ConversationBusinessResult `json:"business,omitempty"`

	Conversation storage.Conversation         `json:"conversation"`
	Message      *storage.ConversationMessage `json:"message"`
	Reply        *storage.ConversationMessage `json:"reply"`
	Intent       *domain.ConversationIntent   `json:"intent"`
	Proposal     *domain.ActionProposal       `json:"proposal,omitempty"`
	Job          *storage.Job                 `json:"job,omitempty"`
	Replayed     bool                         `json:"replayed"`
}

func intentResponse(r storage.IntentTurnResult) IntentResponse {
	out := IntentResponse{Group: r.Group, Business: r.Business, Conversation: r.Conversation, Message: r.Message, Reply: r.Reply, Proposal: r.Proposal, Job: r.Job, Replayed: r.Replayed}
	if r.Intent.ID != "" {
		out.Intent = &r.Intent
	}
	return out
}

// currentIntent godoc
// @Summary Reopen a durable customer goal without calling cloud or model
// @Tags Conversations
// @Produce json
// @Security AppToken
// @Param id path string true "Conversation ID"
// @Success 200 {object} domain.ConversationIntent
// @Failure 404 {object} Error
// @Router /conversations/{id}/intents/current [get]
func (a *API) currentIntent(c *gin.Context) {
	in, e := a.intents.Current(c.Request.Context(), c.Param("id"))
	if e != nil {
		storeError(c, e)
		return
	}
	if in.ID == "" {
		c.JSON(200, nil)
		return
	}
	c.JSON(200, in)
}

// intentTurn godoc
// @Summary Persist a customer goal, clarification and local execution preview
// @Tags Conversations
// @Accept json
// @Produce json
// @Security AppToken
// @Param id path string true "Conversation ID"
// @Param Idempotency-Key header string true "Stable request key"
// @Param request body app.IntentTurnRequest true "Natural goal; no snapshot required"
// @Success 200 {object} IntentResponse
// @Failure 409,422 {object} Error
// @Router /conversations/{id}/intent-turns [post]
func (a *API) intentTurn(c *gin.Context) {
	k, ok := key(c)
	if !ok {
		return
	}
	var req app.IntentTurnRequest
	if !decode(c, &req) {
		return
	}
	out, e := a.intents.Turn(c.Request.Context(), c.Param("id"), k, req)
	reply(c, 200, intentResponse(out), e)
}

// intentChoice godoc
// @Summary Select current server-issued candidates with revision checking
// @Tags Conversations
// @Accept json
// @Produce json
// @Security AppToken
// @Param id path string true "Conversation ID"
// @Param Idempotency-Key header string true "Stable request key"
// @Param request body app.IntentChoiceRequest true "Current intent and candidate references"
// @Success 200 {object} IntentResponse
// @Failure 409,422 {object} Error
// @Router /conversations/{id}/intent-choices [post]
func (a *API) intentChoice(c *gin.Context) {
	k, ok := key(c)
	if !ok {
		return
	}
	var req app.IntentChoiceRequest
	if !decode(c, &req) {
		return
	}
	out, e := a.intents.Choose(c.Request.Context(), c.Param("id"), k, req)
	reply(c, 200, intentResponse(out), e)
}

// acceptIntentProposal godoc
// @Summary Accept the current goal proposal atomically and enqueue one job
// @Tags Conversations
// @Accept json
// @Produce json
// @Security AppToken
// @Param id path string true "Conversation ID"
// @Param proposal_id path string true "Proposal ID"
// @Param Idempotency-Key header string true "Stable execution key"
// @Param request body app.IntentAcceptRequest true "Expected goal revision"
// @Success 202 {object} storage.IntentAcceptResult
// @Failure 409,422 {object} Error
// @Router /conversations/{id}/proposals/{proposal_id}/accept [post]
func (a *API) acceptIntentProposal(c *gin.Context) {
	k, ok := key(c)
	if !ok {
		return
	}
	var req app.IntentAcceptRequest
	if !decode(c, &req) {
		return
	}
	out, e := a.engine.AcceptIntentProposal(c.Request.Context(), c.Param("id"), c.Param("proposal_id"), k, a.plans.ConfigRevision, req.ExpectedIntentRevision)
	status := 202
	if out.Replayed {
		status = 200
	}
	reply(c, status, out, e)
}

// resources godoc
// @Summary Search source-qualified local resource metadata with stable cursors
// @Tags Inputs
// @Produce json
// @Security AppToken
// @Param name query string false "Exact resource name"
// @Param resource_id query string false "Provider or explicit local artifact identifier"
// @Param region query string false "Resource region"
// @Param resource_type query string false "Resource kind"
// @Param source_ref query string false "Configured or local source"
// @Param account_ref query string false "Account reference"
// @Param cursor query string false "Source/filter/revision bound cursor"
// @Param limit query int false "Page size 1..100" default(50)
// @Success 200 {object} storage.ResourcePage
// @Failure 422 {object} Error
// @Router /resources [get]
func (a *API) resources(c *gin.Context) {
	limit := 50
	if v := c.Query("limit"); v != "" {
		n, e := strconv.Atoi(v)
		if e != nil {
			storeError(c, storage.ErrInputInvalid)
			return
		}
		limit = n
	}
	f := storage.ResourceFilter{Name: c.Query("name"), ResourceID: c.Query("resource_id"), Region: c.Query("region"), ResourceType: c.Query("resource_type"), SourceRef: c.Query("source_ref"), AccountRef: c.Query("account_ref"), Group: c.Query("group")}
	p, e := a.engine.Store.SearchResources(c.Request.Context(), f, c.Query("cursor"), limit)
	reply(c, 200, p, e)
}
