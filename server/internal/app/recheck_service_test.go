package app

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"cfw-policy-llm-agent/server/internal/domain"
	"cfw-policy-llm-agent/server/internal/recheck"
	"cfw-policy-llm-agent/server/internal/storage"
)

func TestRecheckKeepsBothRunsAndPersistsComparison(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "recheck.sqlite")
	store, err := storage.Open(path, true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if store != nil {
			store.Close()
		}
	}()
	service := NewAuditService(store)
	in := serviceCase()
	in.SourceKind = "synthetic"
	in.Baseline.EnabledChecks = []string{"CHK-07"}
	// A complete public-source comparison must explicitly settle the IANA range
	// whose global classification is unknown, while preserving broad public risk.
	in.Policies = []domain.Policy{{ID: "drop-unclassified-source", ScopeRef: in.Scope.Ref, Source: domain.AddressSet{Values: []string{"192.88.99.0/24"}}, Destination: domain.AddressSet{Values: []string{"192.0.2.10"}}, Protocol: "any", Ports: domain.PortSet{Values: []domain.PortRange{{From: 1, To: 65535}}}, Action: "drop", Priority: 1, OrderKnown: true, Enabled: "enabled"}}
	first, err := store.SaveSnapshot(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	old, err := service.RunRules(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	in.SnapshotVersion = "snapshot-new"
	in.CollectedAt = "2026-10-28T00:01:00Z"
	in.Policies = append(in.Policies, domain.Policy{ID: "drop-ssh", ScopeRef: in.Scope.Ref, Source: domain.AddressSet{Values: []string{"0.0.0.0/0"}}, Destination: domain.AddressSet{Values: []string{"192.0.2.10"}}, Protocol: "tcp", Ports: domain.PortSet{Values: []domain.PortRange{{From: 22, To: 22}}}, Action: "drop", Priority: 2, OrderKnown: true, Enabled: "enabled"})
	next, err := store.SaveSnapshot(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	newRun, err := service.RunRules(ctx, next.ID)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := service.CompareRuns(ctx, old.ID, newRun.ID)
	if err != nil {
		t.Fatal(err)
	}
	var comparison recheck.Comparison
	if err = json.Unmarshal(saved.Comparison, &comparison); err != nil {
		t.Fatal(err)
	}
	if !comparison.Comparable {
		t.Fatalf("same frozen criteria with new collection should compare: %+v", comparison)
	}
	cleared := false
	for _, issue := range comparison.Issues {
		if issue.State == "not_detected" {
			cleared = true
		}
	}
	if !cleared {
		t.Fatal("supported baseline difference not identified")
	}
	replay, err := service.CompareRuns(ctx, old.ID, newRun.ID)
	if err != nil || replay.ID != saved.ID {
		t.Fatal("comparison duplicated or changed")
	}
	in.SnapshotVersion = "snapshot-gap"
	in.Completeness.EnumerationComplete = false
	gap, err := store.SaveSnapshot(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	partial, err := service.RunRules(ctx, gap.ID)
	if err != nil {
		t.Fatal(err)
	}
	uncertain, err := service.CompareRuns(ctx, old.ID, partial.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(uncertain.Comparison, &comparison); err != nil {
		t.Fatal(err)
	}
	if comparison.Comparable || len(comparison.Issues) != 0 {
		t.Fatal("missing enumeration was counted as repaired")
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store = nil
	store, err = storage.Open(path, false)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := store.GetRecheck(ctx, saved.ID)
	if err != nil || string(loaded.Comparison) != string(saved.Comparison) {
		t.Fatal("comparison not persisted")
	}
}
