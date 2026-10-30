package httpapi_test

import (
	"context"
	"net/url"
	"testing"

	"cfw-policy-llm-agent/server/internal/audit"
)

func TestBoundedArtifactAssetDeepLinkPreservesOpaqueEncodedIdentity(t *testing.T) {
	h := newWorkflowHarness(t)
	in, e := audit.SyntheticExample("internet-in")
	if e != nil {
		t.Fatal(e)
	}
	const id = "vm/web 人工 case"
	in.Assets[0].ID, in.Selection.AssetIDs = id, []string{id}
	snapshot, e := h.engine.Store.SaveSnapshot(context.Background(), in)
	if e != nil {
		t.Fatal(e)
	}
	status, body := h.request(t, "GET", "/api/v1/snapshots/"+snapshot.ID+"/assets/"+url.PathEscape(id), "")
	if status != 200 {
		t.Fatal("valid artifact identity was split into different route segments", status, string(body))
	}
	status, body = h.request(t, "GET", "/api/v1/snapshots/"+snapshot.ID+"/assets/"+url.PathEscape("vm/other 人工 case"), "")
	if status != 404 {
		t.Fatal("opaque path read an unrelated asset", status, string(body))
	}
	jobs, e := h.engine.Store.ListJobs(context.Background(), "", 0, 100)
	if e != nil || len(jobs) != 0 {
		t.Fatal("asset reads started work", e)
	}
}
