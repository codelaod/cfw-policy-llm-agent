package audit

import (
	_ "embed"
	"encoding/csv"
	"math/bits"
	"net/netip"
	"strings"

	"cfw-policy-llm-agent/server/internal/domain"
)

// The embedded IANA registry is fixed at development time. Nothing in this
// package fetches a registry, checks routes, or probes Internet reachability.
const PublicIPv4ClassifierVersion = "iana-special-2025-10-09-read-2026-10-28-v1"
const PublicIPv4RegistryUpdated = "2025-10-09"
const PublicIPv4RegistryCheckedAt = "2026-10-28"

//go:embed data/iana-ipv4-special-2026-10-28.csv
var specialIPv4CSV string

type specialIPv4 struct {
	addresses      interval
	bits           int
	classification string
}

var publicIPv4, uncertainIPv4 = specialIPv4Domains()

func specialIPv4Domains() ([]interval, []interval) {
	rows, err := csv.NewReader(strings.NewReader(specialIPv4CSV)).ReadAll()
	if err != nil || len(rows) < 2 {
		panic("invalid embedded IANA IPv4 registry")
	}
	addressColumn, globalColumn := -1, -1
	for i, column := range rows[0] {
		if column == "Address Block" {
			addressColumn = i
		}
		if column == "Globally Reachable" {
			globalColumn = i
		}
	}
	if addressColumn < 0 || globalColumn < 0 {
		panic("unsupported embedded IANA registry columns")
	}
	entries := []specialIPv4{}
	ranges := []interval{}
	for _, row := range rows[1:] {
		classification := "unknown"
		value := strings.TrimSpace(row[globalColumn])
		if strings.HasPrefix(value, "True") {
			classification = "public"
		} else if strings.HasPrefix(value, "False") {
			classification = "non_public"
		} else if value != "" {
			panic("unknown embedded IANA global status")
		}
		for _, block := range strings.Split(row[addressColumn], ",") {
			fields := strings.Fields(block)
			if len(fields) == 0 {
				panic("empty embedded IANA prefix")
			}
			prefix, err := netip.ParsePrefix(fields[0])
			if err != nil || !prefix.Addr().Is4() {
				panic("invalid embedded IANA IPv4 prefix")
			}
			parsed, err := parseAddresses([]string{prefix.String()})
			if err != nil || len(parsed) != 1 {
				panic("invalid embedded IANA interval")
			}
			entries = append(entries, specialIPv4{parsed[0], prefix.Bits(), classification})
			ranges = append(ranges, parsed[0])
		}
	}
	// RFC 1112 / IANA multicast registry: multicast is outside the unicast
	// service-source subdomain even though it is not listed in this IANA CSV.
	multicast, _ := parseAddresses([]string{"224.0.0.0/4"})
	entries = append(entries, specialIPv4{multicast[0], 4, "non_public"})
	ranges = append(ranges, multicast...)
	boundary := cuts([]interval{{0, 4294967295}}, ranges)
	known, unknown := []interval{}, []interval{}
	for i := 0; i+1 < len(boundary); i++ {
		segment := interval{boundary[i], boundary[i+1] - 1}
		classification, best := "public", -1
		for _, entry := range entries {
			if segment.lo >= entry.addresses.lo && segment.lo <= entry.addresses.hi && entry.bits > best {
				classification = entry.classification
				best = entry.bits
			}
		}
		if classification == "public" {
			known = append(known, segment)
		} else if classification == "unknown" {
			unknown = append(unknown, segment)
		}
	}
	return mergeRanges(known), mergeRanges(unknown)
}

func addressPrefixes(ranges []interval) []string {
	values := []string{}
	for _, x := range ranges {
		for lo := x.lo; lo <= x.hi; {
			alignment := bits.TrailingZeros64(lo)
			if alignment > 32 {
				alignment = 32
			}
			span := 63 - bits.LeadingZeros64(x.hi-lo+1)
			if alignment > span {
				alignment = span
			}
			prefix := netip.PrefixFrom(netip.MustParseAddr(ipText(lo)), 32-alignment)
			values = append(values, prefix.String())
			lo += uint64(1) << uint(alignment)
		}
	}
	return values
}

func publicAddresses() []string          { return addressPrefixes(publicIPv4) }
func uncertainPublicAddresses() []string { return addressPrefixes(uncertainIPv4) }

// Public exposure checks share this domain; ordinary business-set comparison
// continues to use the full explicitly selected IPv4 domain. An unknown IANA
// status is never silently promoted to known non-public or complete safety.
func (r *runState) publicPath(q domain.Query, broadOnly bool) domain.PathResult {
	p := r.path(q)
	if uncertain := uncertainPublicAddresses(); len(uncertain) > 0 {
		unknownQuery := q
		unknownQuery.Addresses = uncertain
		up := r.path(unknownQuery)
		unconfirmed := !up.Complete
		for _, cell := range up.Cells {
			if cell.Action == "unknown" || (cell.Action == "accept" && (!broadOnly || cell.Broad)) {
				unconfirmed = true
				break
			}
		}
		if unconfirmed {
			p.Complete = false
			p.Reasons = uniqueStrings(append(append(p.Reasons, up.Reasons...), "PUBLIC_ADDRESS_CLASSIFICATION_UNKNOWN"))
		}
	}
	return p
}
