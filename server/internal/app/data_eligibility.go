package app

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strings"
	"time"

	"cfw-policy-llm-agent/server/internal/domain"
	"cfw-policy-llm-agent/server/internal/storage"
	"cfw-policy-llm-agent/server/internal/workload"
)

var ErrDataEligibilityRequest = errors.New("DATA_ELIGIBILITY_REQUEST_INVALID")

// Policy thresholds are explicit engineering policy, not cloud service levels.
// Legacy is an audit label; callers can supply a versioned replacement.
type DataFreshnessPolicy struct {
	Version                    string `json:"version"`
	Legacy                     bool   `json:"legacy"`
	ConfigurationMaxAgeSeconds int    `json:"configuration_max_age_seconds"`
	MetadataMaxAgeSeconds      int    `json:"metadata_max_age_seconds"`
}

func LegacyDataFreshnessPolicy() DataFreshnessPolicy {
	return DataFreshnessPolicy{Version: "legacy-uncalibrated-15m-60m-v1", Legacy: true, ConfigurationMaxAgeSeconds: 15 * 60, MetadataMaxAgeSeconds: 60 * 60}
}
func (p DataFreshnessPolicy) Validate() error {
	if p.Version == "" || len(p.Version) > 128 {
		return ErrDataEligibilityRequest
	}
	if _, err := workload.Seconds(p.ConfigurationMaxAgeSeconds); err != nil {
		return ErrDataEligibilityRequest
	}
	if _, err := workload.Seconds(p.MetadataMaxAgeSeconds); err != nil {
		return ErrDataEligibilityRequest
	}
	return nil
}

type DataVersions struct {
	SchemaVersion                                                          int
	NormalizerVersion, KernelVersion, SemanticsProfileID, SemanticsVersion string
}
type DataEligibilityRequest struct {
	Scope                                                         domain.Scope
	Selection                                                     domain.Selection
	SourceRef, ConfigRevision                                     string
	Versions                                                      DataVersions
	Now, FreshAfter, KnownChangedAt                               time.Time
	Policy                                                        DataFreshnessPolicy
	Mode, PinnedSnapshotID                                        string
	RequireMetadata, RequireSecurityGroups, RequireNATAssociation bool
}
type EvidenceWindow struct {
	StartedAt  string `json:"started_at"`
	FinishedAt string `json:"finished_at"`
}
type DataProvenance struct {
	SourceKind          string               `json:"source_kind"`
	CollectedAt         string               `json:"collected_at"`
	Guarantee           string               `json:"guarantee"`
	PolicyVersion       string               `json:"policy_version"`
	ConfigurationWindow EvidenceWindow       `json:"configuration_window"`
	MetadataWindow      EvidenceWindow       `json:"metadata_window"`
	SecurityGroupWindow EvidenceWindow       `json:"security_group_window"`
	Manifest            domain.InputManifest `json:"input_manifest"`
}
type DataEligibilityDecision struct {
	SnapshotID string         `json:"snapshot_id"`
	Eligible   bool           `json:"eligible"`
	Reasons    []string       `json:"reasons"`
	Warnings   []string       `json:"warnings"`
	Provenance DataProvenance `json:"provenance"`
	Input      *domain.Input  `json:"-"`
}
type DataReusePlan struct {
	Reuse         bool                      `json:"reuse"`
	NeedRefresh   bool                      `json:"need_refresh"`
	SnapshotID    string                    `json:"snapshot_id,omitempty"`
	PolicyVersion string                    `json:"policy_version"`
	Decisions     []DataEligibilityDecision `json:"decisions"`
	Reasons       []string                  `json:"reasons"`
	SearchLimited bool                      `json:"search_limited"`
	Input         *domain.Input             `json:"-"`
}
type SnapshotCandidateReader interface {
	SnapshotCandidates(context.Context, storage.SnapshotCandidateQuery) ([]storage.SnapshotCandidate, error)
}

