package httpapi

import (
	"cfw-policy-llm-agent/server/internal/storage"
	"github.com/gin-gonic/gin"
)

// draftCurrentBase godoc
// @Summary Read the same draft target at current confirmed heads without editing
// @Tags Confirmation
// @Produce json
// @Security AppToken
// @Param id path string true "Draft ID"
// @Success 200 {object} storage.DraftCurrentBase
// @Failure 404,422 {object} Error
// @Router /profile-drafts/{id}/current-base [get]
func (a *API) draftCurrentBase(c *gin.Context) {
	v, e := a.engine.Store.ProfileDraftCurrentBase(c.Request.Context(), c.Param("id"))
	reply(c, 200, v, e)
}

// rebaseDraft godoc
// @Summary Explicitly rebase the same draft after reviewing selected current heads
// @Tags Confirmation
// @Accept json
// @Produce json
// @Security AppToken
// @Param id path string true "Draft ID"
// @Param Idempotency-Key header string true "Stable edit receipt key"
// @Param request body storage.RebaseDraftRequest true "Reviewed heads, draft revision and explicitly selected changes"
// @Success 200 {object} storage.ProfileDraft
// @Failure 409,422 {object} Error
// @Router /profile-drafts/{id}/rebase [post]
func (a *API) rebaseDraft(c *gin.Context) {
	k, ok := key(c)
	if !ok {
		return
	}
	var req storage.RebaseDraftRequest
	if !decode(c, &req) {
		return
	}
	v, e := a.engine.Store.RebaseProfileDraft(c.Request.Context(), c.Param("id"), req, k)
	reply(c, 200, v, e)
}
