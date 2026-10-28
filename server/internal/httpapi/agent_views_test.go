package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"cfw-policy-llm-agent/server/internal/agentquery"
	"cfw-policy-llm-agent/server/internal/app"
	"cfw-policy-llm-agent/server/internal/audit"
	"cfw-policy-llm-agent/server/internal/domain"
	"cfw-policy-llm-agent/server/internal/httpapi"
	"cfw-policy-llm-agent/server/internal/report"
	"cfw-policy-llm-agent/server/internal/storage"
)

type agentReadCase struct {
	h        *harness
	job      storage.Job
	snapshot storage.SnapshotRecord
	plans    *app.PlanService
}

func newAgentReadCase(t *testing.T, assistant bool, prepare ...func(*storage.Store, storage.SnapshotRecord)) agentReadCase {
	t.Helper()
	ctx := context.Background()
	store, err := storage.Open(filepath.Join(t.TempDir(), "agent-views.sqlite"), true)
	if err != nil {
		t.Fatal(err)
	}
	engine := app.New(store, nil)
	router, err := httpapi.New(engine, testToken)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(router)
	h := &harness{engine: engine, server: server, router: router, client: &http.Client{Timeout: 5 * time.Second}}
	t.Cleanup(func() { engine.Stop(); server.Close(); store.Close() })
	snapshot, err := app.NewAuditService(store).LoadExample(ctx, "internet-in")
	if err != nil {
		t.Fatal(err)
	}
	conversation, err := store.CreateConversation(ctx, "Agent结果读取", snapshot.ScopeRef)
	if err != nil {
		t.Fatal(err)
	}
	for _, setup := range prepare {
		setup(store, snapshot)
	}
	plans := app.NewPlanService(store)
	plans.ModelReady = true
	var job storage.Job
	if assistant {
		job, _, _, err = plans.StartAssistant(ctx, engine, conversation.ID, app.AssistantRequest{ClientMessageID: "agent-reading-user", SnapshotID: snapshot.ID, PolicyScopeRef: snapshot.ScopeRef, Text: "解释固定资料", ModelUseAccepted: true})
	} else {
		var preview app.PlanPreview
		preview, err = plans.Preview(ctx, app.PlanRequest{Kind: "audit", ConversationID: conversation.ID, DataSourceID: "local-offline", PolicyScopeRef: snapshot.ScopeRef, Selection: app.PlanSelection{Kind: "assets", AssetIDs: snapshot.Input.Selection.AssetIDs}, DataPolicy: app.DataPolicy{Mode: "pinned_snapshot", SnapshotID: snapshot.ID}, BaselineVersionID: "B2-v1", Mode: "rules_only"})
		if err == nil {
			job, _, err = store.EnqueuePlan(ctx, preview.ID, "agent-reading-audit", plans.ConfigRevision)
		}
	}
	if err != nil {
		t.Fatal(err)
	}
	return agentReadCase{h, job, snapshot, plans}
}
func (f agentReadCase) seal(t *testing.T) domain.Input {
	t.Helper()
	ctx := context.Background()
	store := f.h.engine.Store
	claimed, found, err := store.Claim(ctx)
	if err != nil || !found || claimed.ID != f.job.ID {
		t.Fatal(err)
	}
	plan, err := store.GetPlan(ctx, f.job.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	in := plan.PreparedInput
	in.EvaluationAt = time.Now().UTC().Format(time.RFC3339Nano)
	sealed, err := store.SealInput(ctx, f.job.ID, in)
	if err != nil {
		t.Fatal(err)
	}
	return sealed
}
func httpManifest(in domain.Input) domain.InputManifest {
	return domain.InputManifest{SnapshotVersion: in.SnapshotVersion, ProfileVersion: in.ProfileVersion, IntentVersion: in.IntentVersion, BaselineVersion: in.Baseline.Version, NormalizerVersion: in.NormalizerVersion, KernelVersion: in.KernelVersion, ProviderSemantics: in.Semantics, PolicyScope: in.Scope, EvaluationAt: in.EvaluationAt, CollectedAt: in.CollectedAt, SourceKind: in.SourceKind}
}

func TestE3HTTPResultIncludesAuthoritativeAgentAndUsageAtFinalSequence(t *testing.T) {
	for _, success := range []bool{true, false} {
		t.Run(map[bool]string{true: "success", false: "failure"}[success], func(t *testing.T) {
			ctx := context.Background()
			f := newAgentReadCase(t, true)
			in := f.seal(t)
			store := f.h.engine.Store
			plan, err := store.GetPlan(ctx, f.job.PlanID)
			if err != nil {
				t.Fatal(err)
			}
			if err = store.ConfigureBudgetPolicy(ctx, storage.BudgetPolicy{Ref: plan.BudgetRef, ApplicationModelAttempts: 4, SessionModelAttempts: 4, ExpiresAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)}); err != nil {
				t.Fatal(err)
			}
			attempt, err := store.ReserveAttempt(ctx, f.job.ID, "model", "configured-model", plan.ConfigRevision)
			if err != nil {
				t.Fatal(err)
			}
			queries, err := agentquery.New(in, nil)
			if err != nil {
				t.Fatal(err)
			}
			var initial agentquery.InitialView
			json.Unmarshal([]byte(queries.InitialContext()), &initial)
			content := storage.AgentContent{JobID: f.job.ID, Manifest: httpManifest(in), ModelCalls: 1, Context: storage.ContextCoverage{DisclosureVersion: "minimal-v1"}}
			if success {
				content.Output = &agentquery.Output{ScopeRef: initial.ScopeRef, RuleVersion: initial.RuleVersion, SourceKind: initial.SourceKind, AssessmentBasis: initial.AssessmentBasis, Explanation: "依据固定资料，需人工核对业务要求。"}
				content.Bindings = queries.LocalBindings()
				if err = store.RecordAttemptResultWithUsage(ctx, attempt, "response", "", 200, 1, &domain.ModelUsage{PromptTokens: 3, CompletionTokens: 2, TotalTokens: 5}); err != nil {
					t.Fatal(err)
				}
			} else {
				content.ErrorCode = "MODEL_REQUEST_FAILED"
				if err = store.RecordAttemptResult(ctx, attempt, "unknown", "UPSTREAM_TRANSPORT", 0, 1); err != nil {
					t.Fatal(err)
				}
			}
			saved, err := store.FinalizeAssistant(ctx, f.job.ID, content)
			if err != nil {
				t.Fatal(err)
			}
			current, err := store.Get(ctx, f.job.ID)
			if err != nil {
				t.Fatal(err)
			}
			status, body := f.h.request(t, "GET", "/api/v1/jobs/"+f.job.ID+"/result", "")
			var result httpapi.JobResult
			if json.Unmarshal(body, &result) != nil || status != 200 || !result.IsFinal || result.ViewSeq != current.ViewSeq || result.Audit != nil || result.SnapshotID != f.snapshot.ID || result.AgentResult == nil || result.AgentResult.ID != saved.ID {
				t.Fatalf("Agent result is not bound to authoritative task view: %d %s", status, body)
			}
			if !slices.Contains(result.CompletedPhases, "L1") || slices.Contains(result.CompletedPhases, "L2") || slices.Contains(result.CompletedPhases, "L3") != success || slices.Contains(result.CompletedPhases, "L4") {
				t.Fatal("assistant failure/success produced incorrect phase completion")
			}
			if result.AttemptUsage.ModelAttempts != 1 || (result.AttemptUsage.ReportedTokens != nil) != success {
				t.Fatal("unknown usage became zero or saved reported usage disappeared")
			}
			if success && result.AttemptUsage.ReportedTokens.TotalTokens != 5 {
				t.Fatal("reported token counters changed")
			}
		})
	}
}

