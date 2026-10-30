package app

import (
	"context"
	"testing"
)

func TestSingleAssetContextDoesNotStopAtFirstInventoryPage(t *testing.T) {
	s, _, _ := selectedGroupDialog(t, 151)
	ctx := context.Background()
	snapshots, e := s.Store.ListSnapshots(ctx, 0, 100)
	if e != nil {
		t.Fatal(e)
	}
	for _, snapshot := range snapshots {
		if len(snapshot.Input.Assets) != 151 {
			continue
		}
		last := snapshot.Input.Assets[150]
		view, e := NewConfirmationService(s.Store).ReadAssetContext(ctx, snapshot.ID, last.ID)
		if e != nil || view.Asset.ID != last.ID || view.DeclaredAsset.ID != last.ID || view.Head.AssetID != last.ID {
			t.Fatal("exact asset context was incorrectly limited to first list page", e)
		}
		return
	}
	t.Fatal("public bounded inventory fixture missing")
}
