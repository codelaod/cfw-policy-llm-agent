package httpapi_test

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"cfw-policy-llm-agent/server/internal/app"
	"cfw-policy-llm-agent/server/internal/storage"
)

func TestIndependentDraftRebaseHTTPRejectsAliasesAndTargetReplacement(t *testing.T) {
	h := newWorkflowHarness(t)
	ctx := context.Background()
	snapshot, e := app.NewAuditService(h.engine.Store).LoadExample(ctx, "internet-in")
	if e != nil {
		t.Fatal(e)
	}
	draft, e := h.engine.Store.CreateProfileDraft(ctx, storage.CreateDraftRequest{SnapshotID: snapshot.ID, AssetIDs: snapshot.Input.Selection.AssetIDs})
	if e != nil {
		t.Fatal(e)
	}
	path := "/api/v1/profile-drafts/" + draft.ID
	status, data := h.request(t, "GET", path+"/current-base", "")
	var base storage.DraftCurrentBase
	if status != 200 || json.Unmarshal(data, &base) != nil || base.DraftID != draft.ID || base.SnapshotID != snapshot.ID {
		t.Fatal("actual read-only current-base route unavailable", status, string(data))
	}
	good := storage.RebaseDraftRequest{ExpectedDraftRevision: base.DraftRevision, ExpectedBaseRevisions: base.BaseRevisions, Changes: base.Changes}
	for _, kind := range []string{"revision_alias", "heads_alias", "snapshot_replacement", "asset_replacement"} {
		encoded, _ := json.Marshal(good)
		var request map[string]any
		_ = json.Unmarshal(encoded, &request)
		switch kind {
		case "revision_alias":
			request["expected_revision"] = request["expected_draft_revision"]
			delete(request, "expected_draft_revision")
		case "heads_alias":
			request["expected_heads"] = request["expected_base_revisions"]
			delete(request, "expected_base_revisions")
		case "snapshot_replacement":
			request["snapshot_id"] = "unrelated-snapshot"
		case "asset_replacement":
			request["changes"].([]any)[0].(map[string]any)["asset_id"] = "unrelated-object"
		}
		encoded, _ = json.Marshal(request)
		status, body := h.keyed(t, path+"/rebase", string(encoded), "independent-"+kind)
		if status != 422 {
			t.Fatal("alias or target replacement was accepted", kind, status, string(body))
		}
		current, err := h.engine.Store.GetProfileDraft(ctx, draft.ID)
		if err != nil || current.Revision != draft.Revision || !reflect.DeepEqual(current.Changes, draft.Changes) {
			t.Fatal("rejected rebase changed original saved body", kind, err)
		}
	}
	encoded, _ := json.Marshal(good)
	status, body := h.keyed(t, path+"/rebase", string(encoded), "independent-rebase-valid")
	var updated storage.ProfileDraft
	if status != 200 || json.Unmarshal(body, &updated) != nil || updated.ID != draft.ID || updated.Revision != draft.Revision+1 || updated.Status != "draft" {
		t.Fatal("actual route could not complete an explicit same-target edit", status, string(body))
	}
}
