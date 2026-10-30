package audit

import (
	"context"
	"fmt"
	"net/netip"
	"sort"
	"strings"

	"cfw-policy-llm-agent/server/internal/domain"
)

type compiledPolicy struct {
	p                                         domain.Policy
	source, destination, ports                []interval
	sourceKnown, destinationKnown, portsKnown bool
}

func resolveAddress(in domain.Input, s domain.AddressSet) ([]interval, bool) {
	if s.Ref != "" {
		v, ok := in.AddressRefs[s.Ref]
		if !ok {
			return nil, false
		}
		r, _ := parseAddresses(v.Members)
		return r, v.Complete
	}
	r, _ := parseAddresses(s.Values)
	return r, len(s.Values) > 0
}
func resolvePort(in domain.Input, s domain.PortSet) ([]interval, bool) {
	if s.Ref != "" {
		v, ok := in.PortRefs[s.Ref]
		if !ok {
			return nil, false
		}
		return portRanges(v.Members), v.Complete
	}
	return portRanges(s.Values), len(s.Values) > 0
}
func compile(in domain.Input) []compiledPolicy {
	return compilePolicies(in, false)
}
func compilePolicies(in domain.Input, includeDisabled bool) []compiledPolicy {
	out := make([]compiledPolicy, 0, len(in.Policies))
	for _, p := range in.Policies {
		if p.Enabled == "disabled" && !includeDisabled {
			continue
		}
		s, sk := resolveAddress(in, p.Source)
		d, dk := resolveAddress(in, p.Destination)
		ports, pk := resolvePort(in, p.Ports)
		out = append(out, compiledPolicy{p: p, source: s, sourceKnown: sk, destination: d, destinationKnown: dk, ports: ports, portsKnown: pk})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].p.Priority < out[j].p.Priority })
	return out
}

// relation is a three-valued set membership. Partial static reference members
// can prove membership, but absence from an incomplete reference remains unknown.
func relation(r []interval, known bool, n uint64) int {
	if contains(r, n) {
		return 1
	}
	if known {
		return 0
	}
	return -1
}
func dimensions(p compiledPolicy, in domain.Input, q domain.Query, addr, port uint64) int {
	proto := strings.ToLower(p.p.Protocol)
	if (proto == "tcp" || proto == "udp" || proto == "icmp" || proto == "icmpv6") && proto != q.Protocol {
		return 0
	}
	src, dst := ipv4number(q.FixedAddress), addr
	if in.Scope.Direction == "in" {
		src, dst = addr, ipv4number(q.FixedAddress)
	}
	x := []int{relation(p.source, p.sourceKnown, src), relation(p.destination, p.destinationKnown, dst), relation(p.ports, p.portsKnown, port)}
	unknown := proto != "tcp" && proto != "udp" && proto != "any"
	for _, v := range x {
		if v == 0 {
			return 0
		}
		if v < 0 {
			unknown = true
		}
	}
	if unknown {
		return -1
	}
	return 1
}

func queryPrecondition(in domain.Input, q domain.Query) string {
	if !in.Completeness.Declared || !in.Completeness.EnumerationComplete || len(in.Completeness.KnownGaps) > 0 {
		return "DATA_INCOMPLETE"
	}
	if in.Completeness.ObservationConsistency != "fixed_artifact" && in.Completeness.ObservationConsistency != "observed_stable" {
		return "OBSERVATION_UNSTABLE"
	}
	protection := in.ProtectionFor(q.SubjectRef)
	if !protection.Applicable || protection.State != "on" || protection.EvidenceRef == "" {
		return "PROTECTION_UNCONFIRMED"
	}
	for _, a := range in.Assets {
		if a.ID == q.SubjectRef {
			if !a.AddressObserved || a.Address == "" || a.Address != q.FixedAddress {
				return "ENDPOINT_UNCONFIRMED"
			}
			if in.Scope.Boundary == "nat" && !validAssociation(in, a) {
				return "NAT_ASSOCIATION_INCOMPLETE"
			}
			return ""
		}
	}
	return "ENDPOINT_OUTSIDE_SELECTION"
}

