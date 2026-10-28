package storage

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"cfw-policy-llm-agent/server/internal/audit"
	"cfw-policy-llm-agent/server/internal/domain"
)

func executionCase(t *testing.T) (*Store, SnapshotRecord, Conversation, ExecutionPlan) {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "execution.sqlite"), true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	in, err := audit.SyntheticExample("internet-in")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := s.SaveSnapshot(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	conversation, err := s.CreateConversation(context.Background(), "人工会话", in.Scope.Ref)
	if err != nil {
		t.Fatal(err)
	}
	frozen, heads, err := s.FreezeInput(context.Background(), snapshot.ID)
	if err != nil {
		t.Fatal(err)
	}
	p := ExecutionPlan{Kind: "audit", ConversationID: conversation.ID, DataSourceID: "local-offline", PolicyScopeRef: in.Scope.Ref, SnapshotID: snapshot.ID, Mode: "rules_only", ConversationRevision: conversation.Revision, PreparedInput: frozen, Heads: heads, ConfigRevision: "offline-v1", BudgetRef: "shared-local-batch", Request: json.RawMessage(`{"kind":"audit","mode":"rules_only"}`)}
	return s, snapshot, conversation, p
}

func TestPlanConsumptionIsAtomicIdempotentAndBounded(t *testing.T) {
	ctx := context.Background()
	s, _, conversation, p := executionCase(t)
	plan, err := s.SavePlan(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan Job, 8)
	failures := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			job, _, err := s.EnqueuePlan(ctx, plan.ID, "same-operation")
			results <- job
			failures <- err
		}()
	}
	wg.Wait()
	close(results)
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	id := ""
	for job := range results {
		if id == "" {
			id = job.ID
		}
		if id != job.ID {
			t.Fatal("concurrent idempotency created multiple jobs")
		}
	}
	stored, err := s.GetPlan(ctx, plan.ID)
	if err != nil || stored.ConsumedJobID != id {
		t.Fatal("plan not atomically consumed")
	}
	messages, err := s.ConversationMessages(ctx, conversation.ID, 0, 100)
	if err != nil || len(messages.Items) != 1 || messages.Items[0].Origin != "application" {
		t.Fatal("accepted operation has no single durable public event")
	}
	page, job, err := s.Page(ctx, id, 0, 100)
	if err != nil || job.Kind != "audit" || len(page.Events) != 1 || page.Events[0].Payload["status"] != "queued" {
		t.Fatal("task and queued event did not commit together")
	}
	if _, _, err = s.EnqueuePlan(ctx, plan.ID, "new-key-consumed"); !errors.Is(err, ErrPlanConsumed) {
		t.Fatalf("consumed plan reused: %v", err)
	}
	other, err := s.SavePlan(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.EnqueuePlan(ctx, other.ID, "same-operation"); !errors.Is(err, ErrConflict) {
		t.Fatal("same key different plan was not a conflict")
	}
	if _, _, err = s.EnqueuePlan(ctx, other.ID, "another-operation"); !errors.Is(err, ErrBusy) {
		t.Fatal("same conversation admitted another unfinished task")
	}
	if _, err = s.Cancel(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err = s.db.Exec("UPDATE execution_plans SET expires_at=? WHERE id=?", time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano), other.ID).Error; err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.EnqueuePlan(ctx, other.ID, "expired-operation"); !errors.Is(err, ErrPlanStale) {
		t.Fatalf("expired plan accepted: %v", err)
	}
}

