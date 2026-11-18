package cloudconfig

import (
	"strings"
	"testing"
)

func TestDisabledExpiredCloudConfigDoesNotReadCredentials(t *testing.T) {
	body := strings.Replace(strings.Replace(validFile(), `"enabled":true`, `"enabled":false`, 1), "2099-01-01", "2020-01-01", 1)
	c, err := ParseSettings([]byte(body), func(string) (string, bool) { t.Fatal("disabled config read credentials"); return "", false })
	if err != nil || c.RequestedEnabled() || c.Ready() || !c.Summary().StructureValid || c.Summary().LedgerState != "not_checked" {
		t.Fatalf("disabled expired settings were malformed: %v", err)
	}
}
func TestOptionalObjectAllowlistStillChecksRegionTypeAndBoundary(t *testing.T) {
	body := strings.Replace(strings.Replace(validFile(), `"allowed_assets":[{"region":"cn-hangzhou","instance_id":"i-synthetic"}]`, `"allowed_assets":[]`, 1), `"allowed_nat_gateways":[{"region":"cn-hangzhou","id":"ngw-synthetic"}]`, `"allowed_nat_gateways":[]`, 1)
	c, err := ParseSettings([]byte(body), func(string) (string, bool) { return "synthetic", true })
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
	restricted, err := ParseSettings([]byte(validFile()), func(string) (string, bool) { return "synthetic", true })
	if err != nil {
		t.Fatal(err)
	}
	if restricted.AllowsAsset("cn-hangzhou", "i-new") || restricted.AllowsGateway("cn-hangzhou", "ngw-new") {
		t.Fatal("explicit allowlist did not remain restrictive")
	}
}

func TestCloudCredentialControlsAreBlockedAsOneRecord(t *testing.T) {
	for code := 0; code <= 127; code++ {
		if code >= 32 && code != 127 {
			continue
		}
		secret := "synthetic" + string(rune(code)) + "key"
		c, err := ParseSettings([]byte(validFile()), func(key string) (string, bool) {
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
