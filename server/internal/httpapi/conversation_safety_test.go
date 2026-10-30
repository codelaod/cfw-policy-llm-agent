package httpapi_test

import (
	"encoding/json"
	"strings"
	"testing"

	"cfw-policy-llm-agent/server/internal/app"
)

func TestRecognizableCredentialsRejectedAcrossFormsAndMixedChatRecovers(t *testing.T) {
	h := newWorkflowHarness(t)
	secret := "sk-RECOGNIZABLE_ARTIFICIAL_123456"
	status, body := h.keyed(t, "/api/v1/profile-drafts", `{"snapshot_id":"`+secret+`","asset_ids":[]}`, "secret-form")
	if status != 422 || !strings.Contains(string(body), "CREDENTIAL_IN_PAYLOAD") {
		t.Fatal("form credential shape was not rejected", status, string(body))
	}
	status, body = h.keyed(t, "/api/v1/conversations", `{"title":"人工凭据过滤","policy_scope_ref":""}`, "create-chat")
	if status != 201 {
		t.Fatal(status, string(body))
	}
	var c struct {
		ID string `json:"id"`
	}
	if e := json.Unmarshal(body, &c); e != nil || c.ID == "" {
		t.Fatal(e, string(body))
	}
	text, _ := json.Marshal(map[string]string{"client_turn_id": "mixed", "text": secret + "；检查杭州名为 web-public 的ECS"})
	status, body = h.keyed(t, "/api/v1/conversations/"+c.ID+"/intent-turns", string(text), "mixed")
	if status != 200 || strings.Contains(string(body), secret) {
		t.Fatal("mixed chat lost safe text or reflected secret", status, string(body))
	}
	status, body = h.request(t, "GET", "/api/v1/conversations/"+c.ID+"/messages", "")
	if status != 200 || strings.Contains(string(body), secret) {
		t.Fatal("secret reached stored messages", status, string(body))
	}
	status, body = h.request(t, "GET", "/api/v1/guidance?conversation_id="+c.ID+"&surface=conversation", "")
	var guidance app.GuidanceCatalog
	if e := json.Unmarshal(body, &guidance); status != 200 || e != nil || guidance.CatalogVersion == "" || len(guidance.Items) == 0 {
		t.Fatal("actual guidance API unavailable", status, e, string(body))
	}
}

func TestTypedBusinessEmptyTextPassesActualHTTPRoute(t *testing.T) {
	h := newWorkflowHarness(t)
	status, body := h.keyed(t, "/api/v1/conversations", `{"title":"typed业务","policy_scope_ref":""}`, "typed-create")
	if status != 201 {
		t.Fatal(status, string(body))
	}
	var c struct {
		ID string `json:"id"`
	}
	if e := json.Unmarshal(body, &c); e != nil {
		t.Fatal(e)
	}
	status, body = h.keyed(t, "/api/v1/conversations/"+c.ID+"/intent-turns", `{"client_turn_id":"typed-read","text":"","business":{"action":"read"}}`, "typed-read")
	if status != 200 || !strings.Contains(string(body), "sealed_asset_target") || strings.Contains(string(body), "INVALID_REQUEST") {
		t.Fatal("typed action was rejected before business routing", status, string(body))
	}
}
