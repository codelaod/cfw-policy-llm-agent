package app

import (
	"regexp"
	"strconv"
	"strings"

	"cfw-policy-llm-agent/server/internal/domain"
	"cfw-policy-llm-agent/server/internal/storage"
)

var businessNegativeDeclaration = regexp.MustCompile(`不(?:再)?允许|禁止|不能|无需|不必|不需要|不是|并非|不作为|不要|不确认|不用|未(?:经)?确认|没有(?:确认|必要|允许)|不属于|不应|不可|未必|不一定`)
var businessCompletenessUnknown = regexp.MustCompile(`(?:不|未|非|是否|未知|待确认|不确定)[^，,；;。\r\n]{0,16}完整|完整[^，,；;。\r\n]{0,16}(?:未知|待确认|不确定|是否)`)
var businessEndpointPattern = regexp.MustCompile(`(?i)(https?|tcp|udp|端口)\s*(?:端口\s*)?[:：/]?\s*([0-9]+)?(?:\s*[-~～至到–—]\s*([0-9]+))?`)
var businessPortListTail = regexp.MustCompile(`^[\s,，、/和及]+[0-9]`)
var businessMixedProtocols = regexp.MustCompile(`(?i)(?:tcp|udp)\s*[/和及、]\s*(?:tcp|udp)`)
var businessProtocolWord = regexp.MustCompile(`(?i)\b(?:tcp|udp)\b`)

type businessEndpoint struct {
	protocol string
	ports    domain.PortRange
}

func businessAllowedSetComplete(text string) bool {
	return strings.Contains(text, "完整") && !businessCompletenessUnknown.MatchString(text)
}

// Only explicit statement boundaries separate endpoint/address bindings. A
// comma can also separate addresses or a role from necessary access, so a
// statement with multiple distinct endpoints is clarified instead of guessed.
func evaluateBusinessText(current storage.ConversationBusinessContext, text string) (ConversationBusinessRequest, []string) {
	statements := businessStatements(text)
	if len(statements) == 0 {
		return evaluateBusinessStatement(current, text)
	}
	if len(statements) == 1 {
		return evaluateBusinessStatement(current, statements[0])
	}
	combined := ConversationBusinessRequest{UnknownFields: []string{}}
	missing := []string{}
	for _, statement := range statements {
		if strings.TrimSpace(statement) == "" {
			continue
		}
		part, needs := evaluateBusinessStatement(current, strings.TrimSpace(statement))
		if part.Action == "" {
			return ConversationBusinessRequest{}, needs
		}
		if part.Action != "edit" && part.Action != "keep_unknown" {
			return ConversationBusinessRequest{}, []string{"single_business_action"}
		}
		for _, patch := range part.Patches {
			var ok bool
			combined.Patches, ok = mergeBusinessPatch(combined.Patches, patch)
			if !ok {
				return ConversationBusinessRequest{}, []string{"separate_service_statements"}
			}
		}
		for _, field := range part.UnknownFields {
			if !hasString(combined.UnknownFields, field) {
				combined.UnknownFields = append(combined.UnknownFields, field)
			}
		}
		for _, field := range needs {
			if !hasString(missing, field) {
				missing = append(missing, field)
			}
		}
	}
	if len(combined.Patches) > 0 {
		combined.Action = "edit"
	} else if len(combined.UnknownFields) > 0 {
		combined.Action = "keep_unknown"
	}
	return combined, missing
}

func businessStatements(text string) []string {
	quoted := quotedResourceName.FindAllStringIndex(text, -1)
	statements := []string{}
	start, quote := 0, 0
	for offset, r := range text {
		for quote < len(quoted) && offset >= quoted[quote][1] {
			quote++
		}
		if quote < len(quoted) && offset >= quoted[quote][0] || !strings.ContainsRune("；;。\r\n", r) {
			continue
		}
		if part := strings.TrimSpace(text[start:offset]); part != "" {
			statements = append(statements, part)
		}
		start = offset + len(string(r))
	}
	if part := strings.TrimSpace(text[start:]); part != "" {
		statements = append(statements, part)
	}
	return statements
}