func (r DataEligibilityRequest) valid() bool {
	if r.Scope.Ref == "" || r.SourceRef == "" || r.Policy.Validate() != nil || r.Versions.SchemaVersion < 1 || r.Versions.NormalizerVersion == "" || r.Versions.KernelVersion == "" || r.Versions.SemanticsProfileID == "" || r.Versions.SemanticsVersion == "" {
		return false
	}
	if r.Mode != "prefer_fresh" && r.Mode != "force_refresh" && r.Mode != "pinned_snapshot" {
		return false
	}
	if r.Mode == "pinned_snapshot" && r.PinnedSnapshotID == "" {
		return false
	}
	if r.Selection.Kind == "gateway_policy" {
		return r.Scope.Boundary == "nat" && r.Scope.GatewayRef != "" && len(r.Selection.AssetIDs) == 0
	}
	if r.Selection.Kind != "assets" || len(r.Selection.AssetIDs) == 0 {
		return false
	}
	seen := map[string]bool{}
	for _, id := range r.Selection.AssetIDs {
		if id == "" || seen[id] {
			return false
		}
		seen[id] = true
	}
	return true
}
func addCode(values *[]string, code string) {
	for _, v := range *values {
		if v == code {
			return
		}
	}
	*values = append(*values, code)
}
func cachedInputManifest(in domain.Input) domain.InputManifest {
	return domain.InputManifest{SnapshotVersion: in.SnapshotVersion, ProfileVersion: in.ProfileVersion, IntentVersion: in.IntentVersion, BaselineVersion: in.Baseline.Version, NormalizerVersion: in.NormalizerVersion, KernelVersion: in.KernelVersion, ProviderSemantics: in.Semantics, PolicyScope: in.Scope, EvaluationAt: in.EvaluationAt, CollectedAt: in.CollectedAt, SourceKind: in.SourceKind}
}
func parseStamp(value string) (time.Time, bool) {
	at, err := time.Parse(time.RFC3339Nano, value)
	return at, err == nil
}
func extendWindow(w *EvidenceWindow, start, finish string) bool {
	a, ok1 := parseStamp(start)
	b, ok2 := parseStamp(finish)
	if !ok1 || !ok2 || b.Before(a) {
		return false
	}
	old, ok := parseStamp(w.StartedAt)
	if !ok || a.Before(old) {
		w.StartedAt = start
	}
	old, ok = parseStamp(w.FinishedAt)
	if !ok || b.After(old) {
		w.FinishedAt = finish
	}
	return true
}
func staleWindow(w EvidenceWindow, now time.Time, seconds int) (bool, bool) {
	start, a := parseStamp(w.StartedAt)
	finish, b := parseStamp(w.FinishedAt)
	if !a || !b || finish.Before(start) || start.After(now) || finish.After(now) {
		return false, false
	}
	limit, _ := workload.Seconds(seconds)
	return now.Sub(start) > limit, true
}
func currentBinding(c storage.SnapshotCandidate, r DataEligibilityRequest) bool {
	if c.Cloud == nil || c.Cloud.SourceRef != r.SourceRef || c.Cloud.ConfigRevision != r.ConfigRevision || c.Cloud.Content.SnapshotID != c.Snapshot.ID || c.Cloud.Content.ScopeRef != r.Scope.Ref || c.Snapshot.Input.Scope != r.Scope || !c.Cloud.Content.IdentityVerified || !c.Cloud.Content.ProductReady {
		return false
	}
	if c.Cloud.Content.Kind == "collect" {
		return c.ProducerStatus == "completed" || c.ProducerStatus == "partial"
	}
	// Changed configuration necessarily makes a prepared data product partial.
	// It still invalidates older stable data after its parent is committed; the
	// complete-only reuse rule must not erase that newer change observation.
	return c.Cloud.Content.Kind == "audit" && storage.Terminal(c.ProducerStatus) && (c.DataStatus == "complete" || c.DataStatus == "partial")
}

