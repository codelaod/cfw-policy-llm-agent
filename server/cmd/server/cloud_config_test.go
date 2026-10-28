package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCloudConfigurationCheckNeedsOnlyTheSelectedFileAndReturnsRedactedSummary(t *testing.T) {
	var out bytes.Buffer
	if err := checkConfiguration("", "../../cloud-config.example.json", &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"cloud"`) || !strings.Contains(out.String(), `"configured":true`) || !strings.Contains(out.String(), `"enabled":false`) {
		t.Fatal("cloud summary missing", out.String())
	}
	for _, private := range []string{"expected_account_id", "access_key_id_env", "allowed_assets", "aliyuncs.com", "connection_ref"} {
		if strings.Contains(out.String(), private) {
			t.Fatal("cloud check leaked configuration")
		}
	}
	path := filepath.Join(t.TempDir(), "cloud.json")
	if err := os.WriteFile(path, []byte(`{"enabled":true,"credential_mode":"default_profile"}`), 0600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := checkConfiguration("", path, &out); err == nil || out.Len() != 0 {
		t.Fatal("invalid cloud check returned success or partial private data")
	}
}

func TestCombinedConfigurationCheckRejectsDifferentEnabledBudgetPeriods(t *testing.T) {
	b, err := os.ReadFile("../../cloud-config.example.json")
	if err != nil {
		t.Fatal(err)
	}
	var c map[string]any
	if err = json.Unmarshal(b, &c); err != nil {
		t.Fatal(err)
	}
	c["enabled"] = true
	c["credential_mode"] = "env_ak"
	delete(c, "security_token_env")
	t.Setenv("CFW_ALIYUN_ACCESS_KEY_ID", "synthetic-cli-ak")
	t.Setenv("CFW_ALIYUN_ACCESS_KEY_SECRET", "synthetic-cli-secret")
	t.Setenv("CFW_CHECK_MODEL_KEY", "synthetic-cli-model-key")
	cloudFile, modelFile := filepath.Join(t.TempDir(), "cloud.json"), filepath.Join(t.TempDir(), "model.json")
	b, _ = json.Marshal(c)
	if err = os.WriteFile(cloudFile, b, 0600); err != nil {
		t.Fatal(err)
	}
	model := `{"api_style":"chat_completions","provider_profile":"openai-compatible","base_url":"https://example.invalid/v1","model":"explicit","api_key_env":"CFW_CHECK_MODEL_KEY","max_output_tokens":512,"timeout_seconds":10,"max_http_attempts":3,"enabled":true,"budget":{"ref":"different-period","expires_at":"2099-01-01T00:00:00Z","application_model_attempts":20,"session_model_attempts":7}}`
	if err = os.WriteFile(modelFile, []byte(model), 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err = checkConfiguration(modelFile, cloudFile, &out); err == nil || out.Len() != 0 {
		t.Fatal("config-check accepted independently resettable authorization periods")
	}
}
