package storage

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"cfw-policy-llm-agent/server/internal/agentquery"
	"cfw-policy-llm-agent/server/internal/audit"
	"cfw-policy-llm-agent/server/internal/domain"
)

func e3AgentResultCase(t *testing.T, kind string) (*Store, Job, AgentContent) {
	t.Helper()
	ctx := context.Background()
	store, _, _, initial := executionCase(t)
	if err := store.db.Transaction(migrateAgentResults); err != nil {
		t.Fatal(err)
	}
	initial.Kind, initial.Mode = kind, "rules_and_agent"
	initial.EffectCeiling = EffectCeiling{AllowModelRequests: true, ModelHTTPAttempts: 6}
	var job Job
	var err error
	if kind == "assistant_turn" {
		initial.Mode = "assistant"
		initial.EffectCeiling.ModelHTTPAttempts = 4
		job, _, _, err = store.EnqueueAssistant(ctx, initial, MessageInput{ClientMessageID: "e3-assistant-message", Role: "user", Origin: "user", PublicText: "解释固定资料", PolicyScopeRef: initial.PolicyScopeRef})
	} else {
		var plan ExecutionPlan
		plan, err = store.SavePlan(ctx, initial)
		if err == nil {
			job, _, err = store.EnqueuePlan(ctx, plan.ID, "e3-agent-audit")
		}
	}
	if err != nil {
		t.Fatal(err)
	}
	claimed, found, err := store.Claim(ctx)
	if err != nil || !found || claimed.ID != job.ID {
		t.Fatal("agent job was not claimed", err)
	}
	plan, err := store.GetPlan(ctx, job.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	in := plan.PreparedInput
	in.EvaluationAt = time.Now().UTC().Format(time.RFC3339Nano)
	sealed, err := store.SealInput(ctx, job.ID, in)
	if err != nil {
		t.Fatal(err)
	}
	var result *domain.Result
	if kind == "audit" {
		r, err := audit.Run(ctx, sealed)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = store.SaveJobAudit(ctx, job.ID, r); err != nil {
			t.Fatal(err)
		}
		result = &r
	}
	queries, err := agentquery.New(sealed, result)
	if err != nil {
		t.Fatal(err)
	}
	var initialView agentquery.InitialView
	if err = json.Unmarshal([]byte(queries.InitialContext()), &initialView); err != nil {
		t.Fatal(err)
	}
	output := &agentquery.Output{ScopeRef: initialView.ScopeRef, RuleVersion: initialView.RuleVersion, SourceKind: initialView.SourceKind, AssessmentBasis: initialView.AssessmentBasis, Explanation: "基于固定资料，建议人工核对业务要求与覆盖限制。", Roles: []agentquery.RoleCandidate{}, Recommendations: []agentquery.Recommendation{}, Questions: []string{}}
	return store, claimed, AgentContent{JobID: job.ID, Manifest: manifestFor(sealed), Output: output, Bindings: queries.LocalBindings(), ModelCalls: 1, Context: ContextCoverage{DisclosureVersion: "minimal-v1"}}
}

func TestE3AgentResultStoresImmutableBoundContentWithoutChangingRules(t *testing.T) {
	ctx := context.Background()
	store, job, content := e3AgentResultCase(t, "audit")
	_, original, err := store.ReadJobAudit(ctx, job.ID)
	if err != nil || original == nil {
		t.Fatal(err)
	}
	originalBody, _ := json.Marshal(original.Result)
	record, err := store.SaveJobAgentResult(ctx, job.ID, content)
	if err != nil || record.ID == "" {
		t.Fatal(err)
	}
	same, err := store.SaveJobAgentResult(ctx, job.ID, content)
	if err != nil || same.ID != record.ID {
		t.Fatal("same result did not replay immutable record", err)
	}
	content.Output.Explanation = "改变已保存的解释。"
	if _, err = store.SaveJobAgentResult(ctx, job.ID, content); !errors.Is(err, ErrConflict) {
		t.Fatal("another result overwrote immutable content", err)
	}
	loaded, err := store.GetJobAgentResult(ctx, job.ID)
	if err != nil || loaded == nil || loaded.Content.Output.Explanation == content.Output.Explanation {
		t.Fatal("caller memory mutated persisted result", err)
	}
	current, after, err := store.ReadJobAudit(ctx, job.ID)
	if err != nil || after == nil || current.Status != "running" {
		t.Fatal("audit agent completed the whole task", err)
	}
	afterBody, _ := json.Marshal(after.Result)
	if string(afterBody) != string(originalBody) {
		t.Fatal("agent overwrote deterministic rules")
	}
	page, _, err := store.Page(ctx, job.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	last := page.Events[len(page.Events)-1]
	if last.Type != "stage_changed" || last.Payload["stage"] != "agent_result_saved" || last.Payload["agent_result_ref"] != record.ID {
		t.Fatal("result reference did not commit with its event")
	}
	encoded, _ := json.Marshal(loaded)
	for _, field := range []string{`"reasoning_content"`, `"raw_messages"`, `"tool_call_id"`} {
		if strings.Contains(string(encoded), field) {
			t.Fatal("wire reasoning/protocol state entered product result")
		}
	}
}

func TestE3AgentResultRejectsForeignOrUnlicensedContent(t *testing.T) {
	for _, scenario := range []string{"job", "snapshot", "scope", "source", "basis", "rule", "binding", "failure-binding", "effect", "unsealed", "cancelled", "model-limit", "tool-limit", "assistant-model-limit", "assistant-tool-limit", "context-overstated", "fake-zero-model", "unknown-error", "oversized-output", "oversized-body"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			kind := "audit"
			if strings.HasPrefix(scenario, "assistant-") {
				kind = "assistant_turn"
			}
			store, job, content := e3AgentResultCase(t, kind)
			switch scenario {
			case "job":
				content.JobID = "another-job"
			case "snapshot":
				content.Manifest.SnapshotVersion = "another-input"
			case "scope":
				content.Manifest.PolicyScope.Direction = "out"
			case "source":
				content.Output.SourceKind = "live_readonly"
			case "basis":
				content.Output.AssessmentBasis = "live_observed"
			case "rule":
				content.Output.RuleVersion = "model-rule-version"
			case "binding":
				for key, value := range content.Bindings {
					value.Document = "another-job"
					content.Bindings[key] = value
					break
				}
			case "failure-binding":
				content.Output = nil
				content.ErrorCode = "MODEL_REQUEST_FAILED"
				for key, value := range content.Bindings {
					value.Document = "foreign-scope"
					content.Bindings[key] = value
					break
				}
			case "effect":
				plan, err := store.GetPlan(ctx, job.PlanID)
				if err != nil {
					t.Fatal(err)
				}
				plan.EffectCeiling = EffectCeiling{}
				body, _ := json.Marshal(plan)
				if err = store.db.Exec("UPDATE execution_plans SET body=? WHERE id=?", string(body), plan.ID).Error; err != nil {
					t.Fatal(err)
				}
			case "model-limit":
				content.ModelCalls = 7
			case "tool-limit":
				content.ToolCalls = 13
			case "assistant-model-limit":
				content.ModelCalls = 5
			case "assistant-tool-limit":
				content.ToolCalls = 9
			case "context-overstated":
				content.Context.IncludedMessages = AgentContextMessageLimit + 1
			case "unsealed":
				if err := store.db.Exec("UPDATE e0_jobs SET sealed_input=NULL WHERE id=?", job.ID).Error; err != nil {
					t.Fatal(err)
				}
			case "cancelled":
				if _, err := store.Cancel(ctx, job.ID); err != nil {
					t.Fatal(err)
				}
			case "fake-zero-model":
				content.ModelCalls = 0
			case "unknown-error":
				content.Output = nil
				content.ErrorCode = "PRIVATE_PROVIDER_SECRET"
			case "oversized-output":
				content.Output.Explanation = strings.Repeat("x", 33<<10)
			case "oversized-body":
				content.Bindings["injected"] = agentquery.LocalBinding{Kind: "evidence", Field: strings.Repeat("x", 257<<10)}
			}
			if _, err := store.SaveJobAgentResult(ctx, job.ID, content); err == nil {
				t.Fatal("foreign or unlicensed agent content persisted")
			}
			record, err := store.GetJobAgentResult(ctx, job.ID)
			if err != nil || record != nil {
				t.Fatal("rejected content left a result", err)
			}
		})
	}
}