func TestAssistantAdmissionNeverLeavesAnOrphanMessage(t *testing.T) {
	ctx := context.Background()
	s, _, conversation, p := executionCase(t)
	p.Kind = "assistant_turn"
	p.Mode = "assistant"
	p.EffectCeiling = EffectCeiling{AllowModelRequests: true, ModelHTTPAttempts: 4}
	if _, err := s.SavePlan(ctx, p); !errors.Is(err, ErrInputInvalid) {
		t.Fatal("public plan endpoint accepted assistant kind")
	}
	input := MessageInput{ClientMessageID: "explicit-send", Role: "user", Origin: "user", PublicText: "解释这个固定输入", PolicyScopeRef: p.PolicyScopeRef}
	if err := s.db.Exec("CREATE TRIGGER fail_enqueue BEFORE INSERT ON e0_events BEGIN SELECT RAISE(ABORT,'synthetic enqueue failure'); END").Error; err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := s.EnqueueAssistant(ctx, p, input); err == nil {
		t.Fatal("failed enqueue reported accepted")
	}
	page, err := s.ConversationMessages(ctx, conversation.ID, 0, 100)
	if err != nil || len(page.Items) != 0 {
		t.Fatal("message persisted without a task")
	}
	if err = s.db.Exec("DROP TRIGGER fail_enqueue").Error; err != nil {
		t.Fatal(err)
	}
	job, message, replay, err := s.EnqueueAssistant(ctx, p, input)
	if err != nil || replay || job.Kind != "assistant_turn" || message.JobID != job.ID {
		t.Fatalf("assistant acceptance: %v", err)
	}
	duplicate, same, replay, err := s.EnqueueAssistant(ctx, p, input)
	if err != nil || !replay || duplicate.ID != job.ID || same.ID != message.ID {
		t.Fatal("assistant retry did not return original bound turn")
	}
	input.PublicText = "changed request"
	if _, _, _, err = s.EnqueueAssistant(ctx, p, input); !errors.Is(err, ErrConflict) {
		t.Fatal("same client_message_id changed text")
	}
}

