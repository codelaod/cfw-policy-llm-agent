package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSelectedEnvironmentConfigurationCheckIsLocalAndRedacted(t *testing.T) {
	p := filepath.Join(t.TempDir(), "chosen env file")
	body := "CFW_MODEL_BASE_URL=https://private-config.example.invalid/v1\nCFW_MODEL_API_KEY=synthetic-selected-key\nCFW_MODEL_NAME=existing-private-model\nCFW_MODEL_API_STYLE=chat_completions\nCFW_MODEL_TIMEOUT_SECONDS=10\nCFW_MODEL_MAX_CALLS=3\nCFW_MODEL_MAX_OUTPUT_TOKENS=512\n"
	if err := os.WriteFile(p, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"CFW_MODEL_BASE_URL", "CFW_MODEL_API_KEY", "CFW_MODEL_NAME", "CFW_MODEL_API_STYLE", "CFW_MODEL_TIMEOUT_SECONDS", "CFW_MODEL_MAX_CALLS", "CFW_MODEL_MAX_OUTPUT_TOKENS"} {
		t.Setenv(key, "")
		if err := os.Unsetenv(key); err != nil {
			t.Fatal(err)
		}
	}
	var out bytes.Buffer
	if err := checkConfigurationSelected("", "", p, &out); err != nil {
		t.Fatal(err)
	}
	var summary map[string]any
	if err := json.Unmarshal(out.Bytes(), &summary); err != nil {
		t.Fatal(err)
	}
	if summary["configured"] != true || summary["ledger_state"] != "not_checked" || summary["ready"] != false {
		t.Fatalf("inaccurate local check: %s", out.String())
	}
	for _, private := range []string{"synthetic-selected-key", "private-config.example.invalid", "existing-private-model", p, "CFW_MODEL_API_KEY"} {
		if strings.Contains(out.String(), private) {
			t.Fatal("private value in summary")
		}
	}
	out.Reset()
	if err := checkConfigurationSelected("missing.json", "", p, &out); err == nil || out.Len() != 0 {
		t.Fatal("conflict emitted partial output")
	}
}
