package storage

import (
	"testing"

	"cfw-policy-llm-agent/server/internal/audit"
	"cfw-policy-llm-agent/server/internal/domain"
)

func TestIndependentResolvedConfigurationCannotReplaceAssetProtection(t *testing.T) {
	in, err := audit.SyntheticExample("internet-in")
	if err != nil {
		t.Fatal(err)
	}
	in.Assets[0].Protection = &domain.Protection{Applicable: true, Required: true, State: "off", EvidenceRef: "asset-specific-proof"}
	candidate := in
	candidate.Assets = append([]domain.Asset(nil), in.Assets...)
	changed := *in.Assets[0].Protection
	changed.State = "on"
	candidate.Assets[0].Protection = &changed
	if validResolvedConfiguration(in, candidate) {
		t.Fatal("business binding replaced fixed cloud protection state")
	}
	if !validResolvedConfiguration(in, in) {
		t.Fatal("unchanged fixed configuration rejected")
	}
}
