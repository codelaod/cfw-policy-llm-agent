package storage

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm"
)

func e3BudgetCase(t *testing.T, appCloud, appModel, sessionCloud, sessionModel int64) (*Store, ExecutionPlan, Job) {
	t.Helper()
	ctx := context.Background()
	store, _, _, initial := executionCase(t)
	if err := store.db.Transaction(migrateBudgets); err != nil {
		t.Fatal(err)
	}
	policy := BudgetPolicy{Ref: initial.BudgetRef, ApplicationCloudAttempts: appCloud, ApplicationModelAttempts: appModel, SessionCloudAttempts: sessionCloud, SessionModelAttempts: sessionModel, ExpiresAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)}
	if err := store.ConfigureBudgetPolicy(ctx, policy); err != nil {
		t.Fatal(err)
	}
	initial.Mode = "rules_and_agent"
	initial.EffectCeiling = EffectCeiling{AllowCloudReads: true, CloudHTTPAttempts: 100, AllowModelRequests: true, ModelHTTPAttempts: 6}
	plan, err := store.SavePlan(ctx, initial)
	if err != nil {
		t.Fatal(err)
	}
	job, _, err := store.EnqueuePlan(ctx, plan.ID, "e3-budget-job")
	if err != nil {
		t.Fatal(err)
	}
	claimed, found, err := store.Claim(ctx)
	if err != nil || !found || claimed.ID != job.ID {
		t.Fatal("budget task not claimed", err)
	}
	return store, initial, claimed
}

func TestE3BudgetAtomicApplicationLimitBeforeAnyAttempt(t *testing.T) {
	ctx := context.Background()
	store, plan, job := e3BudgetCase(t, 0, 3, 0, 100)
	var wg sync.WaitGroup
	errs := make(chan error, 12)
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := store.ReserveAttempt(ctx, job.ID, "model", "configured-model", plan.ConfigRevision)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	accepted := 0
	for err := range errs {
		if err == nil {
			accepted++
		} else if !errors.Is(err, ErrBudgetExhausted) {
			t.Fatal(err)
		}
	}
	if accepted != 3 {
		t.Fatalf("atomic budget admitted %d instead of 3", accepted)
	}
	usage, err := store.BudgetUsage(ctx, plan.BudgetRef, "", "")
	if err != nil || usage.ModelAttempts != 3 || usage.UnknownDelivery != 3 {
		t.Fatal("reserved attempts were refunded or hidden", err)
	}
}

