package audit

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"sort"
	"strings"
	"time"

	"cfw-policy-llm-agent/server/internal/domain"
)

const MaxInputBytes = 5 << 20
const MaxQueryCells = 50000
const KernelVersion = "1"
const NormalizerVersion = "1"
const ProviderSemanticsVersion = "1"

var ErrInput = errors.New("INPUT_INVALID")
var ErrUnsupported = errors.New("UNSUPPORTED_INPUT")

// DecodeImport only reads bounded JSON. Provenance is assigned here, not by the
// uploader; the synthetic profile is deliberately not an import capability.
func DecodeImport(b []byte) (domain.Input, error) {
	return DecodeImportContext(context.Background(), b)
}
func DecodeImportContext(ctx context.Context, b []byte) (domain.Input, error) {
	var in domain.Input
	if ctx.Err() != nil {
		return in, ctx.Err()
	}
	if len(b) > MaxInputBytes {
		return in, fmt.Errorf("%w: input exceeds 5 MiB", ErrInput)
	}
	d := json.NewDecoder(bytes.NewReader(b))
	depth, tokens := 0, 0
	for {
		tok, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return in, fmt.Errorf("%w: invalid JSON", ErrInput)
		}
		tokens++
		if tokens%64 == 0 && ctx.Err() != nil {
			return in, ctx.Err()
		}
		if tokens > 300000 {
			return in, fmt.Errorf("%w: too many values", ErrInput)
		}
		if v, ok := tok.(json.Delim); ok {
			if v == '{' || v == '[' {
				depth++
			} else {
				depth--
			}
			if depth > 32 {
				return in, fmt.Errorf("%w: excessive nesting", ErrInput)
			}
		}
	}
	d = json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err := d.Decode(&in); err != nil {
		return in, fmt.Errorf("%w: unknown field or malformed document", ErrInput)
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return in, fmt.Errorf("%w: exactly one document required", ErrInput)
	}
	in.SourceKind = "imported"
	if err := ValidateContext(ctx, in); err != nil {
		return domain.Input{}, err
	}
	return in, nil
}

