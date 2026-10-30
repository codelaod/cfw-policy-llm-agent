// Package aliyunapi exposes a fixed read-only operation surface to the collector.
// Clients are bound to an accepted job and the application's shared HTTP ledger.
package aliyunapi

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"cfw-policy-llm-agent/server/internal/cloudconfig"
	"cfw-policy-llm-agent/server/internal/outbound"
	"cfw-policy-llm-agent/server/internal/workload"
	cloudfw "github.com/alibabacloud-go/cloudfw-20171207/v11/client"
	openapi "github.com/alibabacloud-go/darabonba-openapi/v2/client"
	openapiutil "github.com/alibabacloud-go/darabonba-openapi/v2/utils"
	"github.com/alibabacloud-go/tea/dara"
	credential "github.com/aliyun/credentials-go/credentials"
	"unicode/utf8"
)

const MaxResponseBytes = 2 << 20
const CFWSource = "aliyun-cloud-cfw"
const IdentitySource = "aliyun-cloud-identity"

func ECSSource(region string) string { return "aliyun-cloud-ecs-" + region }

type Failure string

func (e Failure) Error() string     { return string(e) }
func (e Failure) ErrorCode() string { return string(e) }

type Config struct {
	Connection           cloudconfig.Config `json:"-"`
	SensitiveValues      []string           `json:"-"`
	FixtureOrigin        string             `json:"-"`
	AllowLoopbackFixture bool               `json:"-"`
}
type CredentialProvider interface {
	Credential(context.Context) (credential.Credential, error)
}
type staticProvider struct {
	value  credential.Credential
	expiry string
}

func (p *staticProvider) Credential(ctx context.Context) (credential.Credential, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if p.expiry != "" {
		when, e := time.Parse(time.RFC3339Nano, p.expiry)
		if e != nil || !when.After(time.Now()) {
			return nil, Failure("CLOUD_CREDENTIAL_EXPIRED")
		}
	}
	return p.value, nil
}
func NewCredentialProvider(c cloudconfig.Config) (CredentialProvider, error) {
	if !c.Enabled() {
		return nil, Failure("CLOUD_CONFIGURATION_INVALID")
	}
	typ := "access_key"
	if c.CredentialMode() == "env_sts" {
		typ = "sts"
	} else if c.CredentialMode() != "env_ak" {
		return nil, Failure("CLOUD_CREDENTIAL_MODE_UNSUPPORTED")
	}
	value, e := credential.NewCredential(&credential.Config{Type: dara.String(typ), AccessKeyId: dara.String(c.AccessKeyID()), AccessKeySecret: dara.String(c.AccessKeySecret()), SecurityToken: dara.String(c.SecurityToken())})
	if e != nil {
		return nil, Failure("CLOUD_CREDENTIAL_UNAVAILABLE")
	}
	return &staticProvider{value: value, expiry: c.CredentialExpiresAt()}, nil
}
func Sources(c cloudconfig.Config) []outbound.Source {
	if c.Revision() == "" {
		return nil
	}
	source := func(ref, class, origin string, actions []string) outbound.Source {
		return outbound.Source{Ref: ref, Class: class, Origin: origin, Timeout: c.Timeout(), MaxResponseBytes: MaxResponseBytes, Routes: []outbound.Route{{Method: "POST", Path: "/", Actions: actions}}}
	}
	out := []outbound.Source{source(CFWSource, "cloud", c.CFWOrigin(), []string{"DescribeAssetList", "DescribeControlPolicy", "DescribeNatFirewallList", "DescribeNatFirewallControlPolicy", "DescribeAddressBook"}), source(IdentitySource, "credential", c.STSOrigin(), []string{"GetCallerIdentity"})}
	for _, r := range c.Regions() {
		out = append(out, source(ECSSource(r), "cloud", c.ECSOrigin(r), []string{"DescribeInstances", "DescribeSecurityGroupAttribute"}))
	}
	return out
}

type Factory struct {
	manager  *outbound.Manager
	config   Config
	provider CredentialProvider
}

