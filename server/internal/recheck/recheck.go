// Package recheck compares fixed deterministic audit results. A disappearance
// is only "not_detected" in comparable configuration evidence, never proof of
// a real cloud change or restored runtime connectivity.
package recheck

import (
	"errors"
	"reflect"
	"sort"
	"strings"
	"time"

	"cfw-policy-llm-agent/server/internal/domain"
)

var ErrInvalidInput = errors.New("invalid or unbound recheck input")

type IssueChange struct {
	IssueKey      string   `json:"issue_key"`
	State         string   `json:"state"`
	OldFindingIDs []string `json:"old_finding_ids"`
	NewFindingIDs []string `json:"new_finding_ids"`
}
type Comparison struct {
	SchemaVersion int           `json:"schema_version"`
	Comparable    bool          `json:"comparable"`
	Reasons       []string      `json:"reasons"`
	Issues        []IssueChange `json:"issues"`
}

func inputManifest(i domain.Input) domain.InputManifest {
	return domain.InputManifest{SnapshotVersion: i.SnapshotVersion, ProfileVersion: i.ProfileVersion, IntentVersion: i.IntentVersion, BaselineVersion: i.Baseline.Version, NormalizerVersion: i.NormalizerVersion, KernelVersion: i.KernelVersion, ProviderSemantics: i.Semantics, PolicyScope: i.Scope, EvaluationAt: i.EvaluationAt, CollectedAt: i.CollectedAt, SourceKind: i.SourceKind}
}
func instant(s string) (time.Time, error) { return time.Parse(time.RFC3339Nano, s) }
func bound(i domain.Input, r domain.Result) bool {
	if i.SchemaVersion != 1 || r.SchemaVersion != 1 || r.Coverage.EvaluatedCells < 0 || r.Coverage.EvaluatedQueries < 0 {
		return false
	}
	expected := inputManifest(i)
	actual := r.Manifest
	a, e1 := instant(expected.EvaluationAt)
	b, e2 := instant(actual.EvaluationAt)
	if e1 != nil || e2 != nil || !a.Equal(b) {
		return false
	}
	a, e1 = instant(expected.CollectedAt)
	b, e2 = instant(actual.CollectedAt)
	if e1 != nil || e2 != nil || !a.Equal(b) {
		return false
	}
	expected.EvaluationAt = ""
	actual.EvaluationAt = ""
	expected.CollectedAt = ""
	actual.CollectedAt = ""
	return reflect.DeepEqual(expected, actual)
}
func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	ac := append([]string(nil), a...)
	bc := append([]string(nil), b...)
	sort.Strings(ac)
	sort.Strings(bc)
	return reflect.DeepEqual(ac, bc)
}
func validVerdict(v string) bool {
	return v == "pass" || v == "fail" || v == "needs_confirmation" || v == "insufficient_data" || v == "unsupported" || v == "not_applicable"
}
func complete(i domain.Input, r domain.Result) bool {
	return i.Completeness.Declared && i.Completeness.EnumerationComplete && len(i.Completeness.KnownGaps) == 0 && (i.Completeness.ObservationConsistency == "fixed_artifact" || i.Completeness.ObservationConsistency == "observed_stable") && r.Coverage.Complete
}
func canonicalScope(s domain.Scope) domain.Scope { s.Ref = ""; return s }
func checkKey(f domain.Finding) string {
	return strings.Join([]string{f.RuleID, f.Subcheck, f.SubjectRef, f.ServiceRef}, "\x00")
}
func records(r domain.Result) (map[string][]domain.Finding, map[string]string, error) {
	groups := map[string][]domain.Finding{}
	versions := map[string]string{}
	ids := map[string]bool{}
	for _, f := range r.Findings {
		if f.ID == "" || ids[f.ID] || f.ScopeRef != r.Manifest.PolicyScope.Ref || !validVerdict(f.Verdict) {
			return nil, nil, ErrInvalidInput
		}
		ids[f.ID] = true
		if f.Origin == "model" {
			continue
		}
		if f.Origin != "rule" || f.RuleID == "" || f.RuleVersion == "" || f.SubjectRef == "" || f.IssueKey == "" {
			return nil, nil, ErrInvalidInput
		}
		key := checkKey(f)
		if prior, ok := versions[key]; ok && prior != f.RuleVersion {
			return nil, nil, ErrInvalidInput
		}
		versions[key] = f.RuleVersion
		groups[f.IssueKey] = append(groups[f.IssueKey], f)
	}
	return groups, versions, nil
}
func hasFail(fs []domain.Finding) bool {
	for _, f := range fs {
		if f.Verdict == "fail" {
			return true
		}
	}
	return false
}
func examined(fs []domain.Finding) bool {
	if len(fs) == 0 {
		return false
	}
	for _, f := range fs {
		if f.Verdict != "pass" && f.Verdict != "fail" {
			return false
		}
	}
	return true
}
func findingIDs(fs []domain.Finding) []string {
	ids := make([]string, 0, len(fs))
	for _, f := range fs {
		ids = append(ids, f.ID)
	}
	sort.Strings(ids)
	return ids
}

