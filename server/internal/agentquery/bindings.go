package agentquery

import (
	"context"
	"encoding/json"
	"net/netip"
	"sort"
	"strconv"
	"strings"

	"cfw-policy-llm-agent/server/internal/domain"
)

// LocalBinding is server-only provenance. Field is a JSON Pointer into the
// frozen input/result; domain identity fields never enter model tool output.
type LocalBinding struct {
	Kind       string `json:"kind"`
	Document   string `json:"document"`
	Field      string `json:"field"`
	AssetRef   string `json:"asset_ref,omitempty"`
	ServiceRef string `json:"service_ref,omitempty"`
	Returned   bool   `json:"returned"`
}

func escapePointer(value string) string {
	return strings.ReplaceAll(strings.ReplaceAll(value, "~", "~0"), "/", "~1")
}
func assetPath(ai, si int) string {
	p := "/assets/" + strconv.Itoa(ai)
	if si >= 0 {
		p += "/services/" + strconv.Itoa(si)
	}
	return p
}
func (s *Service) addressPointers() map[string]string {
	out := map[string]string{}
	add := func(raw, path string) {
		if _, ok := out[raw]; !ok {
			out[raw] = path
		}
	}
	var accesses func([]domain.Access, string)
	accesses = func(values []domain.Access, base string) {
		for i, v := range values {
			for j, raw := range v.Addresses {
				add(raw, base+"/"+strconv.Itoa(i)+"/addresses/"+strconv.Itoa(j))
			}
		}
	}
	for ai, a := range s.input.Assets {
		base := assetPath(ai, -1)
		add(a.Address, base+"/address")
		for gi, group := range a.SecurityGroups {
			for pi, permission := range group.Permissions {
				path := base + "/security_groups/" + strconv.Itoa(gi) + "/permissions/" + strconv.Itoa(pi)
				if permission.Source != "" {
					add(permission.Source, path+"/source")
				}
				if permission.Destination != "" {
					add(permission.Destination, path+"/destination")
				}
			}
		}
		accesses(a.Allowed, base+"/allowed")
		for si, v := range a.Services {
			base := assetPath(ai, si)
			accesses(v.Allowed, base+"/allowed")
			accesses(v.Required, base+"/required")
		}
	}
	for pi, p := range s.input.Policies {
		base := "/policies/" + strconv.Itoa(pi)
		for j, raw := range p.Source.Values {
			add(raw, base+"/source/values/"+strconv.Itoa(j))
		}
		for j, raw := range p.Destination.Values {
			add(raw, base+"/destination/values/"+strconv.Itoa(j))
		}
	}
	keys := []string{}
	for key := range s.input.AddressRefs {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		for j, raw := range s.input.AddressRefs[key].Members {
			add(raw, "/address_refs/"+escapePointer(key)+"/members/"+strconv.Itoa(j))
		}
	}
	return out
}
func (s *Service) bindingField(alias string, r reference, addresses map[string]string) (string, string, bool) {
	switch r.kind {
	case "scope":
		return "input", "/scope", true
	case "owner":
		return "input", "/scope/account_ref", true
	case "control_plane":
		return "input", "/scope/control_plane_ref", true
	case "policy_set":
		return "input", "/scope/policy_set_ref", true
	case "gateway":
		return "input", "/scope/gateway_ref", true
	case "snapshot":
		return "input", "/snapshot_version", true
	case "profile":
		return "input", "/profile_version", true
	case "intent":
		return "input", "/intent_version", true
	case "asset":
		return "input", assetPath(r.asset, -1), true
	case "service":
		return "input", assetPath(r.asset, r.service), true
	case "label":
		return "input", r.field, r.field != ""
	case "security_group":
		return "input", r.key, true
	case "association":
		return "input", assetPath(r.asset, -1) + "/association", true
	case "business":
		return "input", assetPath(r.asset, r.service), true
	case "policy":
		return "input", "/policies/" + strconv.Itoa(r.index), true
	case "component":
		return "input", "/baseline", true
	case "address":
		path, ok := addresses[r.key]
		return "input", path, ok
	case "reference":
		base := "/address_refs/"
		if r.raw == "port" {
			base = "/port_refs/"
		}
		return "input", base + escapePointer(r.key), true
	case "access":
		state, ok := s.access[alias]
		return "input", assetPath(state.asset, state.service) + "/" + state.kind, ok
	case "finding":
		return "result", "/findings/" + strconv.Itoa(r.index), s.result != nil
	case "record":
		if r.key == s.input.Protection.EvidenceRef {
			return "input", "/protection/evidence_ref", true
		}
		for ai, a := range s.input.Assets {
			if a.Protection != nil && r.key == a.Protection.EvidenceRef {
				return "input", assetPath(ai, -1) + "/protection/evidence_ref", true
			}
			if r.key == a.Association.EvidenceRef {
				return "input", assetPath(ai, -1) + "/association/evidence_ref", true
			}
		}
	case "issue":
		if s.result != nil {
			for i, f := range s.result.Findings {
				if f.IssueKey == r.key {
					return "result", "/findings/" + strconv.Itoa(i) + "/issue_key", true
				}
			}
		}
	case "evidence":
		switch {
		case strings.HasPrefix(r.key, "input-pointer/"):
			return "input", strings.TrimPrefix(r.key, "input-pointer/"), true
		case r.key == "partition":
			return "input", "", true
		case strings.HasPrefix(r.key, "baseline/"):
			return "input", "/baseline", true
		case strings.HasPrefix(r.key, "asset/"):
			return "input", assetPath(r.asset, -1), true
		case strings.HasPrefix(r.key, "association/"):
			return "input", assetPath(r.asset, -1) + "/association", true
		case strings.HasPrefix(r.key, "business/"):
			return "input", assetPath(r.asset, r.service), true
		case strings.HasPrefix(r.key, "policy/"):
			return "input", "/policies/" + strings.TrimPrefix(r.key, "policy/"), true
		case strings.HasPrefix(r.key, "reference/address/"):
			return "input", "/address_refs/" + escapePointer(strings.TrimPrefix(r.key, "reference/address/")), true
		case strings.HasPrefix(r.key, "reference/port/"):
			return "input", "/port_refs/" + escapePointer(strings.TrimPrefix(r.key, "reference/port/")), true
		case strings.HasPrefix(r.key, "rule-evidence/") && s.result != nil:
			for i, e := range s.result.Evidence {
				if e.ID == strings.TrimPrefix(r.key, "rule-evidence/") {
					return "result", "/evidence/" + strconv.Itoa(i), true
				}
			}
		}
	}
	return "", "", false
}

