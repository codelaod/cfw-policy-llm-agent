package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cfw-policy-llm-agent/server/internal/modelconfig"
	"cfw-policy-llm-agent/server/internal/storage"
)

func runtimeConfig(t *testing.T, enabled bool) modelconfig.Config {
	t.Helper()
	t.Setenv("CFW_RUNTIME_TEST_KEY", "synthetic-runtime-private-key")
	data := `{"api_style":"chat_completions","provider_profile":"openai-compatible","base_url":"https://runtime.example.invalid/v1","model":"explicit-model","api_key_env":"CFW_RUNTIME_TEST_KEY","max_output_tokens":512,"timeout_seconds":10,"max_http_attempts":3,"enabled":true,"budget":{"ref":"explicit-runtime-test","expires_at":"2099-01-01T00:00:00Z","application_model_attempts":20,"session_model_attempts":7}}`
	if !enabled {
		data = strings.Replace(data, `"enabled":true`, `"enabled":false`, 1)
	}
	path := filepath.Join(t.TempDir(), "model.json")
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := modelconfig.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestE3ModelRuntimeConfiguresOneSharedAdmissionWithoutSendingHTTP(t *testing.T) {
	store, _, _, _, _ := planCase(t)
	cfg := runtimeConfig(t, true)
	runtime, err := NewModelRuntime(store, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	if runtime.Factory == nil || !runtime.Plans.ModelReady || !runtime.Plans.ModelConfigured || runtime.Plans.ModelHTTPAttempts != 3 || runtime.Plans.BudgetRef != cfg.Budget().Ref || runtime.Plans.ConfigRevision != cfg.Revision() {
		t.Fatal("runtime lost explicit configuration binding")
	}
	usage, err := store.BudgetUsage(context.Background(), cfg.Budget().Ref, "", "")
	if err != nil || usage.ModelAttempts != 0 {
		t.Fatal("runtime startup reserved or sent model work", err)
	}
	policy, err := store.GetBudgetPolicy(context.Background(), cfg.Budget().Ref)
	if err != nil || policy.ApplicationModelAttempts != 20 || policy.SessionModelAttempts != 7 {
		t.Fatal("runtime expanded or changed explicit policy")
	}
}

func TestE3DisabledModelConfigDoesNotCreateAnAuthorizationPeriod(t *testing.T) {
	store, _, _, _, _ := planCase(t)
	cfg := runtimeConfig(t, false)
	runtime, err := NewModelRuntime(store, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	if runtime.Factory != nil || runtime.Plans.ModelReady || !runtime.Plans.ModelConfigured {
		t.Fatal("disabled model was made ready")
	}
	if _, err = store.GetBudgetPolicy(context.Background(), cfg.Budget().Ref); err != storage.ErrBudgetNotConfigured {
		t.Fatal("disabled configuration created a budget", err)
	}
}
