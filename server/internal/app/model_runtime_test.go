package app

import (
	"cfw-policy-llm-agent/server/internal/cloudconfig"
	"context"
	"errors"
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

func TestRuntimeSingleOrBlockedAdapterPreservesOtherAuthorizedCategory(t *testing.T) {
	ctx := context.Background()
	store, _, _, _, _ := planCase(t)
	cfg := runtimeConfig(t, true)
	original := storage.BudgetPolicy{Ref: cfg.Budget().Ref, ApplicationCloudAttempts: 20, SessionCloudAttempts: 20, ApplicationModelAttempts: 20, SessionModelAttempts: 20, ExpiresAt: cfg.Budget().ExpiresAt}
	if e := store.ConfigureBudgetPolicy(ctx, original); e != nil {
		t.Fatal(e)
	}
	r, e := NewRuntime(store, cfg, cloudconfig.Config{})
	if e != nil {
		t.Fatal(e)
	}
	r.Close()
	after, e := store.GetBudgetPolicy(ctx, original.Ref)
	if e != nil || after.ApplicationCloudAttempts != 20 || after.SessionCloudAttempts != 20 || after.SessionModelAttempts != 20 {
		t.Fatal("single model source overwrote absent cloud or private session policy", after, e)
	}
	t.Setenv("CFW_RUNTIME_TEST_KEY", "")
	data := `{"api_style":"chat_completions","provider_profile":"openai-compatible","base_url":"https://runtime.example.invalid/v1","model":"explicit-model","api_key_env":"CFW_RUNTIME_TEST_KEY","max_output_tokens":512,"timeout_seconds":10,"max_http_attempts":3,"enabled":true,"budget":{"ref":"explicit-runtime-test","expires_at":"2099-01-01T00:00:00Z","application_model_attempts":20,"session_model_attempts":7}}`
	path := filepath.Join(t.TempDir(), "missing-key.json")
	if e = os.WriteFile(path, []byte(data), 0600); e != nil {
		t.Fatal(e)
	}
	blocked, e := modelconfig.Load(path)
	if e != nil {
		t.Fatal(e)
	}
	r, e = NewRuntime(store, blocked, cloudconfig.Config{})
	if e != nil {
		t.Fatal(e)
	}
	r.Close()
	after, e = store.GetBudgetPolicy(ctx, original.Ref)
	if e != nil || after.ApplicationModelAttempts != 20 || after.ApplicationCloudAttempts != 20 {
		t.Fatal("missing key revoked persistent authorization", after, e)
	}
	if e = store.ConfigureBudgetPolicy(ctx, original); e != nil {
		t.Fatal("original grant no longer compatible after a temporary block", e)
	}
}
func TestRuntimeCloudThenFirstModelCannotCreateDifferentAuthorizationRef(t *testing.T) {
	store, _, _, _, _ := planCase(t)
	cloud := cloudRuntimeConfig(t, true, "shared-cloud-first", 0)
	r, e := NewRuntime(store, modelconfig.Config{}, cloud)
	if e != nil {
		t.Fatal(e)
	}
	r.Close()
	cfg := runtimeConfig(t, true)
	if r, e = NewRuntime(store, cfg, cloudconfig.Config{}); !errors.Is(e, storage.ErrConflict) {
		if r != nil {
			r.Close()
		}
		t.Fatal("new category bypassed stable shared period", e)
	}
	if _, e = store.GetBudgetPolicy(context.Background(), cfg.Budget().Ref); !errors.Is(e, storage.ErrBudgetNotConfigured) {
		t.Fatal("rejected initialization left an orphan grant", e)
	}
}

func TestEnvModelReusesBoundAuthorizationHeadWithoutInventingGrant(t *testing.T) {
	for _, state := range []string{"none", "authorized", "zero"} {
		t.Run(state, func(t *testing.T) {
			store, _, _, _, _ := planCase(t)
			if state != "none" {
				count := int64(20)
				if state == "zero" {
					count = 0
				}
				p := storage.BudgetPolicy{Ref: "durable-env-period", ApplicationCloudAttempts: 17, SessionCloudAttempts: 10, ApplicationModelAttempts: count, SessionModelAttempts: count}
				if e := store.ConfigureBudgetForOwners(context.Background(), p, []string{"model-default"}); e != nil {
					t.Fatal(e)
				}
			}
			file := filepath.Join(t.TempDir(), "public.env")
			body := "CFW_MODEL_BASE_URL=https://model.example.invalid/v1\nCFW_MODEL_API_KEY=synthetic_env_only_key\nCFW_MODEL_NAME=preserved-MiniMax-test\nCFW_MODEL_API_STYLE=chat_completions\nCFW_MODEL_TIMEOUT_SECONDS=120\nCFW_MODEL_MAX_CALLS=15\nCFW_MODEL_MAX_OUTPUT_TOKENS=512\n"
			if e := os.WriteFile(file, []byte(body), 0600); e != nil {
				t.Fatal(e)
			}
			cfg, e := modelconfig.LoadEnvFile(file)
			if e != nil {
				t.Fatal(e)
			}
			runtime, e := NewModelRuntime(store, cfg)
			if e != nil {
				t.Fatal(e)
			}
			defer runtime.Close()
			if runtime.Plans.ModelReady != (state == "authorized") {
				t.Fatal("env readiness invented or discarded bound authorization", state)
			}
			if state != "none" {
				head, e := store.BudgetForOwner(context.Background(), "application-root")
				if e != nil || head.Ref != "durable-env-period" || head.ApplicationCloudAttempts != 17 {
					t.Fatal("env reuse changed shared head", head, e)
				}
				usage, e := store.BudgetUsage(context.Background(), head.Ref, "", "")
				if e != nil || usage.ModelAttempts != 0 || usage.CloudAttempts != 0 {
					t.Fatal("local startup consumed quota", usage, e)
				}
			}
			if cfg.Model() != "preserved-MiniMax-test" || cfg.MaxOutputTokens() != 512 || runtime.Plans.ModelHTTPAttempts != 15 {
				t.Fatal("env compatibility changed explicit private model limits")
			}
		})
	}
}
