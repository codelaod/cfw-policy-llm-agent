package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"cfw-policy-llm-agent/server/internal/app"
	"cfw-policy-llm-agent/server/internal/configuration"
	"cfw-policy-llm-agent/server/internal/storage"
)

func cleanProjectEnvironment(t *testing.T) {
	t.Helper()
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(strings.ToUpper(key), "CFW_") {
			t.Setenv(key, "")
			if err := os.Unsetenv(key); err != nil {
				t.Fatal(err)
			}
		}
	}
}
func commandEnv(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "project.env")
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestUnifiedConfigCheckDoesNoProviderOrDatabaseWork(t *testing.T) {
	cleanProjectEnvironment(t)
	var requests atomic.Int32
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	defer upstream.Close()
	database := filepath.Join(t.TempDir(), "must-not-be-created", "private.sqlite")
	body := "CFW_APP_TOKEN=synthetic-local-app-token\nCFW_DB_PATH=" + database + "\nCFW_MODEL_ENABLED=true\nCFW_MODEL_BASE_URL=" + upstream.URL + "/v1\nCFW_MODEL_NAME=private-fixture-model\nCFW_MODEL_API_KEY=synthetic-fixture-model-key\nCFW_CLOUD_ENABLED=true\nCFW_CLOUD_EXPECTED_ACCOUNT_ID=1234567890123456\nCFW_ALIYUN_ACCESS_KEY_ID=synthetic-cloud-ak\nCFW_ALIYUN_ACCESS_KEY_SECRET=synthetic-cloud-secret\nCFW_BUDGET_REF=shared-command-period\nCFW_BUDGET_APPLICATION_CLOUD_ATTEMPTS=100\nCFW_BUDGET_SESSION_CLOUD_ATTEMPTS=50\nCFW_BUDGET_APPLICATION_MODEL_ATTEMPTS=20\nCFW_BUDGET_SESSION_MODEL_ATTEMPTS=6\n"
	path := commandEnv(t, body)
	var output bytes.Buffer
	if err := runCommand([]string{"--env-file", path, "--config-check"}, &output); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 0 {
		t.Fatal("config-check contacted provider")
	}
	if _, err := os.Stat(filepath.Dir(database)); !os.IsNotExist(err) {
		t.Fatal("config-check opened storage")
	}
	for _, private := range []string{"synthetic-local-app-token", "synthetic-fixture-model-key", "private-fixture-model", upstream.URL, "synthetic-cloud-ak", "synthetic-cloud-secret", "1234567890123456", database, path, "shared-command-period"} {
		if strings.Contains(output.String(), private) {
			t.Fatal("config-check exposed source values")
		}
	}
	for _, expected := range []string{`"model":`, `"cloud":`, `"execution":`, `"token_state":"available"`, `"ledger_state":"not_checked"`, `"upstream_validation":"not_checked"`} {
		if !strings.Contains(output.String(), expected) {
			t.Fatal("missing safe status", expected)
		}
	}
}

func TestUnifiedCommandSupportsProcessOnlyAndExplicitEmptyOverride(t *testing.T) {
	cleanProjectEnvironment(t)
	t.Setenv("CFW_APP_TOKEN", "synthetic-process-local-token")
	var output bytes.Buffer
	if err := runCommand([]string{"--config-check"}, &output); err != nil || !strings.Contains(output.String(), `"token_state":"available"`) {
		t.Fatal("process-only config failed", err)
	}
	path := commandEnv(t, "CFW_APP_TOKEN=synthetic-file-local-token\n")
	t.Setenv("CFW_APP_TOKEN", "")
	output.Reset()
	if err := runCommand([]string{"--env-file", path, "--config-check"}, &output); err != nil || !strings.Contains(output.String(), `"token_state":"missing"`) {
		t.Fatal("empty process token fell back", err)
	}
	if err := runCommand([]string{"--env-file", path}, &output); err == nil {
		t.Fatal("normal startup accepted empty token")
	}
}

