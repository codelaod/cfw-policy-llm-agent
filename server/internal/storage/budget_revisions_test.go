package storage

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestBudgetRevisionAddsServiceWithoutRefundAndPreservesTaskCeiling(t *testing.T) {
	ctx := context.Background()
	s, initial, job := e3BudgetCase(t, 0, 1, 0, 1)
	if _, e := s.ReserveAttempt(ctx, job.ID, "model", "configured-model", initial.ConfigRevision); e != nil {
		t.Fatal(e)
	}
	head, e := s.CurrentBudgetRevision(ctx, initial.BudgetRef)
	if e != nil || head.Revision != 1 {
		t.Fatal(head, e)
	}
	larger := int64(10)
	change := BudgetChange{ExpectedBaseRevision: head.Revision, ChangeID: "add-cloud", ActorRef: "local-operator", Reason: "explicit additional read-only category", AuthorizationRef: "synthetic-authorization", ApplicationCloudAttempts: &larger, SessionCloudAttempts: &larger, ApplicationModelAttempts: &larger, SessionModelAttempts: &larger}
	next, replay, e := s.ReviseBudgetPolicy(ctx, initial.BudgetRef, change)
	if e != nil || replay || next.Revision != 2 {
		t.Fatal(next, e)
	}
	again, replay, e := s.ReviseBudgetPolicy(ctx, initial.BudgetRef, change)
	if e != nil || !replay || again.Revision != next.Revision {
		t.Fatal(again, e)
	}
	usage, e := s.BudgetUsage(ctx, initial.BudgetRef, "", "")
	if e != nil || usage.ModelAttempts != 1 || usage.UnknownDelivery != 1 {
		t.Fatal("revision changed physical history", usage, e)
	}
	for n := 1; n < 6; n++ {
		if _, e = s.ReserveAttempt(ctx, job.ID, "model", "configured-model", initial.ConfigRevision); e != nil {
			t.Fatal(n, e)
		}
	}
	if _, e = s.ReserveAttempt(ctx, job.ID, "model", "configured-model", initial.ConfigRevision); !errors.Is(e, ErrBudgetExhausted) {
		t.Fatal("addition expanded frozen task ceiling", e)
	}
	change.Reason = "different"
	if _, _, e = s.ReviseBudgetPolicy(ctx, initial.BudgetRef, change); !errors.Is(e, ErrConflict) {
		t.Fatal("change id accepted altered body", e)
	}
	change.ChangeID = "stale"
	if _, _, e = s.ReviseBudgetPolicy(ctx, initial.BudgetRef, change); !errors.Is(e, ErrVersionConflict) {
		t.Fatal("stale policy head overwritten", e)
	}
}
func TestBudgetContinuousAuthorizationExplicitRenewalAndTightening(t *testing.T) {
	ctx := context.Background()
	s, initial, job := e3BudgetCase(t, 0, 5, 0, 5)
	head, e := s.CurrentBudgetRevision(ctx, initial.BudgetRef)
	if e != nil {
		t.Fatal(e)
	}
	continuous := ""
	change := BudgetChange{ExpectedBaseRevision: head.Revision, ChangeID: "continuous", ActorRef: "local-operator", Reason: "explicit persistent authorization", AuthorizationRef: "synthetic", ExpiresAt: &continuous}
	revised, _, e := s.ReviseBudgetPolicy(ctx, initial.BudgetRef, change)
	if e != nil || revised.Policy.ExpiresAt != "" {
		t.Fatal(revised, e)
	}
	if _, e = s.ReserveAttempt(ctx, job.ID, "model", "configured-model", initial.ConfigRevision); e != nil {
		t.Fatal("explicit continuous authorization treated as expired", e)
	}
	zero := int64(0)
	change = BudgetChange{ExpectedBaseRevision: revised.Revision, ChangeID: "disable", ActorRef: "local-operator", Reason: "stop further model use", AuthorizationRef: "synthetic", ApplicationModelAttempts: &zero}
	disabled, _, e := s.ReviseBudgetPolicy(ctx, initial.BudgetRef, change)
	if e != nil || disabled.Policy.ApplicationModelAttempts != 0 {
		t.Fatal(disabled, e)
	}
	if _, e = s.ReserveAttempt(ctx, job.ID, "model", "configured-model", initial.ConfigRevision); !errors.Is(e, ErrBudgetExhausted) {
		t.Fatal("running task ignored tightened policy", e)
	}
	policy := disabled.Policy
	policy.ExpiresAt = time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)
	policy.ApplicationModelAttempts = 10
	if e = s.ConfigureBudgetPolicy(ctx, policy); !errors.Is(e, ErrConflict) {
		t.Fatal("ordinary configuration silently expanded authorization", e)
	}
}

func TestAdapterCeilingDoesNotRewriteSharedGrantAndCannotBeBypassed(t *testing.T) {
	ctx := context.Background()
	s, initial, job := e3BudgetCase(t, 10, 10, 10, 10)
	one := int64(1)
	private := AttemptCeiling{Ref: initial.BudgetRef, ApplicationAttempts: &one, SessionAttempts: &one}
	if _, e := s.ReserveAttemptWithCeiling(ctx, job.ID, "model", "configured-model", initial.ConfigRevision, private); e != nil {
		t.Fatal(e)
	}
	if _, e := s.ReserveAttemptWithCeiling(ctx, job.ID, "model", "configured-model", initial.ConfigRevision, private); !errors.Is(e, ErrBudgetExhausted) {
		t.Fatal(e)
	}
	head, e := s.GetBudgetPolicy(ctx, initial.BudgetRef)
	if e != nil || head.ApplicationModelAttempts != 10 || head.SessionModelAttempts != 10 {
		t.Fatal("adapter restriction changed durable grant", head, e)
	}
	if _, e = s.ReserveAttempt(ctx, job.ID, "cloud", "cloud-source", initial.ConfigRevision); e != nil {
		t.Fatal("model private restriction disabled cloud", e)
	}
}
