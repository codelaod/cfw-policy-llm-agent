package httpapi_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"cfw-policy-llm-agent/server/internal/app"
	"cfw-policy-llm-agent/server/internal/audit"
	"cfw-policy-llm-agent/server/internal/domain"
	"cfw-policy-llm-agent/server/internal/storage"
)

func reviewSavedAuditJob(t *testing.T, h *harness, snapshot storage.SnapshotRecord) (storage.Job, storage.AuditRecord, storage.Conversation) {
	t.Helper()
	ctx := context.Background()
	s := h.engine.Store
	conv, err := s.CreateConversation(ctx, "Review 固定业务输入", snapshot.ScopeRef)
	if err != nil {
		t.Fatal(err)
	}
	plans := app.NewPlanService(s)
	preview, err := plans.Preview(ctx, app.PlanRequest{Kind: "audit", ConversationID: conv.ID, DataSourceID: "local-offline", PolicyScopeRef: snapshot.ScopeRef, Selection: app.PlanSelection{Kind: snapshot.Input.Selection.Kind, AssetIDs: snapshot.Input.Selection.AssetIDs}, DataPolicy: app.DataPolicy{Mode: "pinned_snapshot", SnapshotID: snapshot.ID}, BaselineVersionID: "B2-v1", Mode: "rules_only"})
	if err != nil {
		t.Fatal(err)
	}
	job, _, err := s.EnqueuePlan(ctx, preview.ID, "review-saved-audit", plans.ConfigRevision)
	if err != nil {
		t.Fatal(err)
	}
	claimed, found, err := s.Claim(ctx)
	if err != nil || !found || claimed.ID != job.ID {
		t.Fatal("review fixture claim", err)
	}
	in, err := s.SealInput(ctx, job.ID, preview.PreparedInput)
	if err != nil {
		t.Fatal(err)
	}
	result, err := audit.Run(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	run, err := s.SaveJobAudit(ctx, job.ID, result)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Finish(ctx, job.ID, "completed"); err != nil {
		t.Fatal(err)
	}
	return job, run, conv
}

func TestReviewRecheckMissingOldObjectHasExplicitNonComparableReason(t *testing.T) {
	h := newHarness(t, nil)
	h.engine.Stop() // Build fixed local job history without executing any adapter.
	ctx := context.Background()
	in, err := audit.SyntheticExample("internet-in")
	if err != nil {
		t.Fatal(err)
	}
	extra := in.Assets[0]
	extra.ID, extra.Address = "review-old-object-b", "192.0.2.11"
	in.Assets = append(in.Assets, extra)
	in.Selection.AssetIDs = append(in.Selection.AssetIDs, extra.ID)
	old, err := h.engine.Store.SaveSnapshot(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	job, _, conv := reviewSavedAuditJob(t, h, old)
	in.Assets = in.Assets[:1]
	in.Selection.AssetIDs = in.Selection.AssetIDs[:1]
	in.SnapshotVersion = "review-one-old-object-missing"
	fresh, err := h.engine.Store.SaveSnapshot(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	request, _ := json.Marshal(app.RecheckRequest{SnapshotID: fresh.ID, ConversationID: conv.ID})
	status, body := h.request(t, "POST", "/api/v1/audits/"+job.ID+"/rechecks", string(request))
	assertError(t, status, body, 409, "RECHECK_NOT_COMPARABLE")
	if !strings.Contains(string(body), "object_missing") {
		t.Fatalf("missing object had no controlled comparability reason: %s", body)
	}
	jobs, err := h.engine.Store.ListJobs(ctx, conv.ID, 0, 100)
	if err != nil || len(jobs) != 1 {
		t.Fatal("non-comparable recheck started a task or changed old history", err)
	}
}

func TestReviewFormalAndE0TaskRoutesDoNotCrossKinds(t *testing.T) {
	h := newHarness(t, nil)
	h.engine.Stop()
	ctx := context.Background()
	snapshot, err := app.NewAuditService(h.engine.Store).LoadExample(ctx, "internet-in")
	if err != nil {
		t.Fatal(err)
	}
	formal, _, _ := reviewSavedAuditJob(t, h, snapshot)
	probe, _, err := h.engine.Store.Create(ctx, "review-probe-conversation", "review-probe-request")
	if err != nil {
		t.Fatal(err)
	}
	if err = h.engine.Store.Finish(ctx, probe.ID, "completed"); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		"/api/v1/jobs/" + probe.ID,
		"/api/v1/jobs/" + probe.ID + "/events",
		"/api/v1/jobs/" + probe.ID + "/result",
		"/api/v1/e0/jobs/" + formal.ID,
		"/api/v1/e0/jobs/" + formal.ID + "/events",
	} {
		status, body := h.request(t, "GET", path, "")
		if status != 404 {
			t.Errorf("cross-kind lookup %s: %d %s", path, status, body)
		}
	}
	for _, path := range []string{"/api/v1/jobs/" + probe.ID + "/cancel", "/api/v1/e0/jobs/" + formal.ID + "/cancel"} {
		status, body := h.request(t, "POST", path, "")
		if status != 404 {
			t.Errorf("cross-kind cancel %s: %d %s", path, status, body)
		}
	}
	status, body := h.request(t, "GET", "/api/v1/jobs", "")
	var page struct {
		Items []storage.Job `json:"items"`
	}
	if status != 200 || json.Unmarshal(body, &page) != nil {
		t.Fatal(status, string(body))
	}
	for _, job := range page.Items {
		if job.Kind == "e0_probe" {
			t.Error("formal task list contains a synthetic E0 probe")
		}
	}
}

func TestReviewFindingEvidenceKeepsAuditInputAfterBusinessConfirmation(t *testing.T) {
	h := newHarness(t, nil)
	h.engine.Stop()
	ctx := context.Background()
	s := h.engine.Store
	snapshot, err := app.NewAuditService(s).LoadExample(ctx, "internet-in")
	if err != nil {
		t.Fatal(err)
	}
	_, run, _ := reviewSavedAuditJob(t, h, snapshot)
	draft, err := s.CreateProfileDraft(ctx, storage.CreateDraftRequest{SnapshotID: snapshot.ID, AssetIDs: snapshot.Input.Selection.AssetIDs})
	if err != nil {
		t.Fatal(err)
	}
	draft.Changes[0].Roles[0].Role = "background"
	draft, err = s.UpdateProfileDraft(ctx, draft.ID, storage.UpdateDraftRequest{ExpectedDraftRevision: draft.Revision, Changes: draft.Changes})
	if err != nil {
		t.Fatal(err)
	}
	preview, err := s.PreviewProfileDraft(ctx, draft.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ConfirmProfileDraft(ctx, draft.ID, storage.ConfirmDraftRequest{PreviewID: preview.ID, ExpectedDraftRevision: draft.Revision, ConfirmedSections: []string{"roles"}}, "review-newer-role"); err != nil {
		t.Fatal(err)
	}
	findingID := ""
	for _, finding := range run.Findings {
		if finding.Finding.ServiceRef == snapshot.Input.Assets[0].Services[0].ID {
			findingID = finding.ID
			break
		}
	}
	if findingID == "" {
		t.Fatal("fixture has no original service finding")
	}
	status, body := h.request(t, "GET", "/api/v1/findings/"+findingID, "")
	var detail struct {
		Evidence []struct {
			Field string          `json:"field"`
			Value json.RawMessage `json:"value"`
		} `json:"evidence"`
	}
	if status != 200 || json.Unmarshal(body, &detail) != nil {
		t.Fatal(status, string(body))
	}
	seen := false
	for _, e := range detail.Evidence {
		if e.Field != "/assets/0/services/0" {
			continue
		}
		var service domain.Service
		if err = json.Unmarshal(e.Value, &service); err != nil {
			t.Fatal(err)
		}
		if service.Role != snapshot.Input.Assets[0].Services[0].Role || service.Role == "background" {
			t.Fatal("finding read newer business head instead of its frozen evidence")
		}
		seen = true
	}
	if !seen {
		t.Fatal("original service evidence value missing")
	}
}
