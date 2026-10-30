package httpapi_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cfw-policy-llm-agent/server/internal/app"
	"cfw-policy-llm-agent/server/internal/domain"
	"cfw-policy-llm-agent/server/internal/httpapi"
	"cfw-policy-llm-agent/server/internal/report"
	"cfw-policy-llm-agent/server/internal/storage"
)

// This is one actual local HTTP journey, including a process-style store reopen.
// Its public artificial input does not establish cloud/model or full J coverage.
func TestIndependentCustomerJourneyOfflineHistoryAndReceiptAfterRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "customer.sqlite")
	var h *harness
	closeServer := func() {
		if h != nil {
			h.engine.Stop()
			h.server.Close()
			h.engine.Store.Close()
			h = nil
		}
	}
	start := func(migrate bool) {
		store, err := storage.Open(path, migrate)
		if err != nil {
			t.Fatal(err)
		}
		engine := app.New(store, app.NewWorkflow(store).Run)
		router, err := httpapi.New(engine, testToken)
		if err != nil {
			t.Fatal(err)
		}
		h = &harness{engine: engine, server: httptest.NewServer(router), router: router, client: &http.Client{Timeout: 5 * time.Second}}
		engine.Start()
	}
	start(true)
	t.Cleanup(closeServer)
	post := func(endpoint, key string, req any, want int, dst any) []byte {
		t.Helper()
		data, err := json.Marshal(req)
		if err != nil {
			t.Fatal(err)
		}
		status, body := h.keyed(t, endpoint, string(data), key)
		if status != want {
			t.Fatalf("POST %s: %d %s", endpoint, status, body)
		}
		if dst != nil && json.Unmarshal(body, dst) != nil {
			t.Fatalf("invalid response: %s", body)
		}
		return body
	}
	get := func(endpoint string, dst any) []byte {
		t.Helper()
		status, body := h.request(t, "GET", endpoint, "")
		if status != 200 {
			t.Fatalf("GET %s: %d %s", endpoint, status, body)
		}
		if dst != nil && json.Unmarshal(body, dst) != nil {
			t.Fatalf("invalid response: %s", body)
		}
		return body
	}
	var conv storage.Conversation
	post("/api/v1/conversations", "journey-create", map[string]string{"title": "独立离线全链审查"}, 201, &conv)
	turnPath := "/api/v1/conversations/" + conv.ID + "/intent-turns"
	var goal httpapi.IntentResponse
	post(turnPath, "journey-no-data", app.IntentTurnRequest{ClientTurnID: "journey-no-data", Text: "审计 asset-demo-a 的互联网入向，只运行规则"}, 200, &goal)
	if goal.Intent == nil || goal.Intent.Status != "needs_discovery" || goal.Job != nil {
		t.Fatalf("empty start lost goal or executed: %+v", goal)
	}
	var snapshot storage.SnapshotRecord
	post("/api/v1/snapshots/examples/internet-in", "journey-example", map[string]string{}, 201, &snapshot)
	post(turnPath, "journey-pin", app.IntentTurnRequest{ClientTurnID: "journey-pin", Text: "审计 asset-demo-a 的互联网入向，只运行规则", DataPolicy: &domain.IntentDataPolicy{Mode: "pinned_snapshot", SnapshotID: snapshot.ID}}, 200, &goal)
	if goal.Proposal == nil || goal.Proposal.PlanID == "" {
		t.Fatalf("offline goal did not produce plan: %+v", goal)
	}
	var accepted httpapi.IntentResponse
	acceptReq := app.IntentTurnRequest{ClientTurnID: "journey-accept", Text: "确认按当前计划开始审计", ExpectedRevision: &goal.Intent.Revision}
	post(turnPath, "journey-accept", acceptReq, 200, &accepted)
	if accepted.Job == nil || accepted.Proposal == nil {
		t.Fatalf("natural confirmation did not accept: %+v", accepted)
	}
	jobID := accepted.Job.ID
	deadline := time.Now().Add(5 * time.Second)
	var job storage.Job
	for {
		get("/api/v1/jobs/"+jobID, &job)
		if storage.Terminal(job.Status) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("offline journey did not terminate")
		}
		time.Sleep(10 * time.Millisecond)
	}
	var result httpapi.JobResult
	get("/api/v1/jobs/"+jobID+"/result", &result)
	if job.Status != "completed" || result.Audit == nil || len(result.Audit.Findings) == 0 {
		t.Fatalf("missing completed real rule result: %+v", result)
	}
	finding := result.Audit.Findings[0]
	var review storage.ReviewAction
	reviewPath := "/api/v1/findings/" + finding.ID + "/review-actions"
	post(reviewPath, "journey-defer", storage.ReviewRequest{ExpectedRevision: 0, Kind: "plan_change", Plan: "deferred", Reason: "待核对人工变更窗口"}, 201, &review)
	var fixed storage.ReportView
	post("/api/v1/reports", "journey-json", map[string]string{"audit_id": result.Audit.ID, "format": "json"}, 201, &fixed)
	before := get("/api/v1/reports/"+fixed.ID+"/content", nil)
	var doc report.Document
	if json.Unmarshal(before, &doc) != nil {
		t.Fatal("report JSON is invalid")
	}
	found := false
	for _, item := range doc.Findings {
		found = found || item.Plan == "deferred" && item.ReviewRevision == review.Revision
	}
	if !found {
		t.Fatal("fixed export lost captured local review")
	}
	post(reviewPath, "journey-todo", storage.ReviewRequest{ExpectedRevision: review.Revision, Kind: "plan_change", Plan: "todo"}, 201, &review)
	var readable storage.ReportView
	post("/api/v1/reports", "journey-md", map[string]string{"audit_id": result.Audit.ID, "format": "markdown"}, 201, &readable)
	if !strings.Contains(string(get("/api/v1/reports/"+readable.ID+"/content", nil)), "plan=todo") {
		t.Fatal("new readable report lost updated plan")
	}
	closeServer()
	start(false)
	if string(get("/api/v1/reports/"+fixed.ID+"/content", nil)) != string(before) {
		t.Fatal("old report changed after review/restart")
	}
	var replay httpapi.IntentResponse
	post(turnPath, "journey-accept", acceptReq, 200, &replay)
	if !replay.Replayed || replay.Job == nil || replay.Job.ID != jobID {
		t.Fatalf("original acceptance receipt was not recovered: %+v", replay)
	}
	var page httpapi.JobPage
	get("/api/v1/jobs?conversation_id="+conv.ID, &page)
	if len(page.Items) != 1 || page.Items[0].ID != jobID {
		t.Fatal("restart/replay created another execution")
	}
	post(turnPath, "journey-history", app.IntentTurnRequest{ClientTurnID: "journey-history", Text: "解释这份旧报告的依据，只运行规则", ContextReportID: fixed.ID}, 200, &goal)
	// Local history reads preserve the already accepted current goal/receipt.
	// They must not create another executable proposal or task.
	if goal.Job != nil || goal.Proposal != nil && (goal.Proposal.ID != accepted.Proposal.ID || goal.Proposal.Status != "accepted") || goal.Reply == nil || !strings.Contains(goal.Reply.PublicText, fixed.ID) || !strings.Contains(goal.Reply.PublicText, "没有查询当前云配置") {
		t.Fatalf("history explanation lost fixed anchor: %+v", goal)
	}
	get("/api/v1/jobs?conversation_id="+conv.ID, &page)
	if len(page.Items) != 1 || page.Items[0].ID != jobID {
		t.Fatal("local historical explanation created a new execution")
	}
}