func TestE3BudgetCloudAndCredentialShareTaskAllowance(t *testing.T) {
	ctx := context.Background()
	store, initial, oldJob := e3BudgetCase(t, 10, 10, 10, 10)
	if err := store.FinishWithCode(ctx, oldJob.ID, "failed", "TEST_SETUP"); err != nil {
		t.Fatal(err)
	}
	initial.EffectCeiling.CloudHTTPAttempts = 2
	initial.EffectCeiling.ModelHTTPAttempts = 1
	plan, err := store.SavePlan(ctx, initial)
	if err != nil {
		t.Fatal(err)
	}
	job, _, err := store.EnqueuePlan(ctx, plan.ID, "e3-limited-job")
	if err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.Claim(ctx); err != nil || !found {
		t.Fatal(err)
	}
	for _, class := range []string{"credential", "cloud"} {
		if _, err = store.ReserveAttempt(ctx, job.ID, class, "cloud-source", initial.ConfigRevision); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = store.ReserveAttempt(ctx, job.ID, "cloud", "cloud-source", initial.ConfigRevision); !errors.Is(err, ErrBudgetExhausted) {
		t.Fatal("credential exchange got a separate cloud allowance", err)
	}
	if _, err = store.ReserveAttempt(ctx, job.ID, "model", "configured-model", initial.ConfigRevision); err != nil {
		t.Fatal(err)
	}
	if _, err = store.ReserveAttempt(ctx, job.ID, "model", "configured-model", initial.ConfigRevision); !errors.Is(err, ErrBudgetExhausted) {
		t.Fatal("task model limit ignored", err)
	}
}

func TestE3BudgetSessionAndApplicationSurviveNewJobsAndConversations(t *testing.T) {
	ctx := context.Background()
	store, initial, job := e3BudgetCase(t, 0, 3, 0, 2)
	for range 2 {
		if _, err := store.ReserveAttempt(ctx, job.ID, "model", "configured-model", initial.ConfigRevision); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.FinishWithCode(ctx, job.ID, "failed", "TEST_SETUP"); err != nil {
		t.Fatal(err)
	}
	plan, err := store.SavePlan(ctx, initial)
	if err != nil {
		t.Fatal(err)
	}
	next, _, err := store.EnqueuePlan(ctx, plan.ID, "e3-same-session")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.Claim(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = store.ReserveAttempt(ctx, next.ID, "model", "configured-model", initial.ConfigRevision); !errors.Is(err, ErrBudgetExhausted) {
		t.Fatal("new job or key alias reset session usage", err)
	}
	if err = store.FinishWithCode(ctx, next.ID, "failed", "TEST_SETUP"); err != nil {
		t.Fatal(err)
	}
	conversation, err := store.CreateConversation(ctx, "另一个会话", initial.PolicyScopeRef)
	if err != nil {
		t.Fatal(err)
	}
	initial.ConversationID, initial.ConversationRevision = conversation.ID, conversation.Revision
	plan, err = store.SavePlan(ctx, initial)
	if err != nil {
		t.Fatal(err)
	}
	newSession, _, err := store.EnqueuePlan(ctx, plan.ID, "e3-new-session")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.Claim(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = store.ReserveAttempt(ctx, newSession.ID, "model", "configured-model", initial.ConfigRevision); err != nil {
		t.Fatal(err)
	}
	if _, err = store.ReserveAttempt(ctx, newSession.ID, "model", "configured-model", initial.ConfigRevision); !errors.Is(err, ErrBudgetExhausted) {
		t.Fatal("new conversation reset application budget", err)
	}
}

func TestE3BudgetRefusesCancelledTerminalExpiredOrUnauthorizedTasks(t *testing.T) {
	for _, scenario := range []string{"cancelled", "terminal", "deadline", "root-deadline", "stale-config", "missing-policy", "effect-denied"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			store, initial, job := e3BudgetCase(t, 10, 10, 10, 10)
			revision := initial.ConfigRevision
			switch scenario {
			case "cancelled":
				if _, err := store.Cancel(ctx, job.ID); err != nil {
					t.Fatal(err)
				}
			case "terminal":
				if err := store.FinishWithCode(ctx, job.ID, "failed", "TEST_SETUP"); err != nil {
					t.Fatal(err)
				}
			case "deadline":
				store.db.Exec("UPDATE e0_jobs SET execution_deadline=? WHERE id=?", time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano), job.ID)
			case "root-deadline":
				store.db.Exec("UPDATE e0_jobs SET root_deadline=? WHERE id=?", time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano), job.ID)
			case "stale-config":
				revision = "new-server-config"
			case "missing-policy":
				if err := store.db.Transaction(func(tx *gorm.DB) error {
					for _, table := range []string{"budget_policy_heads", "budget_policy_revisions", "execution_budget_bindings", "budget_policies"} {
						column := "ref"
						if table == "execution_budget_bindings" {
							column = "budget_ref"
						}
						if e := tx.Exec("DELETE FROM "+table+" WHERE "+column+"=?", initial.BudgetRef).Error; e != nil {
							return e
						}
					}
					return nil
				}); err != nil {
					t.Fatal("could not construct missing-policy fixture", err)
				}
			case "effect-denied":
				if err := store.db.Transaction(func(tx *gorm.DB) error {
					p, err := planRecord(tx, job.PlanID)
					if err != nil {
						return err
					}
					p.EffectCeiling.AllowModelRequests = false
					p.EffectCeiling.ModelHTTPAttempts = 0
					body, err := marshalBounded(p, 128<<10)
					if err != nil {
						return err
					}
					return tx.Exec("UPDATE execution_plans SET body=? WHERE id=?", string(body), p.ID).Error
				}); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := store.ReserveAttempt(ctx, job.ID, "model", "configured-model", revision); err == nil {
				t.Fatal("closed or unauthorized execution obtained a new HTTP attempt")
			}
			var count int64
			store.db.Raw("SELECT count(*) FROM outbound_attempts").Scan(&count)
			if count != 0 {
				t.Fatal("rejected execution consumed or emitted a request record")
			}
		})
	}
}

func TestE3BudgetUnknownDeliveryIsNotRefundedAndLateResultIsControlled(t *testing.T) {
	ctx := context.Background()
	store, initial, job := e3BudgetCase(t, 0, 2, 0, 2)
	id, err := store.ReserveAttempt(ctx, job.ID, "model", "configured-model", initial.ConfigRevision)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.RecordAttemptResult(ctx, id, "unknown", "PRIVATE_PROVIDER_SECRET", 0, 11); !errors.Is(err, ErrInputInvalid) {
		t.Fatal("uppercase upstream text bypassed controlled error codes", err)
	}
	if _, err = store.Cancel(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	if err = store.RecordAttemptResult(ctx, id, "unknown", "UPSTREAM_TRANSPORT", 0, 11); err != nil {
		t.Fatal("late result bookkeeping was lost", err)
	}
	if err = store.RecordAttemptResult(ctx, id, "unknown", "UPSTREAM_TRANSPORT", 0, 11); err != nil {
		t.Fatal("same completion not idempotent", err)
	}
	if err = store.RecordAttemptResult(ctx, id, "response", "", 200, 11); !errors.Is(err, ErrConflict) {
		t.Fatal("unknown attempt was rewritten as a refunded success", err)
	}
	if err = store.RecordAttemptResult(ctx, id, "failed", "PRIVATE PROVIDER ERROR", 0, 11); !errors.Is(err, ErrInputInvalid) {
		t.Fatal("raw error entered attempt record", err)
	}
	usage, err := store.BudgetUsage(ctx, initial.BudgetRef, "", "")
	if err != nil || usage.ModelAttempts != 1 || usage.UnknownDelivery != 1 {
		t.Fatal("unknown attempt disappeared", err)
	}
}

func TestE3BudgetPolicyCanOnlyTightenWithoutResettingUsage(t *testing.T) {
	ctx := context.Background()
	store, initial, job := e3BudgetCase(t, 0, 3, 0, 3)
	if _, err := store.ReserveAttempt(ctx, job.ID, "model", "configured-model", initial.ConfigRevision); err != nil {
		t.Fatal(err)
	}
	policy, err := store.GetBudgetPolicy(ctx, initial.BudgetRef)
	if err != nil {
		t.Fatal(err)
	}
	policy.ApplicationModelAttempts = 4
	if err = store.ConfigureBudgetPolicy(ctx, policy); !errors.Is(err, ErrConflict) {
		t.Fatal("configuration silently increased approved period", err)
	}
	policy.ApplicationModelAttempts = 1
	if err = store.ConfigureBudgetPolicy(ctx, policy); err != nil {
		t.Fatal(err)
	}
	if _, err = store.ReserveAttempt(ctx, job.ID, "model", "configured-model", initial.ConfigRevision); !errors.Is(err, ErrBudgetExhausted) {
		t.Fatal("tightening reset prior attempts", err)
	}
}
