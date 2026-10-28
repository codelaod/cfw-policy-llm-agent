package app

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"cfw-policy-llm-agent/server/internal/audit"
	"cfw-policy-llm-agent/server/internal/domain"
	"cfw-policy-llm-agent/server/internal/report"
	"cfw-policy-llm-agent/server/internal/storage"
)

func claimedWorkflow(t *testing.T, mode string) (*storage.Store, *Workflow, storage.Job, storage.ExecutionPlan) {
	t.Helper()
	store, plans, _, _, request := planCase(t)
	request.Mode = mode
	preview, err := plans.Preview(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	job, _, err := store.EnqueuePlan(context.Background(), preview.ID, "workflow-accept", plans.ConfigRevision)
	if err != nil {
		t.Fatal(err)
	}
	claimed, found, err := store.Claim(context.Background())
	if err != nil || !found || claimed.ID != job.ID {
		t.Fatal("task not claimed", err)
	}
	return store, NewWorkflow(store), claimed, preview.ExecutionPlan
}

func TestWorkflowRunsRulesOnSealedInputAndPublishesControlledArtifacts(t *testing.T) {
	ctx := context.Background()
	store, workflow, job, plan := claimedWorkflow(t, "rules_only")
	started := time.Now().Add(-time.Second)
	if err := workflow.Run(ctx, job); err != nil {
		t.Fatal(err)
	}
	_, record, err := store.ReadJobAudit(ctx, job.ID)
	if err != nil || record == nil || record.Input.EvaluationAt == plan.PreparedInput.EvaluationAt || len(record.Findings) == 0 {
		t.Fatal("actual rules or execution evaluation not saved", err)
	}
	evaluated, _ := time.Parse(time.RFC3339Nano, record.Input.EvaluationAt)
	if evaluated.Before(started) {
		t.Fatal("evaluation was not sealed at execution")
	}
	if record.Input.Scope.Ref != plan.PolicyScopeRef || record.Result.Manifest.EvaluationAt != record.Input.EvaluationAt {
		t.Fatal("result changed its sealed scope/time")
	}
	reports, err := store.ListReports(ctx, record.ID, 100)
	if err != nil || len(reports) != 1 {
		t.Fatal("fixed report missing", err)
	}
	var doc report.Document
	if err = json.Unmarshal(reports[0].Content, &doc); err != nil || doc.Execution == nil || doc.Execution.Mode != "rules_only" || doc.Execution.ModelStage != "not_requested" {
		t.Fatal("report hid execution mode", err)
	}
	page, err := store.ConversationMessages(ctx, job.ConversationID, 0, 100)
	if err != nil || len(page.Items) != 2 || page.Items[1].Origin != "application" || page.Items[1].Role != "event" {
		t.Fatal("rule template claimed model origin", err)
	}
	events, _, err := store.Page(ctx, job.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	types := map[string]bool{}
	for _, event := range events.Events {
		types[event.Type] = true
	}
	if !types["findings_available"] || !types["report_available"] || !types["message_available"] {
		t.Fatal("durable result references missing")
	}
}

func TestWorkflowUnavailableModelKeepsOriginalRulesAndDisclosesGap(t *testing.T) {
	ctx := context.Background()
	store, workflow, job, _ := claimedWorkflow(t, "rules_and_agent")
	err := workflow.Run(ctx, job)
	var failure JobFailure
	if !errors.As(err, &failure) || !failure.Partial || failure.Code != "MODEL_NOT_CONFIGURED" {
		t.Fatalf("unconfigured model falsely completed: %v", err)
	}
	_, record, err := store.ReadJobAudit(ctx, job.ID)
	if err != nil || record == nil {
		t.Fatal("model gap removed rules", err)
	}
	for _, finding := range record.Result.Findings {
		if finding.Origin != "rule" {
			t.Fatal("fabricated model output")
		}
	}
	reports, err := store.ListReports(ctx, record.ID, 100)
	if err != nil || len(reports) != 1 || !strings.Contains(string(reports[0].Content), `"model_stage": "not_configured"`) {
		t.Fatal("report concealed model gap", err)
	}
	if err = store.FinishWithCode(ctx, job.ID, "partial", failure.Code); err != nil {
		t.Fatal(err)
	}
	exported, err := NewAuditService(store).Report(ctx, record.ID, "markdown", "manual-partial-report")
	if err != nil || !strings.Contains(string(exported.Content), "模型未配置") {
		t.Fatal("manual report lost execution gap", err)
	}
}

func TestWorkflowCancellationPreservesComputedRulesWithoutNormalReply(t *testing.T) {
	store, workflow, job, _ := claimedWorkflow(t, "rules_only")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	workflow.rules = func(ctx context.Context, in domain.Input) (domain.Result, error) {
		result, err := audit.Run(ctx, in)
		if err != nil {
			return result, err
		}
		if _, err = store.Cancel(context.Background(), job.ID); err != nil {
			t.Fatal(err)
		}
		cancel()
		return result, context.Canceled
	}
	err := workflow.Run(ctx, job)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation hidden: %v", err)
	}
	_, record, readErr := store.ReadJobAudit(context.Background(), job.ID)
	if readErr != nil || record == nil || record.Result.Coverage.Complete {
		t.Fatal("computed partial result was lost", readErr)
	}
	reports, readErr := store.ListReports(context.Background(), record.ID, 100)
	if readErr != nil || len(reports) != 0 {
		t.Fatal("cancelled run published normal report", readErr)
	}
	messages, readErr := store.ConversationMessages(context.Background(), job.ConversationID, 0, 100)
	if readErr != nil || len(messages.Items) != 1 {
		t.Fatal("cancelled run appended late completion message", readErr)
	}
}

func TestWorkflowResultSaveFailureCannotPublishReportOrSuccess(t *testing.T) {
	store, workflow, job, _ := claimedWorkflow(t, "rules_only")
	_, err := store.SQL.Exec(`CREATE TRIGGER reject_workflow_result BEFORE INSERT ON audit_runs BEGIN SELECT RAISE(ABORT,'PRIVATE_PROVIDER_ERROR'); END`)
	if err != nil {
		t.Fatal(err)
	}
	err = workflow.Run(context.Background(), job)
	var failure JobFailure
	if !errors.As(err, &failure) || failure.Partial || failure.Code != "RESULT_SAVE_FAILED" || strings.Contains(err.Error(), "PRIVATE") {
		t.Fatalf("uncontrolled result failure: %v", err)
	}
	_, record, readErr := store.ReadJobAudit(context.Background(), job.ID)
	if readErr != nil || record != nil {
		t.Fatal("failed transaction left result", readErr)
	}
}

func TestWorkflowAcceptedPlanKeepsItsBusinessVersionsAfterConfirmationChanges(t *testing.T) {
	ctx := context.Background()
	store, workflow, job, plan := claimedWorkflow(t, "rules_only")
	draft, err := store.CreateProfileDraft(ctx, storage.CreateDraftRequest{SnapshotID: plan.SnapshotID, AssetIDs: plan.PreparedInput.Selection.AssetIDs})
	if err != nil {
		t.Fatal(err)
	}
	change := draft.Changes[0]
	change.Roles[0].Role = "unknown"
	draft, err = store.UpdateProfileDraft(ctx, draft.ID, storage.UpdateDraftRequest{ExpectedDraftRevision: draft.Revision, Changes: []storage.AssetDraftChange{change}})
	if err != nil {
		t.Fatal(err)
	}
	preview, err := store.PreviewProfileDraft(ctx, draft.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.ConfirmProfileDraft(ctx, draft.ID, storage.ConfirmDraftRequest{PreviewID: preview.ID, ExpectedDraftRevision: draft.Revision, ConfirmedSections: []string{"roles"}}, "confirm-after-acceptance"); err != nil {
		t.Fatal(err)
	}
	latest, _, err := store.FreezeInput(ctx, plan.SnapshotID)
	if err != nil || latest.ProfileVersion == plan.PreparedInput.ProfileVersion {
		t.Fatal("test did not change local business head", err)
	}
	if err = workflow.Run(ctx, job); err != nil {
		t.Fatal(err)
	}
	_, record, err := store.ReadJobAudit(ctx, job.ID)
	if err != nil || record == nil || record.Input.ProfileVersion != plan.PreparedInput.ProfileVersion {
		t.Fatal("accepted job silently adopted new business version", err)
	}
	current, err := store.Get(ctx, job.ID)
	if err != nil || current.Status != "completed" {
		t.Fatal("published workflow artifacts did not commit with the terminal state", err)
	}
}

func TestReportRejectsFormalAuditBeforeTaskTerminal(t *testing.T) {
	ctx := context.Background()
	store, _, job, plan := claimedWorkflow(t, "rules_only")
	input := plan.PreparedInput
	input.EvaluationAt = time.Now().UTC().Format(time.RFC3339Nano)
	sealed, err := store.SealInput(ctx, job.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	result, err := audit.Run(ctx, sealed)
	if err != nil {
		t.Fatal(err)
	}
	record, err := store.SaveJobAudit(ctx, job.ID, result)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = NewAuditService(store).Report(ctx, record.ID, "json", "premature-export"); !errors.Is(err, storage.ErrJobTerminal) {
		t.Fatal("running task exported as terminal report", err)
	}
}

func TestWorkflowRecheckKeepsOldArtifactsAndPersistsFixedComparison(t *testing.T) {
	ctx := context.Background()
	store, workflow, oldJob, _ := claimedWorkflow(t, "rules_only")
	if err := workflow.Run(ctx, oldJob); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishWithCode(ctx, oldJob.ID, "completed", ""); err != nil {
		t.Fatal(err)
	}
	_, old, err := store.ReadJobAudit(ctx, oldJob.ID)
	if err != nil || old == nil {
		t.Fatal(err)
	}
	reports, err := store.ListReports(ctx, old.ID, 100)
	if err != nil || len(reports) != 1 {
		t.Fatal("old fixed report missing", err)
	}
	before := append([]byte(nil), reports[0].Content...)
	in := old.Input
	in.SnapshotVersion = "new-recheck-snapshot"
	in.CollectedAt = time.Now().UTC().Format(time.RFC3339Nano)
	in.Policies = append([]domain.Policy(nil), in.Policies...)
	in.Policies[0].Action = "drop"
	newSnapshot, err := store.SaveSnapshot(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	plans := NewPlanService(store)
	preview, err := plans.RecheckPreview(ctx, oldJob.ID, RecheckRequest{SnapshotID: newSnapshot.ID, ConversationID: oldJob.ConversationID})
	if err != nil {
		t.Fatal(err)
	}
	if preview.EvaluationAt != old.Input.EvaluationAt {
		t.Fatal("explicit recheck failed to reuse evaluation time")
	}
	newJob, _, err := store.EnqueuePlan(ctx, preview.ID, "accept-recheck", plans.ConfigRevision)
	if err != nil {
		t.Fatal(err)
	}
	claimed, found, err := store.Claim(ctx)
	if err != nil || !found || claimed.ID != newJob.ID {
		t.Fatal("recheck not claimed", err)
	}
	if err = workflow.Run(ctx, claimed); err != nil {
		t.Fatal(err)
	}
	_, current, err := store.ReadJobAudit(ctx, newJob.ID)
	if err != nil || current == nil {
		t.Fatal("recheck result missing", err)
	}
	var count int
	if err = store.SQL.QueryRowContext(ctx, "SELECT count(*) FROM audit_rechecks WHERE old_audit_id=? AND new_audit_id=?", old.ID, current.ID).Scan(&count); err != nil || count != 1 {
		t.Fatal("comparison not linked to both immutable audits", err)
	}
	unchanged, err := store.GetReport(ctx, reports[0].ID)
	if err != nil || string(unchanged.Content) != string(before) {
		t.Fatal("recheck rewrote old report", err)
	}
}

func TestWorkflowEnginePersistsExpectedTerminalModes(t *testing.T) {
	for _, mode := range []string{"rules_only", "rules_and_agent"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			store, plans, _, _, request := planCase(t)
			request.Mode = mode
			preview, err := plans.Preview(ctx, request)
			if err != nil {
				t.Fatal(err)
			}
			workflow := NewWorkflow(store)
			engine := New(store, workflow.Run)
			engine.Start()
			defer engine.Stop()
			job, _, err := engine.EnqueuePlan(ctx, preview.ID, "engine-workflow", plans.ConfigRevision)
			if err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(5 * time.Second)
			for {
				job, err = store.Get(ctx, job.ID)
				if err != nil {
					t.Fatal(err)
				}
				if storage.Terminal(job.Status) {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("workflow engine failed to terminate")
				}
				time.Sleep(5 * time.Millisecond)
			}
			want := "completed"
			if mode == "rules_and_agent" {
				want = "partial"
			}
			if job.Status != want || job.ResultRef == "" || mode == "rules_and_agent" && job.ErrorCode != "MODEL_NOT_CONFIGURED" {
				t.Fatalf("wrong terminal workflow: %+v", job)
			}
			page, _, err := store.Page(ctx, job.ID, 0, 100)
			if err != nil || len(page.Events) == 0 || page.Events[len(page.Events)-1].Type != "job_terminal" {
				t.Fatal("terminal and event tail disagree", err)
			}
		})
	}
}

func TestWorkflowFinalizationFailureRollsBackReportMessageAndTerminal(t *testing.T) {
	ctx := context.Background()
	store, workflow, job, _ := claimedWorkflow(t, "rules_only")
	_, err := store.SQL.Exec(`CREATE TRIGGER reject_workflow_terminal BEFORE INSERT ON e0_events WHEN NEW.type='job_terminal' BEGIN SELECT RAISE(ABORT,'synthetic terminal persistence failure'); END`)
	if err != nil {
		t.Fatal(err)
	}
	err = workflow.Run(ctx, job)
	var failure JobFailure
	if !errors.As(err, &failure) || !failure.Partial || failure.Code != "FINALIZATION_SAVE_FAILED" {
		t.Fatalf("terminal persistence failure escaped atomic finalization: %v", err)
	}
	current, record, readErr := store.ReadJobAudit(ctx, job.ID)
	if readErr != nil || record == nil || current.Status != "running" {
		t.Fatal("terminal failure lost L2 or partially finished the job", readErr)
	}
	reports, readErr := store.ListReports(ctx, record.ID, 100)
	if readErr != nil || len(reports) != 0 {
		t.Fatal("terminal failure left an unlinked report", readErr)
	}
	messages, readErr := store.ConversationMessages(ctx, job.ConversationID, 0, 100)
	if readErr != nil || len(messages.Items) != 1 {
		t.Fatal("terminal failure left a completion answer", readErr)
	}
	events, _, readErr := store.Page(ctx, job.ID, 0, 100)
	if readErr != nil {
		t.Fatal(readErr)
	}
	for _, event := range events.Events {
		if event.Type == "report_available" || event.Type == "message_available" || event.Type == "job_terminal" {
			t.Fatal("rolled-back finalization left a visible event")
		}
	}
}
