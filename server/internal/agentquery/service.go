package agentquery

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"io"
	"net/netip"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"cfw-policy-llm-agent/server/internal/audit"
	"cfw-policy-llm-agent/server/internal/domain"
)

type reference struct {
	kind, raw, field      string
	asset, service, index int
	key                   string
}
type accessState struct {
	asset, service               int
	kind                         string
	confirmed, complete, denyAll bool
}
type Service struct {
	mu           sync.Mutex
	input        domain.Input
	result       *domain.Result
	namespace    string
	refs         map[string]reference
	keys         map[string]string
	issued       map[string]bool
	pending      map[string]bool
	returned     map[string]bool
	emitting     map[string]bool
	access       map[string]accessState
	replacements map[string]string
	policyAssets map[int]map[int]bool
	topics       map[string]bool
	initial      InitialView
	initialJSON  string
	basis        string
	forbidden    map[string]bool
}

func New(input domain.Input, result *domain.Result) (*Service, error) {
	return NewWithContext(context.Background(), input, result)
}
func NewWithContext(ctx context.Context, input domain.Input, result *domain.Result) (*Service, error) {
	b, e := json.Marshal(input)
	if e != nil || len(b) > audit.MaxInputBytes {
		return nil, ErrInput
	}
	var in domain.Input
	if json.Unmarshal(b, &in) != nil || audit.ValidateContext(ctx, in) != nil || in.Baseline.Version != audit.ProjectBaseline().Version {
		return nil, ErrInput
	}
	basis := map[string]string{"synthetic": "synthetic_assumption", "imported": "imported_assumption", "live_readonly": "live_observed"}[in.SourceKind]
	if basis == "" {
		return nil, ErrInput
	}
	s := &Service{input: in, namespace: rand.Text()[:16], refs: map[string]reference{}, keys: map[string]string{}, issued: map[string]bool{}, pending: map[string]bool{}, returned: map[string]bool{}, access: map[string]accessState{}, replacements: map[string]string{}, policyAssets: map[int]map[int]bool{}, topics: map[string]bool{}, basis: basis}
	if result != nil {
		b, e = json.Marshal(result)
		if e != nil || len(b) > 5<<20 {
			return nil, ErrInput
		}
		var r domain.Result
		if json.Unmarshal(b, &r) != nil || !s.validResult(r) {
			return nil, ErrInput
		}
		s.result = &r
	}
	scope := s.ref("scope", in.Scope.Ref, reference{})
	s.ref("owner", in.Scope.AccountRef, reference{})
	s.ref("control_plane", in.Scope.ControlPlaneRef, reference{})
	s.ref("policy_set", in.Scope.PolicySetRef, reference{})
	if in.Scope.GatewayRef != "" {
		s.ref("gateway", in.Scope.GatewayRef, reference{})
	}
	// ValidateContext has checked both fixed timestamps; normalize their wire
	// spelling without consulting the wall clock or changing their instant.
	evaluation, _ := time.Parse(time.RFC3339Nano, in.EvaluationAt)
	collected, _ := time.Parse(time.RFC3339Nano, in.CollectedAt)
	s.initial = InitialView{EvaluationAt: evaluation.UTC().Format(time.RFC3339Nano), CollectedAt: collected.UTC().Format(time.RFC3339Nano), ObservationConsistency: safeChoice(in.Completeness.ObservationConsistency, "fixed_artifact", "observed_stable", "changed", "unverified", "unknown"), ScopeRef: scope, RuleVersion: in.Baseline.Version, SourceKind: in.SourceKind, AssessmentBasis: basis, Boundary: in.Scope.Boundary, Direction: in.Scope.Direction, SnapshotRef: s.ref("snapshot", in.SnapshotVersion, reference{}), ProfileRef: s.ref("profile", in.ProfileVersion, reference{}), IntentRef: s.ref("intent", in.IntentVersion, reference{}), Assets: []string{}, Findings: []FindingSummary{}, Qualification: "模型补充解释与待确认资料；原始规则判定保持不变。配置允许不证明实际监听或可达。"}
	for ai, a := range in.Assets {
		alias := s.ref("asset", a.ID, reference{asset: ai})
		s.initial.Assets = append(s.initial.Assets, alias)
		s.issued[alias] = true
		s.ref("address", a.Address, reference{asset: ai})
		for si, service := range a.Services {
			s.ref("service", a.ID+"/"+service.ID, reference{raw: service.ID, asset: ai, service: si})
		}
	}
	for pi, p := range in.Policies {
		s.ref("policy", p.ID, reference{index: pi})
		s.policyAssets[pi] = map[int]bool{}
		for ai, a := range in.Assets {
			set := p.Destination
			if in.Scope.Direction == "out" {
				set = p.Source
			}
			if containsEndpoint(in, set, a.Address) {
				s.policyAssets[pi][ai] = true
			}
		}
		if in.Selection.Kind == "gateway_policy" {
			s.policyAssets[pi][-1] = true
		}
		for _, v := range append(append([]string{}, p.Source.Values...), p.Destination.Values...) {
			s.ref("address", v, reference{})
		}
	}
	if s.result != nil {
		s.initial.FindingTotal = len(s.result.Findings)
		for i, f := range s.result.Findings {
			alias := s.ref("finding", f.ID, reference{index: i})
			if i < 32 {
				s.initial.Findings = append(s.initial.Findings, FindingSummary{alias, f.RuleID, f.Subcheck, f.Verdict})
				s.issued[alias] = true
			}
			ai := s.assetIndex(f.SubjectRef)
			for _, cell := range f.Differences {
				if pi := s.policyIndex(cell.PolicyRef); pi >= 0 {
					s.policyAssets[pi][ai] = true
				}
			}
			for _, eid := range f.EvidenceRefs {
				for _, e := range s.result.Evidence {
					if e.ID == eid {
						if pi := s.policyIndex(e.PolicyRef); pi >= 0 {
							s.policyAssets[pi][ai] = true
						}
					}
				}
			}
		}
		s.initial.FindingsTruncated = len(s.result.Findings) > len(s.initial.Findings)
	}
	s.primeProjection()
	s.issued[scope] = true
	for _, ref := range []string{s.initial.SnapshotRef, s.initial.ProfileRef, s.initial.IntentRef} {
		s.issued[ref] = true
	}
	out, e := json.Marshal(s.initial)
	if e != nil || len(out) > MaxOutputBytes {
		return nil, ErrTooLarge
	}
	s.initialJSON = string(out)
	return s, nil
}
func (s *Service) ref(kind, key string, r reference) string {
	k := kind + "\x00" + key
	if old := s.keys[k]; old != "" {
		return old
	}
	alias := kind + "_" + s.namespace + "_" + strconv.Itoa(len(s.refs)+1)
	r.kind = kind
	if r.key == "" {
		r.key = key
	}
	if r.raw == "" {
		r.raw = key
	}
	s.keys[k] = alias
	s.refs[alias] = r
	if key != "" && len(r.raw) >= 4 && kind != "reference" {
		if _, ok := s.replacements[r.raw]; !ok {
			s.replacements[r.raw] = alias
		}
	}
	return alias
}
func (s *Service) emit(alias string) string {
	if alias != "" {
		s.emitting[alias] = true
	}
	return alias
}
func (s *Service) fact(key string, ai, si int) string {
	return s.emit(s.ref("evidence", key, reference{asset: ai, service: si}))
}
func (s *Service) use(alias, kind string) (reference, error) {
	r, ok := s.refs[alias]
	if !ok || r.kind != kind || !s.issued[alias] {
		return r, ErrDenied
	}
	return r, nil
}
func (s *Service) InitialContext() string { return s.initialJSON }
func (s *Service) BeginTurn() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for ref := range s.pending {
		s.issued[ref] = true
		if s.refs[ref].kind == "component" {
			s.topics[s.refs[ref].key] = true
		}
	}
	s.pending = map[string]bool{}
}
func (s *Service) assetIndex(id string) int {
	for i, a := range s.input.Assets {
		if a.ID == id {
			return i
		}
	}
	return -1
}
func (s *Service) policyIndex(id string) int {
	for i, p := range s.input.Policies {
		if p.ID == id {
			return i
		}
	}
	return -1
}
func containsEndpoint(in domain.Input, set domain.AddressSet, raw string) bool {
	addr, e := netip.ParseAddr(raw)
	if e != nil {
		return false
	}
	values := append([]string{}, set.Values...)
	if set.Ref != "" {
		r, ok := in.AddressRefs[set.Ref]
		if !ok || r.ScopeRef != in.Scope.Ref {
			return false
		}
		values = append(values, r.Members...)
	}
	for _, v := range values {
		if a, e := netip.ParseAddr(v); e == nil && a == addr {
			return true
		}
		if p, e := netip.ParsePrefix(v); e == nil && p.Contains(addr) {
			return true
		}
	}
	return false
}
func decode(text string, out any, max int) error {
	if len(text) == 0 || len(text) > max || !strings.HasPrefix(strings.TrimSpace(text), "{") {
		return ErrInput
	}
	d := json.NewDecoder(strings.NewReader(text))
	d.DisallowUnknownFields()
	if d.Decode(out) != nil {
		return ErrInput
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return ErrInput
	}
	return nil
}
func oneArgument(args, key string) (string, error) {
	var m map[string]json.RawMessage
	if decode(args, &m, MaxArgumentsBytes) != nil || len(m) != 1 {
		return "", ErrInput
	}
	raw, ok := m[key]
	var value string
	if !ok || json.Unmarshal(raw, &value) != nil || value == "" || len(value) > 128 {
		return "", ErrInput
	}
	return value, nil
}
func (s *Service) Invoke(ctx context.Context, name, args string) (string, error) {
	if e := ctx.Err(); e != nil {
		return "", e
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := ctx.Err(); e != nil {
		return "", e
	}
	key := toolArgument[name]
	if key == "" {
		return "", ErrUnknownTool
	}
	arg, e := oneArgument(args, key)
	if name == "get_security_group_rules" {
		arg, e = s.securityGroupArgument(args)
	}
	if e != nil {
		return "", e
	}
	s.emitting = map[string]bool{}
	if _, ok := s.refs[arg]; ok {
		s.emit(arg)
	}
	defer func() { s.emitting = nil }()
	value, status, e := s.invoke(name, arg)
	if e != nil {
		return "", e
	}
	if e = ctx.Err(); e != nil {
		return "", e
	}
	evidence := []string{}
	for alias := range s.emitting {
		if s.refs[alias].kind == "evidence" {
			evidence = append(evidence, alias)
		}
	}
	sort.Strings(evidence)
	result := ToolResult{status, s.input.SourceKind, s.basis, evidence, value}
	b, e := json.Marshal(result)
	if e != nil || len(b) > MaxOutputBytes {
		return "", ErrTooLarge
	}
	for alias := range s.emitting {
		s.pending[alias] = true
		s.returned[alias] = true

	}
	return string(b), nil
}
func (s *Service) validResult(r domain.Result) bool {
	in := s.input
	m := domain.InputManifest{SnapshotVersion: in.SnapshotVersion, ProfileVersion: in.ProfileVersion, IntentVersion: in.IntentVersion, BaselineVersion: in.Baseline.Version, NormalizerVersion: in.NormalizerVersion, KernelVersion: in.KernelVersion, ProviderSemantics: in.Semantics, PolicyScope: in.Scope, EvaluationAt: in.EvaluationAt, CollectedAt: in.CollectedAt, SourceKind: in.SourceKind}
	if r.SchemaVersion != 1 || !reflect.DeepEqual(r.Manifest, m) || len(r.Findings) > 10000 || len(r.Evidence) > 50000 {
		return false
	}
	treeBytes, _ := json.Marshal(in)
	var tree any
	json.Unmarshal(treeBytes, &tree)
	evidence := map[string]domain.Evidence{}
	for _, e := range r.Evidence {
		if e.ID == "" || evidence[e.ID].ID != "" || e.ScopeRef != in.Scope.Ref || e.SnapshotVersion != in.SnapshotVersion || !pointerExists(tree, e.Field) {
			return false
		}
		if e.PolicyRef != "" && s.policyIndex(e.PolicyRef) < 0 {
			return false
		}
		evidence[e.ID] = e
	}
	seen := map[string]bool{}
	for _, f := range r.Findings {
		ai := s.assetIndex(f.SubjectRef)
		if f.ID == "" || seen[f.ID] || f.ScopeRef != in.Scope.Ref || f.RuleVersion != in.Baseline.Version || f.Origin != "rule" || ruleDefinitions[f.RuleID] == "" || (f.Subcheck != "" && (f.RuleID != "NAT-02" || ruleDefinitions[f.Subcheck] == "")) || !has(in.Baseline.EnabledChecks, f.RuleID) || !has([]string{"pass", "fail", "needs_confirmation", "insufficient_data", "unsupported", "not_applicable"}, f.Verdict) {
			return false
		}
		seen[f.ID] = true
		if ai < 0 && f.SubjectRef != in.Scope.PolicySetRef && f.SubjectRef != in.Scope.GatewayRef {
			return false
		}
		if f.ServiceRef != "" {
			if ai < 0 {
				return false
			}
			found := false
			for _, sv := range in.Assets[ai].Services {
				found = found || sv.ID == f.ServiceRef
			}
			if !found {
				return false
			}
		}
		for _, id := range f.EvidenceRefs {
			e, ok := evidence[id]
			if !ok || e.SubjectRef != f.SubjectRef {
				return false
			}
		}
		for _, cell := range f.Differences {
			if cell.PolicyRef != "" && s.policyIndex(cell.PolicyRef) < 0 {
				return false
			}
		}
	}
	return true
}
func pointerExists(value any, path string) bool {
	if path == "" {
		return true
	}
	if !strings.HasPrefix(path, "/") {
		return false
	}
	for _, p := range strings.Split(path[1:], "/") {
		p = strings.ReplaceAll(strings.ReplaceAll(p, "~1", "/"), "~0", "~")
		switch v := value.(type) {
		case map[string]any:
			var ok bool
			value, ok = v[p]
			if !ok {
				return false
			}
		case []any:
			n, e := strconv.Atoi(p)
			if e != nil || n < 0 || n >= len(v) {
				return false
			}
			value = v[n]
		default:
			return false
		}
	}
	return true
}
func has(values []string, v string) bool {
	for _, x := range values {
		if x == v {
			return true
		}
	}
	return false
}

func (s *Service) primeProjection() {
	s.forbidden = map[string]bool{}
	block := func(text string) {
		if len(text) >= 4 {
			s.forbidden[text] = true
		}
	}
	if g := s.input.GatewayMetadata; g != nil {
		if g.ResourceID != "" {
			s.replacements[g.ResourceID] = s.ref("gateway", s.input.Scope.GatewayRef, reference{})
		}
		if g.Name != "" && g.Name != g.ResourceID {
			s.ref("label", g.Name, reference{})
		}
		for _, value := range g.SourceNames {
			if value != "" && value != g.ResourceID {
				s.ref("label", value, reference{})
			}
		}
		for key, value := range g.Tags {
			block(key)
			if value != g.Name {
				block(value)
			}
		}
		block(g.OperatingSystem)
	}
	for ai, a := range s.input.Assets {
		if a.ProviderResourceID != "" {
			s.replacements[a.ProviderResourceID] = s.assetRef(ai)
		}
		if a.Name != "" && a.Name != a.ProviderResourceID && a.Name != a.ID {
			s.ref("label", a.Name, reference{asset: ai})
		}
		for key, value := range a.Tags {
			block(key)
			if value != a.Name {
				block(value)
			}
		}
		if !has([]string{"linux", "windows"}, strings.ToLower(a.OperatingSystem)) {
			block(a.OperatingSystem)
		}
		for gi, group := range a.SecurityGroups {
			s.securityGroupRef(ai, gi)
			for _, limitation := range group.Limitations {
				block(limitation)
			}
			for _, permission := range group.Permissions {
				if permission.Source != "" {
					s.ref("address", permission.Source, reference{asset: ai})
				}
				if permission.Destination != "" {
					s.ref("address", permission.Destination, reference{asset: ai})
				}
				for key, value := range permission.Conditions {
					block(key)
					block(value)
					var native any
					if json.Unmarshal([]byte(value), &native) == nil {
						collectStrings(native, block)
					}
				}
			}
		}
	}
	for _, gap := range s.input.Completeness.KnownGaps {
		block(gap)
	}
	if !has([]string{"fixed_artifact", "observed_stable", "changed", "unverified", "unknown"}, s.input.Completeness.ObservationConsistency) {
		block(s.input.Completeness.ObservationConsistency)
	}
	for _, exception := range s.input.Baseline.Exceptions {
		block(exception.Reason)
	}
	for _, p := range s.input.Policies {
		for key, raw := range p.Conditions {
			block(key)
			var value any
			if json.Unmarshal(raw, &value) == nil {
				collectStrings(value, block)
			}
		}
	}
	if s.input.Protection.EvidenceRef != "" {
		s.ref("record", s.input.Protection.EvidenceRef, reference{})
	}
	for ai, a := range s.input.Assets {
		for _, v := range a.Allowed {
			for _, raw := range v.Addresses {
				s.ref("address", raw, reference{})
			}
		}
		for _, service := range a.Services {
			if safeChoice(service.Role, "web", "database", "management", "middleware", "cache", "queue", "background", "unknown") == "unknown" && service.Role != "unknown" {
				block(service.Role)
			}
			for _, list := range [][]domain.Access{service.Allowed, service.Required} {
				for _, v := range list {
					for _, raw := range v.Addresses {
						s.ref("address", raw, reference{})
					}
				}
			}
		}
		if a.Protection != nil && a.Protection.EvidenceRef != "" {
			s.ref("record", a.Protection.EvidenceRef, reference{asset: ai})
		}
		if a.Association.EvidenceRef != "" {
			s.ref("record", a.Association.EvidenceRef, reference{asset: ai})
		}
		if a.Association.GatewayRef != "" && a.Association.GatewayRef != s.input.Scope.GatewayRef {
			block(a.Association.GatewayRef)
		}
	}
	for key, value := range s.input.AddressRefs {
		alias := s.ref("reference", "address/"+key, reference{raw: "address", key: key})
		s.replacements[key] = alias
		for _, raw := range value.Members {
			s.ref("address", raw, reference{})
		}
	}
	for key := range s.input.PortRefs {
		alias := s.ref("reference", "port/"+key, reference{raw: "port", key: key})
		s.replacements[key] = alias
	}
	if s.result != nil {
		for _, evidence := range s.result.Evidence {
			s.ref("evidence", "rule-evidence/"+evidence.ID, reference{raw: evidence.ID, asset: s.assetIndex(evidence.SubjectRef), service: -1})
			block(evidence.Summary)
		}
		for _, finding := range s.result.Findings {
			s.ref("issue", finding.IssueKey, reference{})
			block(finding.Summary)
		}
	}
}
func collectStrings(value any, add func(string)) {
	switch v := value.(type) {
	case string:
		add(v)
	case []any:
		for _, x := range v {
			collectStrings(x, add)
		}
	case map[string]any:
		for k, x := range v {
			add(k)
			collectStrings(x, add)
		}
	}
}
