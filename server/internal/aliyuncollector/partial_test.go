package aliyuncollector

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"cfw-policy-llm-agent/server/internal/audit"
	"cfw-policy-llm-agent/server/internal/domain"
)

func requireFirstPartial(t *testing.T, result Collection, err error, code string) {
	t.Helper()
	if err != nil || result.Input.SchemaVersion != 1 || result.Input.Completeness.EnumerationComplete || result.Input.Completeness.ObservationConsistency != "unverified" || result.Input.Protection.State != "unknown" {
		t.Fatal("verified first-pass evidence did not remain explicitly partial", err, result.Input.Completeness)
	}
	if !strings.Contains(strings.Join(result.Input.Completeness.KnownGaps, ","), code) {
		t.Fatal("controlled missing-data cause disappeared", result.Input.Completeness.KnownGaps)
	}
	if err = audit.Validate(result.Input); err != nil {
		t.Fatal("partial input violated the ordinary immutable input contract", err)
	}
	for _, observation := range result.Observations {
		if observation.Round != 1 {
			t.Fatal("first failure silently retried a second collection pass")
		}
	}
}

func TestFirstACLPageFailureKeepsVerifiedRowsAndObjectMetadata(t *testing.T) {
	cfg := internetConfig()
	cfg.PageSize = 1
	aclCalls := 0
	r := &fixtureReader{read: func(op string, query map[string]string) (json.RawMessage, error) {
		if op != "DescribeControlPolicy" {
			return internetFixture(op, query)
		}
		aclCalls++
		if aclCalls == 2 {
			return nil, codedFixtureFailure("CLOUD_ACCESS_DENIED")
		}
		body, err := internetFixture(op, query)
		var data map[string]any
		_ = json.Unmarshal(body, &data)
		data["TotalCount"] = "2"
		encoded, _ := json.Marshal(data)
		return encoded, err
	}}
	c, err := New(r, cfg)
	if err != nil {
		t.Fatal(err)
	}
	out, err := c.Collect(context.Background(), domain.Selection{Kind: "assets", AssetIDs: []string{"i-one"}})
	requireFirstPartial(t, out, err, "CLOUD_ACCESS_DENIED")
	if aclCalls != 2 || len(out.Input.Policies) != 1 || len(out.Input.Assets) != 1 || !out.Input.Assets[0].MetadataComplete || out.Input.Assets[0].ProviderResourceID != "i-one" || len(out.Input.Assets[0].SecurityGroups) != 1 {
		t.Fatal("first page/object/SG facts were discarded or a retry escaped", aclCalls, out.Input)
	}
}

func TestFirstReferenceFailureRetainsPolicyWithUnresolvedDependency(t *testing.T) {
	r := &fixtureReader{read: func(op string, query map[string]string) (json.RawMessage, error) {
		if op == "DescribeAddressBook" {
			return nil, codedFixtureFailure("CLOUD_ACCESS_DENIED")
		}
		body, err := internetFixture(op, query)
		if op == "DescribeControlPolicy" {
			var data map[string]any
			_ = json.Unmarshal(body, &data)
			policy := data["Policys"].([]any)[0].(map[string]any)
			policy["SourceType"], policy["Source"], policy["SourceGroupType"] = "group", "group-one", "ip"
			encoded, _ := json.Marshal(data)
			return encoded, err
		}
		return body, err
	}}
	c, _ := New(r, internetConfig())
	out, err := c.Collect(context.Background(), domain.Selection{Kind: "assets", AssetIDs: []string{"i-one"}})
	requireFirstPartial(t, out, err, "CLOUD_ACCESS_DENIED")
	if len(out.Input.Policies) != 1 || out.Input.Policies[0].Source.Ref == "" || len(out.Input.AddressRefs) != 0 {
		t.Fatal("unreadable dependency erased policy or invented reference members")
	}
}

