// Package cloudconfig reads only an explicitly selected local connection file.
// Construction and summaries never obtain credentials remotely or perform DNS.
package cloudconfig

import (
	"bytes"
	"cfw-policy-llm-agent/server/internal/workload"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"cfw-policy-llm-agent/server/internal/domain"
)

var ErrInvalid = errors.New("CLOUD_CONFIG_INVALID")
var ErrCredential = errors.New("CLOUD_CREDENTIAL_UNAVAILABLE")

type Budget struct {
	Ref                      string `json:"ref"`
	ExpiresAt                string `json:"expires_at"`
	ApplicationCloudAttempts int    `json:"application_cloud_attempts"`
	SessionCloudAttempts     int    `json:"session_cloud_attempts"`
	ApplicationModelAttempts int    `json:"application_model_attempts"`
	SessionModelAttempts     int    `json:"session_model_attempts"`
}
type Asset struct {
	Region     string `json:"region"`
	InstanceID string `json:"instance_id"`
}
type Gateway struct {
	Region string `json:"region"`
	ID     string `json:"id"`
}
type fileConfig struct {
	Enabled             bool      `json:"enabled"`
	ConnectionRef       string    `json:"connection_ref"`
	CredentialMode      string    `json:"credential_mode"`
	AccessKeyIDEnv      string    `json:"access_key_id_env"`
	AccessKeySecretEnv  string    `json:"access_key_secret_env"`
	SecurityTokenEnv    string    `json:"security_token_env,omitempty"`
	CredentialExpiresAt string    `json:"credential_expires_at,omitempty"`
	ExpectedAccountID   string    `json:"expected_account_id"`
	ControlPlaneRegion  string    `json:"control_plane_region"`
	AllowedBoundaries   []string  `json:"allowed_boundaries"`
	AllowedRegions      []string  `json:"allowed_regions"`
	AllowedAssets       []Asset   `json:"allowed_assets"`
	AllowedNATGateways  []Gateway `json:"allowed_nat_gateways"`
	TimeoutSeconds      int       `json:"timeout_seconds"`
	MaxHTTPAttempts     int       `json:"max_http_attempts"`
	Budget              Budget    `json:"budget"`
}
type Config struct {
	modelBudgetDeclared         bool
	settings                    fileConfig
	ak, secret, token, revision string
	credentialState             string
}
type Summary struct {
	Ready                 bool   `json:"ready"`
	SourceSelected        bool   `json:"source_selected"`
	StructureValid        bool   `json:"structure_valid"`
	CredentialLocalReady  bool   `json:"credential_local_ready"`
	CredentialState       string `json:"credential_state"`
	CredentialExpired     bool   `json:"credential_expired"`
	ExecutionPolicyReady  bool   `json:"execution_policy_ready"`
	AuthorizationState    string `json:"authorization_state"`
	BlockedReason         string `json:"blocked_reason,omitempty"`
	LedgerState           string `json:"ledger_state"`
	UpstreamValidation    string `json:"upstream_validation"`
	Configured            bool   `json:"configured"`
	Enabled               bool   `json:"enabled"`
	CredentialMode        string `json:"credential_mode"`
	CredentialExpiryKnown bool   `json:"credential_expiry_known"`
	Refreshable           bool   `json:"refreshable"`
	RegionCount           int    `json:"region_count"`
	AssetCount            int    `json:"asset_count"`
	NATGatewayCount       int    `json:"nat_gateway_count"`
	ScopeCount            int    `json:"scope_count"`
	TimeoutSeconds        int    `json:"timeout_seconds"`
	MaxHTTPAttempts       int    `json:"max_http_attempts"`
}
type Binding struct {
	Scope             domain.Scope
	GatewayID, Region string
}

var alias = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)
var regionName = regexp.MustCompile(`^[a-z]{2}-[a-z][a-z0-9]{0,20}(-[a-z0-9]{1,20}){0,2}$`)
var accountID = regexp.MustCompile(`^[1-9][0-9]{5,31}$`)
var resourceID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

func uniqueJSON(data []byte) bool {
	d := json.NewDecoder(bytes.NewReader(data))
	var visit func(int) bool
	visit = func(depth int) bool {
		if depth > 20 {
			return false
		}
		t, e := d.Token()
		if e != nil {
			return false
		}
		delim, ok := t.(json.Delim)
		if !ok {
			return true
		}
		if delim == '{' {
			seen := []string{}
			for d.More() {
				k, e := d.Token()
				s, ok := k.(string)
				if e != nil || !ok {
					return false
				}
				// encoding/json matches struct fields with Unicode case folding.
				for _, previous := range seen {
					if strings.EqualFold(previous, s) {
						return false
					}
				}
				seen = append(seen, s)
				if !visit(depth + 1) {
					return false
				}
			}
		} else if delim == '[' {
			for d.More() {
				if !visit(depth + 1) {
					return false
				}
			}
		} else {
			return false
		}
		_, e = d.Token()
		return e == nil
	}
	if !visit(0) {
		return false
	}
	_, e := d.Token()
	return e == io.EOF
}

