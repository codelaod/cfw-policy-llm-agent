package cloudconfig

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func validFile() string {
	return `{"enabled":true,"connection_ref":"explicit-aliyun","credential_mode":"env_ak","access_key_id_env":"CFW_E4_TEST_AK","access_key_secret_env":"CFW_E4_TEST_SECRET","expected_account_id":"1234567890123456","control_plane_region":"cn-hangzhou","allowed_boundaries":["internet/in","internet/out","nat/out"],"allowed_regions":["cn-hangzhou"],"allowed_assets":[{"region":"cn-hangzhou","instance_id":"i-synthetic"}],"allowed_nat_gateways":[{"region":"cn-hangzhou","id":"ngw-synthetic"}],"timeout_seconds":10,"max_http_attempts":30,"budget":{"ref":"same-explicit-period","expires_at":"2099-01-01T00:00:00Z","application_cloud_attempts":100,"session_cloud_attempts":50,"application_model_attempts":20,"session_model_attempts":6}}`
}
func writeFile(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "cloud.json")
	if err := os.WriteFile(p, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	return p
}
func keys(t *testing.T) {
	t.Helper()
	t.Setenv("CFW_E4_TEST_AK", "synthetic-access-key")
	t.Setenv("CFW_E4_TEST_SECRET", "synthetic-access-secret")
}
func TestExplicitConfigAndSafeStableScopes(t *testing.T) {
	keys(t)
	c, err := Load(writeFile(t, validFile()))
	if err != nil {
		t.Fatal(err)
	}
	if c.CFWOrigin() != "https://cloudfw.aliyuncs.com" || len(c.Scopes()) != 3 || len(c.Assets()) != 1 || c.Budget().Ref != "same-explicit-period" {
		t.Fatal("explicit settings changed")
	}
	b, _ := json.Marshal(c)
	summary, _ := json.Marshal(c.Summary())
	scopes, _ := json.Marshal(c.Scopes())
	for _, v := range []string{"synthetic-access-key", "synthetic-access-secret", "1234567890123456", "ngw-synthetic", "i-synthetic", "aliyuncs.com"} {
		if strings.Contains(string(b)+string(summary)+string(scopes), v) {
			t.Fatal("private configuration escaped safe projection")
		}
	}
	for _, s := range c.Scopes() {
		binding, ok := c.ScopeBinding(s.Ref)
		if !ok || binding.Scope != s {
			t.Fatal("scope not bound")
		}
	}
	got := c.Assets()
	got[0].InstanceID = "other"
	if c.Assets()[0].InstanceID != "i-synthetic" {
		t.Fatal("getter exposed mutable slice")
	}
}
func TestNoAmbientCredentialFallbackAndDisabledDoesNotReadCredentials(t *testing.T) {
	t.Setenv("ALIBABA_CLOUD_ACCESS_KEY_ID", "ambient-key")
	t.Setenv("ALIBABA_CLOUD_ACCESS_KEY_SECRET", "ambient-secret")
	t.Setenv("CFW_E4_TEST_AK", "")
	t.Setenv("CFW_E4_TEST_SECRET", "")
	if _, e := Load(writeFile(t, validFile())); e != ErrCredential {
		t.Fatal("missing explicit credentials accepted", e)
	}
	c, e := Load(writeFile(t, strings.Replace(validFile(), `"enabled":true`, `"enabled":false`, 1)))
	if e != nil || c.Enabled() || !c.Summary().Configured {
		t.Fatal("disabled config should validate locally", e)
	}
}
func TestRejectExpandedScopeCredentialModesAndDuplicateJSON(t *testing.T) {
	keys(t)
	for _, change := range [][2]string{{`"env_ak"`, `"default"`}, {`"env_ak"`, `"assume_role"`}, {`"cn-hangzhou","instance_id"`, `"cn-beijing","instance_id"`}, {`"nat/out"`, `"nat/in"`}, {`"max_http_attempts":30`, `"max_http_attempts":101`}, {`"session_cloud_attempts":50`, `"session_cloud_attempts":101`}, {`"control_plane_region":"cn-hangzhou"`, `"control_plane_region":"attacker.invalid"`}, {`"enabled":true`, `"enabled":true,"enabled":false`}, {`"credential_mode":"env_ak"`, `"credential_mode":"env_ak","security_token_env":"TOKEN"`}} {
		if _, e := Load(writeFile(t, strings.Replace(validFile(), change[0], change[1], 1))); e == nil {
			t.Fatalf("invalid config accepted: %s", change[1])
		}
	}
}
func TestCredentialRotationDoesNotRenewBudgetOrChangeRevision(t *testing.T) {
	keys(t)
	p := writeFile(t, validFile())
	a, e := Load(p)
	if e != nil {
		t.Fatal(e)
	}
	t.Setenv("CFW_E4_TEST_SECRET", "rotated-synthetic-secret")
	b, e := Load(p)
	if e != nil || a.Revision() != b.Revision() || a.Budget().Ref != b.Budget().Ref {
		t.Fatal("credential rotation reset authorization", e)
	}
}

func TestStaticSTSIsExplicitAndSampleRemainsDisabled(t *testing.T) {
	keys(t)
	t.Setenv("CFW_E4_TEST_TOKEN", "synthetic-sts-token")
	sts := strings.Replace(validFile(), `"credential_mode":"env_ak"`, `"credential_mode":"env_sts","security_token_env":"CFW_E4_TEST_TOKEN"`, 1)
	c, err := Load(writeFile(t, sts))
	if err != nil || c.SecurityToken() != "synthetic-sts-token" || c.Summary().Refreshable || c.Summary().CredentialExpiryKnown {
		t.Fatal("static STS misrepresented", err)
	}
	c, err = Load("../../cloud-config.example.json")
	if err != nil || c.Enabled() {
		t.Fatal("example must validate without enabling credentials or effects", err)
	}
}

func TestReservedConnectionAliasAndCrossRegionDuplicateInstanceAreRefused(t *testing.T) {
	keys(t)
	for _, body := range []string{strings.Replace(validFile(), `"connection_ref":"explicit-aliyun"`, `"connection_ref":"local-offline"`, 1), strings.Replace(strings.Replace(validFile(), `"allowed_regions":["cn-hangzhou"]`, `"allowed_regions":["cn-hangzhou","cn-beijing"]`, 1), `"allowed_assets":[{"region":"cn-hangzhou","instance_id":"i-synthetic"}]`, `"allowed_assets":[{"region":"cn-hangzhou","instance_id":"i-synthetic"},{"region":"cn-beijing","instance_id":"i-synthetic"}]`, 1)} {
		if _, err := Load(writeFile(t, body)); err == nil {
			t.Fatal("reserved alias or ambiguous instance region accepted")
		}
	}
}