// Compare only groups original rule findings; nondeterministic model hypotheses
// cannot make a deterministic issue disappear. Snapshot versions may differ;
// the meaning, selected objects and coverage must remain comparable.
func Compare(oldInput domain.Input, oldResult domain.Result, newInput domain.Input, newResult domain.Result) (Comparison, error) {
	comparison := Comparison{SchemaVersion: 1, Reasons: []string{}, Issues: []IssueChange{}}
	if !bound(oldInput, oldResult) || !bound(newInput, newResult) {
		return comparison, ErrInvalidInput
	}
	oldGroups, oldChecks, err := records(oldResult)
	if err != nil {
		return comparison, err
	}
	newGroups, newChecks, err := records(newResult)
	if err != nil {
		return comparison, err
	}
	reasons := map[string]bool{}
	add := func(reason string) { reasons[reason] = true }
	if !reflect.DeepEqual(canonicalScope(oldInput.Scope), canonicalScope(newInput.Scope)) {
		add("scope_changed")
	}
	if oldInput.ProfileVersion != newInput.ProfileVersion {
		add("profile_version_changed")
	}
	if oldInput.IntentVersion != newInput.IntentVersion {
		add("intent_version_changed")
	}
	if oldInput.Baseline.Version != newInput.Baseline.Version || !sameSet(oldInput.Baseline.EnabledChecks, newInput.Baseline.EnabledChecks) || !reflect.DeepEqual(oldInput.Baseline.RiskPorts, newInput.Baseline.RiskPorts) || !reflect.DeepEqual(oldInput.Baseline.Exceptions, newInput.Baseline.Exceptions) {
		add("baseline_changed")
	}
	if oldInput.KernelVersion != newInput.KernelVersion {
		add("kernel_version_changed")
	}
	if oldInput.NormalizerVersion != newInput.NormalizerVersion {
		add("normalizer_version_changed")
	}
	if !reflect.DeepEqual(oldInput.Semantics, newInput.Semantics) {
		add("semantics_changed")
	}
	if oldInput.SourceKind != newInput.SourceKind {
		add("source_kind_changed")
	}
	a, _ := instant(oldInput.EvaluationAt)
	b, _ := instant(newInput.EvaluationAt)
	if !a.Equal(b) {
		add("evaluation_time_changed")
	}
	if !complete(oldInput, oldResult) || !complete(newInput, newResult) {
		add("incomplete_coverage")
	}
	if oldInput.Selection.Kind != newInput.Selection.Kind || !sameSet(oldInput.Selection.AssetIDs, newInput.Selection.AssetIDs) {
		add("selection_changed")
	}
	compareObjects(oldInput, newInput, add)
	for key, version := range oldChecks {
		other, exists := newChecks[key]
		if !exists {
			add("check_missing")
		} else if other != version {
			add("rule_version_changed")
		}
	}
	for key := range newChecks {
		if _, exists := oldChecks[key]; !exists {
			add("check_inventory_changed")
		}
	}
	// In particular, a missing, inapplicable or unknown replacement is not proof
	// that an older failure ceased to be detected in comparable evidence.
	for key, old := range oldGroups {
		if hasFail(old) && !examined(newGroups[key]) {
			add("issue_not_examined")
		}
	}
	if len(reasons) > 0 {
		for reason := range reasons {
			comparison.Reasons = append(comparison.Reasons, reason)
		}
		sort.Strings(comparison.Reasons)
		return comparison, nil
	}
	comparison.Comparable = true
	keys := map[string]bool{}
	for key := range oldGroups {
		keys[key] = true
	}
	for key := range newGroups {
		keys[key] = true
	}
	ordered := make([]string, 0, len(keys))
	for key := range keys {
		ordered = append(ordered, key)
	}
	sort.Strings(ordered)
	for _, key := range ordered {
		old, new := oldGroups[key], newGroups[key]
		oldFail, newFail := hasFail(old), hasFail(new)
		if !oldFail && !newFail {
			continue
		}
		state := "persistent"
		if oldFail && !newFail {
			state = "not_detected"
		} else if !oldFail && newFail {
			state = "new"
		}
		comparison.Issues = append(comparison.Issues, IssueChange{key, state, findingIDs(old), findingIDs(new)})
	}
	return comparison, nil
}

func compareObjects(oldInput, newInput domain.Input, add func(string)) {
	oldAssets := map[string]domain.Asset{}
	newAssets := map[string]domain.Asset{}
	for _, asset := range oldInput.Assets {
		if _, duplicate := oldAssets[asset.ID]; duplicate {
			add("object_identity_ambiguous")
		}
		oldAssets[asset.ID] = asset
	}
	for _, asset := range newInput.Assets {
		if _, duplicate := newAssets[asset.ID]; duplicate {
			add("object_identity_ambiguous")
		}
		newAssets[asset.ID] = asset
	}
	for _, id := range oldInput.Selection.AssetIDs {
		old, oldExists := oldAssets[id]
		new, newExists := newAssets[id]
		if !oldExists || !newExists {
			add("object_missing")
			continue
		}
		if old.ScopeRef != oldInput.Scope.Ref || new.ScopeRef != newInput.Scope.Ref {
			add("object_scope_mismatch")
		}
		if old.Address != new.Address || old.AddressObserved != new.AddressObserved {
			add("object_endpoint_changed")
		}
		if !old.AddressObserved || !new.AddressObserved {
			add("endpoint_unverified")
		}
		if old.Association.GatewayRef != new.Association.GatewayRef || old.Association.ObservationPoint != new.Association.ObservationPoint {
			add("observation_point_changed")
		}
		if oldInput.Scope.Boundary == "nat" && (old.Association.Status != "evidenced" || new.Association.Status != "evidenced" || old.Association.ObservationPoint == "" || new.Association.ObservationPoint == "") {
			add("association_unverified")
		}
		if !reflect.DeepEqual(old.Services, new.Services) || old.AllowedConfirmed != new.AllowedConfirmed || old.AllowedComplete != new.AllowedComplete || old.DenyAll != new.DenyAll || !reflect.DeepEqual(old.Allowed, new.Allowed) {
			add("object_business_changed")
		}
	}
}
