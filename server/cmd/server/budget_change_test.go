package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cfw-policy-llm-agent/server/internal/storage"
)

func TestExplicitBudgetChangeCommandKeepsLedgerAndReplaysOriginalRevision(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "ledger.sqlite")
	st, e := storage.Open(db, true)
	if e != nil {
		t.Fatal(e)
	}
	if e = st.ConfigureBudgetPolicy(context.Background(), storage.BudgetPolicy{Ref: "same-period", ApplicationModelAttempts: 1, SessionModelAttempts: 1}); e != nil {
		t.Fatal(e)
	}
	st.Close()
	value := int64(4)
	change := budgetChangeFile{SchemaVersion: 1, BudgetRef: "same-period", Change: storage.BudgetChange{ExpectedBaseRevision: 1, ChangeID: "explicit-extension", ActorRef: "local-operator", Reason: "public fixture revision", AuthorizationRef: "synthetic-evidence", ApplicationModelAttempts: &value, SessionModelAttempts: &value}}
	data, _ := json.Marshal(change)
	path := filepath.Join(dir, "change.json")
	if e = os.WriteFile(path, data, 0600); e != nil {
		t.Fatal(e)
	}
	var first, again bytes.Buffer
	if e = applyBudgetChange(db, path, &first); e != nil {
		t.Fatal(e)
	}
	if e = applyBudgetChange(db, path, &again); e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(first.String(), `"revision":2`) || !strings.Contains(again.String(), `"replayed":true`) {
		t.Fatal(first.String(), again.String())
	}
	st, e = storage.Open(db, false)
	if e != nil {
		t.Fatal(e)
	}
	defer st.Close()
	policy, e := st.GetBudgetPolicy(context.Background(), "same-period")
	if e != nil || policy.ApplicationModelAttempts != 4 || policy.ApplicationCloudAttempts != 0 {
		t.Fatal(policy, e)
	}
	usage, e := st.BudgetUsage(context.Background(), "same-period", "", "")
	if e != nil || usage.CloudAttempts != 0 || usage.ModelAttempts != 0 {
		t.Fatal("administrative local revision sent provider work", usage, e)
	}
}
