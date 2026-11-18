package modelconfig

import (
	"os"
	"strings"
	"testing"
)

func validConfig() string {
	return `{"api_style":"chat_completions","provider_profile":"openai-compatible","base_url":"https://model.example.invalid/v1","model":"explicit-user-model","api_key_env":"CFW_E3_TEST_MODEL_KEY","max_output_tokens":512,"timeout_seconds":10,"enabled":true}`
}

func parseFixture(body []byte) (Config, error) { return ParseSettings(body, os.LookupEnv) }

func TestE3NoConfigOrMissingExplicitKeyNeverUsesAmbientProfiles(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "ambient-key-must-not-be-used")
	t.Setenv("MINIMAX_API_KEY", "ambient-key-must-not-be-used")
	t.Setenv("CFW_E3_TEST_MODEL_KEY", "")
	if _, err := parseFixture(nil); err == nil {
		t.Fatal("missing explicit path was accepted")
	}
	cfg, err := parseFixture([]byte(validConfig()))
	if err != nil || cfg.Ready() || cfg.APIKey() != "" || cfg.Summary().CredentialState != "missing" {
		t.Fatal("missing explicit key must remain blocked without ambient fallback")
	}
}

func TestE3ConfigRejectsExpandedProtocolsEffectsAndBudgets(t *testing.T) {
	t.Setenv("CFW_E3_TEST_MODEL_KEY", "synthetic-config-secret")
	for _, change := range []struct{ old, new string }{
		{`"openai-compatible"`, `"automatic-fallback"`}, {`https://model.example.invalid/v1`, `http://model.example.invalid/v1`},
		{`https://model.example.invalid/v1`, `https://private-user:secret@model.example.invalid/v1`}, {`https://model.example.invalid/v1`, `https://model.example.invalid/v1?token=secret`},
		{`"api_key_env":"CFW_E3_TEST_MODEL_KEY"`, `"api_key":"literal-key-is-not-supported"`},
		{`https://model.example.invalid/v1`, `https://:443/v1`},
		{`https://model.example.invalid/v1`, `https://model.example.invalid/v1?`},
	} {
		t.Run(change.new, func(t *testing.T) {
			_, err := parseFixture([]byte(strings.Replace(validConfig(), change.old, change.new, 1)))
			if err == nil {
				t.Fatal("configuration expanded its declared authorization")
			}
			if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "example.invalid") {
				t.Fatal("invalid configuration leaked private details")
			}
		})
	}
}

func TestE3ConfigCheckReportsBlockedCredentialWithoutChangingIt(t *testing.T) {
	t.Setenv("CFW_E3_TEST_MODEL_KEY", " synthetic-config-secret ")
	cfg, err := parseFixture([]byte(validConfig()))
	if err != nil || cfg.Ready() || cfg.APIKey() != "" || cfg.Summary().CredentialState != "invalid" {
		t.Fatal("invalid credential must be blocked without trimming or fallback")
	}
}

func TestRejectDuplicateAndCaseCollidingJSONFields(t *testing.T) {
	t.Setenv("CFW_E3_TEST_MODEL_KEY", "synthetic-config-secret")
	for _, tc := range []struct{ name, old, replacement string }{
		{"duplicate_enabled", `"enabled":true`, `"enabled":false,"enabled":true`},
		{"case_colliding_enabled", `"enabled":true`, `"enabled":false,"ENABLED":true`},
		{"escaped_duplicate_key", `"enabled":true`, `"enabled":false,"enab\u006ced":true`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := parseFixture([]byte(strings.Replace(validConfig(), tc.old, tc.replacement, 1))); err != ErrInvalid {
				t.Fatalf("ambiguous JSON fields must be rejected: %v", err)
			}
		})
	}
}

func TestModelStatusKeepsDisabledExpiredAndUnsupportedSeparate(t *testing.T) {
	for _, tc := range []struct {
		name, body, state string
		read              bool
	}{
		{"disabled", strings.Replace(validConfig(), `"enabled":true`, `"enabled":false`, 1), "disabled", false},
		{"protocol", strings.Replace(validConfig(), "chat_completions", "responses", 1), "protocol_unsupported", true},
		{"missing", validConfig(), "credential_missing", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, err := ParseSettings([]byte(tc.body), func(string) (string, bool) {
				if !tc.read {
					t.Fatal("disabled adapter read credential")
				}
				if tc.name == "missing" {
					return "", false
				}
				return "synthetic-key", true
			})
			if err != nil || c.Ready() || c.Summary().BlockedReason != tc.state {
				t.Fatal("local states collapsed", err)
			}
		})
	}
}

func TestModelCredentialControlsAreBlockedAndNeverRetained(t *testing.T) {
	for code := 0; code <= 127; code++ {
		if code >= 32 && code != 127 {
			continue
		}
		secret := "synthetic" + string(rune(code)) + "key"
		c, err := ParseSettings([]byte(validConfig()), func(string) (string, bool) { return secret, true })
		if err != nil || c.Ready() || c.APIKey() != "" || c.Summary().CredentialState != "invalid" {
			t.Fatal("credential controls not blocked")
		}
	}
}

func TestReplyLengthAbove1024DoesNotRequireAUsageGrant(t *testing.T) {
	t.Setenv("CFW_E3_TEST_MODEL_KEY", "synthetic-key")
	cfg, e := parseFixture([]byte(strings.Replace(validConfig(), `"max_output_tokens":512`, `"max_output_tokens":8192`, 1)))
	if e != nil || !cfg.Ready() || cfg.MaxOutputTokens() != 8192 {
		t.Fatal("reply length was capped as a spending quota", e)
	}
}
