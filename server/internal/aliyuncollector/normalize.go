package aliyuncollector

import (
	"bytes"
	"encoding/json"
	"net/netip"
	"sort"
	"strconv"
	"strings"

	"cfw-policy-llm-agent/server/internal/domain"
)

type bookRequest struct {
	key, name, kind string
	inline          []string
	inlinePresent   bool
}

func rawValue(v any) json.RawMessage { b, _ := json.Marshal(v); return b }
func mark(p *domain.Policy, key string, raw json.RawMessage) {
	if len(raw) == 0 {
		raw = rawValue("missing")
	}
	p.Conditions[key] = append(json.RawMessage(nil), raw...)
}
func addresses(value string) ([]string, bool) {
	if value == "" {
		return nil, false
	}
	out := []string{}
	for _, s := range strings.Split(value, ",") {
		s = strings.TrimSpace(s)
		if ip, e := netip.ParseAddr(s); e == nil && ip.Is4() {
			out = append(out, ip.String())
			continue
		}
		if p, e := netip.ParsePrefix(s); e == nil && p.Addr().Is4() {
			out = append(out, p.Masked().String())
			continue
		}
		return nil, false
	}
	sort.Strings(out)
	return unique(out), len(out) > 0
}
func ports(value string) ([]domain.PortRange, bool) {
	if value == "" {
		return nil, false
	}
	out := []domain.PortRange{}
	for _, part := range strings.Split(value, ",") {
		part = strings.TrimSpace(part)
		parts := strings.Split(part, "/")
		if len(parts) > 2 || len(parts) == 0 {
			return nil, false
		}
		from, e := strconv.Atoi(parts[0])
		if e != nil {
			return nil, false
		}
		to := from
		if len(parts) == 2 {
			to, e = strconv.Atoi(parts[1])
			if e != nil {
				return nil, false
			}
		}
		if from == 0 && to == 0 && len(parts) == 2 {
			// Cloud Firewall documents 0/0 as all ports (Manage address books;
			// Access control policy examples). Project only into the kernel's
			// supported port domain, 1..65535; other zero ranges stay unknown.
			from, to = 1, 65535
		}
		if from < 1 || to < from || to > 65535 {
			return nil, false
		}
		out = append(out, domain.PortRange{From: from, To: to})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].From == out[j].From {
			return out[i].To < out[j].To
		}
		return out[i].From < out[j].From
	})
	return out, len(out) > 0
}
func stringList(o map[string]json.RawMessage, key string) ([]string, bool) {
	raw, ok := o[key]
	if !ok || bytes.Equal(raw, []byte("null")) {
		return nil, false
	}
	var values []string
	if json.Unmarshal(raw, &values) != nil || values == nil {
		return nil, false
	}
	return values, true
}
func bookKey(scope, name, kind string) string {
	return "book-" + hash([]byte(scope + "\x00" + kind + "\x00" + name))[:32]
}
func normalizePolicy(scope domain.Scope, gatewayID string, row map[string]json.RawMessage) (domain.Policy, []bookRequest, error) {
	id := text(row, "AclUuid")
	if !alias(id) {
		return domain.Policy{}, nil, Failure("PROVIDER_RESPONSE_INVALID")
	}
	if scope.Boundary == "nat" && text(row, "NatGatewayId") != gatewayID {
		return domain.Policy{}, nil, Failure("COLLECTOR_SCOPE_DENIED")
	}
	if direction := text(row, "Direction"); direction != "" && direction != scope.Direction {
		return domain.Policy{}, nil, Failure("COLLECTOR_SCOPE_DENIED")
	}
	p := domain.Policy{ID: id, ScopeRef: scope.Ref, Protocol: strings.ToLower(text(row, "Proto")), Action: text(row, "AclAction"), Enabled: "unknown", Conditions: map[string]json.RawMessage{}}
	if priority, ok := intField(row, "Order"); ok && priority <= 1000000 {
		p.Priority = priority
		p.OrderKnown = true
	} else {
		mark(&p, "Order", row["Order"])
	}
	switch text(row, "Release") {
	case "true":
		p.Enabled = "enabled"
	case "false":
		p.Enabled = "disabled"
	default:
		mark(&p, "Release", row["Release"])
	}
	if scope.Boundary == "internet" && text(row, "Direction") == "" {
		mark(&p, "Direction", row["Direction"])
	}
	refs := []bookRequest{}
	for _, field := range []string{"Source", "Destination"} {
		set := domain.AddressSet{}
		switch text(row, field+"Type") {
		case "net":
			if values, ok := addresses(text(row, field)); ok {
				set.Values = values
			} else {
				mark(&p, field, row[field])
			}
		case "group":
			name := text(row, field)
			kind := text(row, field+"GroupType")
			if alias(name) && kind == "ip" {
				key := bookKey(scope.Ref, name, "ip")
				inline, present := stringList(row, field+"GroupCidrs")
				refs = append(refs, bookRequest{key: key, name: name, kind: "ip", inline: inline, inlinePresent: present})
				set.Ref = key
			} else {
				mark(&p, field+"GroupType", row[field+"GroupType"])
				mark(&p, field, row[field])
			}
		default:
			mark(&p, field+"Type", row[field+"Type"])
			mark(&p, field, row[field])
		}
		if field == "Source" {
			p.Source = set
		} else {
			p.Destination = set
		}
	}
	switch text(row, "DestPortType") {
	case "port":
		if values, ok := ports(text(row, "DestPort")); ok {
			p.Ports.Values = values
		} else {
			mark(&p, "DestPort", row["DestPort"])
		}
	case "group":
		name := text(row, "DestPortGroup")
		if alias(name) {
			key := bookKey(scope.Ref, name, "port")
			inline, present := stringList(row, "DestPortGroupPorts")
			refs = append(refs, bookRequest{key: key, name: name, kind: "port", inline: inline, inlinePresent: present})
			p.Ports.Ref = key
		} else {
			mark(&p, "DestPortGroup", row["DestPortGroup"])
		}
	default:
		mark(&p, "DestPortType", row["DestPortType"])
	}
	application := false
	names, currentPresent := stringList(row, "ApplicationNameList")
	if currentPresent {
		application = len(names) == 1 && names[0] == "ANY"
		if !application {
			mark(&p, "ApplicationNameList", row["ApplicationNameList"])
		}
	} else if _, rawPresent := row["ApplicationNameList"]; rawPresent {
		mark(&p, "ApplicationNameList", row["ApplicationNameList"])
	} else if text(row, "ApplicationName") == "ANY" {
		application = true
	}
	if name := text(row, "ApplicationName"); name != "" && name != "ANY" {
		mark(&p, "ApplicationName", row["ApplicationName"])
	}
	if !application {
		mark(&p, "ApplicationNameList", row["ApplicationNameList"])
	}
	if text(row, "ApplicationId") != "" {
		mark(&p, "ApplicationId", row["ApplicationId"])
	}
	if text(row, "RepeatType") != "Permanent" {
		mark(&p, "RepeatType", row["RepeatType"])
	}
	structural := map[string]bool{}
	for _, key := range []string{"AclUuid", "AclAction", "Proto", "Order", "Release", "NatGatewayId", "Direction", "Source", "SourceType", "SourceGroupType", "SourceGroupCidrs", "Destination", "DestinationType", "DestinationGroupType", "DestinationGroupCidrs", "DestPortType", "DestPort", "DestPortGroup", "DestPortGroupPorts", "ApplicationName", "ApplicationNameList", "ApplicationId", "RepeatType", "Description", "CreateTime", "ModifyTime", "HitTimes", "HitLastTime", "SpreadCnt"} {
		structural[key] = true
	}
	for key, raw := range row {
		if structural[key] {
			continue
		}
		if key == "IpVersion" {
			if string(raw) != "4" {
				mark(&p, key, raw)
			}
			continue
		}
		switch key {
		case "StartTime", "EndTime", "DnsResultTime", "DomainResolveType":
			if bytes.Equal(raw, []byte("0")) || bytes.Equal(raw, []byte(`"0"`)) || bytes.Equal(raw, []byte("null")) {
				continue
			}
		case "RepeatDays":
			if bytes.Equal(raw, []byte("[]")) {
				continue
			}
		case "RepeatStartTime", "RepeatEndTime", "DnsResult":
			if bytes.Equal(raw, []byte(`""`)) {
				continue
			}
		}
		mark(&p, key, raw)
	}
	if len(p.Conditions) > 24 {
		raw, _ := json.Marshal(p.Conditions)
		if len(raw) > 32768 {
			return domain.Policy{}, nil, Failure("PROVIDER_RESPONSE_INVALID")
		}
		p.Conditions = map[string]json.RawMessage{"provider_conditions": raw}
	}
	for _, raw := range p.Conditions {
		if len(raw) > 32768 {
			return domain.Policy{}, nil, Failure("PROVIDER_RESPONSE_INVALID")
		}
	}
	return p, refs, nil
}