func TestUnifiedMigrationUsesFileDatabasePath(t *testing.T) {
	cleanProjectEnvironment(t)
	path := commandEnv(t, "CFW_DB_PATH=private data/business.sqlite\n")
	var output bytes.Buffer
	if err := runCommand([]string{"--env-file", path, "--migrate"}, &output); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(path), "private data/business.sqlite")); err != nil {
		t.Fatal("migration ignored unified database path", err)
	}
}

func TestRetiredConfigurationFlagsAndConflictingActionsAreRejected(t *testing.T) {
	cleanProjectEnvironment(t)
	for _, flag := range []string{"--model-config", "--cloud-config", "--cloud-env-file", "--execution-config", "--db", "--web-dir"} {
		t.Run(flag, func(t *testing.T) {
			var output bytes.Buffer
			if err := runCommand([]string{flag, "unused", "--config-check"}, &output); err == nil {
				t.Fatal("retired entrypoint still accepted")
			}
		})
	}
	for _, args := range [][]string{{"--migrate", "--config-check"}, {"--budget-change", "unused", "--migrate"}, {"unexpected"}} {
		var output bytes.Buffer
		if err := runCommand(args, &output); err == nil {
			t.Fatal("ambiguous action accepted")
		}
	}
}

func TestMalformedUnifiedConfigEmitsNoPartialSummary(t *testing.T) {
	cleanProjectEnvironment(t)
	path := commandEnv(t, "CFW_MODEL_API_KEY=private-marker\nCFW_CLOUD_UNKNOWN=private-marker\n")
	var output bytes.Buffer
	err := runCommand([]string{"--env-file", path, "--config-check"}, &output)
	if err == nil || output.Len() != 0 || strings.Contains(err.Error(), "private-marker") {
		t.Fatal("malformed configuration emitted private or partial output")
	}
}

func TestUnifiedRuntimePreservesBudgetAndDoesNotInheritMissingAuthorization(t *testing.T) {
	cleanProjectEnvironment(t)
	store, err := storage.Open(filepath.Join(t.TempDir(), "business.sqlite"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	path := commandEnv(t, "CFW_MODEL_ENABLED=true\nCFW_MODEL_BASE_URL=https://synthetic-model.example.invalid/v1\nCFW_MODEL_API_KEY=synthetic-local-key\nCFW_MODEL_NAME=synthetic-existing-model\nCFW_MODEL_PROVIDER_PROFILE=minimax\nCFW_MODEL_MAX_OUTPUT_TOKENS=512\nCFW_MODEL_MAX_HTTP_ATTEMPTS=2\nCFW_BUDGET_REF=synthetic-runtime-period\nCFW_BUDGET_APPLICATION_MODEL_ATTEMPTS=10\nCFW_BUDGET_SESSION_MODEL_ATTEMPTS=4\n")
	cfg, err := configuration.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := app.NewRuntimeWithExecution(store, cfg.Model, cfg.Cloud, cfg.Execution)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.Factory == nil || !runtime.Plans.ModelReady || runtime.Plans.ModelHTTPAttempts != 2 {
		t.Fatal("runtime discarded unified adapter or cap")
	}
	runtime.Close()
	usage, err := store.BudgetUsage(context.Background(), cfg.Execution.Budget().Ref, "", "")
	if err != nil || usage.ModelAttempts != 0 || usage.CloudAttempts != 0 {
		t.Fatal("startup sent provider work", err)
	}
	t.Setenv("CFW_BUDGET_REF", "")
	t.Setenv("CFW_BUDGET_APPLICATION_MODEL_ATTEMPTS", "0")
	t.Setenv("CFW_BUDGET_SESSION_MODEL_ATTEMPTS", "0")
	missing, err := configuration.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err = app.NewRuntimeWithExecution(store, missing.Model, missing.Cloud, missing.Execution)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	if runtime.Factory != nil || runtime.Plans.ModelReady {
		t.Fatal("missing explicit authorization inherited a saved grant")
	}
	head, err := store.BudgetForOwner(context.Background(), "application-root")
	if err != nil || head.Ref != "synthetic-runtime-period" || head.ApplicationModelAttempts != 10 {
		t.Fatal("missing policy changed persistent head", err)
	}
}
