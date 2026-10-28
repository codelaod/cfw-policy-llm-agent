package storage

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"cfw-policy-llm-agent/server/internal/domain"
)

func resolvePointer(root any, path string) (any, bool) {
	if path == "" || !strings.HasPrefix(path, "/") {
		return nil, false
	}
	current := root
	for _, token := range strings.Split(path[1:], "/") {
		for i := 0; i < len(token); i++ {
			if token[i] == '~' {
				if i+1 >= len(token) || (token[i+1] != '0' && token[i+1] != '1') {
					return nil, false
				}
				i++
			}
		}
		token = strings.ReplaceAll(strings.ReplaceAll(token, "~1", "/"), "~0", "~")
		switch value := current.(type) {
		case map[string]any:
			var ok bool
			current, ok = value[token]
			if !ok {
				return nil, false
			}
		case []any:
			index, err := strconv.Atoi(token)
			if err != nil || index < 0 || index >= len(value) || strconv.Itoa(index) != token {
				return nil, false
			}
			current = value[index]
		default:
			return nil, false
		}
	}
	return current, true
}

func enum(value string, options ...string) bool {
	for _, option := range options {
		if value == option {
			return true
		}
	}
	return false
}

// Persistence independently enforces origin, assurance and evidence identity.
// Only the deterministic kernel can calculate the facts; this validation keeps
// later application/agent code from storing upgraded or dangling projections.
func validateFrozenResult(in domain.Input, result domain.Result) error {
	if result.SchemaVersion != 1 {
		return ErrInputInvalid
	}
	basis := map[string]string{"synthetic": "synthetic_assumption", "imported": "imported_assumption", "live_readonly": "live_observed"}[in.SourceKind]
	if basis == "" {
		return ErrInputInvalid
	}
	collected, err := time.Parse(time.RFC3339Nano, in.CollectedAt)
	if err != nil {
		return ErrInputInvalid
	}
	rootBytes, err := json.Marshal(in)
	if err != nil {
		return ErrInputInvalid
	}
	var root any
	if json.Unmarshal(rootBytes, &root) != nil {
		return ErrInputInvalid
	}
	subjects := map[string]bool{in.Scope.Ref: true, in.Scope.PolicySetRef: true}
	if in.Scope.GatewayRef != "" {
		subjects[in.Scope.GatewayRef] = true
	}
	for _, id := range in.Selection.AssetIDs {
		subjects[id] = true
	}
	services := map[string]map[string]bool{}
	for _, asset := range in.Assets {
		services[asset.ID] = map[string]bool{}
		for _, service := range asset.Services {
			services[asset.ID][service.ID] = true
		}
	}
	policies := map[string]int{}
	for i, policy := range in.Policies {
		if policy.ScopeRef == in.Scope.Ref {
			policies[policy.ID] = i
		}
	}
	enabled := map[string]bool{}
	for _, id := range in.Baseline.EnabledChecks {
		enabled[id] = true
	}
	evidence := map[string]domain.Evidence{}
	for _, item := range result.Evidence {
		if item.ID == "" || evidence[item.ID].ID != "" || !subjects[item.SubjectRef] || item.ScopeRef != in.Scope.Ref || item.SnapshotVersion != in.SnapshotVersion {
			return ErrInputInvalid
		}
		observed, e := time.Parse(time.RFC3339Nano, item.ObservedAt)
		if e != nil || !observed.Equal(collected) {
			return ErrInputInvalid
		}
		if _, ok := resolvePointer(root, item.Field); !ok {
			return ErrInputInvalid
		}
		if item.PolicyRef != "" {
			i, ok := policies[item.PolicyRef]
			prefix := "/policies/" + strconv.Itoa(i)
			if !ok || (item.Field != prefix && !strings.HasPrefix(item.Field, prefix+"/")) {
				return ErrInputInvalid
			}
		}
		if strings.HasPrefix(item.Field, "/assets/") {
			part := strings.Split(item.Field, "/")
			if len(part) < 3 {
				return ErrInputInvalid
			}
			i, e := strconv.Atoi(part[2])
			if e != nil || i < 0 || i >= len(in.Assets) || in.Assets[i].ID != item.SubjectRef {
				return ErrInputInvalid
			}
		}
		evidence[item.ID] = item
	}
	seen := map[string]bool{}
	for _, finding := range result.Findings {
		if finding.ID == "" || seen[finding.ID] || !enabled[finding.RuleID] || finding.RuleVersion != in.Baseline.Version || finding.Origin != "rule" || finding.ScopeRef != in.Scope.Ref || !subjects[finding.SubjectRef] || finding.AssessmentBasis != basis || len(finding.Assumptions) == 0 || len(finding.EvidenceRefs) == 0 {
			return ErrInputInvalid
		}
		if finding.ServiceRef != "" && !services[finding.SubjectRef][finding.ServiceRef] {
			return ErrInputInvalid
		}
		if finding.QueryScope.SubjectRef != "" && finding.QueryScope.SubjectRef != finding.SubjectRef {
			return ErrInputInvalid
		}
		if finding.QueryScope.ServiceRef != "" && finding.QueryScope.ServiceRef != finding.ServiceRef {
			return ErrInputInvalid
		}
		if !enum(finding.Verdict, "pass", "fail", "needs_confirmation", "insufficient_data", "unsupported", "not_applicable") || !enum(finding.Severity, "high", "medium", "low", "information", "unassessed") || !enum(finding.RemediationPriority, "R1", "R2", "R3", "V0") {
			return ErrInputInvalid
		}
		for _, ref := range finding.EvidenceRefs {
			item, ok := evidence[ref]
			if !ok || item.SubjectRef != finding.SubjectRef {
				return ErrInputInvalid
			}
		}
		for _, cell := range finding.Differences {
			if cell.PolicyRef != "" {
				if _, ok := policies[cell.PolicyRef]; !ok {
					return ErrInputInvalid
				}
			}
		}
		seen[finding.ID] = true
	}
	return nil
}
