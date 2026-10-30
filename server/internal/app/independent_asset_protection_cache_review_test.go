package app

import "testing"

func TestIndependentLegacyLiveProtectionNeedsRefreshButPinnedRemainsReadable(t *testing.T) {
	candidate, request := eligibilityFixture(t)
	if current := EvaluateDataEligibility(candidate, request); !current.Eligible {
		t.Fatalf("complete current per-asset collection rejected: %+v", current)
	}
	candidate.Snapshot.Input.Assets[0].Protection = nil
	legacy := EvaluateDataEligibility(candidate, request)
	if legacy.Eligible || !reasonHas(legacy, "ASSET_PROTECTION_NOT_RECORDED") {
		t.Fatalf("legacy aggregate-only cache reused as current per-asset evidence: %+v", legacy)
	}
	request.Mode, request.PinnedSnapshotID = "pinned_snapshot", candidate.Snapshot.ID
	history := EvaluateDataEligibility(candidate, request)
	if !history.Eligible || history.Provenance.Guarantee != "fixed_history" || history.Input.Assets[0].Protection != nil {
		t.Fatalf("historical scope assertion changed or became unreadable: %+v", history)
	}
	candidate.Snapshot.SourceKind, candidate.Snapshot.Input.SourceKind = "imported", "imported"
	candidate.Snapshot.Input.Completeness.ObservationConsistency = "fixed_artifact"
	candidate.Cloud = nil
	request.Mode, request.PinnedSnapshotID, request.SourceRef = "prefer_fresh", "", "local-offline"
	offline := EvaluateDataEligibility(candidate, request)
	if !offline.Eligible || offline.Provenance.Guarantee != "imported_assumption" {
		t.Fatalf("legacy imported assumption lost compatibility: %+v", offline)
	}
}
