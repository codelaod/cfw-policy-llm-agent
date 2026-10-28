package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cfw-policy-llm-agent/server/internal/cloudconfig"
	"cfw-policy-llm-agent/server/internal/modelconfig"
	"cfw-policy-llm-agent/server/internal/storage"
)

func cloudRuntimeConfig(t *testing.T, enabled bool, period string, modelLimit int) cloudconfig.Config {
	t.Helper()
	t.Setenv("CFW_CLOUD_RUNTIME_AK", "synthetic-cloud-runtime-ak")
	t.Setenv("CFW_CLOUD_RUNTIME_SECRET", "synthetic-cloud-runtime-secret")
	body := map[string]any{"enabled": enabled, "connection_ref": "explicit-cloud", "credential_mode": "env_ak", "access_key_id_env": "CFW_CLOUD_RUNTIME_AK", "access_key_secret_env": "CFW_CLOUD_RUNTIME_SECRET", "expected_account_id": "1234567890123456", "control_plane_region": "cn-hangzhou", "allowed_boundaries": []string{"internet/in", "nat/out"}, "allowed_regions": []string{"cn-hangzhou"}, "allowed_assets": []any{map[string]string{"region": "cn-hangzhou", "instance_id": "i-runtime-synthetic"}}, "allowed_nat_gateways": []any{map[string]string{"region": "cn-hangzhou", "id": "ngw-runtime-synthetic"}}, "timeout_seconds": 10, "max_http_attempts": 20, "budget": map[string]any{"ref": period, "expires_at": "2099-01-01T00:00:00Z", "application_cloud_attempts": 100, "session_cloud_attempts": 50, "application_model_attempts": modelLimit, "session_model_attempts": modelLimit}}
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "cloud.json")
	if err = os.WriteFile(p, b, 0600); err != nil {
		t.Fatal(err)
	}
	c, err := cloudconfig.Load(p)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestCloudRuntimeSharesOneBudgetManagerAndKeepsStartupLocal(t *testing.T) {
	store, _, _, _, _ := planCase(t)
	m := runtimeConfig(t, true)
	c := cloudRuntimeConfig(t, true, m.Budget().Ref, 4)
	r, err := NewRuntime(store, m, c)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if r.Factory == nil || r.Cloud == nil || r.Cloud.NewReader == nil || r.Plans.Cloud != r.Cloud || r.manager == nil || r.Plans.ConfigRevision == m.Revision() || r.Plans.ConfigRevision == c.Revision() {
		t.Fatal("runtime did not compose one shared configuration/admission")
	}
	policy, err := store.GetBudgetPolicy(context.Background(), m.Budget().Ref)
	if err != nil || policy.ApplicationCloudAttempts != 100 || policy.SessionCloudAttempts != 50 || policy.ApplicationModelAttempts != 4 || policy.SessionModelAttempts != 4 {
		t.Fatal("runtime lost explicit stricter shared budget", err)
	}
	usage, err := store.BudgetUsage(context.Background(), m.Budget().Ref, "", "")
	if err != nil || usage.ModelAttempts != 0 || usage.CloudAttempts != 0 {
		t.Fatal("startup performed remote work", err)
	}
	options := r.Cloud.Options()
	if !options.Configured || !options.Available || options.IdentityVerified || len(options.Scopes) != 2 {
		t.Fatal("local options fabricated verification")
	}
	encoded, _ := json.Marshal(options)
	for _, secret := range []string{c.AccessKeyID(), c.AccessKeySecret(), c.ExpectedAccountID(), c.CFWOrigin(), c.STSOrigin()} {
		if strings.Contains(string(encoded), secret) {
			t.Fatal("local collection options exposed private connection values")
		}
	}
}

func TestCloudRuntimeRejectsDifferentBudgetPeriodsBeforePersistingAuthorization(t *testing.T) {
	store, _, _, _, _ := planCase(t)
	m := runtimeConfig(t, true)
	c := cloudRuntimeConfig(t, true, "other-period", 4)
	if r, err := NewRuntime(store, m, c); err == nil {
		r.Close()
		t.Fatal("cloud created a second root budget period")
	}
	for _, ref := range []string{m.Budget().Ref, c.Budget().Ref} {
		if _, err := store.GetBudgetPolicy(context.Background(), ref); err != storage.ErrBudgetNotConfigured {
			t.Fatal("rejected config created authorization", err)
		}
	}
}

func TestDisabledCloudConfigurationRegistersOnlyLocalScopes(t *testing.T) {
	store, _, _, _, _ := planCase(t)
	c := cloudRuntimeConfig(t, false, "disabled-period", 0)
	r, err := NewRuntime(store, modelconfig.Config{}, c)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if r.manager != nil || r.Cloud == nil || r.Cloud.Ready || r.Cloud.Options().IdentityVerified {
		t.Fatal("disabled configuration enabled physical work")
	}
	if _, err = store.GetBudgetPolicy(context.Background(), c.Budget().Ref); err != storage.ErrBudgetNotConfigured {
		t.Fatal("disabled config created budget", err)
	}
	if _, err = store.CreateConversation(context.Background(), "配置范围", c.Scopes()[0].Ref); err != nil {
		t.Fatal("local configured scope absent", err)
	}
}

func TestCloudSharedBudgetExplicitZeroNeverEnablesModelRequests(t *testing.T) {
	store, _, _, _, _ := planCase(t)
	m := runtimeConfig(t, true)
	c := cloudRuntimeConfig(t, true, m.Budget().Ref, 0)
	r, err := NewRuntime(store, m, c)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if r.Factory != nil || r.Plans.ModelReady || !r.Plans.ModelConfigured {
		t.Fatal("zero shared ceiling created a model capability")
	}
	p, err := store.GetBudgetPolicy(context.Background(), m.Budget().Ref)
	if err != nil || p.ApplicationModelAttempts != 0 || p.SessionModelAttempts != 0 {
		t.Fatal("zero shared model ceiling was expanded", err)
	}
}
