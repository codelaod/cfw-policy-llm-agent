package app

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"cfw-policy-llm-agent/server/internal/storage"
)

func TestAssetContextSeparatesImportedClaimsFromLocalConfirmation(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(filepath.Join(t.TempDir(), "asset-context.sqlite"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	// This uses the production explicit migration. It must include the E2
	// collaboration schema before the HTTP layer advertises the feature.
	audits := NewAuditService(store)
	snapshot, err := audits.LoadExample(ctx, "internet-in")
	if err != nil {
		t.Fatal(err)
	}
	in := snapshot.Input
	in.SourceKind = "imported"
	snapshot, err = store.SaveSnapshot(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	service := NewConfirmationService(store)
	id := snapshot.Input.Assets[0].ID
	before, err := service.ReadAssetContext(ctx, snapshot.ID, id)
	if err != nil {
		t.Fatal(err)
	}
	if before.SourceKind != "imported" || len(before.LocalConfirmedSections) != 0 || before.Head.ProfileVersionID != "" || before.Head.IntentVersionID != "" {
		t.Fatal("imported confirmed flags became local authority", before)
	}
	draft, err := service.CreateDraft(ctx, storage.CreateDraftRequest{SnapshotID: snapshot.ID, AssetIDs: []string{id}})
	if err != nil {
		t.Fatal(err)
	}
	draft.Changes[0].Roles[0].Role = "database"
	draft, err = service.UpdateDraft(ctx, draft.ID, storage.UpdateDraftRequest{ExpectedDraftRevision: draft.Revision, Changes: draft.Changes})
	if err != nil {
		t.Fatal(err)
	}
	preview, err := service.Preview(ctx, draft.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.Confirm(ctx, draft.ID, storage.ConfirmDraftRequest{PreviewID: preview.ID, ExpectedDraftRevision: draft.Revision, ConfirmedSections: []string{"roles"}}, "only-roles"); err != nil {
		t.Fatal(err)
	}
	after, err := service.ReadAssetContext(ctx, snapshot.ID, id)
	if err != nil {
		t.Fatal(err)
	}
	if after.SourceKind != "imported" || !reflect.DeepEqual(after.LocalConfirmedSections, []string{"roles"}) || after.Head.IntentVersionID != "" || after.Asset.Services[0].Role != "database" || after.DeclaredAsset.Services[0].Role == "database" {
		t.Fatal("local and declared contexts conflated", after)
	}
	if !reflect.DeepEqual(after.Asset.Allowed, after.DeclaredAsset.Allowed) {
		t.Fatal("roles changed access")
	}
	page, err := service.ReadAssets(ctx, snapshot.ID, 0, 100)
	if err != nil || len(page.Items) != 1 || page.Total != 1 {
		t.Fatal(page, err)
	}
	if _, err = service.ReadAssetContext(ctx, snapshot.ID, "outside-selected-asset"); err != storage.ErrNotFound {
		t.Fatal("unknown asset read", err)
	}
}