type envLookup func(string) (string, bool)

func Load(path string) (Config, error) { return load(path, os.LookupEnv) }

func load(path string, lookup envLookup) (Config, error) {
	var result Config
	if strings.TrimSpace(path) == "" {
		return result, ErrInvalid
	}
	st, e := os.Stat(path)
	if e != nil || !st.Mode().IsRegular() {
		return result, ErrInvalid
	}
	f, e := os.Open(path)
	if e != nil {
		return result, ErrInvalid
	}
	defer f.Close()
	st, e = f.Stat()
	if e != nil || !st.Mode().IsRegular() {
		return result, ErrInvalid
	}
	body, e := io.ReadAll(io.LimitReader(f, (64<<10)+1))
	if e != nil || len(body) > 64<<10 || !uniqueJSON(body) {
		return result, ErrInvalid
	}
	var raw map[string]json.RawMessage
	if json.Unmarshal(body, &raw) != nil {
		return result, ErrInvalid
	}
	for key, value := range raw {
		if strings.EqualFold(key, "budget") {
			var fields map[string]json.RawMessage
			if json.Unmarshal(value, &fields) != nil {
				return result, ErrInvalid
			}
			for field := range fields {
				if strings.EqualFold(field, "application_model_attempts") || strings.EqualFold(field, "session_model_attempts") {
					result.modelBudgetDeclared = true
				}
			}
		}
	}
	d := json.NewDecoder(bytes.NewReader(body))
	d.DisallowUnknownFields()
	var in fileConfig
	if d.Decode(&in) != nil {
		return result, ErrInvalid
	}
	if !alias.MatchString(in.ConnectionRef) || in.ConnectionRef == "local-offline" || !envName.MatchString(in.AccessKeyIDEnv) || !envName.MatchString(in.AccessKeySecretEnv) || in.AccessKeyIDEnv == in.AccessKeySecretEnv || !accountID.MatchString(in.ExpectedAccountID) || in.TimeoutSeconds < 1 || int64(in.TimeoutSeconds) > workload.MaxSeconds || in.MaxHTTPAttempts < 0 || in.MaxHTTPAttempts > workload.MaxCounter {
		return result, ErrInvalid
	}
	if in.ControlPlaneRegion != "cn-hangzhou" && in.ControlPlaneRegion != "ap-southeast-1" && in.ControlPlaneRegion != "ap-southeast-3" {
		return result, ErrInvalid
	}
	if in.CredentialMode != "env_ak" && in.CredentialMode != "env_sts" {
		return result, ErrInvalid
	}
	if in.CredentialMode == "env_ak" && (in.SecurityTokenEnv != "" || in.CredentialExpiresAt != "") {
		return result, ErrInvalid
	}
	if in.CredentialMode == "env_sts" && (!envName.MatchString(in.SecurityTokenEnv) || in.SecurityTokenEnv == in.AccessKeyIDEnv || in.SecurityTokenEnv == in.AccessKeySecretEnv) {
		return result, ErrInvalid
	}
	if in.CredentialExpiresAt != "" {
		_, e := time.Parse(time.RFC3339Nano, in.CredentialExpiresAt)
		if e != nil {
			return result, ErrInvalid
		}
	}
	if len(in.AllowedRegions) == 0 || len(in.AllowedRegions) > 10 || len(in.AllowedAssets) > 50 || len(in.AllowedNATGateways) > 50 || len(in.AllowedBoundaries) == 0 || len(in.AllowedBoundaries) > 3 {
		return result, ErrInvalid
	}
	regions := map[string]bool{}
	for _, r := range in.AllowedRegions {
		if !regionName.MatchString(r) || regions[r] {
			return result, ErrInvalid
		}
		regions[r] = true
	}
	bounds := map[string]bool{}
	for _, b := range in.AllowedBoundaries {
		if (b != "internet/in" && b != "internet/out" && b != "nat/out") || bounds[b] {
			return result, ErrInvalid
		}
		bounds[b] = true
	}
	seen := map[string]bool{}
	for _, a := range in.AllowedAssets {
		k := a.InstanceID
		if !regions[a.Region] || !strings.HasPrefix(a.InstanceID, "i-") || !resourceID.MatchString(a.InstanceID) || seen[k] {
			return result, ErrInvalid
		}
		seen[k] = true
	}
	seen = map[string]bool{}
	for _, g := range in.AllowedNATGateways {
		if !regions[g.Region] || !strings.HasPrefix(g.ID, "ngw-") || !resourceID.MatchString(g.ID) || seen[g.ID] || !bounds["nat/out"] {
			return result, ErrInvalid
		}
		seen[g.ID] = true
	}
	if !validBudget(in.Budget) {
		return result, ErrInvalid
	}
	ak, secret, token := "", "", ""
	credentialState := "disabled"
	if in.Enabled {
		ak, _ = lookup(in.AccessKeyIDEnv)
		secret, _ = lookup(in.AccessKeySecretEnv)
		if in.CredentialMode == "env_sts" {
			token, _ = lookup(in.SecurityTokenEnv)
		}
		credentialState = "available"
		if ak == "" || secret == "" || (in.CredentialMode == "env_sts" && token == "") {
			credentialState = "missing"
		} else if !validSecret(ak) || !validSecret(secret) || (in.CredentialMode == "env_sts" && !validSecret(token)) {
			credentialState = "invalid"
		}
		if credentialState != "available" {
			ak, secret, token = "", "", ""
		}
	}
	// Set order is not a new authorization period or configuration change.
	sort.Strings(in.AllowedRegions)
	sort.Strings(in.AllowedBoundaries)
	sort.Slice(in.AllowedAssets, func(i, j int) bool {
		return in.AllowedAssets[i].Region+in.AllowedAssets[i].InstanceID < in.AllowedAssets[j].Region+in.AllowedAssets[j].InstanceID
	})
	sort.Slice(in.AllowedNATGateways, func(i, j int) bool { return in.AllowedNATGateways[i].ID < in.AllowedNATGateways[j].ID })
	canonical, _ := json.Marshal(in)
	result = Config{modelBudgetDeclared: result.modelBudgetDeclared, settings: in, ak: ak, secret: secret, token: token, revision: "cloud-config_" + digest(string(canonical)), credentialState: credentialState}
	return result, nil
}
func validSecret(s string) bool {
	return len(s) > 0 && len(s) <= 8192 && strings.TrimSpace(s) == s && strings.IndexFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f }) < 0
}
func digest(s string) string            { sum := sha256.Sum256([]byte(s)); return hex.EncodeToString(sum[:])[:32] }
func (c Config) RequestedEnabled() bool { return c.settings.Enabled }
func (c Config) Ready() bool {
	return c.revision != "" && c.settings.Enabled && c.credentialState == "available" && !c.credentialExpired() && c.authorizationState() == "active"
}
func (c Config) Enabled() bool               { return c.Ready() }
func (c Config) Revision() string            { return c.revision }
func (c Config) ConnectionRef() string       { return c.settings.ConnectionRef }
func (c Config) ExpectedAccountID() string   { return c.settings.ExpectedAccountID }
func (c Config) CredentialMode() string      { return c.settings.CredentialMode }
func (c Config) AccessKeyID() string         { return c.ak }
func (c Config) AccessKeySecret() string     { return c.secret }
func (c Config) SecurityToken() string       { return c.token }
func (c Config) CredentialExpiresAt() string { return c.settings.CredentialExpiresAt }
func (c Config) ControlPlaneRegion() string  { return c.settings.ControlPlaneRegion }
func (c Config) CFWOrigin() string {
	r := c.settings.ControlPlaneRegion
	if r == "ap-southeast-3" {
		r = "ap-southeast-1"
	}
	if r == "" {
		return ""
	}
	if r == "cn-hangzhou" {
		return "https://cloudfw.aliyuncs.com"
	}
	return "https://cloudfw." + r + ".aliyuncs.com"
}
func (c Config) STSOrigin() string {
	if c.settings.ControlPlaneRegion == "" {
		return ""
	}
	return "https://sts." + c.settings.ControlPlaneRegion + ".aliyuncs.com"
}
func (c Config) ECSOrigin(region string) string {
	for _, r := range c.settings.AllowedRegions {
		if r == region {
			return "https://ecs." + r + ".aliyuncs.com"
		}
	}
	return ""
}
func (c Config) Timeout() time.Duration {
	return time.Duration(c.settings.TimeoutSeconds) * time.Second
}
func (c Config) MaxHTTPAttempts() int { return c.settings.MaxHTTPAttempts }
func (c Config) Budget() Budget       { return c.settings.Budget }
func (c Config) Assets() []Asset      { return append([]Asset{}, c.settings.AllowedAssets...) }
func (c Config) Gateways() []Gateway  { return append([]Gateway{}, c.settings.AllowedNATGateways...) }
func (c Config) Regions() []string    { return append([]string{}, c.settings.AllowedRegions...) }
func (c Config) SensitiveValues() []string {
	return []string{c.ak, c.secret, c.token, c.settings.ExpectedAccountID}
}
func (c Config) Scopes() []domain.Scope {
	out := []domain.Scope{}
	for _, b := range c.bindings() {
		out = append(out, b.Scope)
	}
	return out
}
func (c Config) ScopeBinding(ref string) (Binding, bool) {
	for _, b := range c.bindings() {
		if b.Scope.Ref == ref {
			return b, true
		}
	}
	return Binding{}, false
}
func (c Config) bindings() []Binding {
	out := []Binding{}
	if c.revision == "" {
		return out
	}
	for _, kind := range c.settings.AllowedBoundaries {
		parts := strings.Split(kind, "/")
		add := func(gateway, region string) {
			identity := c.settings.ConnectionRef + "|" + c.settings.ExpectedAccountID + "|" + c.settings.ControlPlaneRegion + "|" + kind + "|" + gateway
			key := digest(identity)
			s := domain.Scope{Ref: "scope_" + key, Provider: "aliyun", AccountRef: "account_" + digest(c.settings.ConnectionRef+"|"+c.settings.ExpectedAccountID), ControlPlaneRef: "control_" + digest(c.settings.ConnectionRef+"|"+c.settings.ControlPlaneRegion), IPFamily: "ipv4", Boundary: parts[0], Direction: parts[1], PolicySetRef: "policy_" + key}
			if gateway != "" {
				s.GatewayRef = "gateway_" + digest(c.settings.ConnectionRef+"|"+gateway)
			}
			out = append(out, Binding{Scope: s, GatewayID: gateway, Region: region})
		}
		if parts[0] == "nat" {
			for _, g := range c.settings.AllowedNATGateways {
				add(g.ID, g.Region)
			}
		} else {
			add("", "")
		}
	}
	return out
}
func validBudget(b Budget) bool {
	if b.Ref == "" {
		return b.ExpiresAt == "" && b.ApplicationCloudAttempts == 0 && b.SessionCloudAttempts == 0 && b.ApplicationModelAttempts == 0 && b.SessionModelAttempts == 0
	}
	if !alias.MatchString(b.Ref) || b.ApplicationCloudAttempts < 0 || b.ApplicationCloudAttempts > workload.MaxCounter || b.SessionCloudAttempts < 0 || b.SessionCloudAttempts > b.ApplicationCloudAttempts || b.ApplicationModelAttempts < 0 || b.ApplicationModelAttempts > workload.MaxCounter || b.SessionModelAttempts < 0 || b.SessionModelAttempts > b.ApplicationModelAttempts {
		return false
	}
	if b.ExpiresAt != "" {
		if _, err := time.Parse(time.RFC3339Nano, b.ExpiresAt); err != nil {
			return false
		}
	}
	return true
}
func (c Config) credentialExpired() bool {
	if c.settings.CredentialExpiresAt == "" {
		return false
	}
	at, err := time.Parse(time.RFC3339Nano, c.settings.CredentialExpiresAt)
	return err != nil || !at.After(time.Now())
}
func (c Config) authorizationState() string {
	b := c.settings.Budget
	if b.Ref == "" {
		return "not_configured"
	}
	if b.ExpiresAt != "" {
		at, err := time.Parse(time.RFC3339Nano, b.ExpiresAt)
		if err != nil || !at.After(time.Now()) {
			return "expired"
		}
	}
	if b.ApplicationCloudAttempts == 0 || b.SessionCloudAttempts == 0 || c.settings.MaxHTTPAttempts == 0 {
		return "blocked"
	}
	return "active"
}

