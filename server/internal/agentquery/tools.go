package agentquery

import (
	"strconv"

	"cfw-policy-llm-agent/server/internal/domain"
)

func safeChoice(raw string, allowed ...string) string {
	if has(allowed, raw) {
		return raw
	}
	return "unknown"
}
func (s *Service) assetRef(ai int) string {
	return s.ref("asset", s.input.Assets[ai].ID, reference{asset: ai})
}
func (s *Service) serviceRef(ai, si int) string {
	a := s.input.Assets[ai]
	return s.ref("service", a.ID+"/"+a.Services[si].ID, reference{raw: a.Services[si].ID, asset: ai, service: si})
}
func (s *Service) policyRef(pi int) string {
	return s.ref("policy", s.input.Policies[pi].ID, reference{index: pi})
}
func (s *Service) businessRef(ai, si int) string {
	return s.ref("business", strconv.Itoa(ai)+"/"+strconv.Itoa(si), reference{asset: ai, service: si})
}
func (s *Service) policiesFor(ai int) ([]string, int) {
	out := []string{}
	total := 0
	for pi := range s.input.Policies {
		allowed := ai < 0 && len(s.policyAssets[pi]) > 0 || s.policyAssets[pi][ai]
		if allowed {
			total++
			if len(out) < 32 {
				out = append(out, s.emit(s.policyRef(pi)))
			}
		}
	}
	return out, total
}
func (s *Service) invoke(name, arg string) (any, string, error) {
	switch name {
	case "get_partition_context":
		if _, e := s.use(arg, "scope"); e != nil {
			return nil, "", e
		}
		policies, total := s.policiesFor(-1)
		return map[string]any{"scope_ref": arg, "evaluation_at": s.initial.EvaluationAt, "collected_at": s.initial.CollectedAt, "observation_consistency": s.initial.ObservationConsistency, "boundary": s.input.Scope.Boundary, "direction": s.input.Scope.Direction, "owner_ref": s.emit(s.ref("owner", s.input.Scope.AccountRef, reference{})), "policy_set_ref": s.emit(s.ref("policy_set", s.input.Scope.PolicySetRef, reference{})), "ip_family": "ipv4", "default_action": s.input.Semantics.DefaultAction, "protection_state": safeChoice(s.input.Protection.State, "on", "off", "unknown"), "protection_applicable": s.input.Protection.Applicable, "protection_required": s.input.Protection.Required, "completeness_declared": s.input.Completeness.Declared, "enumeration_complete": s.input.Completeness.EnumerationComplete, "known_gap_count": len(s.input.Completeness.KnownGaps), "policy_refs": policies, "policy_total": total, "policies_truncated": total > len(policies), "evidence_ref": s.fact("partition", -1, -1)}, "available", nil
	case "get_asset_context":
		r, e := s.use(arg, "asset")
		if e != nil {
			return nil, "", e
		}
		a := s.input.Assets[r.asset]
		services := []any{}
		for si, v := range a.Services {
			services = append(services, map[string]any{"service_ref": s.emit(s.serviceRef(r.asset, si)), "role": safeChoice(v.Role, "web", "database", "management", "middleware", "cache", "queue", "background", "unknown"), "role_status": safeChoice(v.RoleStatus, "confirmed", "candidate", "unknown"), "protocol": v.Protocol, "ports": v.Ports, "business_ref": s.emit(s.businessRef(r.asset, si))})
		}
		policies, total := s.policiesFor(r.asset)
		out := map[string]any{"asset_ref": arg, "address_ref": s.emit(s.ref("address", a.Address, reference{asset: r.asset})), "address_observed": a.AddressObserved, "services": services, "business_ref": s.emit(s.businessRef(r.asset, -1)), "policy_refs": policies, "policy_total": total, "policies_truncated": total > len(policies), "security_group_status": groupCollection(a), "metadata": s.assetMetadata(r.asset), "evidence_ref": s.fact("asset/"+strconv.Itoa(r.asset), r.asset, -1)}
		protection := s.input.ProtectionFor(a.ID)
		out["protection_state"] = safeChoice(protection.State, "on", "off", "transitioning", "unknown")
		out["protection_applicable"] = protection.Applicable
		out["protection_required"] = protection.Required
		if a.Protection != nil && s.input.Scope.Boundary == "internet" {
			out["protection_evidence_ref"] = s.pointerFact(assetPath(r.asset, -1)+"/protection", r.asset)
		}
		groups, groupTotal := s.groupRefs(r.asset)
		out["security_group_refs"] = groups
		out["security_group_total"] = groupTotal
		out["security_groups_truncated"] = groupTotal > len(groups)
		if s.input.Scope.Boundary == "nat" {
			out["association_ref"] = s.emit(s.ref("association", a.ID, reference{asset: r.asset}))
		}
		return out, "available", nil
	case "get_nat_association":
		r, e := s.use(arg, "association")
		if e != nil || s.input.Scope.Boundary != "nat" {
			return nil, "", ErrDenied
		}
		a := s.input.Assets[r.asset]
		association := a.Association
		valid := association.Status == "evidenced" && association.GatewayRef == s.input.Scope.GatewayRef && association.EvidenceRef != "" && association.ObservedAt != "" && has([]string{"before_translation", "after_translation"}, association.ObservationPoint)
		status := "unknown"
		point := "unknown"
		if valid {
			status = "evidenced"
			point = association.ObservationPoint
		}
		return map[string]any{"association_ref": arg, "asset_ref": s.emit(s.assetRef(r.asset)), "gateway_ref": s.emit(s.ref("gateway", s.input.Scope.GatewayRef, reference{})), "status": status, "observation_point": point, "can_claim_actual_path": false, "evidence_ref": s.fact("association/"+strconv.Itoa(r.asset), r.asset, -1)}, status, nil
	case "get_security_group_rules":
		return s.securityGroups(arg)
	case "get_business_intent":
		r, e := s.use(arg, "business")
		if e != nil {
			return nil, "", e
		}
		a := s.input.Assets[r.asset]
		out := map[string]any{"business_ref": arg, "asset_ref": s.emit(s.assetRef(r.asset)), "evidence_ref": s.fact("business/"+r.key, r.asset, r.service)}
		if r.service < 0 {
			out["allowed"] = s.accessView(r.asset, -1, "allowed", a.Allowed, a.AllowedConfirmed, a.AllowedComplete, a.DenyAll)
		} else {
			v := a.Services[r.service]
			out["service_ref"] = s.emit(s.serviceRef(r.asset, r.service))
			out["public_access"] = safeChoice(v.PublicAccess, "required", "not_required", "unknown")
			out["allowed"] = s.accessView(r.asset, r.service, "allowed", v.Allowed, v.AllowedConfirmed, v.AllowedComplete, v.DenyAll)
			out["required"] = s.accessView(r.asset, r.service, "required", v.Required, v.RequiredConfirmed, true, false)
		}
		return out, "available", nil
	case "get_policy":
		r, e := s.use(arg, "policy")
		if e != nil || len(s.policyAssets[r.index]) == 0 {
			return nil, "", ErrDenied
		}
		p := s.input.Policies[r.index]
		return map[string]any{"policy_ref": arg, "scope_ref": s.initial.ScopeRef, "source": s.addressSet(p.Source), "destination": s.addressSet(p.Destination), "protocol": safeChoice(p.Protocol, "tcp", "udp", "any"), "ports": s.portSet(p.Ports), "action": safeChoice(p.Action, "accept", "drop", "unknown"), "priority": p.Priority, "order_known": p.OrderKnown, "enabled": safeChoice(p.Enabled, "enabled", "disabled", "unknown"), "unsupported_condition_count": len(p.Conditions), "evidence_ref": s.fact("policy/"+strconv.Itoa(r.index), -1, -1)}, "available", nil
	case "resolve_reference":
		r, e := s.use(arg, "reference")
		if e != nil {
			return nil, "", e
		}
		out := map[string]any{"reference_ref": arg}
		if r.raw == "address" {
			v, ok := s.input.AddressRefs[r.key]
			if !ok || v.ScopeRef != s.input.Scope.Ref {
				return nil, "", ErrDenied
			}
			members := []string{}
			for _, v := range v.Members {
				members = append(members, s.emit(s.ref("address", v, reference{})))
			}
			out["member_refs"] = members
			out["complete"] = v.Complete
			out["kind"] = "address"
		} else {
			v, ok := s.input.PortRefs[r.key]
			if !ok || v.ScopeRef != s.input.Scope.Ref {
				return nil, "", ErrDenied
			}
			out["members"] = v.Members
			out["complete"] = v.Complete
			out["kind"] = "port"
		}
		out["evidence_ref"] = s.fact("reference/"+r.raw+"/"+r.key, -1, -1)
		return out, "available", nil
	case "get_finding_evidence":
		r, e := s.use(arg, "finding")
		if e != nil || s.result == nil {
			return nil, "", ErrDenied
		}
		f := s.result.Findings[r.index]
		ai := s.assetIndex(f.SubjectRef)
		policies := []string{}
		seen := map[int]bool{}
		for _, c := range f.Differences {
			if pi := s.policyIndex(c.PolicyRef); pi >= 0 && !seen[pi] {
				seen[pi] = true
				policies = append(policies, s.emit(s.policyRef(pi)))
			}
		}
		evidences := []string{}
		for _, id := range f.EvidenceRefs {
			for _, value := range s.result.Evidence {
				if value.ID == id {
					evidences = append(evidences, s.fact("rule-evidence/"+id, ai, -1))
					if pi := s.policyIndex(value.PolicyRef); pi >= 0 && !seen[pi] {
						seen[pi] = true
						policies = append(policies, s.emit(s.policyRef(pi)))
					}
				}
			}
		}
		out := map[string]any{"finding_ref": arg, "rule_id": f.RuleID, "subcheck": f.Subcheck, "verdict": f.Verdict, "assessment_basis": s.basis, "original_rule_unchanged": true, "evidence_refs": evidences, "policy_refs": policies, "accepted_exception": f.AcceptedException}
		if ai >= 0 {
			out["asset_ref"] = s.emit(s.assetRef(ai))
			out["business_ref"] = s.emit(s.businessRef(ai, -1))
			for si, sv := range s.input.Assets[ai].Services {
				if sv.ID == f.ServiceRef {
					out["service_ref"] = s.emit(s.serviceRef(ai, si))
					out["business_ref"] = s.emit(s.businessRef(ai, si))
				}
			}
		}
		return out, "available", nil
	case "get_rule_definition":
		if ruleDefinitions[arg] == "" || !has(s.input.Baseline.EnabledChecks, arg) {
			return nil, "", ErrDenied
		}
		topics := []any{}
		for _, topic := range componentTopics {
			topics = append(topics, map[string]string{"topic": topic, "topic_ref": s.emit(s.ref("component", topic, reference{}))})
		}
		return map[string]any{"rule_id": arg, "rule_version": s.input.Baseline.Version, "definition": ruleDefinitions[arg], "component_topics": topics, "evidence_ref": s.fact("baseline/"+arg, -1, -1), "m1_enabled": false}, "available", nil
	}
	return nil, "", ErrUnknownTool
}
func (s *Service) addressSet(set domain.AddressSet) map[string]any {
	values := []string{}
	for _, v := range set.Values {
		values = append(values, s.emit(s.ref("address", v, reference{})))
	}
	out := map[string]any{"value_refs": values}
	if set.Ref != "" {
		out["reference_ref"] = s.emit(s.ref("reference", "address/"+set.Ref, reference{raw: "address", key: set.Ref}))
	}
	return out
}
func (s *Service) portSet(set domain.PortSet) map[string]any {
	out := map[string]any{"values": set.Values}
	if set.Ref != "" {
		out["reference_ref"] = s.emit(s.ref("reference", "port/"+set.Ref, reference{raw: "port", key: set.Ref}))
	}
	return out
}
func (s *Service) accessView(ai, si int, kind string, values []domain.Access, confirmed, complete, deny bool) map[string]any {
	key := strconv.Itoa(ai) + "/" + strconv.Itoa(si) + "/" + kind
	alias := s.ref("access", key, reference{asset: ai, service: si})
	s.access[alias] = accessState{ai, si, kind, confirmed, complete, deny}
	entries := []any{}
	for _, value := range values {
		members := []string{}
		for _, raw := range value.Addresses {
			members = append(members, s.emit(s.ref("address", raw, reference{})))
		}
		entries = append(entries, map[string]any{"protocol": value.Protocol, "address_refs": members, "ports": value.Ports, "applicability": value.Applicability})
	}
	return map[string]any{"access_ref": s.emit(alias), "confirmed": confirmed, "complete": complete, "deny_all": deny, "entries": entries}
}
