package workload

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func configText(t *testing.T) string {
	t.Helper()
	profiles := map[string]Limits{}
	for _, kind := range []string{"audit", "assistant_turn", "collect", "connection_check"} {
		limits := Default(kind)
		if limits.FinalizeSeconds == 0 {
			limits.FinalizeSeconds = 10
			if limits.ModelSeconds > 0 {
				limits.ModelSeconds -= 10
			} else {
				limits.CollectionSeconds -= 10
			}
		}
		profiles[kind] = limits
	}
	b, err := json.Marshal(map[string]any{"schema_version": 1, "profile_ref": "explicit-synthetic-profile", "profiles": profiles, "budget": map[string]any{"ref": "explicit-synthetic-policy", "application_cloud_attempts": 100, "session_cloud_attempts": 50, "application_model_attempts": 20, "session_model_attempts": 7}, "model_provider_profile": "minimax"})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
func writeConfig(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "selected execution.json")
	if err := os.WriteFile(p, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	return p
}
func TestExplicitExecutionConfigPreservesPolicyAndProfiles(t *testing.T) {
	cfg, err := Load(writeConfig(t, configText(t)))
	if err != nil {
		t.Fatal(err)
	}
	p, err := cfg.Profile("audit")
	if err != nil || p.ModelHTTPAttempts != 6 || cfg.ProfileRef() != "explicit-synthetic-profile" || cfg.ModelProviderProfile() != "minimax" || !cfg.HasBudget() || cfg.Budget().ExpiresAt != "" {
		t.Fatal("explicit settings changed", err)
	}
	p.ModelHTTPAttempts = 999
	again, _ := cfg.Profile("audit")
	if again.ModelHTTPAttempts != 6 {
		t.Fatal("profile getter exposed mutable state")
	}
	zero := 0
	effective, err := cfg.Resolve("audit", Ceilings{ModelHTTPAttempts: &zero})
	if err != nil || effective.Legacy || effective.Limits.ModelHTTPAttempts != 0 || effective.ProfileRef != cfg.ProfileRef() {
		t.Fatal("effective limits changed profile binding or zero", err)
	}
	if _, err := Load(""); err == nil {
		t.Fatal("missing selected execution file discovered defaults")
	}
	legacy, err := (Config{}).Resolve("audit", Ceilings{})
	if err != nil || !legacy.Legacy || legacy.ProfileRef != LegacyProfileRef {
		t.Fatal("legacy fallback was not explicitly identified")
	}
}
func TestMalformedExecutionConfigCannotCreatePolicyOrChangeSource(t *testing.T) {
	base := configText(t)
	for name, body := range map[string]string{"unknown": strings.Replace(base, `"schema_version":1`, `"schema_version":1,"unexpected":true`, 1), "version": strings.Replace(base, `"schema_version":1`, `"schema_version":2`, 1), "duplicate": strings.Replace(base, `"schema_version":1`, `"schema_version":1,"SCHEMA_VERSION":1`, 1), "missing-profile": strings.Replace(base, `"collect":`, `"unexpected_kind":`, 1), "missing-field": strings.Replace(base, `"model_request_seconds":30,`, ``, 1), "negative-budget": strings.Replace(base, `"application_model_attempts":20`, `"application_model_attempts":-1`, 1), "budget-overflow": strings.Replace(base, `"application_model_attempts":20`, `"application_model_attempts":9223372036854775808`, 1), "bad-date": strings.Replace(base, `"ref":"explicit-synthetic-policy"`, `"ref":"explicit-synthetic-policy","expires_at":"invalid"`, 1), "unknown-provider": strings.Replace(base, `"model_provider_profile":"minimax"`, `"model_provider_profile":"automatic-fallback"`, 1)} {
		t.Run(name, func(t *testing.T) {
			cfg, err := Load(writeConfig(t, body))
			if err == nil || cfg.Configured() {
				t.Fatal("malformed execution config accepted")
			}
			if strings.Contains(err.Error(), "selected execution") || strings.Contains(err.Error(), "explicit-synthetic") {
				t.Fatal("error exposed private setting")
			}
		})
	}
}
func TestBudgetZeroAndAbsentStayDifferentWithoutInventedQuota(t *testing.T) {
	cfg, err := Load(writeConfig(t, strings.Replace(strings.Replace(configText(t), `"application_model_attempts":20`, `"application_model_attempts":0`, 1), `"session_model_attempts":7`, `"session_model_attempts":0`, 1)))
	if err != nil || !cfg.HasBudget() || cfg.Budget().ApplicationModelAttempts != 0 || cfg.Budget().SessionModelAttempts != 0 {
		t.Fatal("explicit zero budget changed", err)
	}
	var raw map[string]any
	if err = json.Unmarshal([]byte(configText(t)), &raw); err != nil {
		t.Fatal(err)
	}
	delete(raw, "budget")
	b, _ := json.Marshal(raw)
	cfg, err = Load(writeConfig(t, string(b)))
	if err != nil || cfg.HasBudget() || cfg.Budget().Ref != "" {
		t.Fatal("absent cumulative policy created a default authorization", err)
	}
}

func TestExecutionExampleHasNoGrantedRequestsAndSummaryHidesReferences(t *testing.T) {
	cfg, err := Load("../../execution-config.example.json")
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.HasBudget() || cfg.Budget().ApplicationCloudAttempts != 0 || cfg.Budget().ApplicationModelAttempts != 0 || cfg.Budget().SessionCloudAttempts != 0 || cfg.Budget().SessionModelAttempts != 0 {
		t.Fatal("example granted external requests")
	}
	encoded, _ := json.Marshal(cfg)
	if strings.Contains(string(encoded), cfg.ProfileRef()) || strings.Contains(string(encoded), cfg.Budget().Ref) || strings.Contains(string(encoded), "expires_at") {
		t.Fatal("execution summary exposed policy references")
	}
	for _, kind := range []string{"audit", "assistant_turn", "collect", "connection_check"} {
		if _, err := cfg.Profile(kind); err != nil {
			t.Fatal(err)
		}
	}
}
func TestSelectedExecutionFileCannotFallBackOrGrowPastItsBound(t *testing.T) {
	for _, path := range []string{filepath.Join(t.TempDir(), "missing.json"), t.TempDir(), writeConfig(t, strings.Repeat(" ", 65537))} {
		if cfg, err := Load(path); err == nil || cfg.Configured() {
			t.Fatal("invalid selected execution file was accepted")
		}
	}
}

func TestNonNullableProfileAndBudgetIntegersRejectJSONNull(t *testing.T) {
	base := configText(t)
	for name, body := range map[string]string{"profile": strings.Replace(base, `"cloud_http_attempts":100`, `"cloud_http_attempts":null`, 1), "budget": strings.Replace(base, `"application_model_attempts":20`, `"application_model_attempts":null`, 1)} {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(writeConfig(t, body)); err == nil {
				t.Fatal("null integer became explicit zero")
			}
		})
	}
}
func TestExplicitProfileReservesFinalizationWithoutChangingLegacy(t *testing.T) {
	body := strings.Replace(configText(t), `"finalize_seconds":10`, `"finalize_seconds":0`, 1)
	if _, err := Load(writeConfig(t, body)); err == nil {
		t.Fatal("explicit profile omitted finalization reserve")
	}
	if Default("collect").FinalizeSeconds != 0 {
		t.Fatal("legacy protection changed")
	}
}

