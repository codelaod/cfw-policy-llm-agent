package httpapi_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cfw-policy-llm-agent/server/internal/app"
	"cfw-policy-llm-agent/server/internal/httpapi"
	"cfw-policy-llm-agent/server/internal/storage"
)

func newWorkflowHarness(t *testing.T) *harness {
	t.Helper()
	store, err := storage.Open(filepath.Join(t.TempDir(), "formal.sqlite"), true)
	if err != nil {
		t.Fatal(err)
	}
	engine := app.New(store, app.NewWorkflow(store).Run)
	router, err := httpapi.New(engine, testToken)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(router)
	engine.Start()
	h := &harness{engine: engine, server: server, router: router, client: &http.Client{Timeout: 5 * time.Second}}
	t.Cleanup(func() { engine.Stop(); server.Close(); store.Close() })
	return h
}

func (h *harness) keyed(t *testing.T, path, body, key string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest("POST", h.server.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-App-Token", testToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", key)
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, data
}

func TestFormalHTTPRunsRealRulesAndReplaysCommittedEvents(t *testing.T) {
	for _, mode := range []string{"rules_only", "rules_and_agent"} {
		t.Run(mode, func(t *testing.T) {
			h := newWorkflowHarness(t)
			ctx := context.Background()
			snapshot, err := app.NewAuditService(h.engine.Store).LoadExample(ctx, "internet-in")
			if err != nil {
				t.Fatal(err)
			}
			conv, err := h.engine.Store.CreateConversation(ctx, "实际规则", snapshot.ScopeRef)
			if err != nil {
				t.Fatal(err)
			}
			request := app.PlanRequest{Kind: "audit", ConversationID: conv.ID, DataSourceID: "local-offline", PolicyScopeRef: snapshot.ScopeRef, Selection: app.PlanSelection{Kind: "assets", AssetIDs: snapshot.Input.Selection.AssetIDs}, DataPolicy: app.DataPolicy{Mode: "pinned_snapshot", SnapshotID: snapshot.ID}, BaselineVersionID: "B2-v1", Mode: mode}
			encoded, _ := json.Marshal(request)
			status, body := h.request(t, "POST", "/api/v1/execution-plans", string(encoded))
			if status != 201 {
				t.Fatalf("preview %d %s", status, body)
			}
			var plan app.PlanPreview
			if err = json.Unmarshal(body, &plan); err != nil {
				t.Fatal(err)
			}
			status, body = h.keyed(t, "/api/v1/jobs", `{"plan_id":"`+plan.ID+`"}`, "formal-accept")
			if status != 202 {
				t.Fatalf("accept %d %s", status, body)
			}
			job := decodeJob(t, body)
			deadline := time.Now().Add(5 * time.Second)
			for {
				status, body = h.request(t, "GET", "/api/v1/jobs/"+job.ID, "")
				job = decodeJob(t, body)
				if storage.Terminal(job.Status) {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("formal job did not end")
				}
				time.Sleep(10 * time.Millisecond)
			}
			want := "completed"
			if mode == "rules_and_agent" {
				want = "partial"
			}
			if job.Status != want || job.ResultRef == "" {
				t.Fatalf("actual workflow %s %s", job.Status, job.ErrorCode)
			}
			status, body = h.request(t, "GET", "/api/v1/jobs/"+job.ID+"/result", "")
			var result httpapi.JobResult
			if err = json.Unmarshal(body, &result); err != nil {
				t.Fatal(err)
			}
			if status != 200 || !result.IsFinal || result.Audit == nil || len(result.Audit.Findings) == 0 || result.ViewSeq != job.ViewSeq {
				t.Fatal("real rule result not available")
			}
			status, body = h.request(t, "GET", "/api/v1/findings/"+result.Audit.Findings[0].ID, "")
			var finding httpapi.FindingDetail
			if err = json.Unmarshal(body, &finding); err != nil {
				t.Fatal(err)
			}
			if status != 200 || len(finding.Evidence) == 0 || finding.Evidence[0].Value == nil {
				t.Fatalf("fixed input evidence %d %s", status, body)
			}
			status, body = h.request(t, "GET", "/api/v1/jobs/"+job.ID+"/events/stream?after_seq=0", "")
			if status != 200 {
				t.Fatalf("committed SSE %d %s", status, body)
			}
			events := parseSSE(t, body)
			if int64(len(events)) != job.ViewSeq || events[len(events)-1].Type != "job_terminal" {
				t.Fatal("formal event tail differs from result")
			}
			status, body = h.request(t, "GET", "/api/v1/reports", "")
			if status != 200 || !strings.Contains(string(body), job.ID) {
				t.Fatal("report lost originating job")
			}
			status, body = h.keyed(t, "/api/v1/jobs", `{"plan_id":"`+plan.ID+`"}`, "formal-accept")
			if status != 200 || decodeJob(t, body).ID != job.ID {
				t.Fatal("formal retry changed task")
			}
		})
	}
}

