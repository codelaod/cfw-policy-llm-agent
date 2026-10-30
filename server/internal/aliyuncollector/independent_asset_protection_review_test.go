package aliyuncollector

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"cfw-policy-llm-agent/server/internal/audit"
	"cfw-policy-llm-agent/server/internal/domain"
)

func TestIndependentMixedAssetProtectionPreservesKnownClosedAsset(t *testing.T) {
	cfg := batchConfig(2)
	counts := map[string]int{}
	reader := &fixtureReader{read: func(op string, q map[string]string) (json.RawMessage, error) {
		raw, err := batchResponse(t, cfg, counts, op, q)
		if err != nil || op != "DescribeAssetList" || q["SearchItem"] != "i-002" {
			return raw, err
		}
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatal(err)
		}
		body["Assets"].([]any)[0].(map[string]any)["ProtectStatus"] = "closed"
		return json.Marshal(body)
	}}
	collector, err := New(reader, cfg)
	if err != nil {
		t.Fatal(err)
	}
	collected, err := collector.Collect(context.Background(), domain.Selection{Kind: "assets", AssetIDs: []string{"i-001", "i-002"}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := audit.Run(context.Background(), collected.Input)
	if err != nil {
		t.Fatal(err)
	}
	path, err := audit.Evaluate(context.Background(), collected.Input, domain.Query{SubjectRef: "i-001", FixedAddress: "192.0.2.1", Protocol: "tcp", Addresses: []string{"10.0.0.1"}, Ports: []domain.PortRange{{From: 443, To: 443}}})
	if err != nil || !path.Complete || len(path.Cells) != 1 || path.Cells[0].Action != "accept" {
		t.Fatalf("stable enabled asset lost independent configuration comparison: %+v %v", path, err)
	}
	observed := map[string]string{}
	for _, finding := range result.Findings {
		if finding.RuleID == "CHK-05" {
			observed[finding.SubjectRef] = finding.Verdict
			protected := false
			for _, ref := range finding.EvidenceRefs {
				for _, evidence := range result.Evidence {
					if evidence.ID == ref && strings.HasSuffix(evidence.Field, "/protection") && strings.HasPrefix(evidence.Field, "/assets/") {
						protected = true
					}
				}
			}
			if !protected {
				t.Fatalf("asset coverage lacks its fixed protection evidence: %+v", finding)
			}
		}
	}
	if observed["i-001"] != "pass" || observed["i-002"] != "fail" {
		t.Fatalf("two stable, independently observed protection states lost: got %v; aggregate=%+v", observed, collected.Input.Protection)
	}
}

func TestIndependentAssetProtectionChangesAreNotHiddenByUnchangedAggregate(t *testing.T) {
	cfg := batchConfig(2)
	counts := map[string]int{}
	observations := map[string]int{}
	reader := &fixtureReader{read: func(op string, q map[string]string) (json.RawMessage, error) {
		raw, err := batchResponse(t, cfg, counts, op, q)
		if err != nil || op != "DescribeAssetList" {
			return raw, err
		}
		id := q["SearchItem"]
		observations[id]++
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatal(err)
		}
		state := "open"
		if id == "i-002" && observations[id] == 1 || id == "i-001" && observations[id] == 2 {
			state = "closed"
		}
		body["Assets"].([]any)[0].(map[string]any)["ProtectStatus"] = state
		return json.Marshal(body)
	}}
	collector, err := New(reader, cfg)
	if err != nil {
		t.Fatal(err)
	}
	collected, err := collector.Collect(context.Background(), domain.Selection{Kind: "assets", AssetIDs: []string{"i-001", "i-002"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, asset := range collected.Input.Assets {
		if asset.Protection == nil || asset.Protection.State != "unknown" {
			t.Fatalf("asset-level conflict hidden by identical mixed aggregate: %+v", asset)
		}
	}
	if !strings.Contains(strings.Join(collected.Input.Completeness.KnownGaps, ","), "PROTECTION_CHANGED") {
		t.Fatal("missing conflict qualification")
	}
}

func TestIndependentStableAssetProtectionSurvivesOtherAssetLateReadFailure(t *testing.T) {
	cfg := batchConfig(2)
	counts := map[string]int{}
	observations := map[string]int{}
	reader := &fixtureReader{read: func(op string, q map[string]string) (json.RawMessage, error) {
		if op == "DescribeAssetList" {
			observations[q["SearchItem"]]++
			if q["SearchItem"] == "i-002" && observations["i-002"] == 2 {
				return nil, codedFixtureFailure("CLOUD_ACCESS_DENIED")
			}
		}
		return batchResponse(t, cfg, counts, op, q)
	}}
	collector, err := New(reader, cfg)
	if err != nil {
		t.Fatal(err)
	}
	collected, err := collector.Collect(context.Background(), domain.Selection{Kind: "assets", AssetIDs: []string{"i-001", "i-002"}})
	if err != nil {
		t.Fatal(err)
	}
	if collected.Input.ProtectionFor("i-001").State != "on" || collected.Input.ProtectionFor("i-002").State != "unknown" {
		t.Fatalf("independent second observations lost: %+v", collected.Input.Assets)
	}
	result, err := audit.Run(context.Background(), collected.Input)
	if err != nil {
		t.Fatal(err)
	}
	for _, finding := range result.Findings {
		if finding.RuleID != "CHK-05" {
			continue
		}
		expected := "pass"
		if finding.SubjectRef == "i-002" {
			expected = "insufficient_data"
		}
		if finding.Verdict != expected {
			t.Fatalf("affected coverage was not scoped: %+v", finding)
		}
	}
}
