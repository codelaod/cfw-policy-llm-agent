package agentquery

import (
	"encoding/json"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"time"

	"cfw-policy-llm-agent/server/internal/domain"
)

const metadataItemLimit = 16

func (s *Service) metadataRef(path, raw string, ai int) string {
	return s.emit(s.ref("label", path, reference{key: path, field: path, raw: raw, asset: ai, service: -1}))
}
func (s *Service) metadataKeyRef(path, raw string, ai int) string {
	return s.emit(s.ref("label", path+"\x00key", reference{key: path + "\x00key", field: path, raw: raw, asset: ai, service: -1}))
}
func (s *Service) pointerFact(path string, ai int) string {
	return s.emit(s.ref("evidence", "input-pointer/"+path, reference{key: "input-pointer/" + path, asset: ai, service: -1}))
}
func (s *Service) securityGroupRef(ai, gi int) string {
	path := assetPath(ai, -1) + "/security_groups/" + strconv.Itoa(gi)
	return s.ref("security_group", path, reference{key: path, raw: s.input.Assets[ai].SecurityGroups[gi].ID, asset: ai, service: -1, index: gi})
}
func groupCollection(a domain.Asset) string {
	if a.SecurityGroupCollection == "complete" {
		for _, g := range a.SecurityGroups {
			if !g.Complete {
				return "partial"
			}
		}
		return "complete"
	}
	if a.SecurityGroupCollection == "partial" || len(a.SecurityGroups) > 0 {
		return "partial"
	}
	return "not_collected"
}
func (s *Service) assetMetadata(ai int) map[string]any {
	a := s.input.Assets[ai]
	base := assetPath(ai, -1)
	out := map[string]any{"complete": a.MetadataComplete, "resource_type": safeChoice(a.ResourceType, "ecs", "nat_gateway"), "operating_system": safeChoice(strings.ToLower(a.OperatingSystem), "linux", "windows"), "tags": []any{}, "tags_total": len(a.Tags), "tags_truncated": len(a.Tags) > metadataItemLimit, "evidence_ref": s.pointerFact(base, ai), "qualification": "云上元数据仅为线索，不能证明实际服务、业务归属或访问授权。"}
	if a.Name != "" {
		out["name_ref"] = s.metadataRef(base+"/name", a.Name, ai)
	}
	if a.ProviderResourceID != "" {
		out["provider_resource_ref"] = s.emit(s.assetRef(ai))
	}
	if a.Region != "" {
		out["region_ref"] = s.metadataRef(base+"/region", a.Region, ai)
	}
	if a.OperatingSystem != "" {
		out["operating_system_evidence_ref"] = s.pointerFact(base+"/operating_system", ai)
		if out["operating_system"] == "unknown" {
			out["operating_system_ref"] = s.metadataRef(base+"/operating_system", a.OperatingSystem, ai)
		}
	}
	keys := []string{}
	for key := range a.Tags {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	tags := []any{}
	for _, key := range keys {
		if len(tags) >= metadataItemLimit {
			break
		}
		path := base + "/tags/" + escapePointer(key)
		tags = append(tags, map[string]any{"key_ref": s.metadataKeyRef(path, key, ai), "value_ref": s.metadataRef(path, a.Tags[key], ai), "evidence_ref": s.pointerFact(path, ai), "trusted_business_mapping": false})
	}
	out["tags"] = tags
	return out
}
func (s *Service) groupRefs(ai int) ([]string, int) {
	refs := []string{}
	for gi := range s.input.Assets[ai].SecurityGroups {
		if len(refs) >= metadataItemLimit {
			break
		}
		refs = append(refs, s.emit(s.securityGroupRef(ai, gi)))
	}
	return refs, len(s.input.Assets[ai].SecurityGroups)
}
func (s *Service) securityGroups(arg string) (any, string, error) {
	r, ok := s.refs[arg]
	if !ok || !s.issued[arg] || (r.kind != "asset" && r.kind != "security_group") {
		return nil, "", ErrDenied
	}
	a := s.input.Assets[r.asset]
	collection := groupCollection(a)
	out := map[string]any{"asset_ref": s.emit(s.assetRef(r.asset)), "collection_status": collection, "groups": []any{}, "group_total": len(a.SecurityGroups), "groups_truncated": false, "permissions_truncated": false, "qualification": "安全组是独立配置层。允许端口、共享组和角色候选不证明监听、实际可达或业务授权。未知条件不视为 ANY。"}
	if collection == "not_collected" {
		out["reason_code"] = "snapshot_has_no_security_group_section"
		return out, "not_collected", nil
	}
	indices := []int{}
	if r.kind == "security_group" {
		indices = append(indices, r.index)
	} else {
		for gi := range a.SecurityGroups {
			indices = append(indices, gi)
		}
	}
	out["group_total"] = len(indices)
	groups := []any{}
	remaining := metadataItemLimit
	truncated := false
	for _, gi := range indices {
		if len(groups) >= metadataItemLimit {
			out["groups_truncated"] = true
			truncated = true
			break
		}
		g := a.SecurityGroups[gi]
		base := assetPath(r.asset, -1) + "/security_groups/" + strconv.Itoa(gi)
		group := map[string]any{"security_group_ref": s.emit(s.securityGroupRef(r.asset, gi)), "complete": g.Complete, "permissions": []any{}, "permission_total": len(g.Permissions), "permissions_truncated": len(g.Permissions) > remaining, "limitations": []any{}, "evidence_ref": s.pointerFact(base, r.asset)}
		if g.Region != "" {
			group["region_ref"] = s.metadataRef(base+"/region", g.Region, r.asset)
		}
		if observed, e := time.Parse(time.RFC3339Nano, g.ObservedAt); e == nil {
			group["observed_at"] = observed.UTC().Format(time.RFC3339Nano)
		} else {
			group["observed_at"] = "unknown"
		}
		limitations := []any{}
		for li, value := range g.Limitations {
			if li >= metadataItemLimit {
				group["limitations_truncated"] = true
				truncated = true
				break
			}
			limitations = append(limitations, map[string]any{"code": safeChoice(value, "SECURITY_GROUP_ENUMERATION_INCOMPLETE", "MULTI_NIC_ASSOCIATION_NOT_VERIFIED"), "detail_ref": s.metadataRef(base+"/limitations/"+strconv.Itoa(li), value, r.asset)})
		}
		group["limitations"] = limitations
		permissions := []any{}
		for pi, p := range g.Permissions {
			if remaining == 0 {
				truncated = true
				out["permissions_truncated"] = true
				break
			}
			if len(p.Conditions) > metadataItemLimit {
				truncated = true
				out["conditions_truncated"] = true
			}
			permissions = append(permissions, s.groupPermission(r.asset, base+"/permissions/"+strconv.Itoa(pi), p))
			remaining--
		}
		group["permissions"] = permissions
		groups = append(groups, group)
	}
	out["groups"] = groups
	out["evidence_ref"] = s.fact("asset/"+strconv.Itoa(r.asset), r.asset, -1)
	status := collection
	if truncated {
		status = "partial"
	}
	return out, status, nil
}
func (s *Service) groupPermission(ai int, base string, p domain.SecurityGroupPermission) map[string]any {
	direction := safeChoice(strings.ToLower(p.Direction), "ingress", "egress", "in", "out")
	action := safeChoice(strings.ToLower(p.Action), "accept", "drop")
	protocol := safeChoice(strings.ToLower(p.Protocol), "tcp", "udp", "icmp", "icmpv6", "gre", "all", "any")
	ports := map[string]any{"kind": "unknown"}
	parts := strings.Split(p.PortRange, "/")
	if len(parts) == 2 {
		from, e1 := strconv.Atoi(parts[0])
		to, e2 := strconv.Atoi(parts[1])
		if e1 == nil && e2 == nil {
			if from == -1 && to == -1 && has([]string{"all", "any"}, protocol) {
				ports["kind"] = "all"
			} else if from >= 1 && from <= to && to <= 65535 {
				ports = map[string]any{"kind": "range", "from": from, "to": to}
			}
		}
	}
	priority, e := strconv.Atoi(p.Priority)
	priorityKnown := e == nil && priority >= 1 && priority <= 2147483647
	out := map[string]any{"direction": direction, "action": action, "protocol": protocol, "ports": ports, "priority_known": priorityKnown, "unsupported_condition_count": len(p.Conditions), "conditions": []any{}, "conditions_truncated": len(p.Conditions) > metadataItemLimit, "evidence_ref": s.pointerFact(base, ai)}
	if priorityKnown {
		out["priority"] = priority
	}
	supported := direction != "unknown" && action != "unknown" && protocol != "unknown" && ports["kind"] != "unknown" && priorityKnown && len(p.Conditions) == 0
	for key, value := range map[string]string{"direction": p.Direction, "action": p.Action, "protocol": p.Protocol, "port_range": p.PortRange, "priority": p.Priority} {
		unknown := key == "direction" && direction == "unknown" || key == "action" && action == "unknown" || key == "protocol" && protocol == "unknown" || key == "port_range" && ports["kind"] == "unknown" || key == "priority" && !priorityKnown
		if value != "" && unknown {
			out[key+"_ref"] = s.metadataRef(base+"/"+key, value, ai)
		}
	}
	if p.Source != "" {
		out["source_status"] = "unknown"
		if _, e := netip.ParsePrefix(p.Source); e == nil {
			out["source_status"] = "literal"
		} else if _, e = netip.ParseAddr(p.Source); e == nil {
			out["source_status"] = "literal"
		} else {
			supported = false
		}
		out["source_ref"] = s.emit(s.ref("address", p.Source, reference{asset: ai, service: -1}))
	}
	if p.Destination != "" {
		out["destination_status"] = "unknown"
		if _, e := netip.ParsePrefix(p.Destination); e == nil {
			out["destination_status"] = "literal"
		} else if _, e = netip.ParseAddr(p.Destination); e == nil {
			out["destination_status"] = "literal"
		} else {
			supported = false
		}
		out["destination_ref"] = s.emit(s.ref("address", p.Destination, reference{asset: ai, service: -1}))
	}
	if p.Source == "" && p.Destination == "" {
		supported = false
	}
	keys := []string{}
	for key := range p.Conditions {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	conditions := []any{}
	for _, key := range keys {
		if len(conditions) >= metadataItemLimit {
			break
		}
		path := base + "/conditions/" + escapePointer(key)
		conditions = append(conditions, map[string]any{"key_ref": s.metadataKeyRef(path, key, ai), "value_ref": s.metadataRef(path, p.Conditions[key], ai), "supported": false, "evidence_ref": s.pointerFact(path, ai)})
	}
	out["conditions"] = conditions
	out["semantics_supported"] = supported
	return out
}

func (s *Service) securityGroupArgument(args string) (string, error) {
	var fields map[string]json.RawMessage
	if decode(args, &fields, MaxArgumentsBytes) != nil || len(fields) != 1 {
		return "", ErrInput
	}
	for key := range fields {
		if key != "asset_ref" && key != "security_group_ref" {
			return "", ErrInput
		}
		arg, err := oneArgument(args, key)
		if err != nil {
			return "", err
		}
		kind := "asset"
		if key == "security_group_ref" {
			kind = "security_group"
		}
		if _, err = s.use(arg, kind); err != nil {
			return "", err
		}
		return arg, nil
	}
	return "", ErrInput
}
