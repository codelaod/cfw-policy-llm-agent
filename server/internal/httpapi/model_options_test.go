package httpapi_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"cfw-policy-llm-agent/server/internal/app"
	"cfw-policy-llm-agent/server/internal/audit"
	"cfw-policy-llm-agent/server/internal/httpapi"
	"cfw-policy-llm-agent/server/internal/storage"
)

func TestE3ConfiguredServerSecretsCannotEnterBusinessOrPublicMetadata(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "options.sqlite"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	engine := app.New(store, nil)
	defer engine.Stop()
	plans := app.NewPlanService(store)
	plans.ModelReady = true
	plans.ModelConfigured = true
	plans.ModelMode = "chat_completions"
	private := []string{"synthetic-private-model-key", "https://private.example.invalid/v1"}
	router, err := httpapi.New(engine, testToken, httpapi.Options{Plans: plans, SensitiveValues: private})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(router)
	defer server.Close()
	for _, secret := range private {
		req, _ := http.NewRequest("POST", server.URL+"/api/v1/conversations", strings.NewReader(`{"title":"`+secret+`","policy_scope_ref":""}`))
		req.Header.Set("X-App-Token", testToken)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		assertError(t, res.StatusCode, body, 422, "CREDENTIAL_IN_PAYLOAD")
	}
	req, _ := http.NewRequest("GET", server.URL+"/api/v1/capabilities", nil)
	req.Header.Set("X-App-Token", testToken)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if !strings.Contains(string(body), `"model_configured":true`) || !strings.Contains(string(body), `"model_available":true`) {
		t.Fatal("server-wired model state not visible")
	}
	for _, secret := range private {
		if strings.Contains(string(body), secret) {
			t.Fatal("capabilities leaked private connection values")
		}
	}
	items, err := store.ListConversations(context.Background(), 0, 100)
	if err != nil || len(items) != 0 {
		t.Fatal("rejected secret-bearing requests were saved")
	}
}

func TestKnownNumericCredentialCannotEnterImportedConditions(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "numeric-secret.sqlite"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	const secret = "3141592653589793"
	router, err := httpapi.New(app.New(store, nil), testToken, httpapi.Options{SensitiveValues: []string{secret}})
	if err != nil {
		t.Fatal(err)
	}
	in, err := audit.SyntheticExample("internet-in")
	if err != nil {
		t.Fatal(err)
	}
	in.Policies[0].Conditions = map[string]json.RawMessage{"FutureMatch": json.RawMessage(secret)}
	body, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/snapshots/import", strings.NewReader(string(body)))
	req.Host = "127.0.0.1"
	req.Header.Set("X-App-Token", testToken)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	assertError(t, rec.Code, rec.Body.Bytes(), 422, "CREDENTIAL_IN_PAYLOAD")
	snapshots, err := store.ListSnapshots(context.Background(), 0, 100)
	if err != nil || len(snapshots) != 0 {
		t.Fatal("numeric credential was saved in business input", err)
	}
}
