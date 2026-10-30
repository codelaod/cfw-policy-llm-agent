package aliyuncollector

import (
	"cfw-policy-llm-agent/server/internal/domain"
	"context"
	"encoding/json"
	"testing"
)

func TestIndependentPartialRetainsEarlierSGWithAllIdentitiesVerified(t *testing.T) {
	cfg := internetConfig()
	cfg.AssetRegions["i-two"] = "cn-hangzhou"
	r := &fixtureReader{read: func(op string, q map[string]string) (json.RawMessage, error) {
		switch op {
		case "DescribeInstances":
			return json.RawMessage(`{"NextToken":"","Instances":{"Instance":[{"InstanceId":"i-one","RegionId":"cn-hangzhou","SecurityGroupIds":{"SecurityGroupId":["sg-one"]}},{"InstanceId":"i-two","RegionId":"cn-hangzhou","SecurityGroupIds":{"SecurityGroupId":["sg-two"]}}]}}`), nil
		case "DescribeAssetList":
			if q["SearchItem"] == "i-two" {
				return nil, codedFixtureFailure("CLOUD_ACCESS_DENIED")
			}
		}
		return internetFixture(op, q)
	}}
	c, err := New(r, cfg)
	if err != nil {
		t.Fatal(err)
	}
	result, err := c.Collect(context.Background(), domain.Selection{Kind: "assets", AssetIDs: []string{"i-one", "i-two"}})
	if err != nil || result.Input.SchemaVersion != 1 || len(result.Input.Assets) != 2 {
		t.Fatalf("second object's CFW failure discarded first object's SG despite both verified ECS identities: error=%v input_assets=%d observations=%d calls=%d", err, len(result.Input.Assets), len(result.Observations), r.calls)
	}
	if r.calls != 4 || len(result.Input.Assets[0].SecurityGroups) != 1 || result.Input.Completeness.EnumerationComplete || result.Input.Protection.State != "unknown" {
		t.Fatal("partial facts lost or claimed complete", result.Input)
	}
	if result.Input.Assets[1].AddressObserved || result.Input.Assets[1].SecurityGroupCollection != "not_collected" {
		t.Fatal("unread data fabricated", result.Input.Assets[1])
	}
	for _, asset := range result.Input.Assets {
		if len(asset.Services) != 0 || asset.AllowedConfirmed || asset.AllowedComplete {
			t.Fatal("metadata-only recovery fabricated confirmed business", asset)
		}
	}
}

func TestIndependentPartialNeverCompletesIdentitiesFromConfiguredAssetMap(t *testing.T) {
	for _, kind := range []string{"second_missing", "metadata_paging_failed", "foreign_region"} {
		t.Run(kind, func(t *testing.T) {
			cfg := internetConfig()
			cfg.AssetRegions["i-two"] = "cn-hangzhou"
			metadataCalls := 0
			r := &fixtureReader{read: func(op string, q map[string]string) (json.RawMessage, error) {
				if op == "DescribeInstances" {
					metadataCalls++
					if kind == "metadata_paging_failed" && metadataCalls > 1 {
						return nil, codedFixtureFailure("CLOUD_ACCESS_DENIED")
					}
					rows := []any{map[string]any{"InstanceId": "i-one", "RegionId": "cn-hangzhou", "SecurityGroupIds": map[string]any{"SecurityGroupId": []string{"sg-one"}}}}
					next := ""
					if kind != "second_missing" {
						region := "cn-hangzhou"
						if kind == "foreign_region" {
							region = "cn-beijing"
						}
						rows = append(rows, map[string]any{"InstanceId": "i-two", "RegionId": region})
					}
					if kind == "metadata_paging_failed" {
						next = "incomplete-page"
					}
					return json.Marshal(map[string]any{"NextToken": next, "Instances": map[string]any{"Instance": rows}})
				}
				if op == "DescribeControlPolicy" {
					return nil, codedFixtureFailure("CLOUD_ACCESS_DENIED")
				}
				return internetFixture(op, q)
			}}
			c, err := New(r, cfg)
			if err != nil {
				t.Fatal(err)
			}
			result, err := c.Collect(context.Background(), domain.Selection{Kind: "assets", AssetIDs: []string{"i-one", "i-two"}})
			if err == nil || result.Input.SchemaVersion != 0 {
				t.Fatal("configuration or incomplete metadata became an observed identity", kind, err, result.Input)
			}
		})
	}
}
