package storage

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"cfw-policy-llm-agent/server/internal/domain"
)

func TestE3AttemptUsageIsAtomicNullableAndReported(t *testing.T) {
	ctx := context.Background()
	store, plan, job := e3BudgetCase(t, 0, 6, 0, 6)
	first, err := store.ReserveAttempt(ctx, job.ID, "model", "configured-model", plan.ConfigRevision)
	if err != nil {
		t.Fatal(err)
	}
	usage := domain.ModelUsage{PromptTokens: 3, CompletionTokens: 2, TotalTokens: 9}
	if err = store.RecordAttemptResultWithUsage(ctx, first, "response", "", 200, 1, &usage); err != nil {
		t.Fatal(err)
	}
	usage.TotalTokens = 99
	second, err := store.ReserveAttempt(ctx, job.ID, "model", "configured-model", plan.ConfigRevision)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.RecordAttemptResult(ctx, second, "response", "", 200, 1); err != nil {
		t.Fatal(err)
	}
	third, err := store.ReserveAttempt(ctx, job.ID, "model", "configured-model", plan.ConfigRevision)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.RecordAttemptResultWithUsage(ctx, third, "response", "", 200, 1, &domain.ModelUsage{}); err != nil {
		t.Fatal(err)
	}
	items, err := store.JobAttempts(ctx, job.ID)
	if err != nil || len(items) != 3 {
		t.Fatal(err)
	}
	if items[0].Usage == nil || items[0].Usage.TotalTokens != 9 || items[1].Usage != nil || items[2].Usage == nil || items[2].Usage.TotalTokens != 0 {
		t.Fatal("known zero and missing usage were merged or caller mutated saved usage")
	}
	summary, err := store.BudgetUsage(ctx, plan.BudgetRef, "", job.ID)
	if err != nil || summary.KnownUsageResponses != 2 || summary.UnknownUsageAttempts != 1 || summary.ReportedTokens == nil || summary.ReportedTokens.TotalTokens != 9 || summary.ReportedTokens.PromptTokens != 3 {
		t.Fatal("reported usage totals or unknown count lost", err)
	}
	if err = store.RecordAttemptResultWithUsage(ctx, first, "response", "", 200, 1, &domain.ModelUsage{PromptTokens: 3, CompletionTokens: 2, TotalTokens: 9}); err != nil {
		t.Fatal("same reported usage not idempotent", err)
	}
	if err = store.RecordAttemptResult(ctx, first, "response", "", 200, 1); !errors.Is(err, ErrConflict) {
		t.Fatal("plain compatibility API erased known usage", err)
	}
}

func TestE3AttemptUsageFailureCannotPartiallyFinishOrInventTokens(t *testing.T) {
	ctx := context.Background()
	store, plan, job := e3BudgetCase(t, 0, 3, 0, 3)
	id, err := store.ReserveAttempt(ctx, job.ID, "model", "configured-model", plan.ConfigRevision)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.RecordAttemptResultWithUsage(ctx, id, "response", "", 200, 1, &domain.ModelUsage{PromptTokens: -1}); !errors.Is(err, ErrInputInvalid) {
		t.Fatal("negative usage accepted", err)
	}
	if err = store.db.Exec(`CREATE TRIGGER reject_usage_write BEFORE UPDATE ON outbound_attempts BEGIN SELECT RAISE(ABORT,'synthetic usage write failure'); END`).Error; err != nil {
		t.Fatal(err)
	}
	if err = store.RecordAttemptResultWithUsage(ctx, id, "response", "", 200, 1, &domain.ModelUsage{PromptTokens: 1, TotalTokens: 1}); err == nil {
		t.Fatal("failed usage transaction reported saved")
	}
	items, err := store.JobAttempts(ctx, job.ID)
	if err != nil || len(items) != 1 || items[0].State != "reserved" || items[0].Usage != nil {
		t.Fatal("usage failure partially saved attempt", err)
	}
	summary, err := store.BudgetUsage(ctx, plan.BudgetRef, "", "")
	if err != nil || summary.KnownUsageResponses != 0 || summary.UnknownUsageAttempts != 1 || summary.ReportedTokens != nil {
		t.Fatal("unknown usage became zero totals", err)
	}
	encoded, _ := json.Marshal(summary)
	if string(encoded) == "" {
		t.Fatal("missing usage projection")
	}
}

func TestE3AttemptUsageStoredPartialProjectionCannotBecomeKnownZero(t *testing.T) {
	ctx := context.Background()
	store, plan, job := e3BudgetCase(t, 0, 3, 0, 3)
	id, err := store.ReserveAttempt(ctx, job.ID, "model", "configured-model", plan.ConfigRevision)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.RecordAttemptResult(ctx, id, "response", "", 200, 1); err != nil {
		t.Fatal(err)
	}
	if err = store.db.Exec(`UPDATE outbound_attempts SET usage_json='{"prompt_tokens":1}' WHERE id=?`, id).Error; err != nil {
		t.Fatal(err)
	}
	if _, err = store.JobAttempts(ctx, job.ID); !errors.Is(err, ErrInputInvalid) {
		t.Fatal("partial stored usage became complete zero counters", err)
	}
}

