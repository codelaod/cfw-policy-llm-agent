package aliyuncollector

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"cfw-policy-llm-agent/server/internal/domain"
	"cfw-policy-llm-agent/server/internal/workload"
)

func explicitWorkload() workload.Resolved {
	l := workload.Default("collect")
	l.ExecutionSeconds, l.CollectionSeconds, l.FinalizeSeconds = 150, 120, 10
	l.CloudHTTPAttempts, l.AssetsPerBatch, l.MaxPolicies = 300, 120, 2000
	return workload.Resolved{ProfileRef: "public-collector-fixture", Limits: l}
}
func batchConfig(count int) Config {
	c := internetConfig()
	c.AssetRegions = map[string]string{}
	c.Workload = explicitWorkload()
	c.AllowedRegions = []string{"cn-hangzhou", "cn-beijing"}
	for i := 1; i <= count; i++ {
		c.AssetRegions[fmt.Sprintf("i-%03d", i)] = "cn-hangzhou"
	}
	return c
}
func batchResponse(t *testing.T, cfg Config, counts map[string]int, op string, q map[string]string) (json.RawMessage, error) {
	t.Helper()
	counts[op]++
	switch op {
	case "DescribeInstances":
		var ids []string
		if json.Unmarshal([]byte(q["InstanceIds"]), &ids) != nil || len(ids) == 0 || len(ids) > 100 {
			t.Fatalf("not an explicit bounded ID batch: %+v", q)
		}
		rows := []any{}
		for _, id := range ids {
			n, _ := strconv.Atoi(strings.TrimPrefix(id, "i-"))
			rows = append(rows, map[string]any{"InstanceId": id, "RegionId": cfg.AssetRegions[id], "InstanceName": fmt.Sprintf("web-%03d", n), "OSType": "linux", "Tags": map[string]any{"Tag": []any{map[string]any{"TagKey": "app", "TagValue": "payments"}}}, "SecurityGroupIds": map[string]any{"SecurityGroupId": []string{fmt.Sprintf("sg-%d", n%4)}}})
		}
		return json.Marshal(map[string]any{"Instances": map[string]any{"Instance": rows}, "NextToken": ""})
	case "DescribeAssetList":
		id := q["SearchItem"]
		if cfg.AssetRegions[id] == "" || q["RegionNo"] != "" {
			t.Fatalf("CFW query widened: %+v", q)
		}
		n, _ := strconv.Atoi(strings.TrimPrefix(id, "i-"))
		return json.Marshal(map[string]any{"TotalCount": 1, "Assets": []any{map[string]any{"BindInstanceId": id, "ResourceInstanceId": "eip-" + id, "RegionID": cfg.AssetRegions[id], "AliUid": "1234567890123456", "InternetAddress": fmt.Sprintf("192.0.2.%d", n), "ProtectStatus": "open"}}})
	case "DescribeSecurityGroupAttribute":
		if q["Direction"] != "all" {
			t.Fatalf("both directions were not requested: %+v", q)
		}
		return json.Marshal(map[string]any{"SecurityGroupId": q["SecurityGroupId"], "RegionId": q["RegionId"], "NextToken": "", "Permissions": map[string]any{"Permission": []any{map[string]any{"Direction": "ingress", "Policy": "accept", "IpProtocol": "tcp", "PortRange": "443/443", "SourceCidrIp": "0.0.0.0/0", "Priority": "1", "SourceGroupId": "sg-peer", "NicType": "intranet"}, map[string]any{"Direction": "egress", "Policy": "drop", "IpProtocol": "udp", "PortRange": "53/53", "DestCidrIp": "0.0.0.0/0", "Priority": "2"}}}})
	case "DescribeControlPolicy":
		p, _ := strconv.Atoi(q["CurrentPage"])
		start, end := (p-1)*50, p*50
		if end > 80 {
			end = 80
		}
		rows := []any{}
		for i := start; i < end; i++ {
			rows = append(rows, map[string]any{"AclUuid": fmt.Sprintf("acl-%03d", i), "Direction": "in", "SourceType": "group", "Source": fmt.Sprintf("sources-%d", i%3), "SourceGroupType": "ip", "DestinationType": "net", "Destination": "0.0.0.0/0", "DestPortType": "port", "DestPort": "443", "Proto": "TCP", "AclAction": "accept", "Release": "true", "Order": i + 1, "ApplicationNameList": []string{"ANY"}, "RepeatType": "Permanent"})
		}
		return json.Marshal(map[string]any{"TotalCount": "80", "Policys": rows})
	case "DescribeAddressBook":
		return json.Marshal(map[string]any{"TotalCount": "1", "Acls": []any{map[string]any{"GroupUuid": "uuid-" + q["Query"], "GroupName": q["Query"], "GroupType": "ip", "AddressList": []string{"10.0.0.0/24"}, "AddressListCount": 1}}})
	default:
		t.Fatalf("unexpected operation %s", op)
	}
	return nil, nil
}
func TestTwentyAssetsBatchECSShareSGAndOnePartitionPerRound(t *testing.T) {
	cfg := batchConfig(20)
	counts := map[string]int{}
	r := &fixtureReader{read: func(op string, q map[string]string) (json.RawMessage, error) {
		return batchResponse(t, cfg, counts, op, q)
	}}
	c, e := New(r, cfg)
	if e != nil {
		t.Fatal(e)
	}
	ids := []string{}
	for i := 1; i <= 20; i++ {
		ids = append(ids, fmt.Sprintf("i-%03d", i))
	}
	out, e := c.Collect(context.Background(), domain.Selection{Kind: "assets", AssetIDs: ids})
	if e != nil {
		t.Fatal(e)
	}
	for op, want := range map[string]int{"DescribeInstances": 2, "DescribeAssetList": 40, "DescribeSecurityGroupAttribute": 8, "DescribeControlPolicy": 4, "DescribeAddressBook": 6} {
		if counts[op] != want {
			t.Errorf("%s got%d want%d", op, counts[op], want)
		}
	}
	if r.calls != 60 || len(out.Input.Assets) != 20 || len(out.Input.Policies) != 80 || !out.Input.Completeness.EnumerationComplete || out.Input.Completeness.ObservationConsistency != "observed_stable" {
		t.Fatalf("batch/sharing contract lost calls%d %+v", r.calls, out.Input.Completeness)
	}
	for _, a := range out.Input.Assets {
		if a.ProviderResourceID != a.ID || a.Name == "" || a.Region != "cn-hangzhou" || a.ResourceType != "ecs" || a.OperatingSystem != "linux" || a.Tags["app"] != "payments" || !a.MetadataComplete || a.SecurityGroupCollection != "complete" || len(a.SecurityGroups) != 1 || len(a.SecurityGroups[0].Permissions) != 2 {
			t.Fatalf("metadata/association lost: %+v", a)
		}
		if len(a.Services) != 0 || a.AllowedConfirmed {
			t.Fatal("infrastructure became business authorization")
		}
	}
	p := out.Input.Assets[0].SecurityGroups[0].Permissions[0]
	if p.Direction != "ingress" || p.Action != "accept" || p.Protocol != "tcp" || p.PortRange != "443/443" || p.Source != "0.0.0.0/0" || p.Priority != "1" || p.Conditions["SourceGroupId"] == "" {
		t.Fatalf("SG conditions reduced: %+v", p)
	}
}
func TestSameSGIDIsNotSharedAcrossRegions(t *testing.T) {
	cfg := batchConfig(2)
	cfg.AssetRegions["i-002"] = "cn-beijing"
	counts := map[string]int{}
	r := &fixtureReader{read: func(op string, q map[string]string) (json.RawMessage, error) {
		raw, e := batchResponse(t, cfg, counts, op, q)
		if op == "DescribeInstances" {
			var m map[string]any
			json.Unmarshal(raw, &m)
			for _, v := range m["Instances"].(map[string]any)["Instance"].([]any) {
				v.(map[string]any)["SecurityGroupIds"] = map[string]any{"SecurityGroupId": []string{"sg-shared"}}
			}
			return json.Marshal(m)
		}
		return raw, e
	}}
	c, e := New(r, cfg)
	if e != nil {
		t.Fatal(e)
	}
	out, e := c.Collect(context.Background(), domain.Selection{Kind: "assets", AssetIDs: []string{"i-001", "i-002"}})
	if e != nil {
		t.Fatal(e)
	}
	if counts["DescribeInstances"] != 4 || counts["DescribeSecurityGroupAttribute"] != 4 {
		t.Fatalf("region scopes merged: %+v", counts)
	}
	for _, a := range out.Input.Assets {
		if a.SecurityGroups[0].Region != a.Region {
			t.Fatal("foreign region group attached")
		}
	}
}
func TestExplicitProfileZerosNeverFallBackAndLargerProfileIsNotLegacyClamped(t *testing.T) {
	cfg := natConfig()
	cfg.Workload = explicitWorkload()
	cfg.Workload.Limits.CloudHTTPAttempts = 0
	r := &fixtureReader{read: natFixture}
	c, e := New(r, cfg)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = c.Collect(context.Background(), domain.Selection{Kind: "gateway_policy"}); e == nil || r.calls != 0 {
		t.Fatalf("zero refilled: %v calls%d", e, r.calls)
	}
	cfg.Workload = explicitWorkload()
	c, e = New(r, cfg)
	if e != nil || c.cfg.MaxAttempts != 300 || c.cfg.MaxPolicies != 2000 {
		t.Fatalf("profile clamped to legacy: %v %+v", e, c)
	}
	legacy, e := New(r, natConfig())
	if e != nil || !legacy.cfg.Workload.Legacy || legacy.cfg.Workload.ProfileRef != workload.LegacyProfileRef {
		t.Fatal("legacy policy not explicit", e)
	}
}
func TestExplicitCollectionWindowUsesProfileAndCallerDeadline(t *testing.T) {
	cfg := natConfig()
	cfg.Workload = explicitWorkload()
	r := &fixtureReader{read: natFixture}
	c, e := New(r, cfg)
	if e != nil {
		t.Fatal(e)
	}
	deadlineReader := &deadlineFixture{t: t, minimum: 100 * time.Second}
	c.reader = deadlineReader
	_, _ = c.Collect(context.Background(), domain.Selection{Kind: "gateway_policy"})
	if deadlineReader.calls != 1 {
		t.Fatal("deadline fixture not reached")
	}
}

