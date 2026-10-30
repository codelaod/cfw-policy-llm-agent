package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cfw-policy-llm-agent/server/internal/app"
	"cfw-policy-llm-agent/server/internal/audit"
	"cfw-policy-llm-agent/server/internal/httpapi"
	"cfw-policy-llm-agent/server/internal/storage"
)

func TestIndependentOpaqueAssetSPADeepLinkSurvivesDirectOpen(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<html>public-workspace-shell</html>"), 0600); err != nil {
		t.Fatal(err)
	}
	store, err := storage.Open(filepath.Join(t.TempDir(), "public.sqlite"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	engine := app.New(store, nil)
	defer engine.Stop()
	router, err := httpapi.New(engine, testToken, httpapi.Options{WebDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"asset-plain", "vm.web.0", "vm/web 人工 case"} {
		t.Run(id, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/inventory/"+url.PathEscape(id)+"?snapshot_id=snapshot_public", nil)
			request.Host = "127.0.0.1"
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != 200 || !strings.Contains(response.Body.String(), "public-workspace-shell") {
				t.Fatalf("valid opaque inventory deep link cannot reopen the SPA: id=%q status=%d body=%s", id, response.Code, response.Body.String())
			}
		})
	}
}

func TestIndependentOpaquePathPlusDoesNotAliasSpaceObject(t *testing.T) {
	h := newWorkflowHarness(t)
	in, err := audit.SyntheticExample("internet-in")
	if err != nil {
		t.Fatal(err)
	}
	const plus = "vm+web/人工"
	const space = "vm web/人工"
	one, two := in.Assets[0], in.Assets[0]
	one.ID, two.ID = plus, space
	in.Assets = append(in.Assets[:0:0], one, two)
	in.Selection.AssetIDs = []string{plus, space}
	snapshot, err := h.engine.Store.SaveSnapshot(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	status, body := h.request(t, "GET", "/api/v1/snapshots/"+snapshot.ID+"/assets/"+url.PathEscape(plus), "")
	var view app.AssetView
	if err = json.Unmarshal(body, &view); err != nil || status != 200 || view.Asset.ID != plus || view.DeclaredAsset.ID != plus {
		t.Fatalf("opaque path '+' was decoded as another object's space: requested=%q escaped=%q status=%d got=%q err=%v", plus, url.PathEscape(plus), status, view.Asset.ID, err)
	}
}

func TestIndependentOpaquePercentIsUnescapedOnlyOnce(t *testing.T) {
	h := newWorkflowHarness(t)
	in, err := audit.SyntheticExample("internet-in")
	if err != nil {
		t.Fatal(err)
	}
	const literal = "vm%2Fweb"
	const slash = "vm/web"
	one, two := in.Assets[0], in.Assets[0]
	one.ID, two.ID = literal, slash
	in.Assets = append(in.Assets[:0:0], one, two)
	in.Selection.AssetIDs = []string{literal, slash}
	snapshot, err := h.engine.Store.SaveSnapshot(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{literal, slash} {
		status, body := h.request(t, "GET", "/api/v1/snapshots/"+snapshot.ID+"/assets/"+url.PathEscape(id), "")
		var view app.AssetView
		if err = json.Unmarshal(body, &view); err != nil || status != 200 || view.Asset.ID != id || view.DeclaredAsset.ID != id {
			t.Fatalf("opaque percent/slash was decoded more than once: requested=%q escaped=%q status=%d got=%q err=%v", id, url.PathEscape(id), status, view.Asset.ID, err)
		}
	}
}
