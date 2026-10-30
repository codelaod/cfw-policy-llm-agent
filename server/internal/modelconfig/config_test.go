package modelconfig

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func validConfig() string {
	return `{"api_style":"chat_completions","provider_profile":"openai-compatible","base_url":"https://model.example.invalid/v1","model":"explicit-user-model","api_key_env":"CFW_E3_TEST_MODEL_KEY","max_output_tokens":512,"timeout_seconds":10,"max_http_attempts":3,"enabled":true,"budget":{"ref":"explicit-local-authorization","expires_at":"2099-01-01T00:00:00Z","application_model_attempts":20,"session_model_attempts":7}}`
}

func saveConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "private-model.json")
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestE3ExplicitConfigPreservesSettingsAndExposesOnlySafeSummary(t *testing.T) {
	t.Setenv("CFW_E3_TEST_MODEL_KEY", "synthetic-config-secret")
	cfg, err := Load(saveConfig(t, validConfig()))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Model() != "explicit-user-model" || cfg.MaxOutputTokens() != 512 || cfg.MaxHTTPAttempts() != 3 || cfg.Endpoint() != "https://model.example.invalid/v1/chat/completions" {
		t.Fatal("explicit model settings changed")
	}
	encoded, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "synthetic-config-secret") || strings.Contains(string(encoded), "example.invalid") || strings.Contains(string(encoded), "explicit-user-model") {
		t.Fatal("configuration serialized private values")
	}
	summary := cfg.Summary()
	if !summary.Configured || !summary.Enabled || summary.MaxOutputTokens != 512 {
		t.Fatal("safe config summary is inaccurate")
	}
}

func TestE3NoConfigOrMissingExplicitKeyNeverUsesAmbientProfiles(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "ambient-key-must-not-be-used")
	t.Setenv("MINIMAX_API_KEY", "ambient-key-must-not-be-used")
	t.Setenv("CFW_E3_TEST_MODEL_KEY", "")
	if _, err := Load(""); err == nil {
		t.Fatal("missing explicit path was accepted")
	}
	cfg, err := Load(saveConfig(t, validConfig()))
	if err != nil || cfg.Ready() || cfg.APIKey() != "" || cfg.Summary().CredentialState != "missing" {
		t.Fatal("missing explicit key must remain blocked without ambient fallback")
	}
}

func TestE3ConfigRejectsExpandedProtocolsEffectsAndBudgets(t *testing.T) {
	t.Setenv("CFW_E3_TEST_MODEL_KEY", "synthetic-config-secret")
	for _, change := range []struct{ old, new string }{
		{`"openai-compatible"`, `"automatic-fallback"`}, {`https://model.example.invalid/v1`, `http://model.example.invalid/v1`},
		{`https://model.example.invalid/v1`, `https://private-user:secret@model.example.invalid/v1`}, {`https://model.example.invalid/v1`, `https://model.example.invalid/v1?token=secret`},
		{`"max_output_tokens":512`, `"max_output_tokens":2048`},
		{`"application_model_attempts":20`, `"application_model_attempts":0`},
		{`"api_key_env":"CFW_E3_TEST_MODEL_KEY"`, `"api_key":"literal-key-is-not-supported"`},
		{`https://model.example.invalid/v1`, `https://:443/v1`},
		{`https://model.example.invalid/v1`, `https://model.example.invalid/v1?`},
	} {
		t.Run(change.new, func(t *testing.T) {
			_, err := Load(saveConfig(t, strings.Replace(validConfig(), change.old, change.new, 1)))
			if err == nil {
				t.Fatal("configuration expanded its declared authorization")
			}
			if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "example.invalid") {
				t.Fatal("invalid configuration leaked private details")
			}
		})
	}
}

func TestE3KeyRotationKeepsAuthorizationCycleAndConfigurationBinding(t *testing.T) {
	path := saveConfig(t, validConfig())
	t.Setenv("CFW_E3_TEST_MODEL_KEY", "synthetic-first-key")
	first, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("CFW_E3_TEST_MODEL_KEY", "synthetic-second-key")
	second, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if first.Budget().Ref != second.Budget().Ref || first.Revision() != second.Revision() {
		t.Fatal("rotating a key changed authorization cycle or configuration identity")
	}
	third, err := Load(saveConfig(t, strings.Replace(validConfig(), "explicit-user-model", "other-explicit-model", 1)))
	if err != nil {
		t.Fatal(err)
	}
	if third.Revision() == second.Revision() {
		t.Fatal("model change failed to invalidate old plans")
	}
}

func TestE3ConfigCheckReportsBlockedCredentialWithoutChangingIt(t *testing.T) {
	t.Setenv("CFW_E3_TEST_MODEL_KEY", " synthetic-config-secret ")
	cfg, err := Load(saveConfig(t, validConfig()))
	if err != nil || cfg.Ready() || cfg.APIKey() != "" || cfg.Summary().CredentialState != "invalid" {
		t.Fatal("invalid credential must be blocked without trimming or fallback")
	}
}

func TestRejectDuplicateAndCaseCollidingJSONFields(t *testing.T) {
	t.Setenv("CFW_E3_TEST_MODEL_KEY", "synthetic-config-secret")
	for _, tc := range []struct{ name, old, replacement string }{
		{"duplicate_enabled", `"enabled":true`, `"enabled":false,"enabled":true`},
		{"case_colliding_enabled", `"enabled":true`, `"enabled":false,"ENABLED":true`},
		{"duplicate_budget_limit", `"application_model_attempts":20`, `"application_model_attempts":1,"application_model_attempts":20`},
		{"case_colliding_budget_limit", `"application_model_attempts":20`, `"application_model_attempts":1,"APPLICATION_MODEL_ATTEMPTS":20`},
		{"escaped_duplicate_key", `"enabled":true`, `"enabled":false,"enab\u006ced":true`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Load(saveConfig(t, strings.Replace(validConfig(), tc.old, tc.replacement, 1))); err != ErrInvalid {
				t.Fatalf("ambiguous JSON fields must be rejected: %v", err)
			}
		})
	}
}
