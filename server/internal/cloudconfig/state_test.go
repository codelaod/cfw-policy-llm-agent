package cloudconfig

import (
	"strings"
	"testing"
)

func TestDisabledExpiredCloudConfigDoesNotReadCredentials(t *testing.T) {
	body := strings.Replace(strings.Replace(validFile(), `"enabled":true`, `"enabled":false`, 1), "2099-01-01", "2020-01-01", 1)
	c, err := load(writeFile(t, body), func(string) (string, bool) { t.Fatal("disabled config read credentials"); return "", false })
	if err != nil || c.RequestedEnabled() || c.Ready() || !c.Summary().StructureValid || c.Summary().LedgerState != "not_checked" {
		t.Fatalf("disabled expired settings were malformed: %v", err)
	}
}
func TestEnabledCloudMissingCredentialAndExpiredPolicyAreBlocked(t *testing.T) {
	c, err := load(writeFile(t, validFile()), func(string) (string, bool) { return "", false })
	if err != nil || !c.RequestedEnabled() || c.Ready() || c.Summary().CredentialState != "missing" {
		t.Fatalf("missing selected credential not blocked: %v", err)
	}
	c, err = load(writeFile(t, strings.Replace(validFile(), "2099-01-01", "2020-01-01", 1)), func(string) (string, bool) { return "synthetic", true })
	if err != nil || c.Ready() || c.Summary().AuthorizationState != "expired" {
		t.Fatalf("expired authorization became structural error or executable: %v", err)
	}
	body := strings.Replace(validFile(), `"credential_mode":"env_ak"`, `"credential_mode":"env_sts","security_token_env":"CFW_FAKE_STS","credential_expires_at":"2020-01-01T00:00:00Z"`, 1)
	c, err = load(writeFile(t, body), func(string) (string, bool) { return "synthetic", true })
	if err != nil || c.Ready() || c.Summary().CredentialState != "expired" || c.Summary().Refreshable {
		t.Fatalf("static credential expiry not independent: %v %+v", err, c.Summary())
	}
}
func TestOptionalObjectAllowlistStillChecksRegionTypeAndBoundary(t *testing.T) {
	body := strings.Replace(strings.Replace(validFile(), `"allowed_assets":[{"region":"cn-hangzhou","instance_id":"i-synthetic"}]`, `"allowed_assets":[]`, 1), `"allowed_nat_gateways":[{"region":"cn-hangzhou","id":"ngw-synthetic"}]`, `"allowed_nat_gateways":[]`, 1)
	c, err := load(writeFile(t, body), func(string) (string, bool) { return "synthetic", true })
	if err != nil {
		t.Fatal(err)
	}
	if !c.AllowsAsset("cn-hangzhou", "i-new") || c.AllowsAsset("cn-beijing", "i-new") || c.AllowsAsset("cn-hangzhou", "ngw-new") {
		t.Fatal("optional object list bypassed identity bounds")
	}
	if !c.AllowsGateway("cn-hangzhou", "ngw-new") || c.AllowsGateway("cn-beijing", "ngw-new") {
		t.Fatal("optional gateway list bypassed region")
	}
	binding, ok := c.ScopeForGateway("cn-hangzhou", "ngw-new")
	if !ok || binding.GatewayID != "ngw-new" || binding.Scope.Boundary != "nat" || binding.Scope.Direction != "out" {
		t.Fatal("resolved gateway did not form scoped binding")
	}
	if _, ok := c.ScopeForGateway("cn-beijing", "ngw-new"); ok {
		t.Fatal("outside gateway formed a scope")
	}
	if len(c.Scopes()) != 2 {
		t.Fatal("empty NAT list implicitly enumerated a gateway")
	}
	restricted, err := load(writeFile(t, validFile()), func(string) (string, bool) { return "synthetic", true })
	if err != nil {
		t.Fatal(err)
	}
	if restricted.AllowsAsset("cn-hangzhou", "i-new") || restricted.AllowsGateway("cn-hangzhou", "ngw-new") {
		t.Fatal("explicit allowlist did not remain restrictive")
	}
}

func TestExplicitCloudZeroAndPolicyBindingDoNotCreateAllowance(t *testing.T) {
	body := strings.Replace(strings.Replace(validFile(), `"application_cloud_attempts":100`, `"application_cloud_attempts":0`, 1), `"session_cloud_attempts":50`, `"session_cloud_attempts":0`, 1)
	c, err := load(writeFile(t, body), func(string) (string, bool) { return "synthetic", true })
	if err != nil || c.Ready() || c.Budget().ApplicationCloudAttempts != 0 || c.Summary().AuthorizationState != "blocked" {
		t.Fatal("zero cloud allowance became executable", err)
	}
	bound, err := c.WithBudget(Budget{Ref: "explicit-new-policy", ApplicationCloudAttempts: 5, SessionCloudAttempts: 2})
	if err != nil || !bound.Ready() || bound.ExpectedAccountID() != c.ExpectedAccountID() || bound.AccessKeyID() != c.AccessKeyID() || bound.MaxHTTPAttempts() != c.MaxHTTPAttempts() {
		t.Fatal("explicit binding changed source identity or limits", err)
	}
	expired, err := bound.WithBudget(Budget{Ref: "explicit-new-policy", ExpiresAt: "2020-01-01T00:00:00Z", ApplicationCloudAttempts: 5, SessionCloudAttempts: 2})
	if err != nil || expired.Ready() || expired.Summary().AuthorizationState != "expired" {
		t.Fatal("expired bound policy became executable", err)
	}
}

func TestCloudCredentialControlsAreBlockedAsOneRecord(t *testing.T) {
	for code := 0; code <= 127; code++ {
		if code >= 32 && code != 127 {
			continue
		}
		secret := "synthetic" + string(rune(code)) + "key"
		c, err := load(writeFile(t, validFile()), func(key string) (string, bool) {
			if key == "CFW_E4_TEST_SECRET" {
				return secret, true
			}
			return "synthetic-valid", true
		})
		if err != nil || c.Ready() || c.AccessKeyID() != "" || c.AccessKeySecret() != "" || c.Summary().CredentialState != "invalid" {
			t.Fatal("invalid cloud record was not blocked atomically")
		}
	}
}

func TestExplicitCloudLimitsAreNotHiddenBehindLegacyProfileCaps(t *testing.T) {
	body := strings.Replace(strings.Replace(validFile(), `"timeout_seconds":10`, `"timeout_seconds":120`, 1), `"max_http_attempts":30`, `"max_http_attempts":1000`, 1)
	c, err := load(writeFile(t, body), func(string) (string, bool) { return "synthetic", true })
	if err != nil || c.Timeout().Seconds() != 120 || c.MaxHTTPAttempts() != 1000 {
		t.Fatal("explicit cloud limit rejected by hidden legacy cap", err)
	}
}