func TestE3AttemptUsageAggregateOverflowKeepsKnownAttemptsWithoutFalseTotals(t *testing.T) {
	ctx := context.Background()
	store, plan, job := e3BudgetCase(t, 0, 3, 0, 3)
	for range 2 {
		id, err := store.ReserveAttempt(ctx, job.ID, "model", "configured-model", plan.ConfigRevision)
		if err != nil {
			t.Fatal(err)
		}
		if err = store.RecordAttemptResultWithUsage(ctx, id, "response", "", 200, 1, &domain.ModelUsage{PromptTokens: domain.MaxReportedModelTokens, TotalTokens: domain.MaxReportedModelTokens}); err != nil {
			t.Fatal(err)
		}
	}
	summary, err := store.BudgetUsage(ctx, plan.BudgetRef, "", "")
	if err != nil || summary.KnownUsageResponses != 2 || summary.UnknownUsageAttempts != 0 || !summary.UsageTotalsOverflow || summary.ReportedTokens != nil {
		t.Fatal("overflow fabricated exact aggregate counters", err)
	}
	attempts, err := store.JobAttempts(ctx, job.ID)
	if err != nil || len(attempts) != 2 || attempts[0].Usage == nil || attempts[1].Usage == nil {
		t.Fatal("aggregate overflow discarded per-response usage", err)
	}
}

func TestE3AttemptUsageOnlyBelongsToSuccessfulModelResponses(t *testing.T) {
	for _, scenario := range []string{"cloud", "http-error", "unknown-delivery"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			store, plan, job := e3BudgetCase(t, 3, 3, 3, 3)
			class, source, state, code, status := "model", "configured-model", "response", "", 200
			switch scenario {
			case "cloud":
				class, source = "cloud", "cloud-cfw"
			case "http-error":
				code, status = "UPSTREAM_HTTP_ERROR", 500
			case "unknown-delivery":
				state, code, status = "unknown", "UPSTREAM_TRANSPORT", 0
			}
			id, err := store.ReserveAttempt(ctx, job.ID, class, source, plan.ConfigRevision)
			if err != nil {
				t.Fatal(err)
			}
			if err = store.RecordAttemptResultWithUsage(ctx, id, state, code, status, 1, &domain.ModelUsage{}); !errors.Is(err, ErrInputInvalid) {
				t.Fatal("non-success/non-model attempt gained known zero usage", err)
			}
			items, err := store.JobAttempts(ctx, job.ID)
			if err != nil || len(items) != 1 || items[0].State != "reserved" || items[0].Usage != nil {
				t.Fatal("invalid usage partially saved attempt", err)
			}
		})
	}
}

func TestE3AttemptUsageExplicitMigrationRepairsWIPSchema3WithoutReset(t *testing.T) {
	ctx := context.Background()
	store, plan, job := e3BudgetCase(t, 0, 3, 0, 3)
	id, err := store.ReserveAttempt(ctx, job.ID, "model", "configured-model", plan.ConfigRevision)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.RecordAttemptResult(ctx, id, "response", "", 200, 1); err != nil {
		t.Fatal(err)
	}
	if err = store.db.Exec("ALTER TABLE outbound_attempts DROP COLUMN usage_json").Error; err != nil {
		t.Fatal(err)
	}
	if _, err = store.ReserveAttempt(ctx, job.ID, "model", "configured-model", plan.ConfigRevision); err == nil {
		t.Fatal("old WIP shape admitted a request before explicit repair")
	}
	for range 2 {
		if err = store.db.Transaction(migrateBudgets); err != nil {
			t.Fatal("explicit usage migration was not idempotent", err)
		}
	}
	next, err := store.ReserveAttempt(ctx, job.ID, "model", "configured-model", plan.ConfigRevision)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.RecordAttemptResultWithUsage(ctx, next, "response", "", 200, 1, &domain.ModelUsage{}); err != nil {
		t.Fatal(err)
	}
	summary, err := store.BudgetUsage(ctx, plan.BudgetRef, "", "")
	if err != nil || summary.ModelAttempts != 2 || summary.KnownUsageResponses != 1 || summary.UnknownUsageAttempts != 1 || summary.ReportedTokens == nil || summary.ReportedTokens.TotalTokens != 0 {
		t.Fatal("migration reset attempts or filled old unknown usage with zero", err)
	}
	var version int
	if err = store.db.Raw("SELECT version FROM business_schema").Scan(&version).Error; err != nil || version != BusinessSchemaVersion {
		t.Fatal("WIP usage migration invented another schema version", err)
	}
}
