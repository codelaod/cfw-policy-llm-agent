package recheck

import (
	"cfw-policy-llm-agent/server/internal/domain"
	"testing"
)

func TestIndependentGatewayPolicyComparisonRejectsChangedProviderIdentity(t *testing.T) {
	for _, field := range []string{"id", "region", "type", "account", "metadata_missing"} {
		t.Run(field, func(t *testing.T) {
			i, old := fixedPair()
			i.Scope.Boundary = "nat"
			i.Scope.Direction = "out"
			i.Scope.GatewayRef = "opaque-gateway-alias"
			i.Selection = domain.Selection{Kind: "gateway_policy"}
			i.Assets = nil
			i.GatewayMetadata = &domain.Resource{Provider: "aliyun", AccountRef: i.Scope.AccountRef, ResourceID: "ngw-observed-before", Region: "cn-hangzhou", ResourceType: "nat_gateway", Name: "display-before"}
			old.Manifest = manifest(i)
			old.Findings[0].RuleID = "NAT-01"
			old.Findings[0].SubjectRef = i.Scope.Ref
			old.Findings[0].ServiceRef = ""
			old.Findings[0].QueryScope = domain.Query{}
			next := copyInput(i)
			next.SnapshotVersion = "gateway-next"
			switch field {
			case "id":
				next.GatewayMetadata.ResourceID = "ngw-other-gateway"
			case "region":
				next.GatewayMetadata.Region = "cn-beijing"
			case "type":
				next.GatewayMetadata.ResourceType = "ecs"
			case "account":
				next.GatewayMetadata.AccountRef = "another-account"
			case "metadata_missing":
				next.GatewayMetadata = nil
			}
			nr := copyResult(old)
			nr.Manifest = manifest(next)
			nr.Findings[0].ID = "gateway-new"
			nr.Findings[0].Verdict = "pass"
			comparison, err := Compare(i, old, next, nr)
			if err != nil {
				t.Fatal(err)
			}
			if comparison.Comparable || len(comparison.Issues) > 0 {
				t.Fatalf("gateway policy mode bypassed provider identity comparison: field=%s result=%+v", field, comparison)
			}
		})
	}
}