// LocalBindings neither promotes pending references nor grants tool access.
func (s *Service) LocalBindings() map[string]LocalBinding {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]LocalBinding{}
	addresses := s.addressPointers()
	for alias := range s.issued {
		r := s.refs[alias]
		document, field, ok := s.bindingField(alias, r, addresses)
		if !ok {
			continue
		}
		binding, _, _, ok := s.canonicalBinding(r.kind, document, field)
		if !ok {
			continue
		}
		binding.Returned = s.returned[alias]
		out[alias] = binding
	}
	return out
}
func pointerParts(field string) ([]string, bool) {
	if field == "" {
		return []string{}, true
	}
	if !strings.HasPrefix(field, "/") {
		return nil, false
	}
	parts := strings.Split(field[1:], "/")
	for i, p := range parts {
		var out strings.Builder
		for j := 0; j < len(p); j++ {
			if p[j] != '~' {
				out.WriteByte(p[j])
				continue
			}
			j++
			if j >= len(p) || p[j] != '0' && p[j] != '1' {
				return nil, false
			}
			if p[j] == '0' {
				out.WriteByte('~')
			} else {
				out.WriteByte('/')
			}
		}
		parts[i] = out.String()
	}
	return parts, true
}
func pointerIndex(raw string, n int) (int, bool) {
	i, e := strconv.Atoi(raw)
	return i, e == nil && i >= 0 && i < n && strconv.Itoa(i) == raw
}
func (s *Service) canonicalBinding(kind, document, field string) (LocalBinding, reference, accessState, bool) {
	b := LocalBinding{Kind: kind, Document: document, Field: field}
	r := reference{kind: kind, asset: -1, service: -1}
	state := accessState{}
	parts, ok := pointerParts(field)
	if !ok {
		return b, r, state, false
	}
	valid := false
	ai, si := -1, -1
	if document == "result" && s.result != nil {
		if len(parts) >= 2 {
			index, exists := pointerIndex(parts[1], len(s.result.Findings))
			if parts[0] == "findings" && exists {
				f := s.result.Findings[index]
				r.index = index
				ai = s.assetIndex(f.SubjectRef)
				if ai >= 0 {
					for j, sv := range s.input.Assets[ai].Services {
						if sv.ID == f.ServiceRef {
							si = j
						}
					}
				}
				valid = kind == "finding" && len(parts) == 2 || kind == "issue" && len(parts) == 3 && parts[2] == "issue_key"
			}
			if parts[0] == "evidence" && kind == "evidence" && len(parts) == 2 {
				index, exists = pointerIndex(parts[1], len(s.result.Evidence))
				if exists {
					ai = s.assetIndex(s.result.Evidence[index].SubjectRef)
					valid = true
				}
			}
		}
	} else if document == "input" {
		global := map[string]string{"scope": "/scope", "owner": "/scope/account_ref", "control_plane": "/scope/control_plane_ref", "policy_set": "/scope/policy_set_ref", "gateway": "/scope/gateway_ref", "snapshot": "/snapshot_version", "profile": "/profile_version", "intent": "/intent_version", "component": "/baseline"}
		valid = global[kind] != "" && global[kind] == field && (kind != "gateway" || s.input.Scope.GatewayRef != "")
		if kind == "evidence" && (field == "" || field == "/baseline") {
			valid = true
		}
		if len(parts) >= 2 && parts[0] == "assets" {
			ai, ok = pointerIndex(parts[1], len(s.input.Assets))
			if !ok {
				return b, r, state, false
			}
			if len(parts) >= 4 && parts[2] == "services" {
				si, ok = pointerIndex(parts[3], len(s.input.Assets[ai].Services))
				if !ok {
					return b, r, state, false
				}
			}
			root := len(parts) == 2 || si >= 0 && len(parts) == 4
			if kind == "asset" {
				valid = len(parts) == 2
			}
			if kind == "service" {
				valid = si >= 0 && len(parts) == 4
			}
			if kind == "business" {
				valid = root
			}
			if kind == "association" {
				valid = len(parts) == 3 && parts[2] == "association"
			}
			if kind == "evidence" {
				valid = root || len(parts) == 3 && (parts[2] == "association" || parts[2] == "protection" && s.input.Assets[ai].Protection != nil) || s.metadataPointer(parts)
			}
			if kind == "label" {
				valid = s.metadataPointer(parts)
				r.field = field
			}
			if kind == "security_group" && len(parts) == 4 && parts[2] == "security_groups" {
				gi, exists := pointerIndex(parts[3], len(s.input.Assets[ai].SecurityGroups))
				valid = exists
				r.index = gi
				r.key = field
			}
			if kind == "access" {
				last := ""
				if len(parts) == 3 || si >= 0 && len(parts) == 5 {
					last = parts[len(parts)-1]
				}
				if last == "allowed" {
					a := s.input.Assets[ai]
					state = accessState{ai, -1, "allowed", a.AllowedConfirmed, a.AllowedComplete, a.DenyAll}
					if si >= 0 {
						v := a.Services[si]
						state = accessState{ai, si, "allowed", v.AllowedConfirmed, v.AllowedComplete, v.DenyAll}
					}
					valid = true
				} else if last == "required" && si >= 0 {
					v := s.input.Assets[ai].Services[si]
					state = accessState{ai, si, "required", v.RequiredConfirmed, true, false}
					valid = true
				}
			}
			if kind == "record" {
				valid = len(parts) == 4 && parts[3] == "evidence_ref" && (parts[2] == "association" && s.input.Assets[ai].Association.EvidenceRef != "" || parts[2] == "protection" && s.input.Assets[ai].Protection != nil && s.input.Assets[ai].Protection.EvidenceRef != "")
			}
		}
		if len(parts) == 2 && parts[0] == "policies" {
			index, exists := pointerIndex(parts[1], len(s.input.Policies))
			if exists && (kind == "policy" || kind == "evidence") {
				valid = true
				r.index = index
			}
		}
		if len(parts) == 2 && (kind == "reference" || kind == "evidence") {
			if parts[0] == "address_refs" {
				_, valid = s.input.AddressRefs[parts[1]]
				r.raw = "address"
				r.key = parts[1]
			} else if parts[0] == "port_refs" {
				_, valid = s.input.PortRefs[parts[1]]
				r.raw = "port"
				r.key = parts[1]
			}
		}
		if kind == "record" && field == "/protection/evidence_ref" {
			valid = s.input.Protection.EvidenceRef != ""
		}
		if kind == "address" {
			for raw, path := range s.addressPointers() {
				if path == field {
					if _, e := netip.ParsePrefix(raw); e == nil {
						valid = true
					} else if _, e = netip.ParseAddr(raw); e == nil {
						valid = true
					}
					r.raw = raw
					r.key = raw
					break
				}
			}
		}
	}
	if !valid {
		return b, r, state, false
	}
	r.asset, r.service = ai, si
	if kind == "evidence" && document == "input" {
		r.key = "input-pointer/" + field
	}
	if ai >= 0 {
		b.AssetRef = s.input.Assets[ai].ID
		if si >= 0 {
			b.ServiceRef = s.input.Assets[ai].Services[si].ID
		}
	}
	return b, r, state, true
}
func validBindingAlias(alias, kind string) (string, bool) {
	prefix := kind + "_"
	if len(alias) > 128 || !strings.HasPrefix(alias, prefix) {
		return "", false
	}
	tail := strings.TrimPrefix(alias, prefix)
	split := strings.LastIndexByte(tail, '_')
	if split != 16 {
		return "", false
	}
	nonce := tail[:split]
	for _, c := range nonce {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9') {
			return "", false
		}
	}
	n, e := strconv.Atoi(tail[split+1:])
	return nonce, e == nil && n > 0 && strconv.Itoa(n) == tail[split+1:]
}
func serviceFromBindings(input domain.Input, result *domain.Result, bindings map[string]LocalBinding) (*Service, error) {
	return serviceFromBindingsContext(context.Background(), input, result, bindings)
}
func serviceFromBindingsContext(ctx context.Context, input domain.Input, result *domain.Result, bindings map[string]LocalBinding) (*Service, error) {
	if len(bindings) == 0 || len(bindings) > 20000 {
		return nil, ErrFinal
	}
	s, e := NewWithContext(ctx, input, result)
	if e != nil {
		return nil, ErrFinal
	}
	s.refs = map[string]reference{}
	s.issued = map[string]bool{}
	s.returned = map[string]bool{}
	s.access = map[string]accessState{}
	s.topics = map[string]bool{}
	namespace := ""
	for alias, binding := range bindings {
		nonce, ok := validBindingAlias(alias, binding.Kind)
		if !ok || namespace != "" && nonce != namespace {
			return nil, ErrFinal
		}
		namespace = nonce
		canonical, r, state, ok := s.canonicalBinding(binding.Kind, binding.Document, binding.Field)
		supplied := binding
		supplied.Returned = false
		if !ok || canonical != supplied {
			return nil, ErrFinal
		}
		s.refs[alias] = r
		s.issued[alias] = true
		s.returned[alias] = binding.Returned
		if binding.Kind == "access" {
			s.access[alias] = state
		}
		if binding.Kind == "component" && binding.Returned {
			for _, topic := range componentTopics {
				s.topics[topic] = true
			}
		}
	}
	return s, nil
}

