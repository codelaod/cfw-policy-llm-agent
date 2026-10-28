package storage

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"cfw-policy-llm-agent/server/internal/audit"
	"gorm.io/gorm"
)

func lifecycleJob(t *testing.T, kind string, claim bool) (*Store, SnapshotRecord, Conversation, Job) {
	t.Helper()
	ctx := context.Background()
	s, snapshot, conv, p := executionCase(t)
	var job Job
	var err error
	if kind == "assistant_turn" {
		p.Kind, p.Mode = "assistant_turn", "assistant"
		p.Request = json.RawMessage(`{"kind":"assistant_turn"}`)
		p.EffectCeiling = EffectCeiling{AllowModelRequests: true, ModelHTTPAttempts: 4}
		job, _, _, err = s.EnqueueAssistant(ctx, p, MessageInput{ClientMessageID: "lifecycle-user", Role: "user", PublicText: "解释固定输入", PolicyScopeRef: snapshot.ScopeRef})
	} else {
		var plan ExecutionPlan
		plan, err = s.SavePlan(ctx, p)
		if err == nil {
			job, _, err = s.EnqueuePlan(ctx, plan.ID, "lifecycle-audit")
		}
	}
	if err != nil {
		t.Fatal(err)
	}
	if claim {
		claimed, found, e := s.Claim(ctx)
		if e != nil || !found || claimed.ID != job.ID {
			t.Fatal("claim", e)
		}
		job = claimed
		if kind == "audit" {
			in, e := s.SealInput(ctx, job.ID, p.PreparedInput)
			if e != nil {
				t.Fatal(e)
			}
			result, e := audit.Run(ctx, in)
			if e != nil {
				t.Fatal(e)
			}
			if _, e = s.SaveJobAudit(ctx, job.ID, result); e != nil {
				t.Fatal(e)
			}
		}
	}
	return s, snapshot, conv, job
}
func steerLifecycleConversation(t *testing.T, s *Store, conv Conversation, change string) {
	t.Helper()
	patch := ConversationPatch{ExpectedRevision: conv.Revision}
	if change == "scope" {
		in, e := audit.SyntheticExample("internet-out")
		if e != nil {
			t.Fatal(e)
		}
		other, e := s.SaveSnapshot(context.Background(), in)
		if e != nil {
			t.Fatal(e)
		}
		patch.PolicyScopeRef = &other.ScopeRef
	} else {
		archived := true
		patch.Archived = &archived
	}
	if _, e := s.PatchConversation(context.Background(), conv.ID, patch); e != nil {
		t.Fatal(e)
	}
}
func TestAcceptedRunningJobFinishesAfterConversationChanges(t *testing.T) {
	for _, kind := range []string{"audit", "assistant_turn"} {
		for _, change := range []string{"scope", "archive"} {
			for _, action := range []string{"reply", "finish", "recover", "cancel"} {
				t.Run(kind+"/"+change+"/"+action, func(t *testing.T) {
					s, snapshot, conv, job := lifecycleJob(t, kind, true)
					ctx := context.Background()
					steerLifecycleConversation(t, s, conv, change)
					want := "completed"
					switch action {
					case "reply":
						role, origin := "event", "application"
						if kind == "assistant_turn" {
							role, origin = "assistant", "model"
						}
						if _, e := s.AppendJobMessage(ctx, job.ID, MessageInput{ClientMessageID: "lifecycle-reply", Role: role, Origin: origin, PublicText: "任务仍引用已接受的固定资料", PolicyScopeRef: snapshot.ScopeRef}); e != nil {
							t.Fatal("accepted reply blocked", e)
						}
						if e := s.Finish(ctx, job.ID, "completed"); e != nil {
							t.Fatal(e)
						}
					case "finish":
						if e := s.Finish(ctx, job.ID, "completed"); e != nil {
							t.Fatal("terminal rollback", e)
						}
					case "recover":
						want = "interrupted"
						if e := s.Recover(ctx); e != nil {
							t.Fatal("recovery blocked", e)
						}
					case "cancel":
						want = "cancelled"
						if _, e := s.Cancel(ctx, job.ID); e != nil {
							t.Fatal(e)
						}
						if _, e := s.AppendJobMessage(ctx, job.ID, MessageInput{ClientMessageID: "late-normal-reply", Role: "assistant", Origin: "model", PublicText: "不得保存", PolicyScopeRef: snapshot.ScopeRef}); !errors.Is(e, ErrJobTerminal) {
							t.Fatal("cancel accepted late normal reply", e)
						}
						if e := s.Finish(ctx, job.ID, "completed"); e != nil {
							t.Fatal(e)
						}
					}
					got, e := s.Get(ctx, job.ID)
					if e != nil || got.Status != want {
						t.Fatal("job never reached terminal", got, e)
					}
					page, e := s.ConversationMessages(ctx, conv.ID, 0, 100)
					if e != nil || len(page.Items) != 2 {
						t.Fatal("terminal public history missing", page, e)
					}
					for _, m := range page.Items {
						if m.PolicyScopeRef != snapshot.ScopeRef {
							t.Fatal("job message silently rebound to current scope", m)
						}
					}
				})
			}
		}
	}
}
func TestAcceptedQueuedJobCancellationExpiryAndRecoveryAfterConversationChanges(t *testing.T) {
	for _, kind := range []string{"audit", "assistant_turn"} {
		for _, change := range []string{"scope", "archive"} {
			for _, action := range []string{"cancel", "expire", "recover"} {
				t.Run(kind+"/"+change+"/"+action, func(t *testing.T) {
					s, snapshot, conv, job := lifecycleJob(t, kind, false)
					ctx := context.Background()
					steerLifecycleConversation(t, s, conv, change)
					want := "cancelled"
					switch action {
					case "cancel":
						if _, e := s.Cancel(ctx, job.ID); e != nil {
							t.Fatal(e)
						}
					case "expire":
						want = "failed"
						if e := s.db.Exec("UPDATE e0_jobs SET queue_deadline=? WHERE id=?", time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano), job.ID).Error; e != nil {
							t.Fatal(e)
						}
						if e := s.ExpireQueued(ctx); e != nil {
							t.Fatal(e)
						}
					case "recover":
						want = "interrupted"
						if e := s.Recover(ctx); e != nil {
							t.Fatal(e)
						}
						if got, e := s.Get(ctx, job.ID); e != nil || got.Status != "queued" {
							t.Fatal("valid accepted queue was discarded", e)
						}
						if _, found, e := s.Claim(ctx); e != nil || !found {
							t.Fatal(e)
						}
						if e := s.Recover(ctx); e != nil {
							t.Fatal(e)
						}
					}
					got, e := s.Get(ctx, job.ID)
					if e != nil || got.Status != want {
						t.Fatal(got, e)
					}
					page, e := s.ConversationMessages(ctx, conv.ID, 0, 100)
					if e != nil {
						t.Fatal(e)
					}
					for _, m := range page.Items {
						if m.PolicyScopeRef != snapshot.ScopeRef {
							t.Fatal("queued message rebound scope", m)
						}
					}
				})
			}
		}
	}
}
func TestOrdinaryMessageAppendCannotUseJobIDAsScopeOrArchiveBypass(t *testing.T) {
	for _, change := range []string{"scope", "archive"} {
		for _, sender := range []struct{ role, origin string }{{"event", "application"}, {"assistant", "model"}} {
			t.Run(change+"/"+sender.role, func(t *testing.T) {
				s, snapshot, conv, job := lifecycleJob(t, "assistant_turn", true)
				steerLifecycleConversation(t, s, conv, change)
				want := ErrInputInvalid
				if change == "archive" {
					want = ErrConversationArchived
				}
				for _, id := range []string{job.ID, "job_fabricated"} {
					_, _, e := s.AppendConversationMessage(context.Background(), conv.ID, MessageInput{ClientMessageID: "unvalidated-" + id, Role: sender.role, Origin: sender.origin, PublicText: "ordinary append must not grant a context", PolicyScopeRef: snapshot.ScopeRef, JobID: id})
					if !errors.Is(e, want) {
						t.Fatal("raw JobID granted context bypass", e)
					}
				}
			})
		}
	}
}
func TestFixedReportFinalizationAfterConversationChanges(t *testing.T) {
	for _, change := range []string{"scope", "archive"} {
		t.Run(change, func(t *testing.T) {
			s, job, record, request := preparedFinalization(t)
			conv, e := s.GetConversation(context.Background(), job.ConversationID)
			if e != nil {
				t.Fatal(e)
			}
			steerLifecycleConversation(t, s, conv, change)
			report, e := s.FinalizeJobWithReport(context.Background(), job.ID, request)
			if e != nil || report.AuditID != record.ID {
				t.Fatal("fixed report lifecycle blocked", e)
			}
			page, e := s.ConversationMessages(context.Background(), conv.ID, 0, 100)
			if e != nil || len(page.Items) != 2 || page.Items[1].PolicyScopeRef != record.Input.Scope.Ref {
				t.Fatal("fixed report message rebound", page, e)
			}
		})
	}
}