func TestE3FinalizeAssistantCommitsResultMessageAndTerminalTogether(t *testing.T) {
	ctx := context.Background()
	store, job, content := e3AgentResultCase(t, "assistant_turn")
	record, err := store.FinalizeAssistant(ctx, job.ID, content)
	if err != nil || record.ID == "" {
		t.Fatal(err)
	}
	current, err := store.Get(ctx, job.ID)
	if err != nil || current.Status != "completed" {
		t.Fatal("assistant finalization missed terminal state", err)
	}
	page, err := store.ConversationMessages(ctx, job.ConversationID, 0, 100)
	if err != nil || len(page.Items) != 2 {
		t.Fatal(err)
	}
	reply := page.Items[1]
	if reply.Role != "assistant" || reply.Origin != "model" || reply.PublicText != content.Output.Explanation || reply.RefKind != "job" || reply.RefID != job.ID {
		t.Fatal("validated model explanation lost origin or job binding")
	}
	events, _, err := store.Page(ctx, job.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	tail := events.Events[len(events.Events)-3:]
	for n, kind := range []string{"stage_changed", "message_available", "job_terminal"} {
		if tail[n].Type != kind {
			t.Fatal("assistant final event sequence incomplete")
		}
	}
	if err = store.FinishWithCode(ctx, job.ID, "failed", "LATE_CALLBACK"); err != nil {
		t.Fatal(err)
	}
	sticky, _ := store.Get(ctx, job.ID)
	if sticky.Status != current.Status || sticky.ViewSeq != current.ViewSeq {
		t.Fatal("late Engine finish changed atomic finalization")
	}
}

func TestE3FinalizeAssistantFailureIsApplicationMetadata(t *testing.T) {
	ctx := context.Background()
	store, job, content := e3AgentResultCase(t, "assistant_turn")
	content.Output = nil
	content.Bindings = nil
	content.ErrorCode = "MODEL_REQUEST_FAILED"
	if _, err := store.FinalizeAssistant(ctx, job.ID, content); err != nil {
		t.Fatal(err)
	}
	current, _ := store.Get(ctx, job.ID)
	if current.Status != "failed" || current.ErrorCode != content.ErrorCode {
		t.Fatal("controlled assistant failure lost its terminal code")
	}
	page, err := store.ConversationMessages(ctx, job.ConversationID, 0, 100)
	if err != nil || len(page.Items) != 2 || page.Items[1].Origin != "application" || page.Items[1].Role != "event" {
		t.Fatal("failure template falsely became a model reply", err)
	}
}

func TestE3FinalizeAssistantFailuresRollbackResultReplyAndState(t *testing.T) {
	for _, stage := range []string{"result", "result-event", "message", "message-event", "terminal"} {
		t.Run(stage, func(t *testing.T) {
			ctx := context.Background()
			store, job, content := e3AgentResultCase(t, "assistant_turn")
			trigger := map[string]string{
				"result":        `CREATE TRIGGER reject_agent_finalization BEFORE INSERT ON job_agent_results BEGIN SELECT RAISE(ABORT,'synthetic result failure'); END`,
				"result-event":  `CREATE TRIGGER reject_agent_finalization BEFORE INSERT ON e0_events WHEN NEW.type='stage_changed' AND json_extract(NEW.payload,'$.stage')='agent_result_saved' BEGIN SELECT RAISE(ABORT,'synthetic result event failure'); END`,
				"message":       `CREATE TRIGGER reject_agent_finalization BEFORE INSERT ON conversation_messages WHEN NEW.role='assistant' BEGIN SELECT RAISE(ABORT,'synthetic message failure'); END`,
				"message-event": `CREATE TRIGGER reject_agent_finalization BEFORE INSERT ON e0_events WHEN NEW.type='message_available' BEGIN SELECT RAISE(ABORT,'synthetic message event failure'); END`,
				"terminal":      `CREATE TRIGGER reject_agent_finalization BEFORE INSERT ON e0_events WHEN NEW.type='job_terminal' BEGIN SELECT RAISE(ABORT,'synthetic terminal failure'); END`,
			}[stage]
			if err := store.db.Exec(trigger).Error; err != nil {
				t.Fatal(err)
			}
			if _, err := store.FinalizeAssistant(ctx, job.ID, content); err == nil {
				t.Fatal("failed transaction reported completed")
			}
			record, err := store.GetJobAgentResult(ctx, job.ID)
			if err != nil || record != nil {
				t.Fatal("failed finalization left agent result", err)
			}
			current, _ := store.Get(ctx, job.ID)
			if current.Status != "running" {
				t.Fatal("failed finalization partially changed status")
			}
			messages, err := store.ConversationMessages(ctx, job.ConversationID, 0, 100)
			if err != nil || len(messages.Items) != 1 {
				t.Fatal("failed finalization left public reply", err)
			}
			if err = store.db.Exec("DROP TRIGGER reject_agent_finalization").Error; err != nil {
				t.Fatal(err)
			}
			if _, err = store.FinalizeAssistant(ctx, job.ID, content); err != nil {
				t.Fatal("rolled-back finalization could not retry", err)
			}
		})
	}
}

func TestE3FinalizeAssistantCancellationWinsAgainstLateOutput(t *testing.T) {
	ctx := context.Background()
	store, job, content := e3AgentResultCase(t, "assistant_turn")
	if _, err := store.Cancel(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.FinalizeAssistant(ctx, job.ID, content); !errors.Is(err, ErrJobTerminal) {
		t.Fatal("late model output bypassed cancellation", err)
	}
	record, err := store.GetJobAgentResult(ctx, job.ID)
	if err != nil || record != nil {
		t.Fatal("late cancelled output persisted", err)
	}
	page, err := store.ConversationMessages(ctx, job.ConversationID, 0, 100)
	if err != nil || len(page.Items) != 1 {
		t.Fatal("late cancelled output appended normal message", err)
	}
}

func TestE3AgentResultRestartRetainsContentWithoutReplayingModel(t *testing.T) {
	ctx := context.Background()
	store, job, content := e3AgentResultCase(t, "audit")
	saved, err := store.SaveJobAgentResult(ctx, job.ID, content)
	if err != nil {
		t.Fatal(err)
	}
	var path string
	if err = store.SQL.QueryRowContext(ctx, "SELECT file FROM pragma_database_list WHERE name='main'").Scan(&path); err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err = reopened.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	restored, err := reopened.GetJobAgentResult(ctx, job.ID)
	if err != nil || restored == nil || restored.ID != saved.ID || restored.Content.Output.Explanation != content.Output.Explanation {
		t.Fatal("restart lost fixed agent result", err)
	}
	current, _ := reopened.Get(ctx, job.ID)
	if current.Status != "interrupted" {
		t.Fatal("restart replayed unfinished model work")
	}
	same, err := reopened.SaveJobAgentResult(ctx, job.ID, content)
	if err != nil || same.ID != saved.ID {
		t.Fatal("read-only result replay failed after terminal recovery", err)
	}
}

func TestE3FinalizeAssistantKeepsAcceptedScopeAfterConversationChanges(t *testing.T) {
	ctx := context.Background()
	store, job, content := e3AgentResultCase(t, "assistant_turn")
	in, err := audit.SyntheticExample("internet-out")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.SaveSnapshot(ctx, in); err != nil {
		t.Fatal(err)
	}
	conversation, err := store.GetConversation(ctx, job.ConversationID)
	if err != nil {
		t.Fatal(err)
	}
	conversation, err = store.PatchConversation(ctx, conversation.ID, ConversationPatch{ExpectedRevision: conversation.Revision, PolicyScopeRef: &in.Scope.Ref})
	if err != nil {
		t.Fatal(err)
	}
	archived := true
	if _, err = store.PatchConversation(ctx, conversation.ID, ConversationPatch{ExpectedRevision: conversation.Revision, Archived: &archived}); err != nil {
		t.Fatal(err)
	}
	if _, err = store.FinalizeAssistant(ctx, job.ID, content); err != nil {
		t.Fatal("accepted job lost its old scope/archived completion context", err)
	}
	messages, err := store.ConversationMessages(ctx, job.ConversationID, 0, 100)
	if err != nil || len(messages.Items) != 2 || messages.Items[1].PolicyScopeRef != content.Manifest.PolicyScope.Ref || messages.Items[1].Origin != "model" {
		t.Fatal("old task reply adopted the current scope", err)
	}
}

func TestE3FinalizeAssistantCannotOverwriteAnExistingTurnReply(t *testing.T) {
	ctx := context.Background()
	store, job, content := e3AgentResultCase(t, "assistant_turn")
	first, err := store.AppendJobMessage(ctx, job.ID, MessageInput{ClientMessageID: "already-committed-answer", Role: "assistant", Origin: "model", PublicText: "已有公开解释。", PolicyScopeRef: content.Manifest.PolicyScope.Ref, RefKind: "job", RefID: job.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.FinalizeAssistant(ctx, job.ID, content); !errors.Is(err, ErrConflict) {
		t.Fatal("finalizer overwrote an existing public turn reply", err)
	}
	var turn ConversationTurn
	if err = store.db.Raw("SELECT * FROM conversation_turns WHERE job_id=?", job.ID).Scan(&turn).Error; err != nil || turn.ReplyMessageID != first.ID {
		t.Fatal("turn reply identity changed", err)
	}
}

func TestE3AgentResultSaveEventFailureCannotLeaveUnreferencedContent(t *testing.T) {
	ctx := context.Background()
	store, job, content := e3AgentResultCase(t, "audit")
	if err := store.db.Exec(`CREATE TRIGGER reject_agent_save_event BEFORE INSERT ON e0_events WHEN NEW.type='stage_changed' AND json_extract(NEW.payload,'$.stage')='agent_result_saved' BEGIN SELECT RAISE(ABORT,'synthetic event failure'); END`).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveJobAgentResult(ctx, job.ID, content); err == nil {
		t.Fatal("failed result event reported success")
	}
	result, err := store.GetJobAgentResult(ctx, job.ID)
	if err != nil || result != nil {
		t.Fatal("result event failure left a half-committed content row", err)
	}
}
