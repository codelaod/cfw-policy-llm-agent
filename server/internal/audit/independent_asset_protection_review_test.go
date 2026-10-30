package audit

import (
	"context"
	"testing"

	"cfw-policy-llm-agent/server/internal/domain"
)

func TestIndependentOptionalAssetProtectionPreservesLegacyAndConstrainsPaths(t *testing.T) {
	in := fixture()
	legacy, err := Evaluate(context.Background(), in, query())
	if err != nil || !legacy.Complete {
		t.Fatalf("legacy scope assertion changed: %+v %v", legacy, err)
	}
	in.Assets[0].Protection = &domain.Protection{Applicable: true, Required: true, State: "off", EvidenceRef: "asset-specific-proof"}
	path, err := Evaluate(context.Background(), in, query())
	if err != nil || path.Complete || len(path.Reasons) == 0 || path.Reasons[0] != "PROTECTION_UNCONFIRMED" {
		t.Fatalf("scope on overrode asset off: %+v %v", path, err)
	}
	in.Assets[0].Protection.State = "invented"
	if err := Validate(in); err == nil {
		t.Fatal("malformed optional protection state accepted")
	}
	in.Assets[0].Protection.State = "off"
	in.Assets[0].Protection.EvidenceRef = ""
	if err := Validate(in); err == nil {
		t.Fatal("known state without evidence accepted")
	}
}