func Validate(in domain.Input) error {
	return ValidateContext(context.Background(), in)
}
func ValidateContext(ctx context.Context, in domain.Input) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	bad := func(message string) error { return fmt.Errorf("%w: %s", ErrInput, message) }
	if in.SchemaVersion != 1 || in.KernelVersion != KernelVersion || in.NormalizerVersion != NormalizerVersion {
		return bad("unsupported or missing version")
	}
	for _, v := range []string{in.SnapshotVersion, in.ProfileVersion, in.IntentVersion, in.Baseline.Version, in.Scope.Ref, in.Scope.AccountRef, in.Scope.PolicySetRef, in.Scope.ControlPlaneRef} {
		if v == "" || len(v) > 128 {
			return bad("missing or overlong identity/version")
		}
	}
	if in.Scope.IPFamily != "ipv4" {
		return fmt.Errorf("%w: only explicit IPv4 partition supported", ErrUnsupported)
	}
	if in.Scope.Provider != "aliyun" || !((in.Scope.Boundary == "internet" && (in.Scope.Direction == "in" || in.Scope.Direction == "out")) || (in.Scope.Boundary == "nat" && in.Scope.Direction == "out" && in.Scope.GatewayRef != "")) {
		return bad("scope is outside the supported partition")
	}
	if in.SourceKind != "synthetic" && in.SourceKind != "imported" && in.SourceKind != "live_readonly" {
		return bad("unknown provenance")
	}
	if _, err := time.Parse(time.RFC3339Nano, in.EvaluationAt); err != nil {
		return bad("evaluation_at must include a timezone")
	}
	if _, err := time.Parse(time.RFC3339Nano, in.CollectedAt); err != nil {
		return bad("collected_at must include a timezone")
	}
	if len(in.Policies) > 1000 || len(in.Assets) > 50 || len(in.Selection.AssetIDs) > 50 || len(in.AddressRefs)+len(in.PortRefs) > 2000 {
		return bad("object limit exceeded")
	}
	if in.Semantics.Version != ProviderSemanticsVersion || in.Semantics.SourceRef == "" {
		return bad("provider semantics needs version and evidence")
	}
	if in.Semantics.DefaultAction != "accept" && in.Semantics.DefaultAction != "drop" && in.Semantics.DefaultAction != "unknown" {
		return bad("unknown default action")
	}
	switch in.Semantics.ProfileID {
	case "synthetic-v1":
		if in.SourceKind != "synthetic" {
			return bad("synthetic semantics cannot be asserted by imports")
		}
	case "aliyun-cfw-internet-v1":
		if in.Scope.Boundary != "internet" || in.Semantics.SourceRef != "T-06" || in.Semantics.DefaultAction == "drop" {
			return bad("provider profile/default/source conflict")
		}
	case "aliyun-cfw-nat-v1":
		if in.Scope.Boundary != "nat" || in.Semantics.SourceRef != "T-06" || in.Semantics.DefaultAction == "drop" {
			return bad("NAT profile/default/source conflict; StrictMode is not a default action")
		}
	default:
		return bad("provider semantics profile is not approved")
	}
	if in.Selection.Kind == "gateway_policy" {
		if in.Scope.Boundary != "nat" || len(in.Selection.AssetIDs) != 0 || len(in.Assets) != 0 {
			return bad("gateway policy mode has no implicit asset matrix")
		}
	} else if in.Selection.Kind != "assets" || len(in.Selection.AssetIDs) == 0 {
		return bad("select explicit assets or one NAT policy set")
	}
	selected := map[string]bool{}
	for _, id := range in.Selection.AssetIDs {
		if selected[id] {
			return bad("duplicate selected asset")
		}
		selected[id] = true
	}
	seen := map[string]bool{}
	members := 0
	// Count all nested members before allocating parsed interval slices.
	for _, p := range in.Policies {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		members += len(p.Source.Values) + len(p.Destination.Values) + len(p.Ports.Values)
		if members > 20000 {
			return bad("total normalized member limit exceeded")
		}
	}
	for _, v := range in.AddressRefs {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		members += len(v.Members)
		if members > 20000 {
			return bad("total normalized member limit exceeded")
		}
	}
	for _, v := range in.PortRefs {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		members += len(v.Members)
		if members > 20000 {
			return bad("total normalized member limit exceeded")
		}
	}
	for _, a := range in.Assets {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		for _, access := range a.Allowed {
			members += len(access.Addresses) + len(access.Ports)
		}
		if len(a.Services) > 100 {
			return bad("service limit exceeded")
		}
		for _, s := range a.Services {
			for _, access := range s.Allowed {
				members += len(access.Addresses) + len(access.Ports)
			}
			for _, access := range s.Required {
				members += len(access.Addresses) + len(access.Ports)
			}
		}
		if members > 20000 {
			return bad("total normalized member limit exceeded")
		}
	}
	members = 0
	checkAccess := func(access []domain.Access) error {
		if len(access) > 1000 {
			return bad("too many declared accesses")
		}
		for _, a := range access {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if a.ScopeRef != in.Scope.Ref {
				return bad("business access belongs to another scope")
			}
			if a.Protocol != "tcp" && a.Protocol != "udp" && a.Protocol != "any" {
				return fmt.Errorf("%w: business protocol", ErrUnsupported)
			}
			if a.Applicability != "applicable" && a.Applicability != "not_applicable" && a.Applicability != "unknown" {
				return bad("access applicability is required")
			}
			if a.Applicability == "applicable" && (len(a.Addresses) == 0 || len(a.Ports) == 0) {
				return bad("applicable access needs explicit address and port sets")
			}
			if _, err := parseAddresses(a.Addresses); err != nil {
				return err
			}
			if err := validPorts(a.Ports); err != nil {
				return err
			}
			members += len(a.Addresses) + len(a.Ports)
		}
		return nil
	}
	for _, a := range in.Assets {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if a.ID == "" || len(a.ID) > 128 || seen[a.ID] || a.ScopeRef != in.Scope.Ref || !selected[a.ID] {
			return bad("asset identity, selection, or scope mismatch")
		}
		seen[a.ID] = true
		if a.Address != "" {
			ip, err := netip.ParseAddr(a.Address)
			if err != nil || !ip.Is4() {
				return fmt.Errorf("%w: IPv4 endpoint required", ErrUnsupported)
			}
		}
		if len(a.Services) > 100 {
			return bad("service limit exceeded")
		}
		if a.AllowedConfirmed && a.AllowedComplete && len(a.Allowed) == 0 && !a.DenyAll {
			return bad("explicit deny_all required for confirmed complete empty permissions")
		}
		if a.DenyAll && len(a.Allowed) > 0 {
			return bad("asset deny_all conflicts with nonempty permissions")
		}
		if err := checkAccess(a.Allowed); err != nil {
			return err
		}
		services := map[string]bool{}
		for _, s := range a.Services {
			if s.ID == "" || len(s.ID) > 128 || services[s.ID] {
				return bad("duplicate or missing service identity")
			}
			services[s.ID] = true
			if s.Protocol != "tcp" && s.Protocol != "udp" && s.Protocol != "unknown" {
				return fmt.Errorf("%w: service protocol", ErrUnsupported)
			}
			if err := validPorts(s.Ports); err != nil {
				return err
			}
			if s.AllowedConfirmed && s.AllowedComplete && len(s.Allowed) == 0 && !s.DenyAll {
				return bad("explicit service deny_all required")
			}
			if s.DenyAll && len(s.Allowed) > 0 {
				return bad("service deny_all conflicts with nonempty permissions")
			}
			if err := checkAccess(s.Allowed); err != nil {
				return err
			}
			if err := checkAccess(s.Required); err != nil {
				return err
			}
			if s.DenyAll && len(s.Required) > 0 && s.RequiredConfirmed {
				return bad("required access conflicts with explicit deny_all")
			}
			if s.RequiredConfirmed && s.AllowedConfirmed && s.AllowedComplete {
				ok, err := requiredSubset(ctx, s.Required, s.Allowed)
				if err != nil {
					return err
				}
				if !ok {
					return bad("required access is outside the confirmed complete service upper bound")
				}
			}
			if s.RequiredConfirmed && a.AllowedConfirmed && a.AllowedComplete {
				ok, err := requiredSubset(ctx, s.Required, a.Allowed)
				if err != nil {
					return err
				}
				if !ok {
					return bad("required access is outside the confirmed complete asset upper bound")
				}
			}
		}
	}
	if len(seen) != len(selected) {
		return bad("selected asset absent from frozen input")
	}
	for _, ref := range in.AddressRefs {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if ref.ScopeRef != in.Scope.Ref {
			return bad("address reference scope mismatch")
		}
		if len(ref.Members) > 20000 {
			return bad("reference member limit")
		}
		members += len(ref.Members)
		if _, err := parseAddresses(ref.Members); err != nil {
			return err
		}
	}
	for _, ref := range in.PortRefs {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if ref.ScopeRef != in.Scope.Ref {
			return bad("port reference scope mismatch")
		}
		members += len(ref.Members)
		if err := validPorts(ref.Members); err != nil {
			return err
		}
	}
	seen = map[string]bool{}
	for _, p := range in.Policies {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if p.ID == "" || len(p.ID) > 128 || seen[p.ID] || p.ScopeRef != in.Scope.Ref {
			return bad("policy identity/scope mismatch")
		}
		seen[p.ID] = true
		if p.Priority < 0 || p.Priority > 1000000 {
			return bad("policy order outside range")
		}
		if (p.Source.Ref != "" && len(p.Source.Values) > 0) || (p.Destination.Ref != "" && len(p.Destination.Values) > 0) || (p.Ports.Ref != "" && len(p.Ports.Values) > 0) {
			return bad("a literal set and reference cannot both define one field")
		}
		if _, err := parseAddresses(p.Source.Values); err != nil {
			return err
		}
		if _, err := parseAddresses(p.Destination.Values); err != nil {
			return err
		}
		if err := validPorts(p.Ports.Values); err != nil {
			return err
		}
		members += len(p.Source.Values) + len(p.Destination.Values) + len(p.Ports.Values)
		if len(p.Conditions) > 32 {
			return bad("advanced condition limit")
		}
		for _, raw := range p.Conditions {
			if len(raw) > 32768 || !json.Valid(raw) {
				return bad("malformed or oversized advanced condition")
			}
		}
	}
	if members > 20000 {
		return bad("total normalized member limit exceeded")
	}
	if len(in.Baseline.EnabledChecks) > 32 || len(in.Baseline.RiskPorts) > 100 || len(in.Baseline.Exceptions) > 1000 {
		return bad("baseline limit")
	}
	checks := map[string]bool{}
	for _, id := range in.Baseline.EnabledChecks {
		if checks[id] {
			return bad("duplicate enabled check")
		}
		checks[id] = true
		if _, err := ExplainRule(id); err != nil {
			return err
		}
	}
	if checks["CHK-07"] && len(in.Baseline.RiskPorts) == 0 {
		return bad("adopted CHK-07 baseline needs explicit versioned risk ports")
	}
	for _, p := range in.Baseline.RiskPorts {
		if (p.Protocol != "tcp" && p.Protocol != "udp") || p.Port < 1 || p.Port > 65535 {
			return bad("invalid baseline port")
		}
	}
	for _, x := range in.Baseline.Exceptions {
		if x.ScopeRef != in.Scope.Ref || x.Reason == "" {
			return bad("exception scope/reason required")
		}
		if _, err := time.Parse(time.RFC3339Nano, x.ExpiresAt); err != nil {
			return bad("exception expiry requires timezone")
		}
	}
	return nil
}

