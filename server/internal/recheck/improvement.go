package recheck

import (
	"encoding/binary"
	"encoding/json"
	"net/netip"
	"sort"
	"strings"

	"cfw-policy-llm-agent/server/internal/domain"
)

type scalarRange struct{ lo, hi uint64 } // half open
type flowRectangle struct{ address, port scalarRange }

// smallerViolation compares exact flow unions after checking that the original
// check/query inventory has the same meaning. Linked rules in one canonical
// issue do not count the same flow twice. Record count, policy IDs, ordering and
// segmentation are not risk measures. All ordinary comparability checks run first.
func smallerViolation(old, next []domain.Finding) bool {
	previous, oldInventory, ok := violationLanes(old)
	if !ok {
		return false
	}
	current, newInventory, ok := violationLanes(next)
	if !ok || len(oldInventory) != len(newInventory) {
		return false
	}
	for key := range oldInventory {
		if !newInventory[key] {
			return false
		}
	}
	strict := false
	for key, rectangles := range current {
		before, exists := previous[key]
		if !exists {
			return false
		}
		oldArea, newArea := rectangleUnionArea(before), rectangleUnionArea(rectangles)
		all := append(append([]flowRectangle{}, before...), rectangles...)
		if newArea > oldArea || rectangleUnionArea(all) != oldArea {
			return false
		}
		strict = strict || newArea < oldArea
	}
	for key, before := range previous {
		if _, exists := current[key]; !exists && len(before) > 0 {
			strict = true
		}
	}
	return strict
}

func violationLanes(findings []domain.Finding) (map[string][]flowRectangle, map[string]bool, bool) {
	lanes, inventory := map[string][]flowRectangle{}, map[string]bool{}
	for _, f := range findings {
		if f.Verdict == "not_applicable" {
			continue // Compare already proved this predicate remains inapplicable.
		}
		q := f.QueryScope
		protocol := strings.ToLower(q.Protocol)
		fixed, fixedOK := ipv4Value(q.FixedAddress)
		if q.SubjectRef != f.SubjectRef || q.ServiceRef != f.ServiceRef || !fixedOK || protocol != "tcp" && protocol != "udp" {
			return nil, nil, false
		}
		addresses, ok := queryAddresses(q.Addresses)
		if !ok {
			return nil, nil, false
		}
		ports, ok := queryPorts(q.Ports)
		if !ok {
			return nil, nil, false
		}
		meaning, _ := json.Marshal(struct {
			Check, Protocol, Basis string
			FixedAddress           uint64
			AcceptedException      bool
			Addresses, Ports       [][2]uint64
		}{checkKey(f), protocol, f.AssessmentBasis, fixed, f.AcceptedException, rangePairs(addresses), rangePairs(ports)})
		inventory[string(meaning)] = true
		if f.Verdict != "fail" {
			continue
		}
		if len(f.Differences) == 0 {
			return nil, nil, false
		}
		for _, cell := range f.Differences {
			from, fromOK := ipv4Value(cell.AddressFrom)
			to, toOK := ipv4Value(cell.AddressTo)
			if !fromOK || !toOK || from > to || cell.PortFrom < 1 || cell.PortTo > 65535 || cell.PortFrom > cell.PortTo || cell.Action != "accept" && cell.Action != "drop" {
				return nil, nil, false
			}
			r := flowRectangle{scalarRange{from, to + 1}, scalarRange{uint64(cell.PortFrom), uint64(cell.PortTo) + 1}}
			if !rangeContains(addresses, r.address) || !rangeContains(ports, r.port) {
				return nil, nil, false
			}
			key, _ := json.Marshal(struct {
				Subject, Service, Protocol, Action, Basis string
				FixedAddress                              uint64
				AcceptedException                         bool
			}{f.SubjectRef, f.ServiceRef, protocol, cell.Action, f.AssessmentBasis, fixed, f.AcceptedException})
			lanes[string(key)] = append(lanes[string(key)], r)
		}
	}
	return lanes, inventory, len(lanes) > 0
}

func ipv4Value(text string) (uint64, bool) {
	address, e := netip.ParseAddr(text)
	if e != nil || !address.Is4() {
		return 0, false
	}
	bytes := address.As4()
	return uint64(binary.BigEndian.Uint32(bytes[:])), true
}

