package storage

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"cfw-policy-llm-agent/server/internal/audit"
	"gorm.io/gorm"
)

func collaborationStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "collaboration.sqlite"), true)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.db.Transaction(migrateCollaboration); err != nil {
		s.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
func collaborationSnapshot(t *testing.T, s *Store) SnapshotRecord {
	t.Helper()
	in, err := audit.SyntheticExample("internet-in")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := s.SaveSnapshot(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func TestConversationsMessagesCASAndReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "history.sqlite")
	s, err := Open(path, true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if s != nil {
			s.Close()
		}
	}()
	if err = s.db.Transaction(migrateCollaboration); err != nil {
		t.Fatal(err)
	}
	snapshot := collaborationSnapshot(t, s)
	conv, err := s.CreateConversation(ctx, "本机规则复核", snapshot.ScopeRef)
	if err != nil || conv.Revision != 1 {
		t.Fatal(conv, err)
	}
	first, replayed, err := s.AppendConversationMessage(ctx, conv.ID, MessageInput{ClientMessageID: "user-message-1", Role: "user", PublicText: "请核查管理入口", PolicyScopeRef: snapshot.ScopeRef})
	if err != nil || replayed || first.Seq != 1 || first.Origin != "user" {
		t.Fatal(first, replayed, err)
	}
	last, replayed, err := s.AppendConversationMessage(ctx, conv.ID, MessageInput{ClientMessageID: "user-message-1", Role: "user", PublicText: "请核查管理入口", PolicyScopeRef: snapshot.ScopeRef})
	if err != nil || !replayed || last.ID != first.ID {
		t.Fatal("message replay changed", err)
	}
	if _, _, err = s.AppendConversationMessage(ctx, conv.ID, MessageInput{ClientMessageID: "user-message-1", Role: "user", PublicText: "different", PolicyScopeRef: snapshot.ScopeRef}); !errors.Is(err, ErrConflict) {
		t.Fatal("message idempotency conflict missing", err)
	}
	if _, _, err = s.AppendConversationMessage(ctx, conv.ID, MessageInput{ClientMessageID: "reply-message-1", Role: "assistant", Origin: "application", PublicText: "规则结果已保存，模型尚未使用。", PolicyScopeRef: snapshot.ScopeRef, JobID: "job-local", RefKind: "job", RefID: "job-local"}); err != nil {
		t.Fatal(err)
	}
	page, err := s.ConversationMessages(ctx, conv.ID, 0, 1)
	if err != nil || len(page.Items) != 1 || page.NextSeq != 1 || !page.HasMore || page.LastSeq != 2 {
		t.Fatal(page, err)
	}
	page, err = s.ConversationMessages(ctx, conv.ID, 1, 100)
	if err != nil || len(page.Items) != 1 || page.Items[0].Seq != 2 || page.HasMore {
		t.Fatal(page, err)
	}
	title := "已归档复核"
	archived := true
	updated, err := s.PatchConversation(ctx, conv.ID, ConversationPatch{ExpectedRevision: 1, Title: &title, Archived: &archived})
	if err != nil || !updated.Archived || updated.Revision != 2 {
		t.Fatal(updated, err)
	}
	if _, err = s.PatchConversation(ctx, conv.ID, ConversationPatch{ExpectedRevision: 1, Title: &title}); !errors.Is(err, ErrVersionConflict) {
		t.Fatal("stale conversation patch accepted", err)
	}
	if _, _, err = s.AppendConversationMessage(ctx, conv.ID, MessageInput{ClientMessageID: "after-archive", Role: "user", PublicText: "new", PolicyScopeRef: snapshot.ScopeRef}); !errors.Is(err, ErrConversationArchived) {
		t.Fatal("archived conversation accepted user send", err)
	}
	if replay, ok, err := s.AppendConversationMessage(ctx, conv.ID, MessageInput{ClientMessageID: "user-message-1", Role: "user", PublicText: "请核查管理入口", PolicyScopeRef: snapshot.ScopeRef}); err != nil || !ok || replay.ID != first.ID {
		t.Fatal("archive blocked a valid accepted replay", err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s = nil
	s, err = Open(path, false)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := s.GetConversation(ctx, conv.ID)
	if err != nil || loaded.Title != title || loaded.LastSeq != 2 || !loaded.Archived {
		t.Fatal(loaded, err)
	}
	falseValue := false
	loaded, err = s.PatchConversation(ctx, conv.ID, ConversationPatch{ExpectedRevision: 2, Archived: &falseValue})
	if err != nil || loaded.Archived || loaded.Revision != 3 {
		t.Fatal("explicit false skipped", loaded, err)
	}
}

func TestConcurrentMessageSequenceAndScopedHistory(t *testing.T) {
	s := collaborationStore(t)
	ctx := context.Background()
	snapshot := collaborationSnapshot(t, s)
	otherInput, err := audit.SyntheticExample("internet-out")
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.SaveSnapshot(ctx, otherInput)
	if err != nil {
		t.Fatal(err)
	}
	conv, err := s.CreateConversation(ctx, "持续会话", snapshot.ScopeRef)
	if err != nil {
		t.Fatal(err)
	}
	var group sync.WaitGroup
	outcomes := make(chan error, 8)
	for i := 0; i < 8; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			_, _, err := s.AppendConversationMessage(ctx, conv.ID, MessageInput{ClientMessageID: "same-click", Role: "user", PublicText: "one turn", PolicyScopeRef: snapshot.ScopeRef})
			outcomes <- err
		}()
	}
	group.Wait()
	close(outcomes)
	for err := range outcomes {
		if err != nil {
			t.Fatal(err)
		}
	}
	scope := other.ScopeRef
	conv, err = s.PatchConversation(ctx, conv.ID, ConversationPatch{ExpectedRevision: 1, PolicyScopeRef: &scope})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.AppendConversationMessage(ctx, conv.ID, MessageInput{ClientMessageID: "other-scope", Role: "user", PublicText: "外联问题", PolicyScopeRef: other.ScopeRef}); err != nil {
		t.Fatal(err)
	}
	current, err := s.MessagesForScope(ctx, conv.ID, other.ScopeRef, 20)
	if err != nil || len(current) != 1 || current[0].PublicText != "外联问题" {
		t.Fatal(current, err)
	}
	old, err := s.MessagesForScope(ctx, conv.ID, snapshot.ScopeRef, 20)
	if err != nil || len(old) != 1 || old[0].Seq != 1 {
		t.Fatal(old, err)
	}
	if _, _, err = s.AppendConversationMessage(ctx, conv.ID, MessageInput{ClientMessageID: "cross-scope-send", Role: "user", PublicText: "wrong current scope", PolicyScopeRef: snapshot.ScopeRef}); !errors.Is(err, ErrInputInvalid) {
		t.Fatal("user send silently changed scope", err)
	}
}

func TestConversationMessageParticipatesInCallerTransaction(t *testing.T) {
	s := collaborationStore(t)
	ctx := context.Background()
	snapshot := collaborationSnapshot(t, s)
	conv, err := s.CreateConversation(ctx, "原子绑定", snapshot.ScopeRef)
	if err != nil {
		t.Fatal(err)
	}
	want := errors.New("synthetic job insert failed")
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if _, _, err := insertConversationMessage(tx, conv.ID, MessageInput{ClientMessageID: "transaction-key", Role: "user", PublicText: "只在任务也保存后才可见", PolicyScopeRef: snapshot.ScopeRef}); err != nil {
			return err
		}
		return want
	})
	if !errors.Is(err, want) {
		t.Fatal(err)
	}
	page, err := s.ConversationMessages(ctx, conv.ID, 0, 100)
	if err != nil || len(page.Items) != 0 || page.LastSeq != 0 {
		t.Fatal("half-saved message escaped rollback", page, err)
	}
	if _, _, err = s.AppendConversationMessage(ctx, conv.ID, MessageInput{ClientMessageID: "transaction-key", Role: "user", PublicText: "只在任务也保存后才可见", PolicyScopeRef: snapshot.ScopeRef}); err != nil {
		t.Fatal("rollback consumed key", err)
	}
}