func matchCell(ctx context.Context, in domain.Input, q domain.Query, policies []compiledPolicy, addr, port uint64) (string, string, bool, string) {
	// An entry with unknown order may precede any known entry. It must be
	// excluded by known dimensions before any later policy can establish a fact.
	for i, p := range policies {
		if i%32 == 0 && ctx.Err() != nil {
			return "unknown", "", false, "QUERY_CANCELLED"
		}
		if !p.p.OrderKnown && dimensions(p, in, q, addr, port) != 0 {
			return "unknown", p.p.ID, false, "ORDER_UNKNOWN"
		}
	}
	for i := 0; i < len(policies); {
		if i%32 == 0 && ctx.Err() != nil {
			return "unknown", "", false, "QUERY_CANCELLED"
		}
		end := i + 1
		for end < len(policies) && policies[end].p.Priority == policies[i].p.Priority {
			end++
		}
		possible := 0
		var candidate compiledPolicy
		match := 0
		for _, p := range policies[i:end] {
			v := dimensions(p, in, q, addr, port)
			if v != 0 {
				possible++
				candidate = p
				match = v
			}
		}
		if possible > 1 {
			return "unknown", "", false, "ORDER_UNKNOWN"
		}
		if possible == 1 {
			p := candidate
			if p.p.Enabled != "enabled" {
				if p.p.Enabled != "unknown" && p.p.Enabled != "" {
					return "unknown", p.p.ID, false, "STATE_UNSUPPORTED"
				}
				return "unknown", p.p.ID, false, "ENABLED_UNKNOWN"
			}
			proto := strings.ToLower(p.p.Protocol)
			if proto != "tcp" && proto != "udp" && proto != "any" {
				return "unknown", p.p.ID, false, "PROTOCOL_UNSUPPORTED"
			}
			if match < 0 {
				return "unknown", p.p.ID, false, "REFERENCE_INCOMPLETE"
			}
			if len(p.p.Conditions) > 0 {
				return "unknown", p.p.ID, false, "CONDITION_UNSUPPORTED"
			}
			if p.p.Action != "accept" && p.p.Action != "drop" {
				if p.p.Action == "unknown" || p.p.Action == "" {
					return "unknown", p.p.ID, false, "ACTION_UNCONFIRMED"
				}
				return "unknown", p.p.ID, false, "ACTION_UNSUPPORTED"
			}
			variable := p.destination
			if in.Scope.Direction == "in" {
				variable = p.source
			}
			broad := len(variable) == 1 && variable[0].lo == 0 && variable[0].hi == 4294967295
			return p.p.Action, p.p.ID, broad && p.p.Action == "accept", ""
		}
		i = end
	}
	if in.Semantics.DefaultAction == "unknown" {
		return "unknown", "", false, "DEFAULT_UNCONFIRMED"
	}
	return in.Semantics.DefaultAction, "", in.Semantics.DefaultAction == "accept", ""
}