// WithBudget binds only an explicitly resolved policy and retains credentials,
// limits and source scope. It does not establish a new authorization period.
func (c Config) WithBudget(b Budget) (Config, error) {
	if c.revision == "" || !validBudget(b) {
		return Config{}, ErrInvalid
	}
	c.settings.Budget = b
	canonical, _ := json.Marshal(c.settings)
	c.revision = "cloud-config_" + digest(string(canonical))
	return c, nil
}
func (c Config) AllowsAsset(region, id string) bool {
	if c.revision == "" || !strings.HasPrefix(id, "i-") || !resourceID.MatchString(id) || c.ECSOrigin(region) == "" {
		return false
	}
	if len(c.settings.AllowedAssets) == 0 {
		return true
	}
	for _, a := range c.settings.AllowedAssets {
		if a.Region == region && a.InstanceID == id {
			return true
		}
	}
	return false
}
func (c Config) AllowsGateway(region, id string) bool {
	if c.revision == "" || !strings.HasPrefix(id, "ngw-") || !resourceID.MatchString(id) || c.ECSOrigin(region) == "" {
		return false
	}
	allowed := false
	for _, b := range c.settings.AllowedBoundaries {
		if b == "nat/out" {
			allowed = true
		}
	}
	if !allowed {
		return false
	}
	if len(c.settings.AllowedNATGateways) == 0 {
		return true
	}
	for _, g := range c.settings.AllowedNATGateways {
		if g.Region == region && g.ID == id {
			return true
		}
	}
	return false
}

