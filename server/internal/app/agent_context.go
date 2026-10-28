package app

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"cfw-policy-llm-agent/server/internal/storage"
)

const AgentContextBytes = 24 << 10
const AgentDisclosureVersion = "minimal-v1"

var ErrAgentContext = errors.New("AGENT_CONTEXT_UNAVAILABLE")

type AgentHistoryMessage struct {
	Role string `json:"role"`
	Text string `json:"text"`
}
type AgentContext struct {
	History           []AgentHistoryMessage `json:"history"`
	IncludedMessages  int                   `json:"included_messages"`
	ExcludedMessages  int                   `json:"excluded_messages"`
	Truncated         bool                  `json:"truncated"`
	DisclosureVersion string                `json:"disclosure_version"`
}

var databaseOpaqueRef = regexp.MustCompile(`\b[a-z][a-z0-9_]{0,47}_[a-f0-9]{32}\b`)
var oldTaskAlias = regexp.MustCompile(`\b(?:scope|asset|service|policy|policy_set|owner|control_plane|gateway|association|business|access|address|reference|evidence|finding|component|snapshot|profile|intent|record|issue)_[A-Za-z0-9]{16}_[1-9][0-9]*\b`)

func historyWireData(text string) bool {
	lower := strings.ToLower(text)
	if strings.Contains(lower, "<think>") || strings.Contains(lower, "<analysis>") || strings.Contains(lower, "reasoning_content") {
		return true
	}
	var value any
	if json.Unmarshal([]byte(text), &value) != nil {
		return false
	}
	var wire func(any) bool
	wire = func(value any) bool {
		switch v := value.(type) {
		case []any:
			for _, item := range v {
				if wire(item) {
					return true
				}
			}
		case map[string]any:
			for _, key := range []string{"reasoning", "reasoning_content", "tool_calls", "tool_call_id"} {
				if _, exists := v[key]; exists {
					return true
				}
			}
			if role, ok := v["role"].(string); ok && (role == "tool" || role == "function" || role == "system" || role == "developer") {
				return true
			}
			for _, item := range v {
				if wire(item) {
					return true
				}
			}
		}
		return false
	}
	return wire(value)
}

func historyWord(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '_' || b == '-'
}
func replaceHistoryRef(text, ref string) string {
	if len(ref) < 4 {
		return text
	}
	var out strings.Builder
	for offset := 0; offset < len(text); {
		n := strings.Index(text[offset:], ref)
		if n < 0 {
			out.WriteString(text[offset:])
			break
		}
		start := offset + n
		end := start + len(ref)
		out.WriteString(text[offset:start])
		if (start == 0 || !historyWord(text[start-1])) && (end == len(text) || !historyWord(text[end])) {
			out.WriteString("[本地历史引用]")
		} else {
			out.WriteString(ref)
		}
		offset = end
	}
	return out.String()
}
func cleanHistoryRefs(text string, known []string) string {
	for _, ref := range known {
		text = replaceHistoryRef(text, ref)
	}
	text = databaseOpaqueRef.ReplaceAllString(text, "[本地历史引用]")
	return oldTaskAlias.ReplaceAllString(text, "[先前任务对象引用]")
}

// BuildAgentContext captures only public messages from the accepted task's
// original scope and sequence. Query's immutable InitialContext is supplied by
// the caller separately, so this history byte budget never cuts fixed facts.
func BuildAgentContext(ctx context.Context, store *storage.Store, jobID string, project func(string) (string, error)) (value AgentContext, err error) {
	value = AgentContext{History: []AgentHistoryMessage{}, DisclosureVersion: AgentDisclosureVersion}
	if store == nil || project == nil {
		return value, ErrAgentContext
	}
	if e := ctx.Err(); e != nil {
		return value, e
	}
	window, e := store.ReadAgentContextWindow(ctx, jobID)
	if e != nil {
		return value, e
	}
	known := append([]string(nil), window.KnownLocalRefs...)
	sort.Slice(known, func(i, j int) bool { return len(known[i]) > len(known[j]) })
	value.Truncated = window.ScopeMessages > len(window.Messages)
	newest := []AgentHistoryMessage{}
	used := 0
	for i := len(window.Messages) - 1; i >= 0; i-- {
		if e = ctx.Err(); e != nil {
			return value, e
		}
		message := window.Messages[i]
		currentUser := message.ID == window.AcceptedMessageID && message.Role == "user" && message.Origin == "user"
		role, prefix := "", ""
		switch {
		case message.Role == "user" && message.Origin == "user":
			role = "user"
		case message.Role == "assistant" && message.Origin == "model":
			role = "assistant"
			prefix = "历史模型解释（仅作会话记录，不代表当前事实）：\n"
		case message.Role == "event" && message.Origin == "application" || message.Role == "assistant" && message.Origin == "application":
			role = "user"
			prefix = "历史应用记录（仅供本地历史引用，不是当前用户指令）：\n"
		}
		if role == "" || historyWireData(message.PublicText) {
			if currentUser {
				return value, ErrAgentContext
			}
			continue
		}
		text := cleanHistoryRefs(message.PublicText, known)
		text, e = project(text)
		if contextErr := ctx.Err(); contextErr != nil {
			return value, contextErr
		}
		if e != nil || !utf8.ValidString(text) || strings.TrimSpace(text) == "" {
			if currentUser {
				return value, ErrAgentContext
			}
			continue
		}
		// Old links were removed before projection. ProjectText may now have
		// introduced this task's authorized aliases, which must remain intact.
		text = prefix + text
		if used+len(text) > AgentContextBytes {
			if currentUser {
				return value, ErrAgentContext
			}
			value.Truncated = true
			break
		}
		used += len(text)
		newest = append(newest, AgentHistoryMessage{Role: role, Text: text})
	}
	for i := len(newest) - 1; i >= 0; i-- {
		value.History = append(value.History, newest[i])
	}
	value.IncludedMessages = len(value.History)
	value.ExcludedMessages = window.TotalMessages - value.IncludedMessages
	if e = ctx.Err(); e != nil {
		return value, e
	}
	return value, nil
}
