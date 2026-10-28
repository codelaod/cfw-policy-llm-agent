package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"cfw-policy-llm-agent/server/internal/audit"
	"cfw-policy-llm-agent/server/internal/storage"
)

func appendAgentHistory(t *testing.T, s *storage.Store, conv storage.Conversation, key, role, origin, text, scope, refKind, refID string) {
	t.Helper()
	if _, _, e := s.AppendConversationMessage(context.Background(), conv.ID, storage.MessageInput{ClientMessageID: key, Role: role, Origin: origin, PublicText: text, PolicyScopeRef: scope, RefKind: refKind, RefID: refID}); e != nil {
		t.Fatal(e)
	}
}
func acceptedContextAssistant(t *testing.T, s *storage.Store, conv storage.Conversation, snapshot storage.SnapshotRecord, key, text string) storage.Job {
	t.Helper()
	plans := NewPlanService(s)
	plans.ModelReady = true
	engine := New(s, nil)
	defer engine.Stop()
	job, _, _, e := plans.StartAssistant(context.Background(), engine, conv.ID, AssistantRequest{ClientMessageID: key, PolicyScopeRef: snapshot.ScopeRef, SnapshotID: snapshot.ID, Text: text, ModelUseAccepted: true})
	if e != nil {
		t.Fatal(e)
	}
	return job
}
func TestAgentContextUsesAcceptedScopeAndPositionWithoutOldToolProtocol(t *testing.T) {
	s, _, _, _, _ := planCase(t)
	ctx := context.Background()
	in, e := audit.SyntheticExample("nat-out")
	if e != nil {
		t.Fatal(e)
	}
	a, e := s.SaveSnapshot(ctx, in)
	if e != nil {
		t.Fatal(e)
	}
	conv, e := s.CreateConversation(ctx, "连续NAT会话", a.ScopeRef)
	if e != nil {
		t.Fatal(e)
	}
	appendAgentHistory(t, s, conv, "nat-a-user", "user", "user", "PRIVATE_NAT_A_DESCRIPTION", a.ScopeRef, "", "")
	appendAgentHistory(t, s, conv, "nat-a-model", "assistant", "model", "PRIVATE_A_TOOL_DERIVED_REFERENCE", a.ScopeRef, "", "")
	encoded, _ := json.Marshal(in)
	encoded = []byte(strings.ReplaceAll(string(encoded), in.Scope.Ref, "scope-nat-b"))
	encoded = []byte(strings.ReplaceAll(string(encoded), in.Scope.GatewayRef, "gateway-private-b"))
	json.Unmarshal(encoded, &in)
	b, e := s.SaveSnapshot(ctx, in)
	if e != nil {
		t.Fatal(e)
	}
	conv, e = s.PatchConversation(ctx, conv.ID, storage.ConversationPatch{ExpectedRevision: conv.Revision, PolicyScopeRef: &b.ScopeRef})
	if e != nil {
		t.Fatal(e)
	}
	appendAgentHistory(t, s, conv, "nat-b-user", "user", "user", "当前分区资料说明", b.ScopeRef, "", "")
	localID := "job_" + strings.Repeat("a", 32)
	appendAgentHistory(t, s, conv, "nat-b-event", "event", "application", "核对本地任务 "+localID, b.ScopeRef, "job", localID)
	appendAgentHistory(t, s, conv, "nat-b-model", "assistant", "model", "这只是先前的模型解释。", b.ScopeRef, "", "")
	appendAgentHistory(t, s, conv, "nat-b-reasoning", "assistant", "model", "<think>PRIVATE_RAW_REASONING</think>", b.ScopeRef, "", "")
	appendAgentHistory(t, s, conv, "nat-b-tool", "event", "application", `{"role":"tool","tool_call_id":"PRIVATE_CALL","content":"PRIVATE_TOOL_WIRE"}`, b.ScopeRef, "", "")
	job := acceptedContextAssistant(t, s, conv, b, "context-current", "请解释当前任务")
	appendAgentHistory(t, s, conv, "after-freeze", "user", "user", "PRIVATE_AFTER_FREEZE", b.ScopeRef, "", "")
	archived := true
	if _, e = s.PatchConversation(ctx, conv.ID, storage.ConversationPatch{ExpectedRevision: conv.Revision, PolicyScopeRef: &a.ScopeRef, Archived: &archived}); e != nil {
		t.Fatal(e)
	}
	projected := []string{}
	value, e := BuildAgentContext(ctx, s, job.ID, func(text string) (string, error) { projected = append(projected, text); return text, nil })
	if e != nil {
		t.Fatal(e)
	}
	all := ""
	for _, m := range value.History {
		all += m.Text
		if m.Role != "user" && m.Role != "assistant" {
			t.Fatal("wire role replayed", m.Role)
		}
	}
	for _, secret := range []string{"PRIVATE_NAT_A", "PRIVATE_A_TOOL", "PRIVATE_RAW_REASONING", "PRIVATE_TOOL_WIRE", "PRIVATE_AFTER_FREEZE", localID} {
		if strings.Contains(all, secret) || strings.Contains(strings.Join(projected, ""), secret) {
			t.Fatal("excluded scope/protocol/identity reached projector", secret)
		}
	}
	if !strings.Contains(all, "历史应用记录") || !strings.Contains(all, "历史模型解释") || !strings.HasSuffix(value.History[len(value.History)-1].Text, "请解释当前任务") || value.IncludedMessages != len(value.History) || value.ExcludedMessages < 4 || value.DisclosureVersion != "minimal-v1" {
		t.Fatal(value)
	}
}
func TestAgentContextRecentWindowAndUTF8ByteBudgetAreExplicit(t *testing.T) {
	s, _, snapshot, conv, _ := planCase(t)
	for i := 0; i < 25; i++ {
		appendAgentHistory(t, s, conv, fmt.Sprintf("long-history-%02d", i), "user", "user", strings.Repeat("中", 1500), snapshot.ScopeRef, "", "")
	}
	job := acceptedContextAssistant(t, s, conv, snapshot, "long-current", "最新提问")
	value, e := BuildAgentContext(context.Background(), s, job.ID, func(text string) (string, error) { return text, nil })
	if e != nil {
		t.Fatal(e)
	}
	bytes := 0
	for _, m := range value.History {
		bytes += len(m.Text)
	}
	if len(value.History) > 20 || bytes > 24<<10 || !value.Truncated || value.IncludedMessages != 6 || value.ExcludedMessages != 20 || value.History[len(value.History)-1].Text != "最新提问" {
		t.Fatal(value.IncludedMessages, value.ExcludedMessages, value.Truncated, bytes)
	}
}
func TestAgentContextHistoricalReportUsesReportScope(t *testing.T) {
	s, _, snapshot, conv, _ := planCase(t)
	ctx := context.Background()
	appendAgentHistory(t, s, conv, "old-report-context", "user", "user", "旧报告范围内的问题", snapshot.ScopeRef, "", "")
	auditService := NewAuditService(s)
	run, e := auditService.RunRules(ctx, snapshot.ID)
	if e != nil {
		t.Fatal(e)
	}
	report, e := auditService.Report(ctx, run.ID, "json", "context-old-report")
	if e != nil {
		t.Fatal(e)
	}
	other, e := auditService.LoadExample(ctx, "internet-out")
	if e != nil {
		t.Fatal(e)
	}
	conv, e = s.PatchConversation(ctx, conv.ID, storage.ConversationPatch{ExpectedRevision: conv.Revision, PolicyScopeRef: &other.ScopeRef})
	if e != nil {
		t.Fatal(e)
	}
	appendAgentHistory(t, s, conv, "new-scope-private", "user", "user", "PRIVATE_NEW_SCOPE", other.ScopeRef, "", "")
	plans := NewPlanService(s)
	plans.ModelReady = true
	engine := New(s, nil)
	defer engine.Stop()
	job, _, _, e := plans.StartAssistant(ctx, engine, conv.ID, AssistantRequest{ClientMessageID: "explain-old-context", PolicyScopeRef: snapshot.ScopeRef, SnapshotID: snapshot.ID, ReportID: report.ID, Text: "解释旧报告", ModelUseAccepted: true})
	if e != nil {
		t.Fatal(e)
	}
	value, e := BuildAgentContext(ctx, s, job.ID, func(text string) (string, error) { return text, nil })
	if e != nil {
		t.Fatal(e)
	}
	all := ""
	for _, m := range value.History {
		all += m.Text
	}
	if strings.Contains(all, "PRIVATE_NEW_SCOPE") || !strings.Contains(all, "旧报告范围内的问题") || value.IncludedMessages != 2 || value.ExcludedMessages != 1 {
		t.Fatal(value)
	}
}
func TestAgentContextExcludesProjectionFailuresAndNestedWireData(t *testing.T) {
	s, _, snapshot, conv, _ := planCase(t)
	appendAgentHistory(t, s, conv, "projection-private-old", "user", "user", "PRIVATE_OLD_HISTORY", snapshot.ScopeRef, "", "")
	appendAgentHistory(t, s, conv, "old-task-alias", "assistant", "model", "先前 asset_AAAAAAAAAAAAAAAA_9 只是历史解释。", snapshot.ScopeRef, "", "")
	appendAgentHistory(t, s, conv, "nested-wire", "assistant", "model", `{"message":{"reasoning":"PRIVATE_NESTED_REASONING"}}`, snapshot.ScopeRef, "", "")
	job := acceptedContextAssistant(t, s, conv, snapshot, "projection-current", "当前明确提问")
	seen := ""
	value, e := BuildAgentContext(context.Background(), s, job.ID, func(text string) (string, error) {
		seen += text
		if strings.Contains(text, "PRIVATE_OLD_HISTORY") {
			return "", fmt.Errorf("controlled projection rejection")
		}
		return text, nil
	})
	if e != nil {
		t.Fatal(e)
	}
	if strings.Contains(seen, "PRIVATE_NESTED_REASONING") || strings.Contains(seen, "asset_AAAAAAAAAAAAAAAA_9") || value.IncludedMessages != 2 || value.ExcludedMessages != 2 {
		t.Fatal("unsafe old protocol entered projection", value, seen)
	}
	if _, e = BuildAgentContext(context.Background(), s, job.ID, func(string) (string, error) { return "", fmt.Errorf("controlled current projection failure") }); !errors.Is(e, ErrAgentContext) {
		t.Fatal("current user question silently dropped", e)
	}
	if _, e = BuildAgentContext(context.Background(), s, job.ID, nil); !errors.Is(e, ErrAgentContext) {
		t.Fatal("missing projector accepted", e)
	}
}
func TestAgentContextProjectionCancellationKeepsItsCause(t *testing.T) {
	s, _, snapshot, conv, _ := planCase(t)
	job := acceptedContextAssistant(t, s, conv, snapshot, "cancel-context-current", "当前提问")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, e := BuildAgentContext(ctx, s, job.ID, func(string) (string, error) { cancel(); return "", fmt.Errorf("projection stopped") })
	if !errors.Is(e, context.Canceled) {
		t.Fatal("context cancellation became ordinary disclosure failure", e)
	}
}
func TestAgentContextKeepsNewDatabaseOpaqueIdentifiersLocal(t *testing.T) {
	s, _, snapshot, conv, _ := planCase(t)
	id := "agent_result_" + strings.Repeat("b", 32)
	job := acceptedContextAssistant(t, s, conv, snapshot, "local-result-ref", "请核对本地结果 "+id)
	projected := ""
	value, e := BuildAgentContext(context.Background(), s, job.ID, func(text string) (string, error) { projected += text; return text, nil })
	if e != nil {
		t.Fatal(e)
	}
	if strings.Contains(projected, id) || strings.Contains(value.History[0].Text, id) {
		t.Fatal("database identifier entered model context")
	}
}