func TestExplicitProfileCannotMasqueradeAsLegacyBinding(t *testing.T) {
	body := strings.Replace(configText(t), `"profile_ref":"explicit-synthetic-profile"`, `"profile_ref":"`+LegacyProfileRef+`"`, 1)
	if _, err := Load(writeConfig(t, body)); err == nil {
		t.Fatal("explicit configuration used reserved legacy identity")
	}
}

func TestExplicitFreshnessPolicyIsValidatedAndDoesNotRefillZero(t *testing.T) {
	var body map[string]any
	if e := json.Unmarshal([]byte(configText(t)), &body); e != nil {
		t.Fatal(e)
	}
	body["data_freshness"] = map[string]any{"version": "freshness-public-v2", "configuration_max_age_seconds": 0, "metadata_max_age_seconds": 120}
	raw, _ := json.Marshal(body)
	cfg, e := Load(writeConfig(t, string(raw)))
	if e != nil {
		t.Fatal(e)
	}
	policy, ok := cfg.DataFreshness()
	if !ok || policy.ConfigurationMaxAgeSeconds != 0 || policy.MetadataMaxAgeSeconds != 120 || policy.Version != "freshness-public-v2" || !cfg.Summary().FreshnessConfigured {
		t.Fatal("freshness changed explicit values")
	}
	policy.MetadataMaxAgeSeconds = 999
	again, _ := cfg.DataFreshness()
	if again.MetadataMaxAgeSeconds != 120 {
		t.Fatal("freshness getter shares mutable config")
	}
	for _, bad := range []any{nil, map[string]any{"version": "v", "configuration_max_age_seconds": 1}, map[string]any{"version": "v", "configuration_max_age_seconds": -1, "metadata_max_age_seconds": 0}} {
		body["data_freshness"] = bad
		raw, _ = json.Marshal(body)
		if _, e = Load(writeConfig(t, string(raw))); e == nil {
			t.Fatal("invalid freshness accepted", bad)
		}
	}
}
