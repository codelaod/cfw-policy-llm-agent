package cloudconfig

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func validFile() string {
	return `{"enabled":true,"connection_ref":"explicit-aliyun","credential_mode":"env_ak","access_key_id_env":"CFW_E4_TEST_AK","access_key_secret_env":"CFW_E4_TEST_SECRET","expected_account_id":"1234567890123456","control_plane_region":"cn-hangzhou","allowed_boundaries":["internet/in","internet/out","nat/out"],"allowed_regions":["cn-hangzhou"],"allowed_assets":[{"region":"cn-hangzhou","instance_id":"i-synthetic"}],"allowed_nat_gateways":[{"region":"cn-hangzhou","id":"ngw-synthetic"}],"timeout_seconds":10}`
}
func parseFixture(body []byte) (Config, error) { return ParseSettings(body, os.LookupEnv) }

func keys(t *testing.T) {
	t.Helper()
	t.Setenv("CFW_E4_TEST_AK", "synthetic-access-key")
	t.Setenv("CFW_E4_TEST_SECRET", "synthetic-access-secret")
}
func TestExplicitConfigAndSafeStableScopes(t *testing.T) {
	keys(t)
	c, err := parseFixture([]byte(validFile()))
	if err != nil {
		t.Fatal(err)
	}
	if c.CFWOrigin() != "https://cloudfw.aliyuncs.com" || len(c.Scopes()) != 3 || len(c.Assets()) != 1 {
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
	blocked, e := parseFixture([]byte(validFile()))
	if e != nil || blocked.Ready() || !blocked.RequestedEnabled() || blocked.Summary().CredentialState != "missing" {
		t.Fatal("missing explicit credentials must block this adapter without ambient fallback", e)
	}
	c, e := parseFixture([]byte(strings.Replace(validFile(), `"enabled":true`, `"enabled":false`, 1)))
	if e != nil || c.Enabled() || !c.Summary().Configured {
		t.Fatal("disabled config should validate locally", e)
	}
}
func TestCredentialRotationDoesNotRenewBudgetOrChangeRevision(t *testing.T) {
	keys(t)
	p := []byte(validFile())
	a, e := parseFixture(p)
	if e != nil {
		t.Fatal(e)
	}
	t.Setenv("CFW_E4_TEST_SECRET", "rotated-synthetic-secret")
	b, e := parseFixture(p)
	if e != nil || a.Revision() != b.Revision() {
		t.Fatal("credential rotation reset authorization", e)
	}
}

func TestStaticSTSIsExplicit(t *testing.T) {
	keys(t)
	t.Setenv("CFW_E4_TEST_TOKEN", "synthetic-sts-token")
	sts := strings.Replace(validFile(), `"credential_mode":"env_ak"`, `"credential_mode":"env_sts","security_token_env":"CFW_E4_TEST_TOKEN"`, 1)
	c, err := parseFixture([]byte(sts))
	if err != nil || c.SecurityToken() != "synthetic-sts-token" || c.Summary().Refreshable || c.Summary().CredentialExpiryKnown {
		t.Fatal("static STS misrepresented", err)
	}

}

func TestReservedConnectionAliasAndCrossRegionDuplicateInstanceAreRefused(t *testing.T) {
	keys(t)
	for _, body := range []string{strings.Replace(validFile(), `"connection_ref":"explicit-aliyun"`, `"connection_ref":"local-offline"`, 1), strings.Replace(strings.Replace(validFile(), `"allowed_regions":["cn-hangzhou"]`, `"allowed_regions":["cn-hangzhou","cn-beijing"]`, 1), `"allowed_assets":[{"region":"cn-hangzhou","instance_id":"i-synthetic"}]`, `"allowed_assets":[{"region":"cn-hangzhou","instance_id":"i-synthetic"},{"region":"cn-beijing","instance_id":"i-synthetic"}]`, 1)} {
		if _, err := parseFixture([]byte(body)); err == nil {
			t.Fatal("reserved alias or ambiguous instance region accepted")
		}
	}
}

func TestRejectCaseCollidingJSONFields(t *testing.T) {
	keys(t)
	for _, tc := range []struct{ name, old, replacement string }{
		{"enabled", `"enabled":true`, `"enabled":false,"ENABLED":true`},
		{"credential_mode", `"credential_mode":"env_ak"`, `"credential_mode":"env_sts","CREDENTIAL_MODE":"env_ak"`},
		{"escaped_case_collision", `"enabled":true`, `"enabled":false,"ENAB\u004ced":true`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := parseFixture([]byte(strings.Replace(validFile(), tc.old, tc.replacement, 1))); err != ErrInvalid {
				t.Fatalf("case-colliding JSON fields must be rejected: %v", err)
			}
		})
	}
}

func TestRejectInvalidCloudIdentityMode(t *testing.T) {
	keys(t)
	for _, mode := range []string{"role", "default-profile", ""} {
		if _, e := parseFixture([]byte(strings.Replace(validFile(), `"env_ak"`, `"`+mode+`"`, 1))); e == nil {
			t.Fatal("unsupported credential mode accepted")
		}
	}
}