func TestWorkspaceKeepsExplicitConversationSelectionAfterRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "workspace.sqlite")
	s, err := Open(path, true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if s != nil {
			s.Close()
		}
	}()
	snapshot := collaborationSnapshot(t, s)
	first, err := s.CreateConversation(ctx, "较早会话", snapshot.ScopeRef)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.CreateConversation(ctx, "较新会话", snapshot.ScopeRef)
	if err != nil {
		t.Fatal(err)
	}
	current, err := s.CurrentWorkspace(ctx)
	if err != nil || current.ID != "local" || current.CurrentConversationID != second.ID {
		t.Fatal(current, err)
	}
	selected, err := s.SelectCurrentConversation(ctx, first.ID, current.Revision)
	if err != nil || selected.CurrentConversationID != first.ID {
		t.Fatal(selected, err)
	}
	if _, _, err = s.AppendConversationMessage(ctx, second.ID, MessageInput{ClientMessageID: "later-background-message", Role: "event", PublicText: "较新会话的后台任务更新", PolicyScopeRef: snapshot.ScopeRef}); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s = nil
	s, err = Open(path, false)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := s.CurrentWorkspace(ctx)
	if err != nil || loaded.CurrentConversationID != first.ID || loaded.Revision != selected.Revision {
		t.Fatal("latest updated conversation replaced explicit persisted choice", loaded, err)
	}
	if _, err = s.SelectCurrentConversation(ctx, second.ID, current.Revision); !errors.Is(err, ErrVersionConflict) {
		t.Fatal("stale workspace preference overwrote another choice", err)
	}
	archived := true
	if _, err = s.PatchConversation(ctx, first.ID, ConversationPatch{ExpectedRevision: first.Revision, Archived: &archived}); err != nil {
		t.Fatal(err)
	}
	empty, err := s.CurrentWorkspace(ctx)
	if err != nil || empty.CurrentConversationID != "" || empty.Revision != selected.Revision+1 {
		t.Fatal("archiving current did not clear persisted pointer atomically", empty, err)
	}
	if _, err = s.SelectCurrentConversation(ctx, first.ID, empty.Revision); !errors.Is(err, ErrConversationArchived) {
		t.Fatal("archived conversation implicitly reopened", err)
	}
	var jobs int64
	if err = s.db.Raw("SELECT count(*) FROM e0_jobs").Scan(&jobs).Error; err != nil || jobs != 0 {
		t.Fatal("changing workspace preference started execution", jobs, err)
	}
}