func queryAddresses(values []string) ([]scalarRange, bool) {
	if len(values) == 0 {
		return nil, false
	}
	ranges := make([]scalarRange, 0, len(values))
	for _, value := range values {
		if address, ok := ipv4Value(value); ok {
			ranges = append(ranges, scalarRange{address, address + 1})
			continue
		}
		prefix, e := netip.ParsePrefix(value)
		if e != nil || !prefix.Addr().Is4() {
			return nil, false
		}
		address, _ := ipv4Value(prefix.Masked().Addr().String())
		ranges = append(ranges, scalarRange{address, address + uint64(1)<<(32-prefix.Bits())})
	}
	return mergedRanges(ranges), true
}

func queryPorts(values []domain.PortRange) ([]scalarRange, bool) {
	if len(values) == 0 {
		return nil, false
	}
	ranges := make([]scalarRange, 0, len(values))
	for _, value := range values {
		if value.From < 1 || value.To > 65535 || value.From > value.To {
			return nil, false
		}
		ranges = append(ranges, scalarRange{uint64(value.From), uint64(value.To) + 1})
	}
	return mergedRanges(ranges), true
}

func mergedRanges(ranges []scalarRange) []scalarRange {
	sort.Slice(ranges, func(i, j int) bool {
		return ranges[i].lo < ranges[j].lo || ranges[i].lo == ranges[j].lo && ranges[i].hi < ranges[j].hi
	})
	merged := make([]scalarRange, 0, len(ranges))
	for _, r := range ranges {
		if len(merged) == 0 || merged[len(merged)-1].hi < r.lo {
			merged = append(merged, r)
		} else if r.hi > merged[len(merged)-1].hi {
			merged[len(merged)-1].hi = r.hi
		}
	}
	return merged
}

func rangeContains(ranges []scalarRange, wanted scalarRange) bool {
	i := sort.Search(len(ranges), func(i int) bool { return ranges[i].hi > wanted.lo })
	return i < len(ranges) && ranges[i].lo <= wanted.lo && ranges[i].hi >= wanted.hi
}
func rangePairs(ranges []scalarRange) [][2]uint64 {
	out := make([][2]uint64, len(ranges))
	for i, r := range ranges {
		out[i] = [2]uint64{r.lo, r.hi}
	}
	return out
}

type sweepEvent struct {
	address uint64
	port    scalarRange
	delta   int
}

// IPv4 cardinality times the 65535 supported ports fits in uint64. This sweep
// computes union area without enumerating addresses or a cross-product grid.
func rectangleUnionArea(rectangles []flowRectangle) uint64 {
	if len(rectangles) == 0 {
		return 0
	}
	events := make([]sweepEvent, 0, 2*len(rectangles))
	ports := make([]uint64, 0, 2*len(rectangles))
	for _, r := range rectangles {
		events = append(events, sweepEvent{r.address.lo, r.port, 1}, sweepEvent{r.address.hi, r.port, -1})
		ports = append(ports, r.port.lo, r.port.hi)
	}
	sort.Slice(events, func(i, j int) bool { return events[i].address < events[j].address })
	sort.Slice(ports, func(i, j int) bool { return ports[i] < ports[j] })
	unique := ports[:0]
	for _, port := range ports {
		if len(unique) == 0 || unique[len(unique)-1] != port {
			unique = append(unique, port)
		}
	}
	tree := portCoverage{points: unique, count: make([]int, 4*len(unique)), covered: make([]uint64, 4*len(unique))}
	area, prior := uint64(0), events[0].address
	for i := 0; i < len(events); {
		address := events[i].address
		area += (address - prior) * tree.covered[1]
		for i < len(events) && events[i].address == address {
			e := events[i]
			from := sort.Search(len(unique), func(j int) bool { return unique[j] >= e.port.lo })
			to := sort.Search(len(unique), func(j int) bool { return unique[j] >= e.port.hi })
			tree.add(1, 0, len(unique)-1, from, to, e.delta)
			i++
		}
		prior = address
	}
	return area
}

type portCoverage struct {
	points  []uint64
	count   []int
	covered []uint64
}

func (t *portCoverage) add(node, left, right, from, to, delta int) {
	if to <= left || right <= from {
		return
	}
	if from <= left && right <= to {
		t.count[node] += delta
	} else {
		middle := (left + right) / 2
		t.add(node*2, left, middle, from, to, delta)
		t.add(node*2+1, middle, right, from, to, delta)
	}
	if t.count[node] > 0 {
		t.covered[node] = t.points[right] - t.points[left]
	} else if right-left == 1 {
		t.covered[node] = 0
	} else {
		t.covered[node] = t.covered[node*2] + t.covered[node*2+1]
	}
}
