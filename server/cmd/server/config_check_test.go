package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
