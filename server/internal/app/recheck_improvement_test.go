package app

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"cfw-policy-llm-agent/server/internal/audit"
	"cfw-policy-llm-agent/server/internal/domain"
	"cfw-policy-llm-agent/server/internal/recheck"
	"cfw-policy-llm-agent/server/internal/storage"
)

// Public artificial policies exercise the actual rules, application comparison
// and durable result. This is not a cloud write or a real remediation result.
func TestRecheckStrictSubsetPersistsImprovedWithoutChangingOriginalFailure(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "improved-recheck.sqlite")
	st, e := storage.Open(path, true)
	if e != nil {
		t.Fatal(e)
	}
	defer func() {
		if st != nil {
			st.Close()
		}
	}()
	service := NewAuditService(st)
	in, e := audit.SyntheticExample("internet-in")
	if e != nil {
		t.Fatal(e)
	}
	in.Policies = append([]domain.Policy{{ID: "settle-unclassified-source", ScopeRef: in.Scope.Ref, Source: domain.AddressSet{Values: []string{"192.88.99.0/24"}}, Destination: domain.AddressSet{Values: []string{in.Assets[0].Address}}, Protocol: "any", Ports: domain.PortSet{Values: []domain.PortRange{{From: 1, To: 65535}}}, Action: "drop", Priority: 1, OrderKnown: true, Enabled: "enabled"}}, in.Policies...)
	oldSnapshot, e := st.SaveSnapshot(ctx, in)
	if e != nil {
		t.Fatal(e)
	}
	old, e := service.RunRules(ctx, oldSnapshot.ID)
	if e != nil || !old.Result.Coverage.Complete {
		t.Fatal("old comparison fixture incomplete", e)
	}
	in.SnapshotVersion, in.CollectedAt = "partial-manual-reduction", "2026-10-28T00:01:00Z"
	in.Policies = append(in.Policies, domain.Policy{ID: "deny-known-subset", ScopeRef: in.Scope.Ref, Source: domain.AddressSet{Values: []string{"8.8.8.0/24"}}, Destination: domain.AddressSet{Values: []string{in.Assets[0].Address}}, Protocol: "tcp", Ports: domain.PortSet{Values: []domain.PortRange{{From: 22, To: 22}}}, Action: "drop", Priority: 2, OrderKnown: true, Enabled: "enabled"})
	nextSnapshot, e := st.SaveSnapshot(ctx, in)
	if e != nil {
		t.Fatal(e)
	}
	next, e := service.RunRules(ctx, nextSnapshot.ID)
	if e != nil {
		t.Fatal(e)
	}
	saved, e := service.CompareRuns(ctx, old.ID, next.ID)
	if e != nil {
		t.Fatal(e)
	}
	var comparison recheck.Comparison
	if e = json.Unmarshal(saved.Comparison, &comparison); e != nil || !comparison.Comparable {
		t.Fatal("persisted comparison did not retain its fixed scope", e, comparison)
	}
	var improved *recheck.IssueChange
	for i := range comparison.Issues {
		if comparison.Issues[i].State == "improved" {
			improved = &comparison.Issues[i]
		}
	}
	if improved == nil || len(improved.OldFindingIDs) == 0 || len(improved.NewFindingIDs) == 0 {
		t.Fatal("exactly reduced real-rule finding was not persisted as improvement", comparison)
	}
	oldBytes, _ := json.Marshal(old.Result)
	oldAgain, e := st.GetAudit(ctx, old.ID)
	if e != nil {
		t.Fatal(e)
	}
	storedBytes, _ := json.Marshal(oldAgain.Result)
	if string(oldBytes) != string(storedBytes) {
		t.Fatal("comparison modified the original immutable rule result")
	}
	stillFailed := false
	for _, f := range next.Result.Findings {
		stillFailed = stillFailed || f.IssueKey == improved.IssueKey && f.Verdict == "fail"
	}
	if !stillFailed {
		t.Fatal("improved falsely converted the remaining violation into a pass")
	}
	if e = st.Close(); e != nil {
		t.Fatal(e)
	}
	st = nil
	st, e = storage.Open(path, false)
	if e != nil {
		t.Fatal(e)
	}
	reopened, e := st.GetRecheck(ctx, saved.ID)
	if e != nil || string(reopened.Comparison) != string(saved.Comparison) {
		t.Fatal("fixed improvement evidence did not survive reopening", e)
	}
}
