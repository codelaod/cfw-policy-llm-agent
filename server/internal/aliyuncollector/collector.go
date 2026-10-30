package aliyuncollector

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"

	"cfw-policy-llm-agent/server/internal/audit"
	"cfw-policy-llm-agent/server/internal/domain"
)

type Collector struct {
	reader Reader
	cfg    Config
}

type run struct {
	collector        *Collector
	ctx              context.Context
	calls            int
	artifacts        []RawArtifact
	observations     []Observation
	instances        map[string]map[string]json.RawMessage
	instanceComplete map[string]bool
	groups           map[string]domain.SecurityGroupEvidence
}
type family struct {
	policies     []domain.Policy
	assets       []domain.Asset
	addresses    map[string]domain.AddressReference
	ports        map[string]domain.PortReference
	protection   domain.Protection
	complete     bool
	gaps         []string
	fingerprints []string
	gateway      *domain.Resource
}

func alias(v string) bool { return len(v) > 0 && len(v) <= 128 && !strings.ContainsAny(v, "\x00\r\n") }
func decimal(v string) bool {
	if len(v) == 0 || len(v) > 32 {
		return false
	}
	for _, r := range v {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
func New(reader Reader, cfg Config) (*Collector, error) {
	if reader == nil || !decimal(cfg.ExpectedAccountID) {
		return nil, Failure("COLLECTOR_CONFIGURATION_INVALID")
	}
	s := cfg.Scope
	if s.Provider != "aliyun" || s.IPFamily != "ipv4" || !alias(s.Ref) || !alias(s.AccountRef) || !alias(s.ControlPlaneRef) || !alias(s.PolicySetRef) || !(s.Boundary == "internet" && (s.Direction == "in" || s.Direction == "out") || s.Boundary == "nat" && s.Direction == "out" && alias(s.GatewayRef)) {
		return nil, Failure("COLLECTOR_CONFIGURATION_INVALID")
	}
	if s.Boundary == "internet" && s.GatewayRef != "" {
		return nil, Failure("COLLECTOR_CONFIGURATION_INVALID")
	}
	var err error
	cfg, err = resolveConfig(cfg)
	if err != nil {
		return nil, err
	}
	regions := make(map[string]string, len(cfg.AssetRegions))
	for id, region := range cfg.AssetRegions {
		if !alias(id) || !alias(region) || !allowedRegion(cfg, region) {
			return nil, Failure("COLLECTOR_CONFIGURATION_INVALID")
		}
		regions[id] = region
	}
	cfg.AssetRegions = regions
	cfg.AllowedGatewayIDs = append([]string(nil), cfg.AllowedGatewayIDs...)
	if s.Boundary == "nat" {
		found := false
		for _, id := range cfg.AllowedGatewayIDs {
			if !alias(id) {
				return nil, Failure("COLLECTOR_CONFIGURATION_INVALID")
			}
			found = found || id == cfg.GatewayID
		}
		if len(cfg.AllowedGatewayIDs) > 0 && !found || !alias(cfg.GatewayID) || !allowedRegion(cfg, cfg.GatewayRegion) || !gatewayAllowed(cfg, cfg.GatewayID, cfg.GatewayRegion) {
			return nil, Failure("COLLECTOR_SCOPE_DENIED")
		}
	}
	return &Collector{reader: reader, cfg: cfg}, nil
}

func (c *Collector) Collect(ctx context.Context, selection domain.Selection) (Collection, error) {
	if ctx == nil {
		return Collection{}, Failure("COLLECTOR_CONFIGURATION_INVALID")
	}
	if err := ctx.Err(); err != nil {
		return Collection{}, err
	}
	if selection.Kind == "gateway_policy" {
		if c.cfg.Scope.Boundary != "nat" || len(selection.AssetIDs) != 0 {
			return Collection{}, Failure("COLLECTOR_SCOPE_DENIED")
		}
	} else if selection.Kind == "assets" {
		if len(selection.AssetIDs) == 0 || len(selection.AssetIDs) > c.cfg.Workload.Limits.AssetsPerBatch {
			return Collection{}, Failure("COLLECTOR_SCOPE_DENIED")
		}
		seen := map[string]bool{}
		for _, id := range selection.AssetIDs {
			if seen[id] || c.cfg.AssetRegions[id] == "" {
				return Collection{}, Failure("COLLECTOR_SCOPE_DENIED")
			}
			seen[id] = true
		}
	} else {
		return Collection{}, Failure("COLLECTOR_SCOPE_DENIED")
	}
	selection.AssetIDs = append([]string(nil), selection.AssetIDs...)
	sort.Strings(selection.AssetIDs)
	window, _ := time.ParseDuration(strconv.Itoa(c.cfg.Workload.Limits.CollectionSeconds) + "s")
	ctx, cancel := context.WithTimeout(ctx, window)
	defer cancel()
	r := &run{collector: c, ctx: ctx}
	started := time.Now().UTC().Format(time.RFC3339Nano)
	first, err := r.collectFamily(1, selection)
	if err != nil {
		if input, ok := r.firstPartialInput(started, selection, first, err); ok {
			return Collection{Input: input, Observations: r.observations, Artifacts: r.artifacts}, nil
		}
		return Collection{Observations: r.observations, Artifacts: r.artifacts}, controlled(err)
	}
	second, secondErr := r.collectFamily(2, selection)
	if ctx.Err() != nil {
		return Collection{Observations: r.observations, Artifacts: r.artifacts}, ctx.Err()
	}
	if fatal(secondErr) {
		return Collection{Observations: r.observations, Artifacts: r.artifacts}, controlled(secondErr)
	}
	consistency := "unverified"
	gaps := append([]string(nil), first.gaps...)
	complete := first.complete
	if secondErr != nil {
		gaps = append(gaps, "SECOND_OBSERVATION_INCOMPLETE", controlled(secondErr).Error())
		complete = false
	} else {
		gaps = append(gaps, second.gaps...)
		complete = first.complete && second.complete
		if first.complete && second.complete {
			if equalFingerprints(first.fingerprints, second.fingerprints) {
				consistency = "observed_stable"
			} else {
				consistency = "changed"
				gaps = append(gaps, "CONFIGURATION_CHANGED")
			}
		}
	}
	if consistency == "unverified" {
		gaps = append(gaps, "OBSERVATION_UNVERIFIED")
	}
	protection := first.protection
	// Coverage is an independent rule fact, so an incomplete ACL does not erase
	// two agreeing protection observations. A later contradiction or unavailable
	// protection observation cannot, however, certify the first state as current.
	// Both original observations remain in the private artifacts.
	if protection.EvidenceRef != "" && protection.State != "unknown" {
		if second.protection.EvidenceRef == "" || second.protection.State == "unknown" {
			protection.State = "unknown"
			gaps = append(gaps, "PROTECTION_UNVERIFIED")
		} else if second.protection.State != protection.State {
			protection.State = "unknown"
			gaps = append(gaps, "PROTECTION_CHANGED")
		}
	}
	if c.cfg.Scope.Boundary == "internet" {
		secondAssets := map[string]domain.Asset{}
		for _, asset := range second.assets {
			secondAssets[asset.ID] = asset
		}
		for i := range first.assets {
			asset := &first.assets[i]
			if asset.Protection == nil || asset.Protection.State == "unknown" {
				continue
			}
			later := secondAssets[asset.ID].Protection
			if later == nil || later.EvidenceRef == "" || later.State == "unknown" {
				asset.Protection.State = "unknown"
				gaps = append(gaps, "PROTECTION_UNVERIFIED")
			} else if later.State != asset.Protection.State {
				asset.Protection.State = "unknown"
				gaps = append(gaps, "PROTECTION_CHANGED")
			}
		}
	}
	profile := "aliyun-cfw-internet-v1"
	if c.cfg.Scope.Boundary == "nat" {
		profile = "aliyun-cfw-nat-v1"
	}
	version := hash([]byte(started + strings.Join(first.fingerprints, "\x00")))
	in := domain.Input{SchemaVersion: 1, SnapshotVersion: "live-" + version[:32], ProfileVersion: "unconfirmed-v1", IntentVersion: "unconfirmed-v1", NormalizerVersion: audit.NormalizerVersion, KernelVersion: audit.KernelVersion, SourceKind: "live_readonly", EvaluationAt: time.Now().UTC().Format(time.RFC3339Nano), CollectedAt: started, Scope: c.cfg.Scope, Semantics: domain.Semantics{ProfileID: profile, Version: audit.ProviderSemanticsVersion, DefaultAction: "unknown", SourceRef: "T-06"}, Completeness: domain.Completeness{Declared: true, EnumerationComplete: complete, ObservationConsistency: consistency, KnownGaps: unique(gaps)}, Protection: protection, Selection: selection, Assets: first.assets, GatewayMetadata: first.gateway, Policies: first.policies, AddressRefs: first.addresses, PortRefs: first.ports, Baseline: audit.ProjectBaseline()}
	validationContext, validationErr := audit.WithInputObjectLimits(ctx, c.cfg.Workload.Limits.AssetsPerBatch, c.cfg.MaxPolicies)
	if validationErr != nil {
		return Collection{Observations: r.observations, Artifacts: r.artifacts}, Failure("COLLECTOR_CONFIGURATION_INVALID")
	}
	if err := audit.ValidateContext(validationContext, in); err != nil {
		return Collection{Observations: r.observations, Artifacts: r.artifacts}, Failure("COLLECTOR_NORMALIZATION_INVALID")
	}
	return Collection{Input: in, Observations: r.observations, Artifacts: r.artifacts}, nil
}
func equalFingerprints(a, b []string) bool {
	a = append([]string(nil), a...)
	b = append([]string(nil), b...)
	sort.Strings(a)
	sort.Strings(b)
	return strings.Join(a, "\x00") == strings.Join(b, "\x00")
}
func hash(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }
func unique(a []string) []string {
	m := map[string]bool{}
	out := []string{}
	for _, v := range a {
		if !m[v] {
			m[v] = true
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}
func cloneQuery(q map[string]string) map[string]string {
	n := map[string]string{}
	for k, v := range q {
		n[k] = v
	}
	return n
}
func controlled(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	var f Failure
	if errors.As(err, &f) {
		return f
	}
	var coded interface{ ErrorCode() string }
	if errors.As(err, &coded) {
		code := coded.ErrorCode()
		for _, allowed := range []string{"CLOUD_ACCESS_DENIED", "CLOUD_THROTTLED", "CLOUD_PROVIDER_UNAVAILABLE", "CLOUD_PROVIDER_REJECTED", "CLOUD_CREDENTIAL_EXPIRED", "CLOUD_CREDENTIAL_UNAVAILABLE", "CLOUD_IDENTITY_NOT_VERIFIED", "CLOUD_ACCOUNT_MISMATCH", "CLOUD_OWNER_MISMATCH", "CLOUD_GATEWAY_MISMATCH", "CLOUD_ASSET_SCOPE_MISMATCH", "CLOUD_GROUP_SCOPE_MISMATCH", "CLOUD_IDENTITY_REFUSED", "CLOUD_IDENTITY_INVALID", "CLOUD_QUERY_REFUSED", "CLOUD_RESPONSE_INVALID", "CLOUD_RESPONSE_READ_FAILED", "CLOUD_RESPONSE_CONTAINS_CREDENTIAL", "UPSTREAM_RESPONSE_TOO_LARGE", "ATTEMPT_LIMIT", "BUDGET_EXPIRED", "BUDGET_NOT_CONFIGURED", "EXECUTION_CLOSED", "EFFECT_DENIED", "CONFIGURATION_STALE"} {
			if code == allowed {
				return Failure(code)
			}
		}
	}
	return Failure("COLLECTION_READ_FAILED")
}
func fatal(err error) bool {
	var f Failure
	if !errors.As(err, &f) {
		return false
	}
	for _, code := range []Failure{"COLLECTOR_SCOPE_DENIED", "PROVIDER_IDENTITY_CONFLICT", "PROVIDER_RESPONSE_INVALID", "UPSTREAM_RESPONSE_TOO_LARGE", "CLOUD_ACCOUNT_MISMATCH", "CLOUD_OWNER_MISMATCH", "CLOUD_GATEWAY_MISMATCH", "CLOUD_ASSET_SCOPE_MISMATCH", "CLOUD_GROUP_SCOPE_MISMATCH", "CLOUD_IDENTITY_NOT_VERIFIED", "CLOUD_IDENTITY_REFUSED", "CLOUD_IDENTITY_INVALID", "CLOUD_RESPONSE_INVALID", "CLOUD_RESPONSE_CONTAINS_CREDENTIAL"} {
		if f == code {
			return true
		}
	}
	return false
}

func (r *run) read(round int, operation string, q map[string]string) (json.RawMessage, error) {
	if err := r.ctx.Err(); err != nil {
		return nil, err
	}
	if r.calls >= r.collector.cfg.MaxAttempts {
		return nil, Failure("COLLECTION_ATTEMPT_LIMIT")
	}
	r.calls++
	body, err := r.collector.reader.Read(r.ctx, operation, cloneQuery(q))
	if err != nil {
		return nil, controlled(err)
	}
	if len(body) > 2<<20 {
		return nil, Failure("UPSTREAM_RESPONSE_TOO_LARGE")
	}
	if _, err := object(body); err != nil {
		return nil, err
	}
	copyBody := append(json.RawMessage(nil), body...)
	r.artifacts = append(r.artifacts, RawArtifact{Round: round, Operation: operation, Query: cloneQuery(q), Body: copyBody, SHA256: hash(copyBody)})
	return copyBody, nil
}

func (r *run) collectFamily(round int, selection domain.Selection) (family, error) {
	f := family{complete: true, policies: []domain.Policy{}, assets: []domain.Asset{}, addresses: map[string]domain.AddressReference{}, ports: map[string]domain.PortReference{}, protection: domain.Protection{Applicable: true, Required: true, State: "unknown"}}
	cfg := r.collector.cfg
	r.instances = map[string]map[string]json.RawMessage{}
	r.instanceComplete = map[string]bool{}
	r.groups = map[string]domain.SecurityGroupEvidence{}
	if selection.Kind == "assets" {
		complete, err := r.collectInstances(round, selection.AssetIDs)
		if err != nil {
			return f, err
		}
		if !complete {
			f.complete = false
			f.gaps = append(f.gaps, "ECS_METADATA_INCOMPLETE")
		}
	}
	if cfg.Scope.Boundary == "nat" {
		meta, complete, err := r.pages(round, "DescribeNatFirewallList", map[string]string{"NatGatewayId": cfg.GatewayID, "RegionNo": cfg.GatewayRegion}, "NatFirewallList", "PageNo", false, "NatGatewayId", 50)
		if err != nil {
			return f, err
		}
		f.complete = f.complete && complete
		if len(meta) != 1 {
			f.complete = false
			f.gaps = append(f.gaps, "GATEWAY_METADATA_INCOMPLETE")
		} else {
			if err := checkAccount(meta[0], cfg.ExpectedAccountID); err != nil {
				return f, err
			}
			if text(meta[0], "NatGatewayId") != cfg.GatewayID || text(meta[0], "RegionId") != cfg.GatewayRegion {
				return f, Failure("COLLECTOR_SCOPE_DENIED")
			}
			f.protection = natProtection(meta[0], cfg.Scope.Ref)
			gateway := gatewayResource(meta[0], cfg, time.Now().UTC().Format(time.RFC3339Nano))
			f.gateway = &gateway
			mode, modePresent := intField(meta[0], "StrictMode")
			if !knownNATMetadata(meta[0]) || !modePresent || (mode != 0 && mode != 1) || !alias(text(meta[0], "ProxyId")) {
				f.complete = false
				f.gaps = append(f.gaps, "GATEWAY_METADATA_UNSUPPORTED")
			}
		}
	} else if selection.Kind == "assets" {
		states := []string{}
		for _, id := range selection.AssetIDs {
			if !r.instanceComplete[id] {
				asset, _ := r.collectAsset(round, id, nil)
				asset.Protection = assetProtectionAssertion(cfg.Scope.Ref, id, nil, false)
				f.assets = append(f.assets, asset)
				states = append(states, "unknown")
				continue
			}
			rows, complete, err := r.pages(round, "DescribeAssetList", map[string]string{"SearchItem": id, "IpVersion": "4"}, "Assets", "CurrentPage", false, "", 1000)
			if err != nil {
				return f, err
			}
			f.complete = f.complete && complete
			matches := []map[string]json.RawMessage{}
			for _, row := range rows {
				if err := checkAccount(row, cfg.ExpectedAccountID); err != nil {
					return f, err
				}
				if text(row, "BindInstanceId") != id && text(row, "ResourceInstanceId") != id {
					return f, Failure("COLLECTOR_SCOPE_DENIED")
				}
				if text(row, "RegionID") != cfg.AssetRegions[id] {
					return f, Failure("COLLECTOR_SCOPE_DENIED")
				}
				matches = append(matches, row)
			}
			if len(matches) == 0 {
				f.gaps = append(f.gaps, "ASSET_PROTECTION_NOT_OBSERVED")
			}
			asset, err := r.collectAsset(round, id, matches)
			asset.Protection = assetProtectionAssertion(cfg.Scope.Ref, id, matches, complete)
			states = append(states, asset.Protection.State)
			if !fatal(controlled(err)) {
				f.assets = append(f.assets, asset)
			}
			if err != nil {
				return f, err
			}
		}
		f.protection.State = aggregateProtection(states)
		f.protection.EvidenceRef = "cfw-protection-" + cfg.Scope.Ref
	}
	if cfg.Scope.Boundary == "nat" && selection.Kind == "assets" {
		for _, id := range selection.AssetIDs {
			asset, err := r.collectAsset(round, id, nil)
			if !fatal(controlled(err)) {
				f.assets = append(f.assets, asset)
			}
			if err != nil {
				return f, err
			}
		}
	}
	op := "DescribeControlPolicy"
	q := map[string]string{"Direction": cfg.Scope.Direction, "IpVersion": "4"}
	if cfg.Scope.Boundary == "nat" {
		op = "DescribeNatFirewallControlPolicy"
		q = map[string]string{"Direction": "out", "NatGatewayId": cfg.GatewayID}
	}
	rows, complete, pageErr := r.pages(round, op, q, "Policys", "CurrentPage", true, "AclUuid", cfg.MaxPolicies)
	if fatal(controlled(pageErr)) {
		return f, pageErr
	}
	f.complete = f.complete && complete && pageErr == nil
	for _, row := range rows {
		p, refs, err := normalizePolicy(cfg.Scope, cfg.GatewayID, row)
		if err != nil {
			return f, err
		}
		for _, ref := range refs {
			a, addressExists := f.addresses[ref.key]
			pv, portExists := f.ports[ref.key]
			if !addressExists && !portExists {
				var e error
				a, pv, e = r.resolveBook(round, ref)
				if e != nil {
					// The policy itself was verified and normalized; an unreadable
					// reference remains unresolved rather than erasing that fact.
					if !fatal(controlled(e)) {
						f.policies = append(f.policies, p)
					}
					return f, e
				}
				if ref.kind == "port" {
					f.ports[ref.key] = pv
				} else {
					f.addresses[ref.key] = a
				}
			}
			if !inlineCompatible(ref, a, pv) {
				mark(&p, "inline_reference_conflict", rawValue(ref.inline))
			}
		}
		f.policies = append(f.policies, p)
	}
	if pageErr != nil {
		return f, pageErr
	}
	for _, o := range r.observations {
		if o.Round == round {
			f.fingerprints = append(f.fingerprints, o.Operation+"\x00"+queryKey(o.Filters)+"\x00"+o.SemanticSHA256)
			if !o.EnumerationComplete {
				f.complete = false
				f.gaps = append(f.gaps, "ENUMERATION_INCOMPLETE")
			}
		}
	}
	return f, nil
}
func queryKey(q map[string]string) string { b, _ := json.Marshal(q); return string(b) }
func checkAccount(row map[string]json.RawMessage, want string) error {
	for _, key := range []string{"AliUid", "MemberUid", "OwnerId"} {
		if raw, ok := row[key]; ok && string(raw) != "null" {
			got := strings.Trim(string(raw), "\"")
			if !decimal(got) {
				return Failure("PROVIDER_RESPONSE_INVALID")
			}
			if got != "0" && got != want {
				return Failure("COLLECTOR_SCOPE_DENIED")
			}
		}
	}
	return nil
}
func natProtection(row map[string]json.RawMessage, scope string) domain.Protection {
	state := "unknown"
	switch text(row, "ProxyStatus") {
	case "normal":
		state = "on"
	case "closed":
		state = "off"
	case "opening", "closing", "configuring", "deleting":
		state = "transitioning"
	}
	return domain.Protection{Applicable: true, Required: true, State: state, EvidenceRef: "nat-metadata-" + scope}
}
func assetProtection(v string) string {
	switch v {
	case "open":
		return "on"
	case "closed":
		return "off"
	case "opening", "closing":
		return "transitioning"
	}
	return "unknown"
}
func aggregateProtection(a []string) string {
	if len(a) == 0 {
		return "unknown"
	}
	state := a[0]
	for _, v := range a[1:] {
		if v != state {
			return "unknown"
		}
	}
	return state
}
func text(o map[string]json.RawMessage, key string) string {
	var s string
	if raw, ok := o[key]; ok && json.Unmarshal(raw, &s) == nil {
		return s
	}
	return ""
}
func intField(o map[string]json.RawMessage, key string) (int, bool) {
	raw, ok := o[key]
	if !ok {
		return 0, false
	}
	n, err := strconv.Atoi(string(raw))
	return n, err == nil && n >= 0
}

func knownNATMetadata(row map[string]json.RawMessage) bool {
	known := map[string]bool{}
	for _, key := range []string{"AliUid", "MemberUid", "ErrorDetail", "FirewallServiceMode", "FirewallServiceZones", "NatGatewayId", "NatGatewayName", "NatRouteEntryList", "ProxyId", "ProxyName", "ProxyNetworkInterfaceId", "ProxyRouteTableId", "ProxyStatus", "ProxyVSwitchId", "RegionId", "StrictMode", "VpcId", "VpcName"} {
		known[key] = true
	}
	for key := range row {
		if !known[key] {
			return false
		}
	}
	return true
}

func assetProtectionAssertion(scope, id string, rows []map[string]json.RawMessage, complete bool) *domain.Protection {
	p := &domain.Protection{Applicable: true, Required: true, State: "unknown", EvidenceRef: "cfw-protection-" + hash([]byte(scope + "\x00" + id))[:24]}
	if complete {
		states := []string{}
		for _, row := range rows {
			states = append(states, assetProtection(text(row, "ProtectStatus")))
		}
		p.State = aggregateProtection(states)
	}
	return p
}
