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

func TestExplicitEnvironmentExecutionBindingPreservesModelAndChecksLocally(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, "project.env")
	fields := map[string]string{"CFW_MODEL_BASE_URL": "https://public-fixture.example.invalid/v1", "CFW_MODEL_NAME": "private-artificial-model", "CFW_MODEL_API_KEY": "synthetic-command-key-only", "CFW_MODEL_API_STYLE": "chat_completions", "CFW_MODEL_TIMEOUT_SECONDS": "120", "CFW_MODEL_MAX_CALLS": "9", "CFW_MODEL_MAX_OUTPUT_TOKENS": "512"}
	lines := []string{}
	for key, value := range fields {
		t.Setenv(key, value)
		lines = append(lines, key+"="+value)
	}
	if e := os.WriteFile(envPath, []byte(strings.Join(lines, "\n")), 0600); e != nil {
		t.Fatal(e)
	}
	raw, e := os.ReadFile("../../execution-config.example.json")
	if e != nil {
		t.Fatal(e)
	}
	execPath := filepath.Join(dir, "execution.json")
	if e = os.WriteFile(execPath, raw, 0600); e != nil {
		t.Fatal(e)
	}
	settings, e := loadExecutionSettings("", "", envPath, execPath)
	if e != nil {
		t.Fatal(e)
	}
	if settings.Model.Model() != fields["CFW_MODEL_NAME"] || settings.Model.MaxOutputTokens() != 512 || settings.Model.ProviderProfile() != "minimax" || settings.Model.Ready() {
		t.Fatal("settings lost old model/private cap or widened explicit zero")
	}
	var out bytes.Buffer
	if e = checkExecutionConfiguration("", "", envPath, execPath, &out); e != nil {
		t.Fatal(e)
	}
	for _, secret := range []string{fields["CFW_MODEL_BASE_URL"], fields["CFW_MODEL_NAME"], fields["CFW_MODEL_API_KEY"], envPath} {
		if strings.Contains(out.String(), secret) {
			t.Fatal("local check disclosed source value")
		}
	}
	if !strings.Contains(out.String(), `"ledger_state":"not_checked"`) {
		t.Fatal(out.String())
	}
}
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