func TestFirstSGPageFailureKeepsReturnedPermissionsAsPartial(t *testing.T) {
	groupCalls := 0
	r := &fixtureReader{read: func(op string, query map[string]string) (json.RawMessage, error) {
		if op != "DescribeSecurityGroupAttribute" {
			return internetFixture(op, query)
		}
		groupCalls++
		if groupCalls > 1 {
			return nil, codedFixtureFailure("CLOUD_ACCESS_DENIED")
		}
		return json.RawMessage(`{"SecurityGroupId":"sg-one","RegionId":"cn-hangzhou","NextToken":"after-first","TotalCount":2,"Permissions":{"Permission":[{"Direction":"ingress","IpProtocol":"tcp","PortRange":"443/443","Policy":"accept","SourceCidrIp":"0.0.0.0/0"}]}}`), nil
	}}
	c, _ := New(r, internetConfig())
	out, err := c.Collect(context.Background(), domain.Selection{Kind: "assets", AssetIDs: []string{"i-one"}})
	requireFirstPartial(t, out, err, "CLOUD_ACCESS_DENIED")
	if groupCalls != 2 || len(out.Input.Assets) != 1 || out.Input.Assets[0].SecurityGroupCollection != "partial" || len(out.Input.Assets[0].SecurityGroups) != 1 || out.Input.Assets[0].SecurityGroups[0].Complete || len(out.Input.Assets[0].SecurityGroups[0].Permissions) != 1 {
		t.Fatal("SG partial permissions became absent/complete", out.Input.Assets)
	}
}

func TestFirstFailureWithoutReturnedIdentityNeverBuildsInput(t *testing.T) {
	r := &fixtureReader{read: func(string, map[string]string) (json.RawMessage, error) {
		return nil, codedFixtureFailure("CLOUD_ACCESS_DENIED")
	}}
	c, _ := New(r, internetConfig())
	out, err := c.Collect(context.Background(), domain.Selection{Kind: "assets", AssetIDs: []string{"i-one"}})
	if err != Failure("CLOUD_ACCESS_DENIED") || out.Input.SchemaVersion != 0 || r.calls != 1 {
		t.Fatal("failed identity lookup invented selected objects", err, out.Input)
	}
}

func TestFirstFatalResponseAfterValidMetadataNeverBuildsPartialInput(t *testing.T) {
	for _, code := range []string{"CLOUD_OWNER_MISMATCH", "CLOUD_RESPONSE_INVALID", "PROVIDER_RESPONSE_INVALID", "CLOUD_RESPONSE_CONTAINS_CREDENTIAL"} {
		t.Run(code, func(t *testing.T) {
			r := &fixtureReader{read: func(op string, query map[string]string) (json.RawMessage, error) {
				if op == "DescribeControlPolicy" {
					return nil, Failure(code)
				}
				return internetFixture(op, query)
			}}
			c, _ := New(r, internetConfig())
			out, err := c.Collect(context.Background(), domain.Selection{Kind: "assets", AssetIDs: []string{"i-one"}})
			if err != Failure(code) || out.Input.SchemaVersion != 0 {
				t.Fatal("unsafe provider data became ordinary partial input", err, out.Input)
			}
		})
	}
}

func TestCancellationAfterMetadataDoesNotSealFirstPartialInput(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := &fixtureReader{read: func(op string, query map[string]string) (json.RawMessage, error) {
		if op == "DescribeControlPolicy" {
			cancel()
			return nil, context.Canceled
		}
		return internetFixture(op, query)
	}}
	c, _ := New(r, internetConfig())
	out, err := c.Collect(ctx, domain.Selection{Kind: "assets", AssetIDs: []string{"i-one"}})
	if !errors.Is(err, context.Canceled) || out.Input.SchemaVersion != 0 || r.calls != 4 {
		t.Fatal("cancelled collection fabricated a sealed input or retried", err, out.Input, r.calls)
	}
}

func TestFirstRequestCancellationOrTimeoutDoesNotBecomePartialInput(t *testing.T) {
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
		t.Run(cause.Error(), func(t *testing.T) {
			r := &fixtureReader{read: func(op string, query map[string]string) (json.RawMessage, error) {
				if op == "DescribeControlPolicy" {
					return nil, cause
				}
				return internetFixture(op, query)
			}}
			c, _ := New(r, internetConfig())
			out, err := c.Collect(context.Background(), domain.Selection{Kind: "assets", AssetIDs: []string{"i-one"}})
			if !errors.Is(err, cause) || out.Input.SchemaVersion != 0 {
				t.Fatal("request interruption was converted to a sealed first input", err, out.Input)
			}
		})
	}
}