// ValidateLocalBindings is for trusted server-captured metadata, including failed
// runs. It validates every entry; it cannot recreate evidence of model calls.
func ValidateLocalBindings(input domain.Input, result *domain.Result, bindings map[string]LocalBinding) error {
	return ValidateLocalBindingsContext(context.Background(), input, result, bindings)
}
func ValidateLocalBindingsContext(ctx context.Context, input domain.Input, result *domain.Result, bindings map[string]LocalBinding) error {
	_, e := serviceFromBindingsContext(ctx, input, result, bindings)
	return e
}
func ValidateBoundOutput(input domain.Input, result *domain.Result, out Output, bindings map[string]LocalBinding) error {
	return ValidateBoundOutputContext(context.Background(), input, result, out, bindings)
}
func ValidateBoundOutputContext(ctx context.Context, input domain.Input, result *domain.Result, out Output, bindings map[string]LocalBinding) error {
	s, e := serviceFromBindingsContext(ctx, input, result, bindings)
	if e != nil {
		return e
	}
	scope, ok := bindings[out.ScopeRef]
	if !ok || scope.Kind != "scope" || scope.Document != "input" || scope.Field != "/scope" {
		return ErrFinal
	}
	s.initial.ScopeRef = out.ScopeRef
	raw, e := json.Marshal(out)
	if e != nil {
		return ErrFinal
	}
	if _, e = s.ValidateFinal(string(raw)); e != nil {
		return ErrFinal
	}
	return nil
}

