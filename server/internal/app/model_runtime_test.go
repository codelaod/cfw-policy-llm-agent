package app

import (
	"context"

	"os"
	"strings"
	"testing"

	"cfw-policy-llm-agent/server/internal/modelconfig"
)

func runtimeConfig(t *testing.T, enabled bool) modelconfig.Config {
	t.Helper()
	t.Setenv("CFW_RUNTIME_TEST_KEY", "synthetic-runtime-private-key")
	data := `{"api_style":"chat_completions","provider_profile":"openai-compatible","base_url":"https://runtime.example.invalid/v1","model":"explicit-model","api_key_env":"CFW_RUNTIME_TEST_KEY","max_output_tokens":512,"timeout_seconds":10,"enabled":true}`
	if !enabled {
		data = strings.Replace(data, `"enabled":true`, `"enabled":false`, 1)
	}
	cfg, err := modelconfig.ParseSettings([]byte(data), os.LookupEnv)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestConfiguredModelNeedsNoCumulativeAuthorizationAndStartupMakesNoRequests(t *testing.T) {
	store, _, _, _, _ := planCase(t)
	for _, enabled := range []bool{true, false, true} {
		runtime, err := NewModelRuntime(store, runtimeConfig(t, enabled))
		if err != nil {
			t.Fatal(err)
		}
		if runtime.Plans.ModelReady != enabled || (runtime.Factory != nil) != enabled {
			t.Fatal("model readiness changed by historical settings")
		}
		runtime.Close()
	}
	usage, err := store.AttemptUsage(context.Background(), "", "")
	if err != nil || usage.ModelAttempts != 0 || usage.CloudAttempts != 0 {
		t.Fatal("startup performed provider work", err)
	}
}