func TestSealAndResultEventsUseOneImmutableResolvedInput(t *testing.T) {
	ctx := context.Background()
	s, snapshot, _, p := executionCase(t)
	plan, err := s.SavePlan(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	job, _, err := s.EnqueuePlan(ctx, plan.ID, "seal-job")
	if err != nil {
		t.Fatal(err)
	}
	claimed, found, err := s.Claim(ctx)
	if err != nil || !found || claimed.ID != job.ID {
		t.Fatal("claim failed")
	}
	in := plan.PreparedInput
	in.EvaluationAt = "2026-10-28T03:00:00Z"
	sealed, err := s.SealInput(ctx, job.ID, in)
	if err != nil {
		t.Fatal(err)
	}
	sealed.Assets[0].Address = "203.0.113.40"
	loaded, err := s.GetJobInput(ctx, job.ID)
	if err != nil || loaded.Assets[0].Address == "203.0.113.40" {
		t.Fatal("sealed input aliased caller memory")
	}
	changed := in
	changed.Policies = append([]domain.Policy(nil), in.Policies...)
	changed.Policies[0].Action = "drop"
	if _, err = s.SealInput(ctx, job.ID, changed); !errors.Is(err, ErrConflict) {
		t.Fatal("old sealed input overwritten")
	}
	result, err := audit.Run(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.db.Exec("CREATE TRIGGER fail_result_event BEFORE INSERT ON e0_events WHEN NEW.type='findings_available' BEGIN SELECT RAISE(ABORT,'synthetic result failure'); END").Error; err != nil {
		t.Fatal(err)
	}
	if _, err = s.SaveJobAudit(ctx, job.ID, result); err == nil {
		t.Fatal("result event failure swallowed")
	}
	current, err := s.Get(ctx, job.ID)
	if err != nil || current.ResultRef != "" {
		t.Fatal("result reference partially saved")
	}
	var audits int64
	s.db.Raw("SELECT count(*) FROM audit_runs").Scan(&audits)
	if audits != 0 {
		t.Fatal("unlinked result committed before event")
	}
	s.db.Exec("DROP TRIGGER fail_result_event")
	record, err := s.SaveJobAudit(ctx, job.ID, result)
	if err != nil || record.SnapshotID != snapshot.ID || record.Input.EvaluationAt != in.EvaluationAt {
		t.Fatal("resolved input not bound to result", err)
	}
	if err = s.FinishWithCode(ctx, job.ID, "partial", "MODEL_NOT_CONFIGURED"); err != nil {
		t.Fatal(err)
	}
	page, terminal, err := s.Page(ctx, job.ID, 0, 100)
	if err != nil || terminal.Status != "partial" || terminal.ResultRef != record.ID || page.Events[len(page.Events)-1].Type != "job_terminal" {
		t.Fatal("partial result/terminal history invalid")
	}
	if err = s.AppendJobEvent(ctx, job.ID, "stage_changed", map[string]any{"stage": "late"}); !errors.Is(err, ErrJobTerminal) {
		t.Fatal("late event appended after terminal")
	}
}

func TestQueuedExpiryAndRecoveryNeverReplayRunningWork(t *testing.T) {
	ctx := context.Background()
	s, _, _, p := executionCase(t)
	plan, err := s.SavePlan(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	expired, _, err := s.EnqueuePlan(ctx, plan.ID, "expired-queue")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.db.Exec("UPDATE e0_jobs SET queue_deadline=? WHERE id=?", time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano), expired.ID).Error; err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.Claim(ctx); err != nil || ok {
		t.Fatal("expired queue was claimed")
	}
	got, err := s.Get(ctx, expired.ID)
	if err != nil || got.Status != "failed" || got.ErrorCode != "QUEUE_DEADLINE_EXCEEDED" {
		t.Fatalf("queue expiry not durable: %+v %v", got, err)
	}
	queuedPlan, err := s.SavePlan(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	queued, _, err := s.EnqueuePlan(ctx, queuedPlan.ID, "restart-queued")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Get(ctx, queued.ID)
	if got.Status != "queued" {
		t.Fatal("valid unstarted queue lost its accepted intent")
	}
	running, ok, err := s.Claim(ctx)
	if err != nil || !ok {
		t.Fatal(err)
	}
	if err = s.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Get(ctx, running.ID)
	if got.Status != "interrupted" {
		t.Fatal("running task replayed after recovery")
	}
}

func TestHistoricalReportAssistantUsesSavedInputAfterHeadsAndScopeChange(t *testing.T) {
	ctx := context.Background()
	s, source, conversation, p := executionCase(t)
	oldInput := p.PreparedInput
	oldInput.EvaluationAt = "2026-10-28T03:00:00Z"
	result, err := audit.Run(ctx, oldInput)
	if err != nil {
		t.Fatal(err)
	}
	run, err := s.SaveAuditInput(ctx, source.ID, oldInput, result)
	if err != nil {
		t.Fatal(err)
	}
	_, reviews, err := s.CaptureReportInput(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	report, err := s.SaveReport(ctx, run.ID, "markdown", reviews, []byte("# Frozen report"), "old-report")
	if err != nil {
		t.Fatal(err)
	}
	draft := mustDraft(t, s, source, source.Input.Assets[0].ID)
	draft.Changes[0].Roles[0].Role = "database"
	draft, err = s.UpdateProfileDraft(ctx, draft.ID, UpdateDraftRequest{ExpectedDraftRevision: draft.Revision, Changes: draft.Changes})
	if err != nil {
		t.Fatal(err)
	}
	mustConfirm(t, s, draft, mustPreview(t, s, draft.ID), "new-role-after-report", "roles")
	other, err := audit.SyntheticExample("internet-out")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SaveSnapshot(ctx, other); err != nil {
		t.Fatal(err)
	}
	conversation, err = s.PatchConversation(ctx, conversation.ID, ConversationPatch{ExpectedRevision: conversation.Revision, PolicyScopeRef: &other.Scope.Ref})
	if err != nil {
		t.Fatal(err)
	}
	p.Kind, p.Mode = "assistant_turn", "assistant"
	p.ContextReportID, p.EvaluationAt, p.Heads = report.ID, oldInput.EvaluationAt, nil
	p.ConversationRevision, p.PreparedInput = conversation.Revision, oldInput
	p.EffectCeiling = EffectCeiling{AllowModelRequests: true, ModelHTTPAttempts: 4}
	job, _, _, err := s.EnqueueAssistant(ctx, p, MessageInput{ClientMessageID: "explain-old-report", Role: "user", PublicText: "解释旧报告", PolicyScopeRef: source.ScopeRef})
	if err != nil {
		t.Fatalf("historical report blocked by current heads/scope: %v", err)
	}
	stored, err := s.GetPlan(ctx, job.PlanID)
	if err != nil || stored.ContextReportID != report.ID {
		t.Fatal("report context not persisted", err)
	}
	if _, found, err := s.Claim(ctx); err != nil || !found {
		t.Fatal(err)
	}
	sealed, err := s.SealInput(ctx, job.ID, oldInput)
	if err != nil || sealed.ProfileVersion != oldInput.ProfileVersion || sealed.EvaluationAt != oldInput.EvaluationAt {
		t.Fatal("old immutable input changed", err)
	}
	if _, err = s.AppendJobMessage(ctx, job.ID, MessageInput{ClientMessageID: "old-report-reply", Role: "assistant", Origin: "model", PublicText: "按旧清单解释", PolicyScopeRef: source.ScopeRef}); err != nil {
		t.Fatal(err)
	}
	current, err := s.GetConversation(ctx, conversation.ID)
	if err != nil || current.PolicyScopeRef != other.Scope.Ref {
		t.Fatal("historical explanation changed current scope", err)
	}
	if err = s.Finish(ctx, job.ID, "completed"); err != nil {
		t.Fatal(err)
	}
	p.ContextReportID = "report_missing"
	if _, _, _, err = s.EnqueueAssistant(ctx, p, MessageInput{ClientMessageID: "false-report", Role: "user", PublicText: "伪造报告", PolicyScopeRef: source.ScopeRef}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("fabricated report accepted: %v", err)
	}
	p.ContextReportID, p.PolicyScopeRef, p.PreparedInput, p.SnapshotID = report.ID, other.Scope.Ref, other, ""
	if _, _, _, err = s.EnqueueAssistant(ctx, p, MessageInput{ClientMessageID: "cross-scope-report", Role: "user", PublicText: "跨scope报告", PolicyScopeRef: other.Scope.Ref}); !errors.Is(err, ErrInputInvalid) {
		t.Fatalf("cross-scope report accepted: %v", err)
	}
}

func TestReportAssistantRejectsForgedBindingsAndBindsIdempotencyToReport(t *testing.T) {
	ctx := context.Background()
	s, source, conversation, p := executionCase(t)
	result, err := audit.Run(ctx, p.PreparedInput)
	if err != nil {
		t.Fatal(err)
	}
	run, err := s.SaveAuditInput(ctx, source.ID, p.PreparedInput, result)
	if err != nil {
		t.Fatal(err)
	}
	_, reviews, err := s.CaptureReportInput(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	report, err := s.SaveReport(ctx, run.ID, "json", reviews, []byte(`{"public":true}`), "report-binding")
	if err != nil {
		t.Fatal(err)
	}
	otherReport, err := s.SaveReport(ctx, run.ID, "markdown", reviews, []byte("# Original input"), "report-binding-other")
	if err != nil {
		t.Fatal(err)
	}
	otherSnapshot, err := s.SaveSnapshot(ctx, source.Input)
	if err != nil {
		t.Fatal(err)
	}
	p.Kind, p.Mode, p.ContextReportID, p.Heads, p.EvaluationAt = "assistant_turn", "assistant", report.ID, nil, run.Input.EvaluationAt
	p.EffectCeiling = EffectCeiling{AllowModelRequests: true, ModelHTTPAttempts: 4}
	for _, test := range []struct {
		name   string
		change func(*ExecutionPlan)
		want   error
	}{
		{"missing report", func(p *ExecutionPlan) { p.ContextReportID = "report_missing" }, ErrNotFound},
		{"another snapshot", func(p *ExecutionPlan) { p.SnapshotID = otherSnapshot.ID }, ErrInputInvalid},
		{"missing snapshot", func(p *ExecutionPlan) { p.SnapshotID = "" }, ErrInputInvalid},
		{"another scope", func(p *ExecutionPlan) { p.PolicyScopeRef = "different-scope" }, ErrInputInvalid},
		{"changed evaluation", func(p *ExecutionPlan) {
			p.EvaluationAt = "2026-10-27T01:00:00Z"
			p.PreparedInput.EvaluationAt = p.EvaluationAt
		}, ErrInputInvalid},
		{"changed collection fact", func(p *ExecutionPlan) { p.PreparedInput.Assets[0].Address = "198.51.100.40" }, ErrInputInvalid},
		{"changed business role", func(p *ExecutionPlan) { p.PreparedInput.Assets[0].Services[0].Role = "background" }, ErrInputInvalid},
		{"current heads injected", func(p *ExecutionPlan) { p.Heads = []AssetHeadVersion{{AssetID: source.Input.Assets[0].ID}} }, ErrInputInvalid},
		{"stale conversation revision", func(p *ExecutionPlan) { p.ConversationRevision++ }, ErrPlanStale},
	} {
		t.Run(test.name, func(t *testing.T) {
			bad := p
			bad.PreparedInput, err = cloneConfirmationInput(p.PreparedInput)
			if err != nil {
				t.Fatal(err)
			}
			test.change(&bad)
			_, _, _, got := s.EnqueueAssistant(ctx, bad, MessageInput{ClientMessageID: "invalid-report-" + test.name, Role: "user", PublicText: "解释指定的历史报告", PolicyScopeRef: bad.PolicyScopeRef})
			if !errors.Is(got, test.want) {
				t.Fatalf("forged report binding: want %v got %v", test.want, got)
			}
		})
	}
	for _, kind := range []string{"audit", "collect", "connection_check"} {
		bad := p
		bad.Kind, bad.Mode = kind, "rules_only"
		bad.EffectCeiling = EffectCeiling{}
		if _, err = s.SavePlan(ctx, bad); !errors.Is(err, ErrInputInvalid) {
			t.Fatal("non-assistant plan used report to bypass current business heads", kind, err)
		}
	}
	page, err := s.ConversationMessages(ctx, conversation.ID, 0, 100)
	if err != nil || len(page.Items) != 0 {
		t.Fatal("rejected report context left an orphan public message", err)
	}
	messageInput := MessageInput{ClientMessageID: "report-idempotent", Role: "user", PublicText: "解释指定的历史报告", PolicyScopeRef: p.PolicyScopeRef}
	job, message, replay, err := s.EnqueueAssistant(ctx, p, messageInput)
	if err != nil || replay {
		t.Fatal(job, replay, err)
	}
	changed := p
	changed.ContextReportID = otherReport.ID
	if _, _, _, err = s.EnqueueAssistant(ctx, changed, messageInput); !errors.Is(err, ErrConflict) {
		t.Fatal("same message key changed its report", err)
	}
	archived := true
	if _, err = s.PatchConversation(ctx, conversation.ID, ConversationPatch{ExpectedRevision: conversation.Revision, Archived: &archived}); err != nil {
		t.Fatal(err)
	}
	if same, saved, replay, err := s.EnqueueAssistant(ctx, p, messageInput); err != nil || !replay || same.ID != job.ID || saved.ID != message.ID {
		t.Fatal("accepted replay was blocked by later conversation changes", err)
	}
	messageInput.ClientMessageID = "archived-new-report-question"
	if _, _, _, err = s.EnqueueAssistant(ctx, p, messageInput); !errors.Is(err, ErrConversationArchived) {
		t.Fatal("historic context bypassed archive", err)
	}
	if err = s.FinishWithCode(ctx, job.ID, "failed", "MODEL_NOT_CONFIGURED"); err != nil {
		t.Fatal("historic task terminal message failed", err)
	}
	page, err = s.ConversationMessages(ctx, conversation.ID, 0, 100)
	if err != nil || len(page.Items) != 2 || page.Items[1].Role != "event" || page.Items[1].PolicyScopeRef != p.PolicyScopeRef {
		t.Fatal("terminal fallback lost its historical report scope", page, err)
	}
}