func (r *run) resolveBook(round int, request bookRequest) (domain.AddressReference, domain.PortReference, error) {
	scope := r.collector.cfg.Scope.Ref
	a := domain.AddressReference{ScopeRef: scope, Members: []string{}}
	p := domain.PortReference{ScopeRef: scope, Members: []domain.PortRange{}}
	rows, complete, err := r.pages(round, "DescribeAddressBook", map[string]string{"Query": request.name, "GroupType": request.kind}, "Acls", "CurrentPage", true, "GroupUuid", 2000)
	if err != nil {
		return a, p, err
	}
	exact := []map[string]json.RawMessage{}
	for _, row := range rows {
		if text(row, "GroupName") == request.name && text(row, "GroupType") == request.kind {
			exact = append(exact, row)
		}
	}
	if !complete || len(exact) != 1 {
		return a, p, nil
	}
	row := exact[0]
	dynamic := false
	for _, key := range []string{"AckClusterConnectorId", "AckClusterConnectorName", "AckLabels", "AckNamespaces", "AssetMemberUids", "AssetRegionResourceTypes", "AutoAddTagEcs", "TagList", "TagRelation", "RegionNo"} {
		if raw, ok := row[key]; ok && !bytes.Equal(raw, []byte("null")) && !bytes.Equal(raw, []byte("0")) && !bytes.Equal(raw, []byte("[]")) && !bytes.Equal(raw, []byte(`""`)) {
			dynamic = true
		}
	}
	known := map[string]bool{}
	for _, key := range []string{"GroupName", "GroupType", "GroupUuid", "AddressList", "AddressListCount", "Addresses", "ReferenceCount", "Description", "AckClusterConnectorId", "AckClusterConnectorName", "AckLabels", "AckNamespaces", "AssetMemberUids", "AssetRegionResourceTypes", "AutoAddTagEcs", "TagList", "TagRelation", "RegionNo"} {
		known[key] = true
	}
	for key := range row {
		if !known[key] {
			dynamic = true
		}
	}
	values, present := stringList(row, "AddressList")
	count, knownCount := intField(row, "AddressListCount")
	if dynamic || !present || !knownCount || count != len(values) || !bookRepresentationsAgree(row, values, request.kind) {
		return a, p, nil
	}
	if request.kind == "ip" {
		for _, v := range values {
			parsed, ok := addresses(v)
			if !ok {
				return a, p, nil
			}
			a.Members = append(a.Members, parsed...)
		}
		a.Members = unique(a.Members)
		a.Complete = true
	} else {
		for _, v := range values {
			parsed, ok := ports(v)
			if !ok {
				return a, p, nil
			}
			p.Members = append(p.Members, parsed...)
		}
		p.Complete = true
	}
	if request.inlinePresent {
		if request.kind == "ip" {
			inline := []string{}
			for _, v := range request.inline {
				parsed, ok := addresses(v)
				if !ok {
					a.Complete = false
					a.Members = nil
					return a, p, nil
				}
				inline = append(inline, parsed...)
			}
			if strings.Join(unique(inline), "\x00") != strings.Join(a.Members, "\x00") {
				a.Complete = false
				a.Members = nil
			}
		} else {
			inline := []domain.PortRange{}
			for _, v := range request.inline {
				parsed, ok := ports(v)
				if !ok {
					p.Complete = false
					p.Members = nil
					return a, p, nil
				}
				inline = append(inline, parsed...)
			}
			ib, _ := json.Marshal(inline)
			pb, _ := json.Marshal(p.Members)
			if !bytes.Equal(ib, pb) {
				p.Complete = false
				p.Members = nil
			}
		}
	}
	return a, p, nil
}

