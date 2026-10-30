package aliyunapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"cfw-policy-llm-agent/server/internal/aliyuncollector"
	"cfw-policy-llm-agent/server/internal/domain"
	"cfw-policy-llm-agent/server/internal/workload"
)

// Synthetic signed-loopback test of the workload formula: one identity plus
// two rounds of 20 precise CFW lookups, one ECS batch, four SG pages, two ACL
// pages and three reference lookups. This is not a real-account measurement.
func TestTwentyAssetSignedCollectorUsesSixtyOneSharedPhysicalAttempts(t *testing.T) {
	counts := map[string]int{}
	reader, ledger := unrestrictedFixture(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		op := action(req)
		counts[op]++
		req.ParseForm()
		switch op {
		case "GetCallerIdentity":
			identity(w)
		case "DescribeInstances":
			var ids []string
			json.Unmarshal([]byte(req.Form.Get("InstanceIds")), &ids)
			if len(ids) != 20 {
				t.Errorf("ECS was not batched: %d", len(ids))
			}
			rows := []any{}
			for _, id := range ids {
				n, _ := strconv.Atoi(strings.TrimPrefix(id, "i-public-"))
				rows = append(rows, map[string]any{"InstanceId": id, "InstanceName": id, "RegionId": "cn-hangzhou", "OSType": "linux", "SecurityGroupIds": map[string]any{"SecurityGroupId": []string{fmt.Sprintf("sg-public-%d", n%4)}}})
			}
			json.NewEncoder(w).Encode(map[string]any{"Instances": map[string]any{"Instance": rows}, "NextToken": "", "TotalCount": 20})
		case "DescribeAssetList":
			id := req.Form.Get("SearchItem")
			n, _ := strconv.Atoi(strings.TrimPrefix(id, "i-public-"))
			if id == "" {
				t.Error("CFW read scope was expanded")
			}
			json.NewEncoder(w).Encode(map[string]any{"TotalCount": 1, "Assets": []any{map[string]any{"BindInstanceId": id, "ResourceInstanceId": "eip-" + id, "RegionID": "cn-hangzhou", "InternetAddress": fmt.Sprintf("192.0.2.%d", n), "ProtectStatus": "open"}}})
		case "DescribeSecurityGroupAttribute":
			rows := []any{}
			for i := 0; i < 20; i++ {
				rows = append(rows, map[string]string{"Direction": "ingress", "Policy": "accept", "IpProtocol": "tcp", "SourceCidrIp": "10.0.0.0/24", "PortRange": fmt.Sprintf("%d/%d", 4000+i, 4000+i), "Priority": strconv.Itoa(i + 1)})
			}
			json.NewEncoder(w).Encode(map[string]any{"SecurityGroupId": req.Form.Get("SecurityGroupId"), "RegionId": "cn-hangzhou", "Permissions": map[string]any{"Permission": rows}, "TotalCount": 20, "NextToken": ""})
		case "DescribeControlPolicy":
			page, _ := strconv.Atoi(req.Form.Get("CurrentPage"))
			start, end := (page-1)*50, page*50
			if end > 80 {
				end = 80
			}
			rows := []any{}
			for i := start; i < end; i++ {
				rows = append(rows, map[string]any{"AclUuid": fmt.Sprintf("acl-public-%d", i), "Direction": "in", "SourceType": "group", "Source": fmt.Sprintf("group-public-%d", i%3), "SourceGroupType": "ip", "DestinationType": "net", "Destination": "0.0.0.0/0", "DestPortType": "port", "DestPort": "443", "Proto": "TCP", "AclAction": "accept", "Release": "true", "Order": i + 1, "ApplicationNameList": []string{"ANY"}, "RepeatType": "Permanent"})
			}
			json.NewEncoder(w).Encode(map[string]any{"TotalCount": "80", "Policys": rows})
		case "DescribeAddressBook":
			json.NewEncoder(w).Encode(map[string]any{"TotalCount": "1", "Acls": []any{map[string]any{"GroupUuid": "uuid-" + req.Form.Get("Query"), "GroupName": req.Form.Get("Query"), "GroupType": "ip", "AddressList": []string{"10.0.0.0/24"}, "AddressListCount": 1}}})
		default:
			t.Errorf("unexpected operation %s", op)
			json.NewEncoder(w).Encode(map[string]any{})
		}
	}))
	if e := reader.VerifyIdentity(context.Background()); e != nil {
		t.Fatal(e)
	}
	limits := workload.Default("collect")
	cfg := aliyuncollector.Config{Scope: domain.Scope{Ref: "scope-public-in", Provider: "aliyun", AccountRef: "account-public", ControlPlaneRef: "control-public", IPFamily: "ipv4", Boundary: "internet", Direction: "in", PolicySetRef: "set-public"}, ExpectedAccountID: "1234567890123456", AssetRegions: map[string]string{}, AllowedRegions: []string{"cn-hangzhou"}, Workload: workload.Resolved{ProfileRef: workload.LegacyProfileRef, Legacy: true, Limits: limits}}
	ids := []string{}
	for i := 1; i <= 20; i++ {
		id := fmt.Sprintf("i-public-%d", i)
		ids = append(ids, id)
		cfg.AssetRegions[id] = "cn-hangzhou"
	}
	collector, e := aliyuncollector.New(reader, cfg)
	if e != nil {
		t.Fatal(e)
	}
	out, e := collector.Collect(context.Background(), domain.Selection{Kind: "assets", AssetIDs: ids})
	if e != nil {
		t.Fatal(e)
	}
	if !out.Input.Completeness.EnumerationComplete || out.Input.Completeness.ObservationConsistency != "observed_stable" || len(out.Input.Assets) != 20 || len(out.Input.Policies) != 80 {
		t.Fatalf("invalid fixed observation %+v", out.Input.Completeness)
	}
	for op, want := range map[string]int{"GetCallerIdentity": 1, "DescribeInstances": 2, "DescribeAssetList": 40, "DescribeSecurityGroupAttribute": 8, "DescribeControlPolicy": 4, "DescribeAddressBook": 6} {
		if counts[op] != want {
			t.Errorf("%s %d != %d", op, counts[op], want)
		}
	}
	t.Logf("synthetic-loopback physical attempts reserved=%d recorded=%d operations=%v", ledger.reserved, ledger.recorded, counts)
	if ledger.reserved != 61 || ledger.recorded != 61 {
		t.Fatalf("physical ledger mismatch reserved%d recorded%d", ledger.reserved, ledger.recorded)
	}
}