type deadlineFixture struct {
	t       *testing.T
	minimum time.Duration
	calls   int
}

func (d *deadlineFixture) Read(ctx context.Context, _ string, _ map[string]string) (json.RawMessage, error) {
	d.calls++
	until, ok := ctx.Deadline()
	if !ok || time.Until(until) < d.minimum {
		d.t.Fatal("profile window replaced by legacy deadline")
	}
	return nil, context.Canceled
}

func TestHundredIDChunksAndTokenPagesRemainWithinProfile(t *testing.T) {
	cfg := batchConfig(120)
	counts := map[string]int{}
	r := &fixtureReader{read: func(op string, q map[string]string) (json.RawMessage, error) {
		if op != "DescribeInstances" {
			return batchResponse(t, cfg, counts, op, q)
		}
		counts[op]++
		var ids []string
		if json.Unmarshal([]byte(q["InstanceIds"]), &ids) != nil || len(ids) > 100 {
			t.Fatal("invalid ID batch")
		}
		offset := 0
		if q["NextToken"] != "" {
			offset, _ = strconv.Atoi(q["NextToken"])
		}
		end := offset + 50
		if end > len(ids) {
			end = len(ids)
		}
		rows := []any{}
		for _, id := range ids[offset:end] {
			n, _ := strconv.Atoi(strings.TrimPrefix(id, "i-"))
			rows = append(rows, map[string]any{"InstanceId": id, "RegionId": "cn-hangzhou", "SecurityGroupIds": map[string]any{"SecurityGroupId": []string{fmt.Sprintf("sg-%d", n%4)}}})
		}
		next := ""
		if end < len(ids) {
			next = strconv.Itoa(end)
		}
		return json.Marshal(map[string]any{"NextToken": next, "TotalCount": len(ids), "Instances": map[string]any{"Instance": rows}})
	}}
	c, e := New(r, cfg)
	if e != nil {
		t.Fatal(e)
	}
	ids := []string{}
	for i := 1; i <= 120; i++ {
		ids = append(ids, fmt.Sprintf("i-%03d", i))
	}
	out, e := c.Collect(context.Background(), domain.Selection{Kind: "assets", AssetIDs: ids})
	if e != nil {
		t.Fatal(e)
	}
	if len(out.Input.Assets) != 120 || counts["DescribeInstances"] != 6 || !out.Input.Completeness.EnumerationComplete {
		t.Fatalf("ID/token/profile limit lost: assets%d instances%d %+v", len(out.Input.Assets), counts["DescribeInstances"], out.Input.Completeness)
	}
}
func TestSharedGroupIsRefetchedAfterRoundBarrier(t *testing.T) {
	cfg := batchConfig(2)
	counts := map[string]int{}
	groupReads := 0
	r := &fixtureReader{read: func(op string, q map[string]string) (json.RawMessage, error) {
		if op == "DescribeInstances" {
			counts[op]++
			return json.RawMessage(`{"NextToken":"","Instances":{"Instance":[{"InstanceId":"i-001","RegionId":"cn-hangzhou","SecurityGroupIds":{"SecurityGroupId":["sg-shared"]}},{"InstanceId":"i-002","RegionId":"cn-hangzhou","SecurityGroupIds":{"SecurityGroupId":["sg-shared"]}}]}}`), nil
		}
		if op == "DescribeSecurityGroupAttribute" {
			groupReads++
			port := "443/443"
			if groupReads == 2 {
				port = "8443/8443"
			}
			return json.Marshal(map[string]any{"SecurityGroupId": "sg-shared", "RegionId": "cn-hangzhou", "NextToken": "", "Permissions": map[string]any{"Permission": []any{map[string]string{"Direction": "ingress", "IpProtocol": "tcp", "Policy": "accept", "PortRange": port}}}})
		}
		return batchResponse(t, cfg, counts, op, q)
	}}
	c, _ := New(r, cfg)
	out, e := c.Collect(context.Background(), domain.Selection{Kind: "assets", AssetIDs: []string{"i-001", "i-002"}})
	if e != nil {
		t.Fatal(e)
	}
	if groupReads != 2 || out.Input.Completeness.ObservationConsistency != "changed" || out.Input.Assets[0].SecurityGroups[0].Permissions[0].PortRange != "443/443" {
		t.Fatal("cross-round group cache hid change", groupReads, out.Input.Completeness)
	}
}
func TestEmptyAndMissingGroupAssociationsRemainDifferent(t *testing.T) {
	for _, empty := range []bool{true, false} {
		t.Run(strconv.FormatBool(empty), func(t *testing.T) {
			cfg := internetConfig()
			r := &fixtureReader{read: func(op string, q map[string]string) (json.RawMessage, error) {
				if op == "DescribeInstances" {
					row := map[string]any{"InstanceId": "i-one", "RegionId": "cn-hangzhou"}
					if empty {
						row["SecurityGroupIds"] = map[string]any{"SecurityGroupId": []string{}}
					}
					return json.Marshal(map[string]any{"NextToken": "", "Instances": map[string]any{"Instance": []any{row}}})
				}
				if op == "DescribeSecurityGroupAttribute" {
					t.Fatal("unknown/empty association queried an arbitrary group")
				}
				return internetFixture(op, q)
			}}
			c, _ := New(r, cfg)
			out, e := c.Collect(context.Background(), domain.Selection{Kind: "assets", AssetIDs: []string{"i-one"}})
			if e != nil {
				t.Fatal(e)
			}
			want := "not_collected"
			if empty {
				want = "complete"
			}
			if out.Input.Assets[0].SecurityGroupCollection != want || len(out.Input.Assets[0].SecurityGroups) != 0 {
				t.Fatalf("group absence confused: %+v", out.Input.Assets[0])
			}
		})
	}
}