func mergeBusinessPatch(patches []storage.ConversationBusinessPatch, next storage.ConversationBusinessPatch) ([]storage.ConversationBusinessPatch, bool) {
	for i := range patches {
		old := &patches[i]
		if old.AssetID != next.AssetID {
			continue
		}
		if old.ServiceID == "" && next.ServiceID == "" && intentDigest(old.Role) != intentDigest(next.Role) {
			// Do not guess whether separate declarations describe one new service
			// or two when a stable service ID/endpoint is missing or conflicting.
			if old.Role == nil || next.Role == nil || old.Role.Protocol == "unknown" || next.Role.Protocol == "unknown" || old.Role.Protocol == next.Role.Protocol && businessSamePorts(old.Role.Ports, next.Role.Ports) {
				return nil, false
			}
			continue
		}
		if old.ServiceID != next.ServiceID {
			continue
		}
		if old.Role != nil && next.Role != nil && intentDigest(old.Role) != intentDigest(next.Role) || old.Allowed != nil && next.Allowed != nil && intentDigest(old.Allowed) != intentDigest(next.Allowed) || old.Required != nil && next.Required != nil && intentDigest(old.Required) != intentDigest(next.Required) || old.Exceptions != nil && next.Exceptions != nil && intentDigest(old.Exceptions) != intentDigest(next.Exceptions) {
			return nil, false
		}
		if next.Role != nil {
			old.Role = next.Role
		}
		if next.Allowed != nil {
			old.Allowed = next.Allowed
		}
		if next.Required != nil {
			old.Required = next.Required
		}
		if next.Exceptions != nil {
			old.Exceptions = next.Exceptions
		}
		return patches, true
	}
	return append(patches, next), true
}

// Resource and service identifiers are data, including names such as svc-https.
// Mask complete locator tokens without stripping a declared TCP1000-2000 range.
func businessEndpointText(current storage.ConversationBusinessContext, text string) string {
	text = explicitQuotedResourceName.ReplaceAllString(text, " ")
	text = quotedResourceName.ReplaceAllString(text, " ")
	text = explicitResourceName.ReplaceAllString(text, " ")
	ids := map[string]bool{}
	for _, asset := range current.Input.Assets {
		ids[asset.ID], ids[asset.ProviderResourceID] = true, true
		for _, service := range businessCurrentServices(current, asset.ID) {
			ids[service.ID] = true
		}
	}
	text = identifierToken.ReplaceAllStringFunc(text, func(token string) string {
		if ids[token] {
			return " "
		}
		return token
	})
	return resourceIdentifier.ReplaceAllString(text, " ")
}

func businessEndpoints(text string) ([]businessEndpoint, bool) {
	endpoints := []businessEndpoint{}
	if businessMixedProtocols.MatchString(text) {
		return nil, false
	}
	for _, span := range businessEndpointPattern.FindAllStringSubmatchIndex(text, -1) {
		if span[0] > 0 && asciiEndpointWord(text[span[0]-1]) || span[1] < len(text) && asciiEndpointWord(text[span[1]]) {
			continue
		}
		protocol := strings.ToLower(text[span[2]:span[3]])
		if protocol == "端口" {
			if span[4] < 0 {
				continue
			}
			protocol = ""
			for _, word := range businessProtocolWord.FindAllString(text, -1) {
				word = strings.ToLower(word)
				if protocol != "" && protocol != word {
					return nil, false
				}
				protocol = word
			}
			if protocol == "" {
				return nil, false
			}
		}
		from := 0
		if span[4] >= 0 {
			var err error
			from, err = strconv.Atoi(text[span[4]:span[5]])
			if err != nil || from < 1 || from > 65535 {
				return nil, false
			}
		} else if protocol == "https" {
			from = 443
		} else if protocol == "http" {
			from = 80
		} else {
			continue
		}
		to := from
		if span[6] >= 0 {
			var err error
			to, err = strconv.Atoi(text[span[6]:span[7]])
			if err != nil || to < from || to > 65535 {
				return nil, false
			}
		}
		tail := strings.TrimSpace(text[span[1]:])
		if strings.HasPrefix(tail, "-") || strings.HasPrefix(tail, "~") || strings.HasPrefix(tail, "～") || strings.HasPrefix(tail, "至") || strings.HasPrefix(tail, "到") || strings.HasPrefix(tail, "–") || strings.HasPrefix(tail, "—") || businessPortListTail.MatchString(tail) {
			return nil, false
		}
		if protocol != "udp" {
			protocol = "tcp"
		}
		endpoint := businessEndpoint{protocol, domain.PortRange{From: from, To: to}}
		found := false
		for _, old := range endpoints {
			found = found || old == endpoint
		}
		if !found {
			endpoints = append(endpoints, endpoint)
		}
	}
	return endpoints, true
}

func asciiEndpointWord(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '_'
}
