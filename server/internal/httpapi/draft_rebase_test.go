package httpapi_test

import (
	"context"
	"encoding/json"
	"testing"

	"cfw-policy-llm-agent/server/internal/audit"
	"cfw-policy-llm-agent/server/internal/storage"
)

func TestActualDraftCurrentBaseAndIdempotentRebaseRoute(t *testing.T) {
	h := newWorkflowHarness(t)
	ctx := context.Background()
	in, e := audit.SyntheticExample("internet-in")
	if e != nil {
		t.Fatal(e)
	}
	snapshot, e := h.engine.Store.SaveSnapshot(ctx, in)
	if e != nil {
		t.Fatal(e)
	}
	draft, e := h.engine.Store.CreateProfileDraft(ctx, storage.CreateDraftRequest{SnapshotID: snapshot.ID, AssetIDs: in.Selection.AssetIDs})
	if e != nil {
		t.Fatal(e)
	}
	status, body := h.request(t, "GET", "/api/v1/profile-drafts/"+draft.ID+"/current-base", "")
	var base storage.DraftCurrentBase
	if e = json.Unmarshal(body, &base); e != nil || status != 200 || base.DraftID != draft.ID || base.DraftRevision != draft.Revision || base.SnapshotID != snapshot.ID {
		t.Fatal("current base HTTP projection failed", status, e, string(body))
	}
	req, _ := json.Marshal(storage.RebaseDraftRequest{ExpectedDraftRevision: draft.Revision, ExpectedBaseRevisions: base.BaseRevisions, Changes: base.Changes})
	path := "/api/v1/profile-drafts/" + draft.ID + "/rebase"
	status, body = h.keyed(t, path, string(req), "rebase-http-once")
	var saved storage.ProfileDraft
	if e = json.Unmarshal(body, &saved); e != nil || status != 200 || saved.ID != draft.ID || saved.Revision != draft.Revision+1 || saved.Status != "draft" || saved.PreviewID != "" {
		t.Fatal("explicit same-draft rebase route failed", status, e, string(body))
	}
	status, body = h.keyed(t, path, string(req), "rebase-http-once")
	var replay storage.ProfileDraft
	if e = json.Unmarshal(body, &replay); e != nil || status != 200 || replay.ID != saved.ID || replay.Revision != saved.Revision {
		t.Fatal("same HTTP request did not recover the edit receipt", status, e)
	}
	_, heads, e := h.engine.Store.FreezeInput(ctx, snapshot.ID)
	if e != nil || heads[0].Revision != 0 {
		t.Fatal("rebase confirmed business data", e)
	}
}