// ScopeForGateway constructs only a previously resolved, bounded gateway scope;
// it does not discover gateways or expand the configured account/regions.
func (c Config) ScopeForGateway(region, id string) (Binding, bool) {
	if !c.AllowsGateway(region, id) {
		return Binding{}, false
	}
	identity := c.settings.ConnectionRef + "|" + c.settings.ExpectedAccountID + "|" + c.settings.ControlPlaneRegion + "|nat/out|" + id
	key := digest(identity)
	scope := domain.Scope{Ref: "scope_" + key, Provider: "aliyun", AccountRef: "account_" + digest(c.settings.ConnectionRef+"|"+c.settings.ExpectedAccountID), ControlPlaneRef: "control_" + digest(c.settings.ConnectionRef+"|"+c.settings.ControlPlaneRegion), IPFamily: "ipv4", Boundary: "nat", Direction: "out", PolicySetRef: "policy_" + key, GatewayRef: "gateway_" + digest(c.settings.ConnectionRef+"|"+id)}
	return Binding{Scope: scope, GatewayID: id, Region: region}, true
}
func (c Config) Summary() Summary {
	credentialState := c.credentialState
	if c.RequestedEnabled() && credentialState == "available" && c.credentialExpired() {
		credentialState = "expired"
	}
	out := Summary{Configured: c.revision != "", Enabled: c.RequestedEnabled(), Ready: c.Ready(), SourceSelected: c.revision != "", StructureValid: c.revision != "", CredentialLocalReady: c.credentialState == "available" && !c.credentialExpired(), CredentialState: credentialState, CredentialExpired: c.credentialExpired(), ExecutionPolicyReady: c.authorizationState() == "active", AuthorizationState: c.authorizationState(), LedgerState: "not_checked", UpstreamValidation: "not_checked", CredentialMode: c.settings.CredentialMode, CredentialExpiryKnown: c.settings.CredentialExpiresAt != "", Refreshable: false, RegionCount: len(c.settings.AllowedRegions), AssetCount: len(c.settings.AllowedAssets), NATGatewayCount: len(c.settings.AllowedNATGateways), ScopeCount: len(c.Scopes()), TimeoutSeconds: c.settings.TimeoutSeconds, MaxHTTPAttempts: c.settings.MaxHTTPAttempts}
	switch {
	case !out.Configured:
		out.BlockedReason = "not_configured"
		out.CredentialState = "not_checked"
	case !out.Enabled:
		out.BlockedReason = "disabled"
	case !out.CredentialLocalReady:
		out.BlockedReason = "credential_" + out.CredentialState
	case !out.ExecutionPolicyReady:
		out.BlockedReason = "authorization_" + out.AuthorizationState
	}
	return out
}
func (c Config) MarshalJSON() ([]byte, error) { return json.Marshal(c.Summary()) }
func (c Config) String() string               { b, _ := c.MarshalJSON(); return string(b) }
func (c Config) GoString() string             { return c.String() }

func (c Config) AllowsBoundary(kind string) bool {
	for _, boundary := range c.settings.AllowedBoundaries {
		if boundary == kind {
			return true
		}
	}
	return false
}
func (c Config) AllowedBoundaries() []string {
	return append([]string{}, c.settings.AllowedBoundaries...)
}

func (c Config) ModelBudgetDeclared() bool { return c.modelBudgetDeclared }

// Local aliases carry no provider credential or raw account number.
func (c Config) AccountAlias() string {
	return "account_" + digest(c.settings.ConnectionRef+"|"+c.settings.ExpectedAccountID)
}
func (c Config) ControlPlaneAlias() string {
	return "control_" + digest(c.settings.ConnectionRef+"|"+c.settings.ControlPlaneRegion)
}