func New(manager *outbound.Manager, cfg Config) (*Factory, error) {
	if manager == nil || !cfg.Connection.Enabled() {
		return nil, Failure("CLOUD_CONFIGURATION_INVALID")
	}
	if cfg.FixtureOrigin != "" {
		u, e := url.Parse(cfg.FixtureOrigin)
		if e != nil || !cfg.AllowLoopbackFixture || u.Scheme != "http" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Path != "" || u.Opaque != "" || u.RawPath != "" {
			return nil, Failure("CLOUD_CONFIGURATION_INVALID")
		}
		ip := net.ParseIP(u.Hostname())
		if ip == nil || !ip.IsLoopback() {
			return nil, Failure("CLOUD_CONFIGURATION_INVALID")
		}
	}
	p, e := NewCredentialProvider(cfg.Connection)
	if e != nil {
		return nil, e
	}
	cfg.SensitiveValues = append([]string{cfg.Connection.AccessKeyID(), cfg.Connection.AccessKeySecret(), cfg.Connection.SecurityToken()}, cfg.SensitiveValues...)
	return &Factory{manager: manager, config: cfg, provider: p}, nil
}

type Reader struct {
	factory   *Factory
	ctx       context.Context
	jobID     string
	mu        sync.Mutex
	verified  bool
	groups    map[string]bool
	instances map[string]string
	gateways  map[string]string
}

