package app

import (
	"encoding/json"
	"os"
	"testing"

	"cfw-policy-llm-agent/server/internal/cloudconfig"
)

func cloudRuntimeConfig(t *testing.T, enabled bool) cloudconfig.Config {
	t.Helper()
	t.Setenv("CFW_CLOUD_RUNTIME_AK", "synthetic-cloud-runtime-ak")
	t.Setenv("CFW_CLOUD_RUNTIME_SECRET", "synthetic-cloud-runtime-secret")
	body := map[string]any{"enabled": enabled, "connection_ref": "explicit-cloud", "credential_mode": "env_ak", "access_key_id_env": "CFW_CLOUD_RUNTIME_AK", "access_key_secret_env": "CFW_CLOUD_RUNTIME_SECRET", "expected_account_id": "1234567890123456", "control_plane_region": "cn-hangzhou", "allowed_boundaries": []string{"internet/in", "nat/out"}, "allowed_regions": []string{"cn-hangzhou"}, "allowed_assets": []any{map[string]string{"region": "cn-hangzhou", "instance_id": "i-runtime-synthetic"}}, "allowed_nat_gateways": []any{map[string]string{"region": "cn-hangzhou", "id": "ngw-runtime-synthetic"}}, "timeout_seconds": 10}
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	c, err := cloudconfig.ParseSettings(b, os.LookupEnv)
	if err != nil {
		t.Fatal(err)
	}
	return c
}
