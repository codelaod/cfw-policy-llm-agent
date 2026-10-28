package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"cfw-policy-llm-agent/server/internal/aliyuncollector"
	"cfw-policy-llm-agent/server/internal/app"
	"cfw-policy-llm-agent/server/internal/audit"
	"cfw-policy-llm-agent/server/internal/httpapi"
	"cfw-policy-llm-agent/server/internal/storage"
)

func TestConfiguredCloudCatalogAndOptionsAreProtectedLocalReads(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "cloud-options.sqlite"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	in, err := audit.SyntheticExample("internet-in")
	if err != nil {
		t.Fatal(err)
	}
	plans := app.NewPlanService(store)
	plans.Cloud = &app.CloudService{Store: store, SourceRef: "explicit-cloud", ConfigRevision: plans.ConfigRevision, Configured: true, Ready: true, MaxHTTPAttempts: 20, Scopes: map[string]aliyuncollector.Config{in.Scope.Ref: {Scope: in.Scope, AssetRegions: map[string]string{"i-allowed": "cn-hangzhou"}}}, NewReader: func(context.Context, string) (app.CloudReader, error) {
		t.Fatal("read-only catalog initialized a remote reader")
		return nil, nil
	}}
	router, err := httpapi.New(app.New(store, nil), testToken, httpapi.Options{Plans: plans})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/v1/data-sources", "/api/v1/data-sources/explicit-cloud/collection-options", "/api/v1/capabilities"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Host = "127.0.0.1"
		req.Header.Set("X-App-Token", testToken)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("%s returned %d %s", path, rec.Code, rec.Body.String())
		}
		if strings.Contains(path, "collection-options") {
			var o app.CollectionOptions
			if json.Unmarshal(rec.Body.Bytes(), &o) != nil || o.IdentityVerified || !o.Available || len(o.Scopes) != 1 || o.Scopes[0].Assets[0].ID != "i-allowed" {
				t.Fatal("local collection options changed or claimed identity", rec.Body.String())
			}
		}
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/data-sources/explicit-cloud/collection-options", nil)
	req.Host = "127.0.0.1"
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Fatal("cloud options bypassed application authentication")
	}
}