func TestForeignIDInOversizedBatchCannotHideBehindTruncation(t *testing.T) {
	cfg := internetConfig()
	r := &fixtureReader{read: func(op string, q map[string]string) (json.RawMessage, error) {
		if op == "DescribeInstances" {
			return json.RawMessage(`{"NextToken":"","Instances":{"Instance":[{"InstanceId":"i-one","RegionId":"cn-hangzhou"},{"InstanceId":"i-foreign","RegionId":"cn-hangzhou"}]}}`), nil
		}
		return internetFixture(op, q)
	}}
	c, _ := New(r, cfg)
	if _, e := c.Collect(context.Background(), domain.Selection{Kind: "assets", AssetIDs: []string{"i-one"}}); e != Failure("COLLECTOR_SCOPE_DENIED") {
		t.Fatal("foreign identity was hidden by capacity check", e)
	}
}

func TestMultipleNICsDoNotBecomeCompleteGroupAssociation(t *testing.T) {
	r := &fixtureReader{read: func(op string, q map[string]string) (json.RawMessage, error) {
		raw, e := internetFixture(op, q)
		if op == "DescribeInstances" {
			var body map[string]any
			json.Unmarshal(raw, &body)
			row := body["Instances"].(map[string]any)["Instance"].([]any)[0].(map[string]any)
			row["NetworkInterfaces"] = map[string]any{"NetworkInterface": []any{map[string]string{"NetworkInterfaceId": "eni-one"}, map[string]string{"NetworkInterfaceId": "eni-two"}}}
			return json.Marshal(body)
		}
		return raw, e
	}}
	c, _ := New(r, internetConfig())
	out, e := c.Collect(context.Background(), domain.Selection{Kind: "assets", AssetIDs: []string{"i-one"}})
	if e != nil {
		t.Fatal(e)
	}
	if out.Input.Assets[0].SecurityGroupCollection != "partial" || len(out.Input.Assets[0].SecurityGroups) != 1 {
		t.Fatal("multi-NIC association assumed complete")
	}
}
