package storage

import (
	"cfw-policy-llm-agent/server/internal/domain"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"

	"context"
	"testing"
)

func e3AttemptCase(t *testing.T) (*Store, ExecutionPlan, Job) {
	t.Helper()
	ctx := context.Background()
	store, _, _, initial := executionCase(t)
	if err := store.db.Transaction(migrateAttempts); err != nil {
		t.Fatal(err)
	}
	initial.Mode = "rules_and_agent"
	initial.EffectCeiling = EffectCeiling{AllowCloudReads: true, AllowModelRequests: true}
	plan, err := store.SavePlan(ctx, initial)
	if err != nil {
		t.Fatal(err)
	}
	job, _, err := store.EnqueuePlan(ctx, plan.ID, "e3-attempt-job")
	if err != nil {
		t.Fatal(err)
	}
	claimed, found, err := store.Claim(ctx)
	if err != nil || !found || claimed.ID != job.ID {
		t.Fatal("attempt task not claimed", err)
	}
	return store, initial, claimed
}

func TestAttemptHistorySurvivesRestartWithoutBlockingLaterTasks(t *testing.T) {
	ctx := context.Background()
	s, template, job := e3AttemptCase(t)
	var path string
	if e := s.SQL.QueryRow("SELECT file FROM pragma_database_list WHERE name='main'").Scan(&path); e != nil {
		t.Fatal(e)
	}
	for round := 0; round < 3; round++ {
		// More than the removed per-task HTTP ceiling; these are local ledger writes.
		for n := 0; n < 120; n++ {
			id, e := s.ReserveAttempt(ctx, job.ID, "model", "configured-model", template.ConfigRevision)
			if e != nil {
				t.Fatal(e)
			}
			if e = s.RecordAttemptResultWithUsage(ctx, id, "response", "", 200, 1, &domain.ModelUsage{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2}); e != nil {
				t.Fatal(e)
			}
		}
		list, e := s.JobAttempts(ctx, job.ID)
		if e != nil || len(list) != 120 {
			t.Fatal("attempt list truncated", e, len(list))
		}
		if e = s.FinishWithCode(ctx, job.ID, "interrupted", "EXECUTION_INTERRUPTED"); e != nil {
			t.Fatal(e)
		}
		if _, e = s.ReserveAttempt(ctx, job.ID, "model", "configured-model", template.ConfigRevision); !errors.Is(e, ErrExecutionClosed) {
			t.Fatal("closed task admitted", e)
		}
		if round == 1 {
			if e = s.Close(); e != nil {
				t.Fatal(e)
			}
			s, e = Open(path, false)
			if e != nil {
				t.Fatal(e)
			}
			defer s.Close()
		}
		if round < 2 {
			c, e := s.GetConversation(ctx, template.ConversationID)
			if e != nil {
				t.Fatal(e)
			}
			template.ConversationRevision = c.Revision
			template.ID = ""
			template.ConsumedJobID = ""
			p, e := s.SavePlan(ctx, template)
			if e != nil {
				t.Fatal(e)
			}
			if _, _, e = s.EnqueuePlan(ctx, p.ID, fmt.Sprintf("later-%d", round)); e != nil {
				t.Fatal(e)
			}
			var found bool
			job, found, e = s.Claim(ctx)
			if e != nil || !found {
				t.Fatal(e)
			}
		}
	}
	u, e := s.AttemptUsage(ctx, template.ConversationID, "")
	if e != nil || u.ModelAttempts != 360 || u.ReportedTokens == nil || u.ReportedTokens.TotalTokens != 720 {
		t.Fatal("history lost", e, u)
	}
}

func TestSchema6RefusesOldSchemaWithoutChangingItsData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.sqlite")
	s, e := Open(path, true)
	if e != nil {
		t.Fatal(e)
	}
	in, _ := storedCase()
	snap, e := s.SaveSnapshot(context.Background(), in)
	if e != nil {
		t.Fatal(e)
	}
	for _, sql := range []string{"DROP TABLE business_schema", "CREATE TABLE business_schema(version INTEGER PRIMARY KEY)", "INSERT INTO business_schema VALUES(5)"} {
		if e = s.db.Exec(sql).Error; e != nil {
			t.Fatal(e)
		}
	}
	s.Close()
	for _, migrate := range []bool{false, true} {
		old, e := Open(path, migrate)
		if old != nil {
			old.Close()
		}
		if !errors.Is(e, errUnsupportedBusinessSchema) {
			t.Fatal("old schema did not direct the operator to a new path", migrate, e)
		}
	}
	db, e := sql.Open("sqlite", path)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	var body string
	if e = db.QueryRow("SELECT body FROM snapshots WHERE id=?", snap.ID).Scan(&body); e != nil || body != snap.Body {
		t.Fatal("old data changed", e)
	}
}
