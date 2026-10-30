package httpapi

import (
	"cfw-policy-llm-agent/server/internal/storage"
	"errors"
	"github.com/gin-gonic/gin"
)

// conversationBusinessContext godoc
// @Summary Read the current target's shared business draft and evidence candidates
// @Tags Conversations
// @Produce json
// @Security AppToken
// @Param id path string true "Conversation ID"
// @Success 200 {object} storage.ConversationBusinessContext
// @Failure 404,422 {object} Error
// @Router /conversations/{id}/business-context [get]
func (a *API) conversationBusinessContext(c *gin.Context) {
	out, e := a.engine.Store.ConversationBusinessContext(c.Request.Context(), c.Param("id"))
	if errors.Is(e, storage.ErrBusinessTargetUnresolved) {
		c.JSON(200, nil)
		return
	}
	reply(c, 200, out, e)
}
