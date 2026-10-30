package httpapi

import "github.com/gin-gonic/gin"

// guidance godoc
// @Summary Read local contextual next steps shared by conversation and views
// @Tags Conversations
// @Produce json
// @Security AppToken
// @Param conversation_id query string false "Durable conversation reference"
// @Param job_id query string false "Selected task reference"
// @Param report_id query string false "Fixed report reference"
// @Param surface query string false "conversation,overview,profile,audit,finding,report"
// @Success 200 {object} app.GuidanceCatalog
// @Failure 404,422 {object} Error
// @Router /guidance [get]
func (a *API) guidance(c *gin.Context) {
	out, e := a.intents.Guidance(c.Request.Context(), c.Query("conversation_id"), c.Query("job_id"), c.Query("report_id"), c.Query("surface"))
	reply(c, 200, out, e)
}