func TestValidatedJobMessageContextChecksBindingAndSender(t *testing.T) {
	s, snapshot, conv, job := lifecycleJob(t, "assistant_turn", true)
	ctx := context.Background()
	other, e := s.CreateConversation(ctx, "另一个会话", snapshot.ScopeRef)
	if e != nil {
		t.Fatal(e)
	}
	ordinary := MessageInput{ClientMessageID: "validated-reply", Role: "assistant", Origin: "model", PublicText: "固定任务解释", PolicyScopeRef: snapshot.ScopeRef, JobID: job.ID}
	for _, mode := range []string{"foreign-conversation", "forged-job", "user-sender", "false-application-sender", "wrong-scope"} {
		t.Run(mode, func(t *testing.T) {
			input := ordinary
			conversationID := conv.ID
			contextID := job.ID
			switch mode {
			case "foreign-conversation":
				conversationID = other.ID
			case "forged-job":
				input.JobID = "job_fabricated"
			case "user-sender":
				input.Role = "user"
				input.Origin = "user"
			case "false-application-sender":
				input.Role = "event"
				input.Origin = "model"
			case "wrong-scope":
				input.PolicyScopeRef = "scope_wrong"
			}
			e := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
				_, _, err := insertConversationMessageContext(tx, conversationID, input, "", contextID)
				return err
			})
			if !errors.Is(e, ErrInputInvalid) {
				t.Fatal("forged job context accepted", e)
			}
		})
	}
	if _, e = s.Cancel(ctx, job.ID); e != nil {
		t.Fatal(e)
	}
	e = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error { _, _, err := insertJobConversationMessage(tx, job.ID, ordinary); return err })
	if !errors.Is(e, ErrJobTerminal) {
		t.Fatal("cancelling job accepted direct late model output", e)
	}
	if e = s.Finish(ctx, job.ID, "completed"); e != nil {
		t.Fatal(e)
	}
	ordinary.Role, ordinary.Origin = "event", "application"
	e = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error { _, _, err := insertJobConversationMessage(tx, job.ID, ordinary); return err })
	if !errors.Is(e, ErrJobTerminal) {
		t.Fatal("terminal job accepted a normal application reply", e)
	}
}