func cuts(ranges []interval, extras ...[]interval) []uint64 {
	out := make([]uint64, 0, 2*len(ranges))
	add := func(rs []interval) {
		for _, r := range rs {
			out = append(out, r.lo, r.hi+1)
		}
	}
	add(ranges)
	for _, rs := range extras {
		add(rs)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	n := 0
	for _, v := range out {
		if n == 0 || out[n-1] != v {
			out[n] = v
			n++
		}
	}
	return out[:n]
}
func segments(boundaries []uint64, scope []interval) []interval {
	out := []interval{}
	for i := 0; i+1 < len(boundaries); i++ {
		if contains(scope, boundaries[i]) {
			out = append(out, interval{boundaries[i], boundaries[i+1] - 1})
		}
	}
	return out
}

// Evaluate partitions the selected address/port subdomain at every relevant
// boundary. No network probes or sample-point approximation are used.
func Evaluate(ctx context.Context, in domain.Input, q domain.Query) (domain.PathResult, error) {
	r := domain.PathResult{Query: cloneQuery(q), Cells: []domain.Cell{}, Reasons: []string{}, Complete: true}
	if err := ValidateContext(ctx, in); err != nil {
		return r, err
	}
	if q.Protocol != "tcp" && q.Protocol != "udp" {
		r.Complete = false
		if q.Protocol == "unknown" || q.Protocol == "" {
			r.Reasons = append(r.Reasons, "QUERY_DIMENSION_UNKNOWN")
			return r, nil
		}
		r.Reasons = append(r.Reasons, "PROTOCOL_UNSUPPORTED")
		return r, nil
	}
	if ctx.Err() != nil {
		return r, ctx.Err()
	}
	if ip, err := netip.ParseAddr(q.FixedAddress); err != nil || !ip.Is4() {
		r.Complete = false
		r.Reasons = append(r.Reasons, "ENDPOINT_UNCONFIRMED")
		return r, nil
	}
	addresses, err := parseAddresses(q.Addresses)
	if err != nil {
		return r, err
	}
	if err = validPorts(q.Ports); err != nil {
		return r, err
	}
	ports := portRanges(q.Ports)
	if len(addresses) == 0 || len(ports) == 0 {
		return r, fmt.Errorf("%w: an explicit query subdomain is required", ErrInput)
	}
	policies := compile(in)
	relevant := make([]compiledPolicy, 0, len(policies))
	addressExtra, portExtra := []interval{}, []interval{}
	for _, p := range policies {
		fixed, known := p.source, p.sourceKnown
		if in.Scope.Direction == "in" {
			fixed, known = p.destination, p.destinationKnown
		}
		proto := strings.ToLower(p.p.Protocol)
		if relation(fixed, known, ipv4number(q.FixedAddress)) == 0 || ((proto == "tcp" || proto == "udp" || proto == "icmp" || proto == "icmpv6") && proto != q.Protocol) {
			continue
		}
		relevant = append(relevant, p)
		variable := p.destination
		if in.Scope.Direction == "in" {
			variable = p.source
		}
		addressExtra = append(addressExtra, variable...)
		portExtra = append(portExtra, p.ports...)
	}
	addAccess := func(access []domain.Access) {
		for _, a := range access {
			if a.Protocol != q.Protocol && a.Protocol != "any" {
				continue
			}
			rr, _ := parseAddresses(a.Addresses)
			addressExtra = append(addressExtra, rr...)
			portExtra = append(portExtra, portRanges(a.Ports)...)
		}
	}
	for _, a := range in.Assets {
		if a.ID != q.SubjectRef {
			continue
		}
		addAccess(a.Allowed)
		for _, s := range a.Services {
			if q.ServiceRef != "" && s.ID != q.ServiceRef {
				continue
			}
			addAccess(s.Allowed)
			addAccess(s.Required)
		}
	}
	aSegments := segments(cuts(addresses, addressExtra), addresses)
	pSegments := segments(cuts(ports, portExtra), ports)
	// Division prevents overflow, and the Cartesian product is never allocated
	// until the complete boundary count has passed the cap.
	if len(aSegments) > queryCellLimit(ctx) || len(pSegments) > queryCellLimit(ctx) || len(pSegments) > 0 && len(aSegments) > queryCellLimit(ctx)/len(pSegments) {
		r.Complete = false
		r.Reasons = append(r.Reasons, "QUERY_CELL_LIMIT")
		return r, nil
	}
	precondition := queryPrecondition(in, q)
	for _, a := range aSegments {
		for _, p := range pSegments {
			if ctx.Err() != nil {
				r.Complete = false
				r.Reasons = uniqueStrings(append(r.Reasons, "QUERY_CANCELLED"))
				return r, ctx.Err()
			}
			action, policy, broad, reason := "unknown", "", false, precondition
			if reason == "" {
				action, policy, broad, reason = matchCell(ctx, in, q, relevant, a.lo, p.lo)
			}
			if action == "unknown" {
				r.Complete = false
				r.Reasons = append(r.Reasons, reason)
			}
			r.Cells = append(r.Cells, domain.Cell{AddressFrom: ipText(a.lo), AddressTo: ipText(a.hi), PortFrom: int(p.lo), PortTo: int(p.hi), Action: action, PolicyRef: policy, Broad: broad, Reason: reason})
			r.EvaluatedCells++
		}
	}
	r.Cells = mergeCells(r.Cells)
	r.Reasons = uniqueStrings(r.Reasons)
	return r, nil
}

func cloneQuery(q domain.Query) domain.Query {
	q.Addresses = append([]string(nil), q.Addresses...)
	q.Ports = append([]domain.PortRange(nil), q.Ports...)
	return q
}

func equalClass(a, b domain.Cell) bool {
	return a.Action == b.Action && a.PolicyRef == b.PolicyRef && a.Broad == b.Broad && a.Reason == b.Reason
}
func mergeCells(cells []domain.Cell) []domain.Cell {
	// First combine port neighbours, then address neighbours sharing the same
	// port span and provenance; merging never hides differing path evidence.
	out := []domain.Cell{}
	for _, c := range cells {
		if len(out) > 0 {
			p := &out[len(out)-1]
			if equalClass(*p, c) && p.AddressFrom == c.AddressFrom && p.AddressTo == c.AddressTo && p.PortTo+1 == c.PortFrom {
				p.PortTo = c.PortTo
				continue
			}
		}
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.PortFrom != b.PortFrom {
			return a.PortFrom < b.PortFrom
		}
		if a.PortTo != b.PortTo {
			return a.PortTo < b.PortTo
		}
		if a.Action != b.Action {
			return a.Action < b.Action
		}
		if a.PolicyRef != b.PolicyRef {
			return a.PolicyRef < b.PolicyRef
		}
		if a.Reason != b.Reason {
			return a.Reason < b.Reason
		}
		if a.Broad != b.Broad {
			return !a.Broad
		}
		return ipv4number(a.AddressFrom) < ipv4number(b.AddressFrom)
	})
	merged := []domain.Cell{}
	for _, c := range out {
		if len(merged) > 0 {
			p := &merged[len(merged)-1]
			if equalClass(*p, c) && p.PortFrom == c.PortFrom && p.PortTo == c.PortTo && ipv4number(p.AddressTo)+1 == ipv4number(c.AddressFrom) {
				p.AddressTo = c.AddressTo
				continue
			}
		}
		merged = append(merged, c)
	}
	sort.Slice(merged, func(i, j int) bool {
		if merged[i].AddressFrom != merged[j].AddressFrom {
			return ipv4number(merged[i].AddressFrom) < ipv4number(merged[j].AddressFrom)
		}
		return merged[i].PortFrom < merged[j].PortFrom
	})
	return merged
}