// Addresses is the provider's second, annotated representation of AddressList.
// When present it must describe the same complete set; choosing one side of a
// conflicting or malformed response would manufacture trusted membership.
func bookRepresentationsAgree(row map[string]json.RawMessage, values []string, kind string) bool {
	if _, exists := row["Addresses"]; !exists {
		return true
	}
	entries, present, err := list(row, "Addresses")
	if err != nil || !present || len(entries) != len(values) {
		return false
	}
	annotated := make([]string, 0, len(entries))
	for _, entry := range entries {
		for key := range entry {
			if key != "Address" && key != "Note" {
				return false
			}
		}
		value := text(entry, "Address")
		if value == "" {
			return false
		}
		annotated = append(annotated, value)
	}
	canonical := func(items []string) ([]byte, bool) {
		if kind == "ip" {
			members := []string{}
			for _, item := range items {
				parsed, ok := addresses(item)
				if !ok {
					return nil, false
				}
				members = append(members, parsed...)
			}
			encoded, err := json.Marshal(unique(members))
			return encoded, err == nil
		}
		members := []domain.PortRange{}
		for _, item := range items {
			parsed, ok := ports(item)
			if !ok {
				return nil, false
			}
			members = append(members, parsed...)
		}
		sort.Slice(members, func(i, j int) bool {
			if members[i].From == members[j].From {
				return members[i].To < members[j].To
			}
			return members[i].From < members[j].From
		})
		encoded, err := json.Marshal(members)
		return encoded, err == nil
	}
	want, valid := canonical(values)
	got, validAnnotated := canonical(annotated)
	return valid && validAnnotated && bytes.Equal(want, got)
}

func inlineCompatible(ref bookRequest, a domain.AddressReference, p domain.PortReference) bool {
	if !ref.inlinePresent {
		return true
	}
	if ref.kind == "ip" {
		values := []string{}
		for _, value := range ref.inline {
			parsed, ok := addresses(value)
			if !ok {
				return false
			}
			values = append(values, parsed...)
		}
		return strings.Join(unique(values), "\x00") == strings.Join(unique(a.Members), "\x00")
	}
	values := []domain.PortRange{}
	for _, value := range ref.inline {
		parsed, ok := ports(value)
		if !ok {
			return false
		}
		values = append(values, parsed...)
	}
	less := func(v []domain.PortRange) {
		sort.Slice(v, func(i, j int) bool {
			if v[i].From == v[j].From {
				return v[i].To < v[j].To
			}
			return v[i].From < v[j].From
		})
	}
	less(values)
	members := append([]domain.PortRange(nil), p.Members...)
	less(members)
	b, _ := json.Marshal(values)
	c, _ := json.Marshal(members)
	return bytes.Equal(b, c)
}
