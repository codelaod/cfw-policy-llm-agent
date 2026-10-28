package storage

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"cfw-policy-llm-agent/server/internal/domain"
	"gorm.io/gorm"
)

func TestE3JobWorkspaceReadsAllAuthoritativePartsInOneTransaction(t *testing.T) {
	ctx := context.Background()
	store, job, content := e3AgentResultCase(t, "audit")
	plan, err := store.GetPlan(ctx, job.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.ConfigureBudgetPolicy(ctx, BudgetPolicy{Ref: plan.BudgetRef, ApplicationModelAttempts: 6, SessionModelAttempts: 6, ExpiresAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)}); err != nil {
		t.Fatal(err)
	}
	id, err := store.ReserveAttempt(ctx, job.ID, "model", "configured-model", plan.ConfigRevision)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.RecordAttemptResultWithUsage(ctx, id, "response", "", 200, 1, &domain.ModelUsage{}); err != nil {
		t.Fatal(err)
	}
	agent, err := store.SaveJobAgentResult(ctx, job.ID, content)
	if err != nil {
		t.Fatal(err)
	}
	// Observe the actual SQL transaction identity. Calling public convenience
	// getters here would silently mix independently committed snapshots/seqs.
	var connection *sql.Tx
	queries := 0
	outside := false
	if err = store.db.Callback().Row().Before("gorm:row").Register("assert_workspace_read_tx", func(tx *gorm.DB) {
		current, ok := tx.Statement.ConnPool.(*sql.Tx)
		if !ok {
			outside = true
			return
		}
		if connection == nil {
			connection = current
		} else if connection != current {
			outside = true
		}
		queries++
	}); err != nil {
		t.Fatal(err)
	}
	defer store.db.Callback().Row().Remove("assert_workspace_read_tx")
	view, err := store.ReadJobWorkspace(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if outside || queries < 6 {
		t.Fatal("workspace used independent read transactions", queries)
	}
	if view.Job.ID != job.ID || view.SnapshotID != plan.SnapshotID || !view.InputSealed || view.Audit == nil || view.AgentResult == nil || view.AgentResult.ID != agent.ID || view.AttemptUsage.ReportedTokens == nil || view.AttemptUsage.KnownUsageResponses != 1 {
		t.Fatal("authoritative result parts missing")
	}
	page, _, err := store.Page(ctx, job.ID, 0, 100)
	if err != nil || view.Job.ViewSeq != page.LastSeq {
		t.Fatal("workspace sequence differs from committed result tail", err)
	}
}

func TestE3JobWorkspaceL4RequiresThisJobsCommittedReportEvent(t *testing.T) {
	for _, published := range []bool{true, false} {
		t.Run(map[bool]string{true: "finalizer", false: "manual-report-only"}[published], func(t *testing.T) {
			ctx := context.Background()
			store, job, run, request := preparedFinalization(t)
			var report ReportRecord
			var err error
			if published {
				report, err = store.FinalizeJobWithReport(ctx, job.ID, request)
			} else {
				if err = store.FinishWithCode(ctx, job.ID, "completed", ""); err != nil {
					t.Fatal(err)
				}
				report, err = store.SaveReport(ctx, run.ID, request.Format, request.Heads, request.Content, "later-manual-export")
			}
			if err != nil {
				t.Fatal(err)
			}
			view, err := store.ReadJobWorkspace(ctx, job.ID)
			if err != nil || view.ReportPublished != published {
				t.Fatal("report existence was confused with original task publication", err)
			}
			if published && view.ReportRef != report.ID {
				t.Fatal("published report reference not bound to its job event")
			}
			if !published && view.ReportRef != "" {
				t.Fatal("later manual export acquired an original execution reference")
			}
		})
	}
}
