package agentquery

import (
	"cfw-policy-llm-agent/server/internal/domain"
	"encoding/json"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

var ipv4Literal = regexp.MustCompile(`\b(?:[0-9]{1,3}\.){3}[0-9]{1,3}(?:/[0-9]{1,2})?\b`)
var rfc3339Literal = regexp.MustCompile(`\b[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(?:\.[0-9]+)?(?:Z|[+-][0-9]{2}:[0-9]{2})\b`)
var ipv6Literal = regexp.MustCompile(`(?i)(?:[0-9a-f]{0,4}:){2,}[0-9a-f:]+`)
var urlOrCommand = regexp.MustCompile("(?i)(?:https?|file|ftp|ssh)://|www\\.|\\b(?:curl|wget|bash|sh|zsh|fish|powershell|cmd|sudo|python|node|rm|rmdir|del|erase|mkdir|touch|chmod|chown|cp|mv|dd|mkfs|kill|systemctl|kubectl|docker|aliyun|aws|az|gcloud|ssh|scp|nc|ncat|telnet|ping|nmap)\\s+|\\$\\(|`|\\$\\{")
var sdkMethod = regexp.MustCompile(`\b(?:Describe|describe|Get|get|List|list|Create|create|Add|add|Delete|delete|Remove|remove|Modify|modify|Update|update|Set|set|Put|put|Enable|enable|Disable|disable|Authorize|authorize|Revoke|revoke|Start|start|Stop|stop|Run|run|Invoke|invoke|Assume|assume)[A-Z][A-Za-z0-9_]*\b`)

var cloudIdentity = regexp.MustCompile(`(?i)\b(?:i|sg|eip|vpc|vsw|gw|nat|acl|scope|policy|asset|service|address|evidence|business|association|finding|gateway|reference|snapshot|profile|intent)[-_][a-z0-9][a-z0-9_-]{3,}\b`)

// Reserved reasoning tags and wire fields cannot become public explanations or
// replayable history. Ordinary evidence-based analysis remains allowed.
var privateWireMarker = regexp.MustCompile("(?i)<\\s*/?\\s*(?:think|analysis)(?:\\s+[^>]*)?\\s*/?>|\\breasoning_content\\b|[\"'](?:reasoning|tool_calls|tool_call_id)[\"']\\s*:|[\"']role[\"']\\s*:\\s*[\"'](?:tool|function|system|developer)[\"']")

func (s *Service) textSafe(text string, max int, allowInternal bool) bool {
	if len(text) > max || !utf8.ValidString(text) || privateWireMarker.MatchString(text) {
		return false
	}
	for raw := range s.forbidden {
		if strings.Contains(text, raw) {
			return false
		}
	}
	probe := text
	aliases := make([]string, 0, len(s.refs))
	for alias := range s.refs {
		if allowInternal || s.issued[alias] {
			aliases = append(aliases, alias)
		}
	}
	sort.Slice(aliases, func(i, j int) bool { return len(aliases[i]) > len(aliases[j]) })
	for _, alias := range aliases {
		probe = replaceIdentity(probe, alias, "reference")
	}
	for raw := range s.replacements {
		if len(raw) >= 4 && strings.Contains(probe, raw) {
			return false
		}
	}
	// Full validated timestamps are not endpoint literals. Keep all other
	// checks on the original text so this exception cannot hide a URL or ID.
	networkProbe := rfc3339Literal.ReplaceAllStringFunc(probe, func(value string) string {
		if _, err := time.Parse(time.RFC3339Nano, value); err == nil {
			return "[fixed timestamp]"
		}
		return value
	})
	return !ipv4Literal.MatchString(networkProbe) && !ipv6Literal.MatchString(networkProbe) && !urlOrCommand.MatchString(probe) && !sdkMethod.MatchString(probe) && !cloudIdentity.MatchString(probe)
}

// ProjectText handles already-authorized, same-scope public history only. The
// caller still selects scope and blocks configured secrets before projection.
func (s *Service) ProjectText(text string) (string, error) {
	if len(text) > 16<<10 || !utf8.ValidString(text) || privateWireMarker.MatchString(text) {
		return "", ErrProjection
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for raw := range s.forbidden {
		if strings.Contains(text, raw) {
			return "", ErrProjection
		}
	}
	raws := make([]string, 0, len(s.replacements))
	for raw := range s.replacements {
		raws = append(raws, raw)
	}
	sort.Slice(raws, func(i, j int) bool { return len(raws[i]) > len(raws[j]) })
	for _, raw := range raws {
		text = replaceIdentity(text, raw, s.replacements[raw])
	}
	if !s.textSafe(text, 16<<10, true) {
		return "", ErrProjection
	}
	return text, nil
}
func (s *Service) refsValid(refs []string, required bool, asset int) bool {
	if len(refs) > 32 || required && len(refs) == 0 {
		return false
	}
	for _, alias := range refs {
		r, e := s.use(alias, "evidence")
		if e != nil || !s.returned[alias] || asset >= 0 && r.asset >= 0 && asset != r.asset {
			return false
		}
	}
	return true
}
func (s *Service) textsValid(texts []string, required bool) bool {
	if len(texts) > 8 || required && len(texts) == 0 {
		return false
	}
	for _, text := range texts {
		if strings.TrimSpace(text) == "" || !s.textSafe(text, 2048, false) {
			return false
		}
	}
	return true
}
func (s *Service) validAlias(alias, kind string, returned bool) (reference, bool) {
	r, e := s.use(alias, kind)
	return r, e == nil && (!returned || s.returned[alias])
}
func (s *Service) ValidateFinal(text string) (Output, error) {
	var out Output
	if decode(text, &out, MaxOutputBytes) != nil {
		return Output{}, ErrFinal
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if out.ScopeRef != s.initial.ScopeRef || out.RuleVersion != s.input.Baseline.Version || out.SourceKind != s.input.SourceKind || out.AssessmentBasis != s.basis || strings.TrimSpace(out.Explanation) == "" || !s.textSafe(out.Explanation, 4096, false) || len(out.Roles) > 20 || len(out.Recommendations) > 20 || !s.textsValid(out.Questions, false) {
		return Output{}, ErrFinal
	}
	for _, role := range out.Roles {
		a, ok := s.validAlias(role.AssetRef, "asset", false)
		if !ok {
			return Output{}, ErrFinal
		}
		sv, ok := s.validAlias(role.ServiceRef, "service", true)
		if !ok || sv.asset != a.asset || !has([]string{"web", "database", "management", "middleware", "cache", "queue", "background", "unknown"}, role.Role) || !s.refsValid(role.SupportingEvidence, true, a.asset) || !s.refsValid(role.ContradictingEvidence, false, a.asset) || !s.textsValid(role.Unknowns, true) || !s.textsValid(role.Questions, true) {
			return Output{}, ErrFinal
		}
	}
	for _, rec := range out.Recommendations {
		if !s.validateRecommendation(rec) {
			return Output{}, ErrFinal
		}
	}
	if out.Roles == nil {
		out.Roles = []RoleCandidate{}
	}
	if out.Recommendations == nil {
		out.Recommendations = []Recommendation{}
	}
	if out.Questions == nil {
		out.Questions = []string{}
	}
	return out, nil
}
func (s *Service) validateRecommendation(rec Recommendation) bool {
	targetKind := map[string]string{"asset": "asset", "asset_service": "service", "business_intent": "business", "scope": "scope", "protection_scope": "scope", "policy": "policy", "policy_set": "policy_set"}[rec.TargetKind]
	if targetKind == "" || !has([]string{"known_gap", "applicability_assessment", "unknown_followup"}, rec.BasisKind) || !s.textsValid(rec.Conditions, true) || strings.TrimSpace(rec.Impact) == "" || !s.textSafe(rec.Impact, 2048, false) || strings.TrimSpace(rec.ManualNextStep) == "" || !s.textSafe(rec.ManualNextStep, 2048, false) {
		return false
	}
	target, ok := s.validAlias(rec.TargetRef, targetKind, targetKind != "asset" && targetKind != "scope")
	if !ok {
		return false
	}
	ai := -1
	if targetKind == "asset" || targetKind == "service" || targetKind == "business" {
		ai = target.asset
	}
	var business reference
	if rec.BusinessRef != "" {
		business, ok = s.validAlias(rec.BusinessRef, "business", true)
		if !ok || ai >= 0 && business.asset != ai {
			return false
		}
		if ai < 0 && targetKind == "policy" && !s.policyAssets[target.index][business.asset] {
			return false
		}
	}
	var finding reference
	if rec.FindingRef != "" {
		finding, ok = s.validAlias(rec.FindingRef, "finding", true)
		if !ok || s.result == nil {
			return false
		}
		if ai >= 0 && s.result.Findings[finding.index].SubjectRef != s.input.Assets[ai].ID {
			return false
		}
	}
	if !s.refsValid(rec.EvidenceRefs, true, ai) {
		return false
	}
	if rec.Kind != "policy_change" && (rec.AccessRef != "" || rec.Field != "") {
		return false
	}
	if rec.Kind != "component_consideration" && rec.ComponentTopic != "" {
		return false
	}
	switch rec.Kind {
	case "policy_change":
		field := "source"
		if s.input.Scope.Direction == "out" {
			field = "destination"
		}
		if rec.Field != field || !has([]string{"policy", "policy_set"}, rec.TargetKind) || rec.FindingRef == "" || !has([]string{"review_rule", "conditional_restrict", "conditional_allow_required"}, rec.Action) {
			return false
		}
		if rec.BusinessRef == "" && !(s.input.Selection.Kind == "gateway_policy" && rec.Action == "review_rule") {
			return false
		}
		if rec.BusinessRef != "" && s.result.Findings[finding.index].SubjectRef != s.input.Assets[business.asset].ID {
			return false
		}
		original := s.result.Findings[finding.index]
		if rec.BusinessRef != "" {
			if original.ServiceRef == "" && business.service >= 0 {
				return false
			}
			if original.ServiceRef != "" && (business.service < 0 || s.input.Assets[business.asset].Services[business.service].ID != original.ServiceRef) {
				return false
			}
		}
		if rec.BasisKind == "known_gap" && original.Verdict != "fail" {
			return false
		}
		if rec.Action != "review_rule" && rec.BasisKind != "known_gap" {
			return false
		}
		if targetKind == "policy" {
			related := false
			id := s.input.Policies[target.index].ID
			for _, cell := range original.Differences {
				related = related || cell.PolicyRef == id
			}
			for _, evidenceID := range original.EvidenceRefs {
				for _, evidence := range s.result.Evidence {
					related = related || evidence.ID == evidenceID && evidence.PolicyRef == id
				}
			}
			if !related {
				return false
			}
		}
		if rec.Action == "review_rule" {
			return rec.AccessRef == ""
		}
		state, ok := s.access[rec.AccessRef]
		if !ok || !s.issued[rec.AccessRef] || !s.returned[rec.AccessRef] || !state.confirmed || state.asset != business.asset || state.service != business.service {
			return false
		}
		if rec.Action == "conditional_restrict" {
			values := s.input.Assets[state.asset].Allowed
			if state.service >= 0 {
				values = s.input.Assets[state.asset].Services[state.service].Allowed
			}
			return state.kind == "allowed" && (state.complete || state.denyAll) && accessMatchesQuery(values, original.QueryScope, state.denyAll)
		}
		return state.kind == "required" && state.service >= 0 && accessMatchesQuery(s.input.Assets[state.asset].Services[state.service].Required, original.QueryScope, false)
	case "coverage_followup":
		if !has([]string{"protection_scope", "asset_service"}, rec.TargetKind) || !has([]string{"collect_missing_evidence", "review_rule"}, rec.Action) {
			return false
		}
		if rec.BasisKind == "known_gap" {
			return s.input.Protection.Applicable && s.input.Protection.Required && s.input.Protection.State == "off"
		}
		return rec.BasisKind == "unknown_followup"
	case "component_consideration":
		if !has([]string{"asset_service", "scope"}, rec.TargetKind) || !s.topics[rec.ComponentTopic] || rec.BasisKind == "known_gap" || rec.Action != "collect_missing_evidence" {
			return false
		}
		if rec.ComponentTopic == "waf" || rec.ComponentTopic == "database_segmentation" {
			if targetKind != "service" {
				return false
			}
			v := s.input.Assets[target.asset].Services[target.service]
			if v.RoleStatus != "confirmed" {
				return false
			}
			if rec.ComponentTopic == "database_segmentation" {
				return v.Role == "database"
			}
			if v.Role != "web" || v.PublicAccess != "required" || v.Protocol != "tcp" {
				return false
			}
			for _, p := range v.Ports {
				if p.From <= 80 && p.To >= 80 || p.From <= 443 && p.To >= 443 {
					return true
				}
			}
			return false
		}
		return true
	case "confirm_information":
		return has([]string{"asset", "asset_service", "business_intent", "scope"}, rec.TargetKind) && rec.BasisKind == "unknown_followup" && has([]string{"confirm_intent", "collect_missing_evidence"}, rec.Action)
	}
	return false
}

// MarshalOutput is a convenience for adapters; it does not replace validation.
func MarshalOutput(out Output) string { b, _ := json.Marshal(out); return string(b) }

func identityByte(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || strings.ContainsRune("_-.:/", rune(b))
}
func replaceIdentity(text, raw, alias string) string {
	if raw == "" {
		return text
	}
	var out strings.Builder
	for offset := 0; offset < len(text); {
		relative := strings.Index(text[offset:], raw)
		if relative < 0 {
			out.WriteString(text[offset:])
			break
		}
		start := offset + relative
		end := start + len(raw)
		out.WriteString(text[offset:start])
		if (start == 0 || !identityByte(text[start-1])) && (end == len(text) || !identityByte(text[end])) {
			out.WriteString(alias)
		} else {
			out.WriteString(raw)
		}
		offset = end
	}
	return out.String()
}

// A whole returned set may only be referenced as an ACL change when its
// protocol/port meaning stays inside the original finding query. Ambiguous
// multi-port matrices remain review-only instead of flattening their sources.
func accessMatchesQuery(values []domain.Access, query domain.Query, denyAll bool) bool {
	if !has([]string{"tcp", "udp", "any"}, query.Protocol) || len(query.Ports) == 0 {
		return false
	}
	ranges := append([]domain.PortRange(nil), query.Ports...)
	for _, p := range ranges {
		if p.From < 1 || p.To < p.From || p.To > 65535 {
			return false
		}
	}
	sort.Slice(ranges, func(i, j int) bool { return ranges[i].From < ranges[j].From })
	if len(values) == 0 {
		return denyAll
	}
	for _, v := range values {
		if v.Applicability != "applicable" || len(v.Ports) == 0 || query.Protocol != "any" && v.Protocol != query.Protocol {
			return false
		}
		for _, p := range v.Ports {
			covered := false
			start := p.From
			for _, q := range ranges {
				if q.To < start {
					continue
				}
				if q.From > start {
					break
				}
				if q.To >= p.To {
					covered = true
					break
				}
				start = q.To + 1
			}
			if !covered {
				return false
			}
		}
	}
	return true
}