func requiredSubset(ctx context.Context, required, allowed []domain.Access) (bool, error) {
	for _, a := range allowed {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		if a.Applicability == "unknown" {
			return true, nil
		}
	} // Unknown is not an explicit contradiction.
	compiled, err := compileAccess(ctx, allowed)
	if err != nil {
		return false, err
	}
	needs, err := compileAccess(ctx, required)
	if err != nil {
		return false, err
	}
	for _, need := range needs {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		addresses, ports := need.addresses, need.ports
		if len(ports) > 0 && len(addresses) > MaxQueryCells/len(ports) {
			return false, nil
		}
		protocols := []string{need.protocol}
		if need.protocol == "any" {
			protocols = []string{"tcp", "udp"}
		}
		for _, protocol := range protocols {
			for _, address := range addresses {
				for _, port := range ports {
					c := domain.Cell{AddressFrom: ipText(address.lo), AddressTo: ipText(address.hi), PortFrom: int(port.lo), PortTo: int(port.hi)}
					outside, complete, err := outsideAccess(ctx, c, compiled, protocol)
					if err != nil {
						return false, err
					}
					if !complete || len(outside) > 0 {
						return false, nil
					}
				}
			}
		}
	}
	return true, nil
}

type interval struct{ lo, hi uint64 }

