package httpapi

import (
	"cfw-policy-llm-agent/server/internal/storage"
	"errors"
	"github.com/gin-gonic/gin"
)

// resourceGroupPreview godoc
// @Summary Read the latest unconfirmed local resource membership preview
// @Tags Conversations
// @Produce json
// @Security AppToken
// @Param id path string true "Conversation ID"
// @Success 200 {object} storage.ResourceGroupPreview
// @Failure 404,422 {object} Error
// @Router /conversations/{id}/resource-group-preview [get]
func (a *API) resourceGroupPreview(c *gin.Context) {
	if _, e := a.engine.Store.GetConversation(c.Request.Context(), c.Param("id")); e != nil {
		storeError(c, e)
		return
	}
	p, e := a.engine.Store.PendingResourceGroupPreview(c.Request.Context(), c.Param("id"))
	if errors.Is(e, storage.ErrNotFound) {
		c.JSON(200, nil)
		return
	}
	reply(c, 200, p, e)
}
