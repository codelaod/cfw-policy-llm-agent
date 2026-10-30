package modelconfig

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const explicitEnv = "CFW_MODEL_BASE_URL=https://model.example.invalid/v1\nCFW_MODEL_API_KEY=synthetic-file-key\nCFW_MODEL_NAME=explicit-existing-model\nCFW_MODEL_API_STYLE=chat_completions\nCFW_MODEL_TIMEOUT_SECONDS=10\nCFW_MODEL_MAX_CALLS=3\nCFW_MODEL_MAX_OUTPUT_TOKENS=512\n"

func envFile(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "selected model.env")
	if err := os.WriteFile(p, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	return p
}
func noEnv(string) (string, bool) { return "", false }
func TestSelectedEnvPreservesSettingsWithoutCreatingAuthorization(t *testing.T) {
	c, err := loadEnvFile(envFile(t, explicitEnv), noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if c.Model() != "explicit-existing-model" || c.APIKey() != "synthetic-file-key" || c.MaxOutputTokens() != 512 || c.MaxHTTPAttempts() != 3 || c.Timeout().Seconds() != 10 {
		t.Fatal("selected environment settings changed")
	}
	if c.Ready() || c.Enabled() || !c.RequestedEnabled() || c.Budget().Ref != "" {
		t.Fatal("environment file created an execution authorization")
	}
	s := c.Summary()
	if !s.Configured || !s.StructureValid || !s.CredentialLocalReady || s.ExecutionPolicyReady || s.AuthorizationState != "not_configured" || s.LedgerState != "not_checked" || s.UpstreamValidation != "not_checked" || s.SourceKind != "env_file" {
		t.Fatalf("wrong local state: %+v", s)
	}
	encoded, _ := json.Marshal(c)
	for _, out := range []string{fmt.Sprint(c), fmt.Sprintf("%#v", c), string(encoded)} {
		for _, private := range []string{"synthetic-file-key", "example.invalid", "explicit-existing-model", "CFW_MODEL_API_KEY"} {
			if strings.Contains(out, private) {
				t.Fatal("configuration leaked private settings")
			}
		}
	}
	ready, err := c.WithBudget(Budget{Ref: "explicit-synthetic-policy", ApplicationModelAttempts: 10, SessionModelAttempts: 4})
	if err != nil || !ready.Ready() || !ready.Enabled() {
		t.Fatalf("explicit policy did not enable ready adapter: %v %+v", err, ready.Summary())
	}
	if ready.Model() != c.Model() || ready.MaxOutputTokens() != 512 {
		t.Fatal("policy binding changed existing model")
	}
}
func TestEveryProcessFieldOverridesFileIncludingExplicitEmpty(t *testing.T) {
	p := envFile(t, explicitEnv)
	changes := map[string]string{"CFW_MODEL_BASE_URL": "https://other.example.invalid/v2", "CFW_MODEL_API_KEY": "synthetic-process-key", "CFW_MODEL_NAME": "process-model", "CFW_MODEL_API_STYLE": "responses", "CFW_MODEL_TIMEOUT_SECONDS": "3", "CFW_MODEL_MAX_CALLS": "2", "CFW_MODEL_MAX_OUTPUT_TOKENS": "128"}
	for key, value := range changes {
		t.Run(key, func(t *testing.T) {
			c, err := loadEnvFile(p, func(k string) (string, bool) {
				if k == key {
					return value, true
				}
				return "", false
			})
			if err != nil {
				t.Fatal(err)
			}
			switch key {
			case "CFW_MODEL_BASE_URL":
				if c.BaseURL() != value {
					t.Fatal("URL override ignored")
				}
			case "CFW_MODEL_API_KEY":
				if c.APIKey() != value {
					t.Fatal("key override ignored")
				}
			case "CFW_MODEL_NAME":
				if c.Model() != value {
					t.Fatal("model override ignored")
				}
			case "CFW_MODEL_API_STYLE":
				if c.Summary().APIStyle != value || c.Summary().ProtocolSupported {
					t.Fatal("unsupported protocol conflated")
				}
			case "CFW_MODEL_TIMEOUT_SECONDS":
				if c.Timeout().Seconds() != 3 {
					t.Fatal("timeout override ignored")
				}
			case "CFW_MODEL_MAX_CALLS":
				if c.MaxHTTPAttempts() != 2 {
					t.Fatal("calls override ignored")
				}
			case "CFW_MODEL_MAX_OUTPUT_TOKENS":
				if c.MaxOutputTokens() != 128 {
					t.Fatal("tokens override ignored")
				}
			}
			empty, err := loadEnvFile(p, func(k string) (string, bool) {
				if k == key {
					return "", true
				}
				return "", false
			})
			if key == "CFW_MODEL_API_KEY" {
				if err != nil || empty.Summary().CredentialLocalReady || empty.Ready() {
					t.Fatal("explicit empty key fell back")
				}
			} else if err == nil {
				t.Fatal("explicit empty metadata fell back or defaulted")
			}
		})
	}
}
func TestExplicitZeroCeilingsStayBlockedAndUnsupportedProtocolIsNotRewritten(t *testing.T) {
	body := strings.Replace(strings.Replace(validConfig(), `"application_model_attempts":20`, `"application_model_attempts":0`, 1), `"session_model_attempts":7`, `"session_model_attempts":0`, 1)
	c, err := loadJSON(saveConfig(t, body), func(string) (string, bool) { return "synthetic-key", true })
	if err != nil || c.Ready() || c.Budget().ApplicationModelAttempts != 0 || c.Budget().SessionModelAttempts != 0 || c.Summary().AuthorizationState != "blocked" {
		t.Fatal("zero budget acquired a default allowance", err)
	}
	c, err = loadJSON(saveConfig(t, strings.Replace(validConfig(), `"chat_completions"`, `"responses"`, 1)), func(string) (string, bool) { return "synthetic-key", true })
	if err != nil || c.Ready() || c.APIStyle() != "responses" || c.Summary().BlockedReason != "protocol_unsupported" {
		t.Fatal("unsupported known protocol was rejected as malformed or silently rewritten", err)
	}
}

func TestEnvSourceIsExplicitAndCannotMixWithJSON(t *testing.T) {
	t.Setenv("CFW_MODEL_API_KEY", "ambient-key")
	t.Setenv("CFW_MODEL_NAME", "ambient-model")
	c, err := LoadSelected("", "")
	if err != nil || c.Summary().Configured {
		t.Fatal("ambient environment discovered")
	}
	if _, err := LoadSelected("/unreadable/private.json", "/unreadable/private.env"); err != ErrSourceConflict {
		t.Fatal("conflicting sources read")
	}
	if _, err := loadEnvFile(filepath.Join(t.TempDir(), "missing.env"), func(string) (string, bool) { return "ambient", true }); err != ErrInvalid {
		t.Fatal("missing explicit file fell back")
	}
}
func TestEnvRejectsAmbiguityAndNeverExecutesValues(t *testing.T) {
	for name, body := range map[string]string{"duplicate": explicitEnv + "CFW_MODEL_NAME=again\n", "case": explicitEnv + "cfw_model_name=again\n", "unknown": explicitEnv + "CFW_MODEL_UNKNOWN=bad\n", "quote": strings.Replace(explicitEnv, "synthetic-file-key", "'unclosed", 1), "control": explicitEnv + "CFW_MODEL_NAME=bad\x00\n", "oversize": strings.Repeat("#", 65537)} {
		t.Run(name, func(t *testing.T) {
			if _, err := loadEnvFile(envFile(t, body), noEnv); err != ErrInvalid {
				t.Fatal("ambiguous environment accepted", err)
			}
		})
	}
	marker := filepath.Join(t.TempDir(), "must-not-exist")
	body := strings.Replace(explicitEnv, "synthetic-file-key", "$(touch "+marker+")", 1) + "OTHER_SERVICE_KEY=ignored\n"
	c, err := loadEnvFile(envFile(t, body), noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if c.APIKey() != "$(touch "+marker+")" {
		t.Fatal("literal key expanded")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("value executed")
	}
}
func TestJSONStateSeparatesDisabledBlockedAndExpired(t *testing.T) {
	p := saveConfig(t, strings.Replace(strings.Replace(validConfig(), `"enabled":true`, `"enabled":false`, 1), "2099-01-01", "2020-01-01", 1))
	c, err := loadJSON(p, func(string) (string, bool) { t.Fatal("disabled credential read"); return "", false })
	if err != nil || c.RequestedEnabled() || c.Ready() || !c.Summary().StructureValid {
		t.Fatalf("disabled expired structure: %v", err)
	}
	c, err = loadJSON(saveConfig(t, validConfig()), noEnv)
	if err != nil || !c.RequestedEnabled() || c.Ready() || c.Summary().CredentialLocalReady {
		t.Fatalf("missing credential not blocked: %v", err)
	}
	c, err = loadJSON(saveConfig(t, strings.Replace(validConfig(), "2099-01-01", "2020-01-01", 1)), func(string) (string, bool) { return "synthetic-key", true })
	if err != nil || c.Ready() || c.Summary().AuthorizationState != "expired" {
		t.Fatalf("expired policy conflated: %v %+v", err, c.Summary())
	}
}

func TestSourceNormalizationKeepsPolicyIdentityAcrossKeyRotation(t *testing.T) {
	path := envFile(t, explicitEnv)
	first, err := loadEnvFile(path, noEnv)
	if err != nil {
		t.Fatal(err)
	}
	second, err := loadEnvFile(path, func(key string) (string, bool) {
		if key == "CFW_MODEL_API_KEY" {
			return "synthetic-rotated-key", true
		}
		return "", false
	})
	if err != nil || first.Revision() != second.Revision() || first.Budget().Ref != second.Budget().Ref {
		t.Fatal("key rotation changed the configuration or created policy", err)
	}
	profiled, err := first.WithProviderProfile("minimax")
	if err != nil || profiled.ProviderProfile() != "minimax" || profiled.Model() != first.Model() || profiled.APIKey() != first.APIKey() || profiled.MaxOutputTokens() != 512 {
		t.Fatal("explicit protocol profile changed user's model settings", err)
	}
	if profiled.Revision() == first.Revision() {
		t.Fatal("protocol profile change failed to invalidate old bindings")
	}
	if _, err := first.WithProviderProfile("automatic-profile"); err == nil {
		t.Fatal("unknown provider profile accepted")
	}
}

func TestJSONModeOnlyLooksUpItsNamedCredential(t *testing.T) {
	var seen []string
	cfg, err := loadJSON(saveConfig(t, validConfig()), func(key string) (string, bool) {
		seen = append(seen, key)
		if key == "CFW_E3_TEST_MODEL_KEY" {
			return "synthetic-json-key", true
		}
		return "unexpected-ambient-value", true
	})
	if err != nil || len(seen) != 1 || seen[0] != "CFW_E3_TEST_MODEL_KEY" || cfg.Model() != "explicit-user-model" || cfg.MaxOutputTokens() != 512 {
		t.Fatal("JSON source mixed metadata with environment compatibility mode", err)
	}
}

func TestCredentialControlsNeverBecomeReadyOrEscapeSummary(t *testing.T) {
	for code := 0; code <= 127; code++ {
		if code >= 32 && code != 127 {
			continue
		}
		t.Run(fmt.Sprintf("control_%02x", code), func(t *testing.T) {
			secret := "synthetic" + string(rune(code)) + "key"
			c, err := loadJSON(saveConfig(t, validConfig()), func(string) (string, bool) { return secret, true })
			if err != nil || c.Ready() || c.APIKey() != "" || c.Summary().CredentialState != "invalid" {
				t.Fatal("control-bearing credential was not blocked")
			}
			c, err = c.WithBudget(Budget{Ref: "explicit-fake-policy", ApplicationModelAttempts: 10, SessionModelAttempts: 3})
			if err != nil || c.Ready() {
				t.Fatal("policy binding activated blocked credential")
			}
			b, _ := json.Marshal(c.Summary())
			if strings.Contains(string(b), "synthetic") {
				t.Fatal("blocked credential escaped summary")
			}
		})
	}
	for _, control := range []string{"\t", string(rune(127))} {
		c, err := loadEnvFile(envFile(t, strings.Replace(explicitEnv, "synthetic-file-key", "fake"+control+"key", 1)), noEnv)
		if err != nil || c.Ready() || c.APIKey() != "" || c.Summary().CredentialState != "invalid" {
			t.Fatal("environment control-bearing credential was not blocked")
		}
	}
}

func TestExplicitConnectionLimitsAreNotHiddenBehindLegacyProfileCaps(t *testing.T) {
	body := strings.Replace(strings.Replace(validConfig(), `"timeout_seconds":10`, `"timeout_seconds":60`, 1), `"max_http_attempts":3`, `"max_http_attempts":12`, 1)
	c, err := loadJSON(saveConfig(t, body), func(string) (string, bool) { return "synthetic", true })
	if err != nil || c.Timeout().Seconds() != 60 || c.MaxHTTPAttempts() != 12 || c.MaxOutputTokens() != 512 {
		t.Fatal("explicit private limit rejected by hidden legacy cap", err)
	}
}
