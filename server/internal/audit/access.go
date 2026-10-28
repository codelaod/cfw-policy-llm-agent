package audit

import (
	"context"
	"encoding/json"

	"cfw-policy-llm-agent/server/internal/domain"
)

type compiledAccess struct {
	protocol         string
	addresses, ports []interval
}

// Parse/normalize each declaration once, and remove semantically identical
// declarations before the cell walk. The source input itself remains untouched.
func compileAccess(ctx context.Context, access []domain.Access) ([]compiledAccess, error) {
	out := []compiledAccess{}
	seen := map[string]bool{}
	for i, v := range access {
		if i%32 == 0 && ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if v.Applicability != "applicable" {
			continue
		}
		addresses, err := parseAddresses(v.Addresses)
		if err != nil {
			return nil, err
		}
		ports := portRanges(v.Ports)
		key, _ := json.Marshal(struct {
			Protocol         string
			Addresses, Ports [][2]uint64
		}{v.Protocol, rangePairs(addresses), rangePairs(ports)})
		if !seen[string(key)] {
			seen[string(key)] = true
			out = append(out, compiledAccess{v.Protocol, addresses, ports})
		}
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return out, nil
}

func compiledAccessContains(allowed []compiledAccess, protocol string, address, port uint64) bool {
	for _, v := range allowed {
		if (v.protocol == protocol || v.protocol == "any") && contains(v.addresses, address) && contains(v.ports, port) {
			return true
		}
	}
	return false
}

// Path cells can merge across business boundaries when their firewall action
// is identical. Repartition before subtraction, checking cancellation between
// bounded batches and retaining already-proved differences on interruption.
func outsideAccess(ctx context.Context, c domain.Cell, allowed []compiledAccess, protocol string) ([]domain.Cell, bool, error) {
	if ctx.Err() != nil {
		return nil, false, ctx.Err()
	}
	addresses := []interval{{ipv4number(c.AddressFrom), ipv4number(c.AddressTo)}}
	ports := []interval{{uint64(c.PortFrom), uint64(c.PortTo)}}
	ax, px := []interval{}, []interval{}
	for i, v := range allowed {
		if i%32 == 0 && ctx.Err() != nil {
			return nil, false, ctx.Err()
		}
		if v.protocol != protocol && v.protocol != "any" {
			continue
		}
		ax = append(ax, v.addresses...)
		px = append(px, v.ports...)
	}
	as, ps := segments(cuts(addresses, ax), addresses), segments(cuts(ports, px), ports)
	if len(ps) > 0 && len(as) > MaxQueryCells/len(ps) {
		return nil, false, nil
	}
	out := []domain.Cell{}
	processed := 0
	for _, a := range as {
		for _, p := range ps {
			if processed%64 == 0 && ctx.Err() != nil {
				return out, false, ctx.Err()
			}
			processed++
			if !compiledAccessContains(allowed, protocol, a.lo, p.lo) {
				part := c
				part.AddressFrom = ipText(a.lo)
				part.AddressTo = ipText(a.hi)
				part.PortFrom = int(p.lo)
				part.PortTo = int(p.hi)
				out = append(out, part)
			}
		}
	}
	if ctx.Err() != nil {
		return out, false, ctx.Err()
	}
	return out, true, nil
}