func TestWorkspaceCRUDAndUnavailableModelHaveDurableSemantics(t *testing.T) {
	h := newHarness(t, nil)
	status, body := h.request(t, "GET", "/api/v1/capabilities", "")
	if status != 200 || !strings.Contains(string(body), `"worker_slots":3`) || !strings.Contains(string(body), `"model_available":false`) {
		t.Fatalf("capabilities %d %s", status, body)
	}
	status, body = h.request(t, "POST", "/api/v1/snapshots/examples/internet-in", `{}`)
	if status != 201 {
		t.Fatalf("load example %d %s", status, body)
	}
	var snapshot storage.SnapshotRecord
	if err := json.Unmarshal(body, &snapshot); err != nil {
		t.Fatal(err)
	}
	status, body = h.request(t, "POST", "/api/v1/conversations", `{"title":"审计会话","policy_scope_ref":"`+snapshot.ScopeRef+`"}`)
	if status != 201 {
		t.Fatalf("conversation %d %s", status, body)
	}
	var conv storage.Conversation
	json.Unmarshal(body, &conv)
	status, body = h.request(t, "GET", "/api/v1/workspaces/current", "")
	if status != 200 || !strings.Contains(string(body), conv.ID) {
		t.Fatal("current conversation was not saved")
	}
	status, body = h.request(t, "GET", "/api/v1/snapshots/"+snapshot.ID+"/assets", "")
	if status != 200 || !strings.Contains(string(body), `"local_confirmed_sections":[]`) {
		t.Fatalf("asset confirmation provenance %d %s", status, body)
	}
	message := `{"client_message_id":"unconfigured-message","policy_scope_ref":"` + snapshot.ScopeRef + `","snapshot_id":"` + snapshot.ID + `","text":"解释规则","model_use_accepted":true}`
	status, body = h.request(t, "POST", "/api/v1/conversations/"+conv.ID+"/messages", message)
	assertError(t, status, body, 409, "MODEL_NOT_CONFIGURED")
	status, body = h.request(t, "GET", "/api/v1/conversations/"+conv.ID+"/messages", "")
	if status != 200 || !strings.Contains(string(body), `"items":[]`) {
		t.Fatal("unconfigured send left an orphan message")
	}
	status, body = h.request(t, "PATCH", "/api/v1/conversations/"+conv.ID, `{"expected_revision":1,"title":"new title"}`)
	if status != 200 {
		t.Fatalf("valid edit: %d %s", status, body)
	}
	status, body = h.request(t, "PATCH", "/api/v1/conversations/"+conv.ID, `{"expected_revision":1,"title":"stale"}`)
	assertError(t, status, body, 409, "VERSION_CONFLICT")
}

func TestFormalPlanOnlyAcceptsServerFrozenEffects(t *testing.T) {
	h := newHarness(t, nil)
	ctx := context.Background()
	snapshot, err := app.NewAuditService(h.engine.Store).LoadExample(ctx, "internet-in")
	if err != nil {
		t.Fatal(err)
	}
	conv, err := h.engine.Store.CreateConversation(ctx, "计划", snapshot.ScopeRef)
	if err != nil {
		t.Fatal(err)
	}
	request := app.PlanRequest{Kind: "audit", ConversationID: conv.ID, DataSourceID: "local-offline", PolicyScopeRef: snapshot.ScopeRef, Selection: app.PlanSelection{Kind: "assets", AssetIDs: snapshot.Input.Selection.AssetIDs}, DataPolicy: app.DataPolicy{Mode: "pinned_snapshot", SnapshotID: snapshot.ID}, BaselineVersionID: "B2-v1", Mode: "rules_only"}
	encoded, _ := json.Marshal(request)
	status, body := h.request(t, "POST", "/api/v1/execution-plans", string(encoded))
	if status != 201 {
		t.Fatalf("preview %d %s", status, body)
	}
	var plan app.PlanPreview
	json.Unmarshal(body, &plan)
	if plan.ID == "" {
		t.Fatal("preview has no saved identity")
	}
	jobs, err := h.engine.Store.ListJobs(ctx, conv.ID, 0, 100)
	if err != nil || len(jobs) != 0 {
		t.Fatal("preview started execution")
	}
	status, body = h.request(t, "POST", "/api/v1/jobs", `{"plan_id":"`+plan.ID+`","mode":"rules_and_agent"}`)
	assertError(t, status, body, 422, "INVALID_REQUEST")
	status, body = h.request(t, "POST", "/api/v1/jobs", `{"plan_id":"`+plan.ID+`"}`)
	assertError(t, status, body, 422, "IDEMPOTENCY_KEY_REQUIRED")
}

