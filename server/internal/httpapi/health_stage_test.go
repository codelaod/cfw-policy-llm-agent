package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"cfw-policy-llm-agent/server/internal/app"
	"cfw-policy-llm-agent/server/internal/httpapi"
	"cfw-policy-llm-agent/server/internal/storage"
)

func TestHealthReportsImplementedE3Stage(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "health.sqlite"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	engine := app.New(store, func(context.Context, storage.Job) error { return nil })
	router, err := httpapi.New(engine, testToken)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	req.Host = "127.0.0.1"
	req.Header.Set("X-App-Token", testToken)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	var health httpapi.Health
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &health) != nil || health.Stage != "E3" || health.Source != "local-offline" || health.Status != "ok" {
		t.Fatalf("health does not report the implemented offline stage: %d %s", rec.Code, rec.Body.String())
	}
}