func TestUserMessageCanUseOnlyValidatedHistoricReportScope(t *testing.T) {
	s := collaborationStore(t)
	ctx := context.Background()
	old := collaborationSnapshot(t, s)
	otherInput, err := audit.SyntheticExample("internet-out")
	if err != nil {
		t.Fatal(err)
	}
	current, err := s.SaveSnapshot(ctx, otherInput)
	if err != nil {
		t.Fatal(err)
	}
	makeReport := func(snapshot SnapshotRecord, key string) ReportRecord {
		t.Helper()
		result, err := audit.Run(ctx, snapshot.Input)
		if err != nil {
			t.Fatal(err)
		}
		run, err := s.SaveAudit(ctx, snapshot.ID, result)
		if err != nil {
			t.Fatal(err)
		}
		_, heads, err := s.CaptureReportInput(ctx, run.ID)
		if err != nil {
			t.Fatal(err)
		}
		report, err := s.SaveReport(ctx, run.ID, "json", heads, []byte(`{"public":true}`), key)
		if err != nil {
			t.Fatal(err)
		}
		return report
	}
	oldReport := makeReport(old, "historic-report")
	secondOldReport := makeReport(old, "second-historic-report")
	currentReport := makeReport(current, "current-report")
	conv, err := s.CreateConversation(ctx, "当前出向与历史入向报告", current.ScopeRef)
	if err != nil {
		t.Fatal(err)
	}
	input := MessageInput{ClientMessageID: "historic-question", Role: "user", PublicText: "解释这份历史报告", PolicyScopeRef: old.ScopeRef}
	if _, _, err = s.AppendConversationMessage(ctx, conv.ID, input); !errors.Is(err, ErrInputInvalid) {
		t.Fatal("ordinary append accepted a historic scope", err)
	}
	insert := func(reportIDs ...string) (ConversationMessage, bool, error) {
		var message ConversationMessage
		var replay bool
		err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var e error
			message, replay, e = insertConversationMessage(tx, conv.ID, input, reportIDs...)
			return e
		})
		return message, replay, err
	}
	if _, _, err = insert("report_missing"); !errors.Is(err, ErrNotFound) {
		t.Fatal("fabricated report context was accepted", err)
	}
	if _, _, err = insert(currentReport.ID); !errors.Is(err, ErrInputInvalid) {
		t.Fatal("report with another scope was accepted", err)
	}
	if _, _, err = insert(oldReport.ID, secondOldReport.ID); !errors.Is(err, ErrInputInvalid) {
		t.Fatal("multiple report contexts were accepted", err)
	}
	message, replay, err := insert(oldReport.ID)
	if err != nil || replay || message.PolicyScopeRef != old.ScopeRef {
		t.Fatal(message, replay, err)
	}
	got, err := s.GetConversation(ctx, conv.ID)
	if err != nil || got.PolicyScopeRef != current.ScopeRef || got.Revision != conv.Revision || got.LastSeq != 1 {
		t.Fatal("historic question changed current conversation scope", got, err)
	}
	if _, _, err = insert(secondOldReport.ID); !errors.Is(err, ErrConflict) {
		t.Fatal("same message key silently changed its report context", err)
	}
	archived := true
	if _, err = s.PatchConversation(ctx, conv.ID, ConversationPatch{ExpectedRevision: conv.Revision, Archived: &archived}); err != nil {
		t.Fatal(err)
	}
	if saved, replay, err := insert(oldReport.ID); err != nil || !replay || saved.ID != message.ID {
		t.Fatal("archive blocked replay of accepted historic question", err)
	}
	input.ClientMessageID = "after-archive-historic"
	if _, _, err = insert(oldReport.ID); !errors.Is(err, ErrConversationArchived) {
		t.Fatal("historic report bypassed archived conversation", err)
	}
}