func TestApplicationTokenCannotEnterBusinessRecords(t *testing.T) {
	h := newHarness(t, nil)
	for _, text := range []string{testToken, strings.ReplaceAll(testToken, "s", `\u0073`)} {
		status, body := h.request(t, http.MethodPost, "/api/v1/conversations", `{"title":"`+text+`","policy_scope_ref":""}`)
		assertError(t, status, body, 422, "CREDENTIAL_IN_PAYLOAD")
	}
	status, body := h.request(t, "POST", "/api/v1/conversations", `{"title":"合法","policy_scope_ref":"","unknown":true}`)
	assertError(t, status, body, 422, "INVALID_REQUEST")
}

func TestRecheckComparisonIsReadableWithTheNewJobResult(t *testing.T) {
	h := newWorkflowHarness(t)
	ctx := context.Background()
	snapshot, err := app.NewAuditService(h.engine.Store).LoadExample(ctx, "internet-in")
	if err != nil {
		t.Fatal(err)
	}
	conv, err := h.engine.Store.CreateConversation(ctx, "复审比较", snapshot.ScopeRef)
	if err != nil {
		t.Fatal(err)
	}
	request := app.PlanRequest{Kind: "audit", ConversationID: conv.ID, DataSourceID: "local-offline", PolicyScopeRef: snapshot.ScopeRef, Selection: app.PlanSelection{Kind: "assets", AssetIDs: snapshot.Input.Selection.AssetIDs}, DataPolicy: app.DataPolicy{Mode: "pinned_snapshot", SnapshotID: snapshot.ID}, BaselineVersionID: "B2-v1", Mode: "rules_only"}
	plan, err := app.NewPlanService(h.engine.Store).Preview(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	status, body := h.keyed(t, "/api/v1/jobs", `{"plan_id":"`+plan.ID+`"}`, "recheck-old")
	if status != 202 {
		t.Fatalf("%d %s", status, body)
	}
	old := decodeJob(t, body)
	awaitFormal := func(id string) storage.Job {
		t.Helper()
		until := time.Now().Add(5 * time.Second)
		for {
			status, body := h.request(t, "GET", "/api/v1/jobs/"+id, "")
			if status != 200 {
				t.Fatal(status, string(body))
			}
			j := decodeJob(t, body)
			if storage.Terminal(j.Status) {
				return j
			}
			if time.Now().After(until) {
				t.Fatal("recheck job timeout")
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	old = awaitFormal(old.ID)
	status, body = h.request(t, "POST", "/api/v1/audits/"+old.ID+"/rechecks", `{"snapshot_id":"`+snapshot.ID+`","conversation_id":"`+conv.ID+`","reuse_evaluation_at":true}`)
	if status != 201 {
		t.Fatalf("recheck preview %d %s", status, body)
	}
	json.Unmarshal(body, &plan)
	status, body = h.keyed(t, "/api/v1/jobs", `{"plan_id":"`+plan.ID+`"}`, "recheck-new")
	if status != 202 {
		t.Fatalf("%d %s", status, body)
	}
	fresh := awaitFormal(decodeJob(t, body).ID)
	status, body = h.request(t, "GET", "/api/v1/jobs/"+fresh.ID+"/result", "")
	var view struct {
		Recheck *storage.RecheckRecord `json:"recheck"`
	}
	if status != 200 || json.Unmarshal(body, &view) != nil || view.Recheck == nil || view.Recheck.OldAuditID != old.ResultRef || view.Recheck.NewAuditID != fresh.ResultRef {
		t.Fatalf("persistent comparison unavailable: status=%d, comparison_present=%v", status, view.Recheck != nil)
	}
}