// metadataPointer admits only the frozen metadata sections exposed by these tools.
func (s *Service) metadataPointer(parts []string) bool {
	if len(parts) < 3 || parts[0] != "assets" {
		return false
	}
	ai, ok := pointerIndex(parts[1], len(s.input.Assets))
	if !ok {
		return false
	}
	a := s.input.Assets[ai]
	if len(parts) == 3 {
		switch parts[2] {
		case "name":
			return a.Name != ""
		case "region":
			return a.Region != ""
		case "operating_system":
			return a.OperatingSystem != ""
		}
	}
	if len(parts) == 4 && parts[2] == "tags" {
		_, exists := a.Tags[parts[3]]
		return exists
	}
	if len(parts) < 4 || parts[2] != "security_groups" {
		return false
	}
	gi, ok := pointerIndex(parts[3], len(a.SecurityGroups))
	if !ok {
		return false
	}
	g := a.SecurityGroups[gi]
	if len(parts) == 4 {
		return true
	}
	if len(parts) == 5 && parts[4] == "region" {
		return g.Region != ""
	}
	if len(parts) == 6 && parts[4] == "limitations" {
		_, ok := pointerIndex(parts[5], len(g.Limitations))
		return ok
	}
	if len(parts) < 6 || parts[4] != "permissions" {
		return false
	}
	pi, ok := pointerIndex(parts[5], len(g.Permissions))
	if !ok {
		return false
	}
	if len(parts) == 6 {
		return true
	}
	if len(parts) == 7 && has([]string{"direction", "action", "protocol", "port_range", "priority", "source", "destination"}, parts[6]) {
		return true
	}
	if len(parts) == 8 && parts[6] == "conditions" {
		_, exists := g.Permissions[pi].Conditions[parts[7]]
		return exists
	}
	return false
}