func (f *Factory) Reader(ctx context.Context, jobID string) (*Reader, error) {
	if ctx == nil || ctx.Err() != nil {
		return nil, Failure("CLOUD_CONTEXT_INVALID")
	}
	if _, e := f.manager.Client(ctx, jobID, IdentitySource); e != nil {
		return nil, safeError(e)
	}
	return &Reader{factory: f, ctx: ctx, jobID: jobID, groups: map[string]bool{}, instances: map[string]string{}, gateways: map[string]string{}}, nil
}
func (r *Reader) VerifyIdentity(ctx context.Context) error {
	_, e := r.Read(ctx, "GetCallerIdentity", nil)
	return e
}
func ErrorCode(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.Canceled) {
		return "REQUEST_CANCELLED"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "REQUEST_DEADLINE_EXCEEDED"
	}
	var f Failure
	if errors.As(err, &f) {
		return string(f)
	}
	switch code := outbound.ErrorCode(err); code {
	case "ATTEMPT_LIMIT", "EXECUTION_CLOSED", "BUDGET_NOT_CONFIGURED", "BUDGET_EXPIRED", "EFFECT_DENIED", "CONFIGURATION_STALE", "ATTEMPT_RECORD_FAILED", "ADMISSION_UNAVAILABLE", "OUTBOUND_STOPPING", "DESTINATION_REFUSED", "UPSTREAM_TRANSPORT", "UPSTREAM_HTTP_ERROR", "UPSTREAM_READ", "UPSTREAM_RESPONSE_TOO_LARGE", "UPSTREAM_ENCODING_UNSUPPORTED", "REDIRECT_REFUSED":
		return code
	}
	return "CLOUD_REQUEST_FAILED"
}
func safeError(e error) error {
	if errors.Is(e, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(e, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return Failure(ErrorCode(e))
}
func (r *Reader) Read(ctx context.Context, operation string, query map[string]string) (body json.RawMessage, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	defer func() {
		if recover() != nil {
			body = nil
			err = Failure("CLOUD_ADAPTER_FAILED")
		}
	}()
	if operation == "GetCallerIdentity" {
		r.verified = false
		r.groups = map[string]bool{}
		r.instances = map[string]string{}
		r.gateways = map[string]string{}
	}
	if ctx == nil {
		return nil, Failure("CLOUD_CONTEXT_INVALID")
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if r.ctx.Err() != nil {
		return nil, r.ctx.Err()
	}
	if operation != "GetCallerIdentity" && !r.verified {
		return nil, Failure("CLOUD_IDENTITY_NOT_VERIFIED")
	}
	q := map[string]string{}
	for k, v := range query {
		q[k] = v
	}
	source, origin, e := r.validate(operation, q)
	if e != nil {
		return nil, e
	}
	client, e := r.factory.manager.Client(r.ctx, r.jobID, source)
	if e != nil {
		return nil, safeError(e)
	}
	if r.factory.config.FixtureOrigin != "" {
		origin = r.factory.config.FixtureOrigin
	}
	capture := &sdkHTTP{client: client, sensitive: r.factory.config.SensitiveValues}
	provider, e := r.factory.provider.Credential(ctx)
	if e != nil {
		return nil, e
	}
	u, _ := url.Parse(origin)
	settings := &openapi.Config{Endpoint: dara.String(u.Host), Protocol: dara.String(u.Scheme), RegionId: dara.String(r.factory.config.Connection.ControlPlaneRegion()), Credential: provider, HttpClient: capture, RetryOptions: &dara.RetryOptions{Retryable: false, MaxAttempts: 1}, UserAgent: dara.String("cfw-readonly-collector")}
	if source != CFWSource && source != IdentitySource {
		settings.RegionId = dara.String(q["RegionId"])
	}
	runtime := &dara.RuntimeOptions{}
	if strings.HasPrefix(operation, "Describe") && source == CFWSource {
		sdk, e := cloudfw.NewClient(settings)
		if e != nil {
			return nil, Failure("CLOUD_INITIALIZATION_FAILED")
		}
		switch operation {
		case "DescribeControlPolicy":
			_, e = sdk.DescribeControlPolicyWithContext(ctx, &cloudfw.DescribeControlPolicyRequest{Direction: ptr(q, "Direction"), IpVersion: ptr(q, "IpVersion"), CurrentPage: ptr(q, "CurrentPage"), PageSize: ptr(q, "PageSize")}, runtime)
		case "DescribeNatFirewallControlPolicy":
			_, e = sdk.DescribeNatFirewallControlPolicyWithContext(ctx, &cloudfw.DescribeNatFirewallControlPolicyRequest{Direction: ptr(q, "Direction"), NatGatewayId: ptr(q, "NatGatewayId"), CurrentPage: ptr(q, "CurrentPage"), PageSize: ptr(q, "PageSize")}, runtime)
		case "DescribeNatFirewallList":
			page, _ := strconv.ParseInt(q["PageNo"], 10, 64)
			size, _ := strconv.ParseInt(q["PageSize"], 10, 64)
			_, e = sdk.DescribeNatFirewallListWithContext(ctx, &cloudfw.DescribeNatFirewallListRequest{NatGatewayId: ptr(q, "NatGatewayId"), ProxyName: ptr(q, "ProxyName"), ProxyId: ptr(q, "ProxyId"), RegionNo: ptr(q, "RegionNo"), PageNo: dara.Int64(page), PageSize: dara.Int64(size)}, runtime)
		case "DescribeAddressBook":
			_, e = sdk.DescribeAddressBookWithContext(ctx, &cloudfw.DescribeAddressBookRequest{GroupUuid: ptr(q, "GroupUuid"), Query: ptr(q, "Query"), GroupType: ptr(q, "GroupType"), CurrentPage: ptr(q, "CurrentPage"), PageSize: ptr(q, "PageSize")}, runtime)
		case "DescribeAssetList":
			_, e = sdk.DescribeAssetListWithContext(ctx, &cloudfw.DescribeAssetListRequest{IpVersion: ptr(q, "IpVersion"), CurrentPage: ptr(q, "CurrentPage"), PageSize: ptr(q, "PageSize"), SearchItem: ptr(q, "SearchItem")}, runtime)
		}
		if e != nil {
			if capture.failure != nil {
				return nil, capture.failure
			}
			return nil, safeError(e)
		}
	} else {
		sdk, e := openapi.NewClient(settings)
		if e != nil {
			return nil, Failure("CLOUD_INITIALIZATION_FAILED")
		}
		version := "2014-05-26"
		if operation == "GetCallerIdentity" {
			version = "2015-04-01"
		}
		values := map[string]*string{}
		for k, v := range q {
			values[k] = dara.String(v)
		}
		_, e = sdk.CallApiWithCtx(ctx, &openapiutil.Params{Action: dara.String(operation), Version: dara.String(version), Protocol: dara.String("HTTPS"), Pathname: dara.String("/"), Method: dara.String("POST"), AuthType: dara.String("AK"), Style: dara.String("RPC"), ReqBodyType: dara.String("formData"), BodyType: dara.String("json")}, &openapiutil.OpenApiRequest{Query: values}, runtime)
		if e != nil {
			if capture.failure != nil {
				return nil, capture.failure
			}
			return nil, safeError(e)
		}
	}
	if len(capture.raw) == 0 {
		return nil, Failure("CLOUD_RESPONSE_INVALID")
	}
	if e = r.accept(operation, q, capture.raw); e != nil {
		return nil, e
	}
	return append(json.RawMessage(nil), capture.raw...), nil
}
func ptr(q map[string]string, k string) *string {
	if v, ok := q[k]; ok {
		return dara.String(v)
	}
	return nil
}

var idPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

func number(v string, max int) bool {
	n, e := strconv.Atoi(v)
	return e == nil && n >= 1 && n <= max && strconv.Itoa(n) == v
}
func plain(v string, max int) bool {
	return len(v) > 0 && len(v) <= max && strings.TrimSpace(v) == v && !strings.ContainsAny(v, "\r\n\x00")
}
func (r *Reader) validate(op string, q map[string]string) (string, string, error) {
	c := r.factory.config.Connection
	invalid := func() (string, string, error) { return "", "", Failure("CLOUD_QUERY_REFUSED") }
	allowed := map[string]bool{}
	keys := func(names ...string) {
		for _, n := range names {
			allowed[n] = true
		}
	}
	source, origin := CFWSource, c.CFWOrigin()
	hasScope := func(kind string) bool { return c.AllowsBoundary(kind) }
	hasRegion := func(region string) bool { return c.ECSOrigin(region) != "" }
	gateway := func() bool {
		region := q["RegionNo"]
		if region == "" {
			for _, g := range c.Gateways() {
				if g.ID == q["NatGatewayId"] {
					region = g.Region
				}
			}
		}
		if region == "" {
			region = r.gateways[q["NatGatewayId"]]
		}
		return c.AllowsGateway(region, q["NatGatewayId"]) && (len(c.Gateways()) > 0 || r.gateways[q["NatGatewayId"]] != "")
	}
	switch op {
	case "GetCallerIdentity":
		source, origin = IdentitySource, c.STSOrigin()
		if len(q) != 0 {
			return invalid()
		}
	case "DescribeControlPolicy":
		keys("Direction", "CurrentPage", "PageSize", "IpVersion")
		if !hasScope("internet/"+q["Direction"]) || q["IpVersion"] != "" && q["IpVersion"] != "4" {
			return invalid()
		}
	case "DescribeNatFirewallControlPolicy":
		keys("Direction", "NatGatewayId", "CurrentPage", "PageSize")
		if q["Direction"] != "out" || !hasScope("nat/out") || !gateway() {
			return invalid()
		}
	case "DescribeNatFirewallList":
		keys("NatGatewayId", "ProxyId", "ProxyName", "RegionNo", "PageNo", "PageSize")
		region := q["RegionNo"]
		if region == "" {
			for _, g := range c.Gateways() {
				if g.ID == q["NatGatewayId"] {
					region = g.Region
				}
			}
		}
		permitted := q["NatGatewayId"] != "" && c.AllowsGateway(region, q["NatGatewayId"])
		if q["NatGatewayId"] == "" {
			permitted = len(c.Gateways()) == 0 && hasRegion(region)
		}
		if !hasScope("nat/out") || !permitted || !number(q["PageNo"], workload.MaxCounter) || q["ProxyName"] != "" && !nameValue(q["ProxyName"]) || q["ProxyId"] != "" && !idPattern.MatchString(q["ProxyId"]) {
			return invalid()
		}
	case "DescribeAddressBook":
		keys("GroupUuid", "Query", "GroupType", "CurrentPage", "PageSize")
		if (q["GroupUuid"] == "") == (q["Query"] == "") || (q["GroupType"] != "ip" && q["GroupType"] != "port") || q["GroupUuid"] != "" && !idPattern.MatchString(q["GroupUuid"]) || q["Query"] != "" && !plain(q["Query"], 128) {
			return invalid()
		}
	case "DescribeAssetList":
		keys("CurrentPage", "PageSize", "IpVersion", "SearchItem")
		region := r.instances[q["SearchItem"]]
		for _, asset := range c.Assets() {
			if asset.InstanceID == q["SearchItem"] {
				region = asset.Region
			}
		}
		if !hasScope("internet/in") && !hasScope("internet/out") || !c.AllowsAsset(region, q["SearchItem"]) {
			return invalid()
		}
		if q["IpVersion"] != "" && q["IpVersion"] != "4" || q["SearchItem"] != "" && !plain(q["SearchItem"], 128) {
			return invalid()
		}
	case "DescribeInstances":
		keys("RegionId", "InstanceIds", "InstanceName", "NextToken", "MaxResults")
		if !hasRegion(q["RegionId"]) || q["InstanceName"] != "" && !nameValue(q["InstanceName"]) {
			return invalid()
		}
		if q["InstanceIds"] != "" {
			var ids []string
			if json.Unmarshal([]byte(q["InstanceIds"]), &ids) != nil || len(ids) < 1 || len(ids) > 100 {
				return invalid()
			}
			seen := map[string]bool{}
			for _, id := range ids {
				if !c.AllowsAsset(q["RegionId"], id) || seen[id] {
					return invalid()
				}
				seen[id] = true
			}
			if q["InstanceName"] != "" {
				return invalid()
			}
		} else if len(c.Assets()) > 0 {
			return invalid()
		}
		tagKeys := map[int]bool{}
		tagValues := map[int]bool{}
		for key, value := range q {
			if strings.HasPrefix(key, "Tag.") {
				parts := strings.Split(key, ".")
				if len(parts) != 3 || !number(parts[1], 20) || !nameValue(value) {
					return invalid()
				}
				index, _ := strconv.Atoi(parts[1])
				switch parts[2] {
				case "Key":
					tagKeys[index] = true
				case "Value":
					tagValues[index] = true
				default:
					return invalid()
				}
				allowed[key] = true
			}
		}
		if q["InstanceIds"] != "" && len(tagKeys) > 0 {
			return invalid()
		}
		for index := range tagValues {
			if !tagKeys[index] {
				return invalid()
			}
		}
		for index := 1; index <= len(tagKeys); index++ {
			if !tagKeys[index] {
				return invalid()
			}
		}
		source, origin = ECSSource(q["RegionId"]), c.ECSOrigin(q["RegionId"])
	case "DescribeSecurityGroupAttribute":
		keys("RegionId", "SecurityGroupId", "NextToken", "MaxResults", "Direction")
		if !hasRegion(q["RegionId"]) || !idPattern.MatchString(q["SecurityGroupId"]) || !r.groups[q["RegionId"]+"/"+q["SecurityGroupId"]] || q["Direction"] != "" && q["Direction"] != "all" {
			return invalid()
		}
		source, origin = ECSSource(q["RegionId"]), c.ECSOrigin(q["RegionId"])
	default:
		return invalid()
	}
	for k, v := range q {
		bound := 8192
		if k == "InstanceIds" {
			bound = 16384
		}
		if !allowed[k] || !plain(v, bound) {
			return invalid()
		}
	}
	switch op {
	case "DescribeControlPolicy", "DescribeNatFirewallControlPolicy", "DescribeAddressBook", "DescribeAssetList":
		if !number(q["CurrentPage"], workload.MaxCounter) || !number(q["PageSize"], 50) {
			return invalid()
		}
	case "DescribeNatFirewallList":
		if !number(q["PageSize"], 50) {
			return invalid()
		}
	case "DescribeInstances":
		if !number(q["MaxResults"], 100) || q["NextToken"] != "" && !plain(q["NextToken"], 4096) {
			return invalid()
		}
	case "DescribeSecurityGroupAttribute":
		if !number(q["MaxResults"], 1000) || q["MaxResults"] != "" && mustNumber(q["MaxResults"]) < 10 || q["NextToken"] != "" && !plain(q["NextToken"], 4096) {
			return invalid()
		}
	}
	return source, origin, nil
}

func nameValue(value string) bool {
	return plain(value, 512) && utf8.ValidString(value) && utf8.RuneCountInString(value) <= 128
}
func mustNumber(value string) int { n, _ := strconv.Atoi(value); return n }