func ipv4number(s string) uint64 {
	ip, err := netip.ParseAddr(s)
	if err != nil || !ip.Is4() {
		return 0
	}
	b := ip.As4()
	return uint64(binary.BigEndian.Uint32(b[:]))
}
func ipText(v uint64) string {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], uint32(v))
	return netip.AddrFrom4(b).String()
}
func parseAddresses(values []string) ([]interval, error) {
	r := make([]interval, 0, len(values))
	for _, s := range values {
		if strings.Contains(s, "/") {
			p, err := netip.ParsePrefix(s)
			if err != nil || !p.Addr().Is4() {
				return nil, fmt.Errorf("%w: literal IPv4/CIDR required", ErrUnsupported)
			}
			p = p.Masked()
			lo := ipv4number(p.Addr().String())
			r = append(r, interval{lo, lo + (uint64(1) << uint(32-p.Bits())) - 1})
		} else {
			a, err := netip.ParseAddr(s)
			if err != nil || !a.Is4() {
				return nil, fmt.Errorf("%w: literal IPv4 required", ErrUnsupported)
			}
			n := ipv4number(s)
			r = append(r, interval{n, n})
		}
	}
	return mergeRanges(r), nil
}
func validPorts(ports []domain.PortRange) error {
	for _, p := range ports {
		if p.From < 1 || p.To > 65535 || p.From > p.To {
			return fmt.Errorf("%w: invalid port interval", ErrInput)
		}
	}
	return nil
}
func portRanges(ports []domain.PortRange) []interval {
	r := make([]interval, 0, len(ports))
	for _, p := range ports {
		r = append(r, interval{uint64(p.From), uint64(p.To)})
	}
	return mergeRanges(r)
}
func mergeRanges(r []interval) []interval {
	sort.Slice(r, func(i, j int) bool {
		if r[i].lo == r[j].lo {
			return r[i].hi < r[j].hi
		}
		return r[i].lo < r[j].lo
	})
	out := make([]interval, 0, len(r))
	for _, v := range r {
		if len(out) > 0 && v.lo <= out[len(out)-1].hi+1 {
			if v.hi > out[len(out)-1].hi {
				out[len(out)-1].hi = v.hi
			}
		} else {
			out = append(out, v)
		}
	}
	return out
}
func contains(r []interval, n uint64) bool {
	for _, v := range r {
		if n >= v.lo && n <= v.hi {
			return true
		}
	}
	return false
}
func uniqueStrings(in []string) []string {
	m := map[string]bool{}
	out := []string{}
	for _, s := range in {
		if s != "" && !m[s] {
			m[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}