func TestE3HTTPReportPhaseRequiresOriginalExecutionPublication(t *testing.T) {
	for _, published := range []bool{true, false} {
		t.Run(map[bool]string{true: "finalizer", false: "later-manual-export"}[published], func(t *testing.T) {
			ctx := context.Background()
			f := newAgentReadCase(t, false)
			sealed := f.seal(t)
			store := f.h.engine.Store
			result, err := audit.Run(ctx, sealed)
			if err != nil {
				t.Fatal(err)
			}
			run, err := store.SaveJobAudit(ctx, f.job.ID, result)
			if err != nil {
				t.Fatal(err)
			}
			if published {
				_, heads, err := store.CaptureReportInput(ctx, run.ID)
				if err != nil {
					t.Fatal(err)
				}
				public := []report.ReviewHead{}
				for _, head := range heads {
					public = append(public, report.ReviewHead{FindingID: head.FindingKey, Revision: head.Revision, Plan: head.Plan})
				}
				body, err := report.Build(ctx, run.Result, public, "json")
				if err != nil {
					t.Fatal(err)
				}
				if _, err = store.FinalizeJobWithReport(ctx, f.job.ID, storage.FinalizeJobReportRequest{Format: "json", Heads: heads, Content: body, Key: "original-phase-report", Status: "completed"}); err != nil {
					t.Fatal(err)
				}
			} else {
				if err = store.FinishWithCode(ctx, f.job.ID, "completed", ""); err != nil {
					t.Fatal(err)
				}
				if _, err = app.NewAuditService(store).Report(ctx, run.ID, "json", "later-manual-phase-report"); err != nil {
					t.Fatal(err)
				}
			}
			status, body := f.h.request(t, "GET", "/api/v1/jobs/"+f.job.ID+"/result", "")
			var view httpapi.JobResult
			if json.Unmarshal(body, &view) != nil || status != 200 || !view.IsFinal || slices.Contains(view.CompletedPhases, "L4") != published {
				t.Fatalf("report phase mismatch: status=%d final=%t phases=%v published=%t", status, view.IsFinal, view.CompletedPhases, published)
			}
		})
	}
}

