package httpapi_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cfw-policy-llm-agent/server/internal/app"
	"cfw-policy-llm-agent/server/internal/httpapi"
	"cfw-policy-llm-agent/server/internal/storage"
)

func TestStaticSPAHasOnlyBusinessFallbackAndNoPrivateFileAccess(t *testing.T) {
	root := t.TempDir()
	dist := filepath.Join(root, "dist")
	if err := os.MkdirAll(filepath.Join(dist, "assets"), 0700); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dist, "index.html"), []byte("<html>workspace-shell</html>"), 0600)
	os.WriteFile(filepath.Join(dist, "assets", "app.js"), []byte("export const workspace=true"), 0600)
	os.WriteFile(filepath.Join(root, "private.txt"), []byte("private-sentinel"), 0600)
	os.Symlink(filepath.Join(root, "private.txt"), filepath.Join(dist, "assets", "leak.js"))
	store, err := storage.Open(filepath.Join(root, "db.sqlite"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	engine := app.New(store, func(context.Context, storage.Job) error { return nil })
	defer engine.Stop()
	router, err := httpapi.New(engine, testToken, httpapi.Options{WebDir: dist})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(router)
	defer server.Close()
	for _, tc := range []struct {
		path     string
		status   int
		contains string
	}{
		{"/overview", 200, "workspace-shell"}, {"/audits/job_example", 200, "workspace-shell"}, {"/assets/app.js", 200, "workspace=true"},
		{"/assets/leak.js", 404, "NOT_FOUND"}, {"/private.txt", 404, "NOT_FOUND"}, {"/data/private/db.sqlite", 404, "NOT_FOUND"}, {"/unknown-route", 404, "NOT_FOUND"},
		{"/api/v1/missing", 401, "AUTH_REQUIRED"},
	} {
		resp, err := http.Get(server.URL + tc.path)
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != tc.status || !strings.Contains(string(data), tc.contains) || strings.Contains(string(data), "private-sentinel") {
			t.Errorf("%s: %d %s", tc.path, resp.StatusCode, data)
		}
	}
}