// EvaluateDataEligibility is pure: it never updates a snapshot, current heads,
// its original timestamps, a budget, a directory or a provider connection.
func EvaluateDataEligibility(c storage.SnapshotCandidate, r DataEligibilityRequest) DataEligibilityDecision {
	in := c.Snapshot.Input
	d := DataEligibilityDecision{SnapshotID: c.Snapshot.ID, Reasons: []string{}, Warnings: []string{}, Provenance: DataProvenance{SourceKind: in.SourceKind, CollectedAt: in.CollectedAt, PolicyVersion: r.Policy.Version, Manifest: cachedInputManifest(in)}}
	reject := func(code string) { addCode(&d.Reasons, code) }
	if !r.valid() {
		reject("INVALID_DATA_REQUEST")
		return d
	}
	if r.Now.IsZero() {
		r.Now = time.Now().UTC()
	}
	if r.Mode == "force_refresh" {
		reject("FORCE_REFRESH_REQUIRED")
		return d
	}
	pinned := r.Mode == "pinned_snapshot"
	offline := r.SourceRef == "local-offline" && (in.SourceKind == "synthetic" || in.SourceKind == "imported")
	if pinned && r.PinnedSnapshotID != c.Snapshot.ID {
		reject("SNAPSHOT_NOT_PINNED")
	}
	if c.Snapshot.ScopeRef != in.Scope.Ref || !reflect.DeepEqual(in.Scope, r.Scope) {
		reject("SCOPE_MISMATCH")
	}
	if c.Snapshot.SourceKind != in.SourceKind {
		reject("SOURCE_KIND_MISMATCH")
	}
	if in.SchemaVersion != r.Versions.SchemaVersion {
		reject("SCHEMA_VERSION_MISMATCH")
	}
	if in.NormalizerVersion != r.Versions.NormalizerVersion {
		reject("NORMALIZER_VERSION_MISMATCH")
	}
	if in.KernelVersion != r.Versions.KernelVersion {
		reject("KERNEL_VERSION_MISMATCH")
	}
	if in.Semantics.ProfileID != r.Versions.SemanticsProfileID || in.Semantics.Version != r.Versions.SemanticsVersion {
		reject("SEMANTICS_VERSION_MISMATCH")
	}
	selected := map[string]bool{}
	for _, id := range r.Selection.AssetIDs {
		selected[id] = true
	}
	covered := map[string]bool{}
	for _, id := range in.Selection.AssetIDs {
		if covered[id] {
			reject("SELECTION_MISMATCH")
		}
		covered[id] = true
	}
	if in.Selection.Kind != r.Selection.Kind {
		reject("SELECTION_MISMATCH")
	}
	if in.Selection.Kind == "assets" && len(in.Selection.AssetIDs) != len(in.Assets) {
		reject("SELECTION_MISMATCH")
	}
	assets := map[string]domain.Asset{}
	for _, a := range in.Assets {
		if _, duplicate := assets[a.ID]; duplicate {
			reject("SELECTION_MISMATCH")
		}
		assets[a.ID] = a
	}
	for id := range selected {
		if !covered[id] {
			reject("ASSET_NOT_COVERED")
		}
		a, ok := assets[id]
		if !ok {
			reject("ASSET_NOT_COVERED")
		} else if a.ScopeRef != r.Scope.Ref {
			reject("ASSET_SCOPE_MISMATCH")
		}
	}
	if _, valid := parseStamp(in.CollectedAt); !valid {
		reject("COLLECTION_TIME_UNKNOWN")
	}
	strict := !pinned && !offline
	if !strict {
		extendWindow(&d.Provenance.ConfigurationWindow, in.CollectedAt, in.CollectedAt)
		d.Provenance.MetadataWindow = d.Provenance.ConfigurationWindow
	}
	if strict {
		d.Provenance.Guarantee = "readonly_collection_conditions"
		if in.SourceKind != "live_readonly" {
			reject("SOURCE_NOT_LIVE")
		}
		if c.Cloud == nil {
			reject("CLOUD_BINDING_MISSING")
		} else {
			if c.Cloud.SourceRef != r.SourceRef {
				reject("SOURCE_MISMATCH")
			}
			if c.Cloud.ConfigRevision != r.ConfigRevision {
				reject("CONFIG_REVISION_MISMATCH")
			}
			if c.Cloud.Content.Kind == "audit" {
				if !storage.Terminal(c.ProducerStatus) {
					reject("PRODUCER_NOT_COMMITTED")
				}
				if c.DataStatus != "complete" {
					reject("PREPARED_DATA_INCOMPLETE")
				}
			} else {
				if c.ProducerStatus != "completed" && c.ProducerStatus != "partial" {
					reject("PRODUCER_NOT_COMMITTED")
				}
				if c.ProducerStatus == "partial" {
					reject("PRODUCER_PARTIAL")
				}
			}
			if len(c.Cloud.Content.Limitations) > 0 {
				reject("PRODUCER_LIMITATIONS")
			}
			if c.Cloud.Content.Kind != "collect" && c.Cloud.Content.Kind != "audit" || c.Cloud.Content.SnapshotID != c.Snapshot.ID || c.Cloud.Content.ScopeRef != r.Scope.Ref {
				reject("CLOUD_BINDING_MISMATCH")
			}
			if !c.Cloud.Content.IdentityVerified {
				reject("IDENTITY_UNVERIFIED")
			}
			if !c.Cloud.Content.ProductReady {
				reject("PRODUCT_NOT_READY")
			}
			if len(c.Cloud.Content.Observations) == 0 {
				reject("OBSERVATION_MISSING")
			}
			rounds := map[int]bool{}
			for _, o := range c.Cloud.Content.Observations {
				if !o.EnumerationComplete {
					reject("OBSERVATION_INCOMPLETE")
				}
				var unused EvidenceWindow
				target := &d.Provenance.ConfigurationWindow
				switch o.Operation {
				case "DescribeInstances":
					target = &d.Provenance.MetadataWindow
				case "DescribeSecurityGroupAttribute":
					target = &unused
				default:
					rounds[o.Round] = true
				}

				if !extendWindow(target, o.StartedAt, o.FinishedAt) {
					reject("WINDOW_INVALID")
				}
			}
			if !rounds[1] || !rounds[2] {
				reject("OBSERVATION_ROUNDS_INCOMPLETE")
			}
		}
		if d.Provenance.ConfigurationWindow.StartedAt == "" {
			extendWindow(&d.Provenance.ConfigurationWindow, in.CollectedAt, in.CollectedAt)
		}
		if d.Provenance.MetadataWindow.StartedAt == "" {
			extendWindow(&d.Provenance.MetadataWindow, in.CollectedAt, in.CollectedAt)
		}
		if !in.Completeness.Declared || !in.Completeness.EnumerationComplete {
			reject("ENUMERATION_INCOMPLETE")
		}
		switch in.Completeness.ObservationConsistency {
		case "observed_stable":
		case "changed":
			reject("CONFIGURATION_CHANGED")
		default:
			reject("OBSERVATION_UNVERIFIED")
		}
		if len(in.Completeness.KnownGaps) > 0 {
			reject("KNOWN_GAPS_PRESENT")
		}
		if stale, valid := staleWindow(d.Provenance.ConfigurationWindow, r.Now, r.Policy.ConfigurationMaxAgeSeconds); !valid {
			reject("WINDOW_INVALID")
		} else if stale {
			reject("CONFIGURATION_STALE")
		}
		if r.RequireSecurityGroups && r.Selection.Kind == "assets" {
			if stale, valid := staleWindow(d.Provenance.MetadataWindow, r.Now, r.Policy.ConfigurationMaxAgeSeconds); !valid {
				reject("WINDOW_INVALID")
			} else if stale {
				reject("SECURITY_GROUP_ASSOCIATION_STALE")
			}
		}
		if r.RequireMetadata {
			if stale, valid := staleWindow(d.Provenance.MetadataWindow, r.Now, r.Policy.MetadataMaxAgeSeconds); !valid {
				reject("WINDOW_INVALID")
			} else if stale {
				reject("METADATA_STALE")
			}
		}
		start, ok := parseStamp(d.Provenance.ConfigurationWindow.StartedAt)
		if ok {
			if !r.FreshAfter.IsZero() && start.Before(r.FreshAfter) {
				reject("BEFORE_FRESH_AFTER")
			}
			if !r.KnownChangedAt.IsZero() && start.Before(r.KnownChangedAt) {
				reject("KNOWN_CHANGE_AFTER_SNAPSHOT")
			}
		}
	} else {
		if pinned {
			d.Provenance.Guarantee = "fixed_history"
		} else if in.SourceKind == "imported" {
			d.Provenance.Guarantee = "imported_assumption"
		} else {
			d.Provenance.Guarantee = "synthetic_assumption"
		}
		addCode(&d.Warnings, "FIXED_ARTIFACT_NOT_CURRENT_CLOUD")
		if !in.Completeness.Declared || !in.Completeness.EnumerationComplete || len(in.Completeness.KnownGaps) > 0 || in.Completeness.ObservationConsistency != "fixed_artifact" {
			addCode(&d.Warnings, "HISTORICAL_OR_DECLARED_LIMITATIONS")
		}
	}
	for id := range selected {
		a, ok := assets[id]
		if !ok {
			continue
		}
		// Legacy live snapshots collapsed distinct assets into one partition
		// assertion. They remain readable when pinned, but cannot satisfy a new
		// current-cloud request after per-asset protection was introduced.
		if strict && r.Scope.Boundary == "internet" && a.Protection == nil {
			reject("ASSET_PROTECTION_NOT_RECORDED")
		}
		if strict && r.RequireMetadata && (!a.MetadataComplete || a.ProviderResourceID == "" || a.Region == "" || !strings.EqualFold(a.ResourceType, "ecs")) {
			reject("METADATA_INCOMPLETE")
		}
		if strict && r.RequireSecurityGroups {
			if a.SecurityGroupCollection != "complete" {
				if a.SecurityGroupCollection == "partial" {
					reject("SECURITY_GROUPS_PARTIAL")
				} else {
					reject("SECURITY_GROUPS_NOT_COLLECTED")
				}
			}
			seen := map[string]bool{}
			for _, g := range a.SecurityGroups {
				if g.ID == "" || g.Region == "" || g.Region != a.Region || seen[g.ID] {
					reject("SECURITY_GROUP_ASSOCIATION_INVALID")
				}
				seen[g.ID] = true
				if !g.Complete || len(g.Limitations) > 0 {
					reject("SECURITY_GROUPS_PARTIAL")
				}
				if !extendWindow(&d.Provenance.SecurityGroupWindow, g.ObservedAt, g.ObservedAt) {
					reject("SECURITY_GROUP_TIME_UNKNOWN")
				}
			}
		}
		if strict && r.RequireNATAssociation && r.Scope.Boundary == "nat" {
			b := a.Association
			if b.Status != "evidenced" || b.GatewayRef != r.Scope.GatewayRef || (b.ObservationPoint != "before_translation" && b.ObservationPoint != "after_translation") || b.EvidenceRef == "" {
				reject("NAT_ASSOCIATION_INCOMPLETE")
			}
			relationWindow := EvidenceWindow{StartedAt: b.ObservedAt, FinishedAt: b.ObservedAt}
			if stale, valid := staleWindow(relationWindow, r.Now, r.Policy.ConfigurationMaxAgeSeconds); !valid {
				reject("NAT_ASSOCIATION_INCOMPLETE")
			} else if stale {
				reject("NAT_ASSOCIATION_STALE")
			}
		}
	}
	if strict && r.RequireSecurityGroups && d.Provenance.SecurityGroupWindow.StartedAt != "" {
		if stale, valid := staleWindow(d.Provenance.SecurityGroupWindow, r.Now, r.Policy.ConfigurationMaxAgeSeconds); !valid {
			reject("WINDOW_INVALID")
		} else if stale {
			reject("SECURITY_GROUP_STALE")
		}
	}
	if strict && r.RequireMetadata && r.Selection.Kind == "gateway_policy" {
		g := in.GatewayMetadata
		if g == nil || g.ResourceType != "nat_gateway" || g.Region == "" || g.ResourceID == "" {
			reject("GATEWAY_METADATA_INCOMPLETE")
		}
	}
	for _, p := range in.Policies {
		if p.ScopeRef != r.Scope.Ref {
			reject("POLICY_SCOPE_MISMATCH")
		}
		for _, ref := range []string{p.Source.Ref, p.Destination.Ref} {
			if ref != "" {
				a, ok := in.AddressRefs[ref]
				if !ok || !a.Complete || a.ScopeRef != r.Scope.Ref {
					if strict {
						reject("REFERENCE_NOT_CLOSED")
					} else {
						addCode(&d.Warnings, "REFERENCE_NOT_CLOSED")
					}
				}
			}
		}
		if p.Ports.Ref != "" {
			a, ok := in.PortRefs[p.Ports.Ref]
			if !ok || !a.Complete || a.ScopeRef != r.Scope.Ref {
				if strict {
					reject("REFERENCE_NOT_CLOSED")
				} else {
					addCode(&d.Warnings, "REFERENCE_NOT_CLOSED")
				}
			}
		}
	}
	if len(d.Reasons) == 0 {
		projected, err := projectCachedInput(in, r.Selection)
		if err != nil {
			reject("INPUT_PROJECTION_INVALID")
		} else {
			d.Eligible = true
			d.Input = &projected
		}
	}
	sort.Strings(d.Reasons)
	sort.Strings(d.Warnings)
	return d
}
func projectCachedInput(in domain.Input, selection domain.Selection) (domain.Input, error) {
	data, err := json.Marshal(in)
	if err != nil {
		return domain.Input{}, err
	}
	var out domain.Input
	if err = json.Unmarshal(data, &out); err != nil {
		return domain.Input{}, err
	}
	selected := map[string]bool{}
	for _, id := range selection.AssetIDs {
		selected[id] = true
	}
	out.Selection = domain.Selection{Kind: selection.Kind, AssetIDs: append([]string{}, selection.AssetIDs...)}
	sort.Strings(out.Selection.AssetIDs)
	out.Assets = []domain.Asset{}
	for _, a := range in.Assets {
		if selected[a.ID] {
			out.Assets = append(out.Assets, a)
		}
	}
	// Detach the selected structs as well; no mutable maps/slices escape back to
	// the committed candidate. Whole-partition ACLs remain for matching order.
	data, err = json.Marshal(out.Assets)
	if err != nil {
		return domain.Input{}, err
	}
	if err = json.Unmarshal(data, &out.Assets); err != nil {
		return domain.Input{}, err
	}
	out.Baseline.Exceptions = []domain.Exception{}
	for _, e := range in.Baseline.Exceptions {
		if selected[e.SubjectRef] {
			out.Baseline.Exceptions = append(out.Baseline.Exceptions, e)
		}
	}
	usedAddresses, usedPorts := map[string]bool{}, map[string]bool{}
	for _, p := range out.Policies {
		usedAddresses[p.Source.Ref] = true
		usedAddresses[p.Destination.Ref] = true
		usedPorts[p.Ports.Ref] = true
	}
	for ref := range out.AddressRefs {
		if !usedAddresses[ref] {
			delete(out.AddressRefs, ref)
		}
	}
	for ref := range out.PortRefs {
		if !usedPorts[ref] {
			delete(out.PortRefs, ref)
		}
	}
	if out.Scope.Boundary != "nat" {
		out.GatewayMetadata = nil
	}
	return out, nil
}