func TestE3HTTPQueuedResultHasUnknownUsageAndInputRequiresSealing(t *testing.T) {
	f := newAgentReadCase(t, false)
	status, body := f.h.request(t, "GET", "/api/v1/jobs/"+f.job.ID+"/input", "")
	assertError(t, status, body, 409, "INPUT_NOT_SEALED")
	status, body = f.h.request(t, "GET", "/api/v1/jobs/"+f.job.ID+"/result", "")
	var result httpapi.JobResult
	if json.Unmarshal(body, &result) != nil || status != 200 || result.SnapshotID != f.snapshot.ID || result.AgentResult != nil || result.AttemptUsage.ReportedTokens != nil || result.IsFinal || len(result.CompletedPhases) != 0 || !strings.Contains(string(body), `"reported_tokens":null`) {
		t.Fatalf("queued view invented completed data or zero usage: %d %s", status, body)
	}
}

func TestE3HTTPInputReturnsSealedOverlayAndSeparatesE0(t *testing.T) {
	ctx := context.Background()
	confirm := func(store *storage.Store, snapshot storage.SnapshotRecord, role, key string) {
		draft, err := store.CreateProfileDraft(ctx, storage.CreateDraftRequest{SnapshotID: snapshot.ID, AssetIDs: snapshot.Input.Selection.AssetIDs})
		if err != nil {
			t.Fatal(err)
		}
		change := draft.Changes[0]
		change.Roles[0].Role = role
		draft, err = store.UpdateProfileDraft(ctx, draft.ID, storage.UpdateDraftRequest{ExpectedDraftRevision: draft.Revision, Changes: []storage.AssetDraftChange{change}})
		if err != nil {
			t.Fatal(err)
		}
		preview, err := store.PreviewProfileDraft(ctx, draft.ID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = store.ConfirmProfileDraft(ctx, draft.ID, storage.ConfirmDraftRequest{PreviewID: preview.ID, ExpectedDraftRevision: draft.Revision, ConfirmedSections: []string{"roles"}}, key); err != nil {
			t.Fatal(err)
		}
	}
	f := newAgentReadCase(t, true, func(store *storage.Store, snapshot storage.SnapshotRecord) {
		confirm(store, snapshot, "unknown", "confirm-before-plan")
	})
	sealed := f.seal(t)
	store := f.h.engine.Store
	if sealed.ProfileVersion == f.snapshot.Input.ProfileVersion || sealed.Assets[0].Services[0].Role != "unknown" {
		t.Fatal("test did not freeze a local overlay distinct from snapshot declarations")
	}
	confirm(store, f.snapshot, "database", "confirm-after-seal")
	current, _, err := store.FreezeInput(ctx, f.snapshot.ID)
	if err != nil || current.ProfileVersion == sealed.ProfileVersion {
		t.Fatal("test did not change current business head", err)
	}
	status, body := f.h.request(t, "GET", "/api/v1/jobs/"+f.job.ID+"/input", "")
	var actual domain.Input
	if json.Unmarshal(body, &actual) != nil || status != 200 || actual.ProfileVersion != sealed.ProfileVersion || actual.Assets[0].Services[0].Role != sealed.Assets[0].Services[0].Role || actual.EvaluationAt != sealed.EvaluationAt {
		t.Fatal("input endpoint replaced immutable input with a current head")
	}
	probe, _, err := store.Create(ctx, "reading-probe", "reading-probe-key")
	if err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"input", "result"} {
		status, body = f.h.request(t, "GET", "/api/v1/jobs/"+probe.ID+"/"+suffix, "")
		assertError(t, status, body, 404, "NOT_FOUND")
	}
}
