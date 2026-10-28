package aliyuncollector

import (
	"context"
	"encoding/json"
	"testing"

	"cfw-policy-llm-agent/server/internal/audit"
	"cfw-policy-llm-agent/server/internal/domain"
)

func internetConfig() Config {
	return Config{Scope: domain.Scope{Ref: "scope-in", Provider: "aliyun", AccountRef: "account-a", ControlPlaneRef: "control-a", IPFamily: "ipv4", Boundary: "internet", Direction: "in", PolicySetRef: "set-in"}, ExpectedAccountID: "1234567890123456", AssetRegions: map[string]string{"i-one": "cn-hangzhou"}}
}
func internetFixture(op string, q map[string]string) (json.RawMessage, error) {
	switch op {
	case "DescribeAssetList":
		return json.RawMessage(`{"TotalCount":1,"Assets":[{"BindInstanceId":"i-one","ResourceInstanceId":"eip-one","RegionID":"cn-hangzhou","AliUid":1234567890123456,"InternetAddress":"192.0.2.10","ProtectStatus":"open"}]}`), nil
	case "DescribeInstances":
		return json.RawMessage(`{"NextToken":"","Instances":{"Instance":[{"InstanceId":"i-one","RegionId":"cn-hangzhou","VpcAttributes":{"VpcId":"vpc-one","PrivateIpAddress":{"IpAddress":["10.0.0.10"]}},"SecurityGroupIds":{"SecurityGroupId":["sg-one"]}}]}}`), nil
	case "DescribeSecurityGroupAttribute":
		return json.RawMessage(`{"SecurityGroupId":"sg-one","RegionId":"cn-hangzhou","NextToken":"","Permissions":{"Permission":[{"IpProtocol":"tcp","PortRange":"443/443","Policy":"accept","SourceCidrIp":"0.0.0.0/0"}]}}`), nil
	case "DescribeControlPolicy":
		return json.RawMessage(`{"TotalCount":"1","Policys":[{"AclUuid":"acl-one","Direction":"in","SourceType":"net","Source":"0.0.0.0/0","DestinationType":"net","Destination":"192.0.2.10","DestPortType":"port","DestPort":"443","Proto":"TCP","AclAction":"accept","Release":"true","Order":1,"ApplicationNameList":["ANY"],"RepeatType":"Permanent"}]}`), nil
	default:
		panic(op)
	}
}
func TestExplicitAssetECSAndAssociatedSGOnly(t *testing.T) {
	r := &fixtureReader{read: func(op string, q map[string]string) (json.RawMessage, error) {
		switch op {
		case "DescribeAssetList":
			if q["SearchItem"] != "i-one" || q["RegionNo"] != "" || q["IpVersion"] != "4" {
				t.Fatalf("broad asset query:%+v", q)
			}
		case "DescribeInstances":
			if q["InstanceIds"] != `["i-one"]` || q["RegionId"] != "cn-hangzhou" {
				t.Fatalf("broad ECS query:%+v", q)
			}
		case "DescribeSecurityGroupAttribute":
			if q["SecurityGroupId"] != "sg-one" {
				t.Fatalf("unrelated group:%+v", q)
			}
		}
		return internetFixture(op, q)
	}}
	c, e := New(r, internetConfig())
	if e != nil {
		t.Fatal(e)
	}
	out, e := c.Collect(context.Background(), domain.Selection{Kind: "assets", AssetIDs: []string{"i-one"}})
	if e != nil {
		t.Fatal(e)
	}
	if r.calls != 8 || !out.Input.Completeness.EnumerationComplete || out.Input.Protection.State != "on" || len(out.Input.Assets) != 1 || out.Input.Assets[0].Address != "192.0.2.10" || !out.Input.Assets[0].AddressObserved || len(out.Input.Assets[0].Services) != 0 || out.Input.Assets[0].AllowedConfirmed {
		t.Fatalf("infrastructure became business claims:%+v calls%d", out.Input, r.calls)
	}
	if e = audit.Validate(out.Input); e != nil {
		t.Fatal(e)
	}
}
func TestCFWEndpointCIDRIsNotObservedLiteralEndpoint(t *testing.T) {
	r := &fixtureReader{read: func(op string, q map[string]string) (json.RawMessage, error) {
		b, e := internetFixture(op, q)
		if op == "DescribeAssetList" {
			var o map[string]any
			_ = json.Unmarshal(b, &o)
			o["Assets"].([]any)[0].(map[string]any)["InternetAddress"] = "192.0.2.0/24"
			return json.Marshal(o)
		}
		return b, e
	}}
	c, _ := New(r, internetConfig())
	out, e := c.Collect(context.Background(), domain.Selection{Kind: "assets", AssetIDs: []string{"i-one"}})
	if e != nil {
		t.Fatal(e)
	}
	if out.Input.Assets[0].AddressObserved || out.Input.Assets[0].Address != "" {
		t.Fatal("CIDR treated as observed instance address")
	}
}
func TestNATSameVPCNeverCreatesAssociation(t *testing.T) {
	cfg := natConfig()
	cfg.AssetRegions = map[string]string{"i-one": "cn-hangzhou"}
	r := &fixtureReader{read: func(op string, q map[string]string) (json.RawMessage, error) {
		if op == "DescribeInstances" || op == "DescribeSecurityGroupAttribute" {
			return internetFixture(op, q)
		}
		return natFixture(op, q)
	}}
	c, e := New(r, cfg)
	if e != nil {
		t.Fatal(e)
	}
	out, e := c.Collect(context.Background(), domain.Selection{Kind: "assets", AssetIDs: []string{"i-one"}})
	if e != nil {
		t.Fatal(e)
	}
	a := out.Input.Assets[0]
	if a.Address != "10.0.0.10" || a.Association.Status != "unknown" || a.Association.GatewayRef != "" {
		t.Fatalf("NAT path inferred:%+v", a)
	}
}
func TestMissingNextTokenAndCyclesAreIncomplete(t *testing.T) {
	for _, mode := range []string{"missing", "cycle"} {
		t.Run(mode, func(t *testing.T) {
			r := &fixtureReader{read: func(op string, q map[string]string) (json.RawMessage, error) {
				b, e := internetFixture(op, q)
				if op == "DescribeInstances" {
					var o map[string]any
					_ = json.Unmarshal(b, &o)
					if mode == "missing" {
						delete(o, "NextToken")
					} else {
						o["NextToken"] = "repeated"
						if q["NextToken"] != "" {
							o["Instances"] = map[string]any{"Instance": []any{}}
						}
					}
					return json.Marshal(o)
				}
				return b, e
			}}
			c, _ := New(r, internetConfig())
			out, e := c.Collect(context.Background(), domain.Selection{Kind: "assets", AssetIDs: []string{"i-one"}})
			if e != nil {
				t.Fatal(e)
			}
			if out.Input.Completeness.EnumerationComplete || out.Input.Assets[0].AddressObserved {
				t.Fatal("unverified token enumeration treated complete")
			}
		})
	}
}