// FindEligibleSnapshot considers only already committed immutable products. A
// known newer change prevents silently falling back to an older stable cache.
// It has no CloudReader, model factory, credential provider or inflight map.
func FindEligibleSnapshot(ctx context.Context, reader SnapshotCandidateReader, r DataEligibilityRequest, limit int) (DataReusePlan, error) {
	plan := DataReusePlan{NeedRefresh: true, PolicyVersion: r.Policy.Version, Decisions: []DataEligibilityDecision{}, Reasons: []string{}}
	if reader == nil || !r.valid() || limit < 1 || limit > 100 {
		return plan, ErrDataEligibilityRequest
	}
	if r.Now.IsZero() {
		r.Now = time.Now().UTC()
	}
	if r.Mode == "force_refresh" {
		plan.Reasons = []string{"FORCE_REFRESH_REQUIRED"}
		return plan, nil
	}
	query := storage.SnapshotCandidateQuery{ScopeRef: r.Scope.Ref, Limit: limit}
	if r.Mode == "pinned_snapshot" {
		query.SnapshotID = r.PinnedSnapshotID
	}
	candidates, err := reader.SnapshotCandidates(ctx, query)
	if err != nil {
		return plan, err
	}
	plan.SearchLimited = len(candidates) == limit
	if r.Mode == "prefer_fresh" && r.SourceRef != "local-offline" {
		for _, c := range candidates {
			if currentBinding(c, r) && c.Snapshot.Input.Completeness.ObservationConsistency == "changed" {
				at, ok := parseStamp(c.Snapshot.Input.CollectedAt)
				for _, o := range c.Cloud.Content.Observations {
					if finish, valid := parseStamp(o.FinishedAt); valid && (!ok || finish.After(at)) {
						at, ok = finish, true
					}
				}
				if ok && at.After(r.KnownChangedAt) {
					r.KnownChangedAt = at
				}
			}
		}
	}
	for _, c := range candidates {
		if err := ctx.Err(); err != nil {
			return plan, err
		}
		decision := EvaluateDataEligibility(c, r)
		plan.Decisions = append(plan.Decisions, decision)
		if !plan.Reuse && decision.Eligible {
			plan.Reuse = true
			plan.NeedRefresh = false
			plan.SnapshotID = decision.SnapshotID
			plan.Input = decision.Input
		}
	}
	if !plan.Reuse {
		plan.Reasons = []string{"NO_QUALIFIED_CACHE"}
	}
	return plan, nil
}
