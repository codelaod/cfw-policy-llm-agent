package httpapi_test

import (
	"encoding/json"
	"testing"
	"time"

	"cfw-policy-llm-agent/server/internal/app"
	"cfw-policy-llm-agent/server/internal/audit"
	"cfw-policy-llm-agent/server/internal/domain"
	"cfw-policy-llm-agent/server/internal/httpapi"
	"cfw-policy-llm-agent/server/internal/storage"
)

// Exercise public HTTP producers and consumers, including the next audit's
// actual rules. This artificial offline journey makes no cloud/model claim.
func TestBusinessDeclarationHTTPJourneyPreservesVersionsAndRuleMeaning(t *testing.T) {
	h := newWorkflowHarness(t)
	post := func(path, key string, request any, want int, response any) {
		t.Helper()
		data, err := json.Marshal(request)
		if err != nil {
			t.Fatal(err)
		}
		status, body := h.keyed(t, path, string(data), key)
		if status != want {
			t.Fatalf("POST %s: %d %s", path, status, body)
		}
		if response != nil {
			if err = json.Unmarshal(body, response); err != nil {
				t.Fatal(err)
			}
		}
	}
	get := func(path string, response any) {
		t.Helper()
		status, body := h.request(t, "GET", path, "")
		if status != 200 {
			t.Fatalf("GET %s: %d %s", path, status, body)
		}
		if err := json.Unmarshal(body, response); err != nil {
			t.Fatal(err)
		}
	}
	input, err := audit.SyntheticExample("internet-in")
	if err != nil {
		t.Fatal(err)
	}
	input.SnapshotVersion = "business-declaration-http-artificial"
	for i := range input.Assets {
		asset := &input.Assets[i]
		asset.AllowedConfirmed, asset.AllowedComplete, asset.DenyAll = false, false, false
		asset.Allowed = []domain.Access{}
		for j := range asset.Services {
			service := &asset.Services[j]
			service.RoleStatus, service.PublicAccess = "candidate", "unknown"
			service.AllowedConfirmed, service.AllowedComplete, service.RequiredConfirmed, service.DenyAll = false, false, false, false
			service.Allowed, service.Required = []domain.Access{}, []domain.Access{}
		}
	}
	var snapshot storage.SnapshotRecord
	post("/api/v1/snapshots/import", "business-journey-import", input, 201, &snapshot)
	if snapshot.SourceKind != "imported" {
		t.Fatal("uploaded artificial input was promoted to cloud observation")
	}
	var conversation storage.Conversation
	post("/api/v1/conversations", "business-journey-create", map[string]string{"title": "多服务业务确认离线回归"}, 201, &conversation)
	turnPath := "/api/v1/conversations/" + conversation.ID + "/intent-turns"
	runAudit := func(key string) (httpapi.JobResult, string) {
		t.Helper()
		var goal, accepted httpapi.IntentResponse
		post(turnPath, key+"-goal", app.IntentTurnRequest{ClientTurnID: key + "-goal", Text: "审计asset-demo-a的互联网入向，只运行规则", DataPolicy: &domain.IntentDataPolicy{Mode: "pinned_snapshot", SnapshotID: snapshot.ID}}, 200, &goal)
		if goal.Intent == nil || goal.Proposal == nil || goal.Proposal.PlanID == "" {
			t.Fatalf("customer goal did not produce a plan: %+v", goal)
		}
		post(turnPath, key+"-accept", app.IntentTurnRequest{ClientTurnID: key + "-accept", Text: "确认按当前计划开始审计", ExpectedRevision: &goal.Intent.Revision}, 200, &accepted)
		if accepted.Job == nil {
			t.Fatal("accepted customer plan did not identify its job")
		}
		var job storage.Job
		deadline := time.Now().Add(5 * time.Second)
		for {
			get("/api/v1/jobs/"+accepted.Job.ID, &job)
			if storage.Terminal(job.Status) {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("offline audit did not finish")
			}
			time.Sleep(10 * time.Millisecond)
		}
		var result httpapi.JobResult
		get("/api/v1/jobs/"+job.ID+"/result", &result)
		if job.Status != "completed" || result.Audit == nil {
			t.Fatalf("actual offline audit failed: %+v", result)
		}
		return result, job.ID
	}
	_, beforeJob := runAudit("business-journey-before")
	var rejected httpapi.IntentResponse
	post(turnPath, "business-journey-negative", app.IntentTurnRequest{ClientTurnID: "business-journey-negative", Text: "确认管理服务，不允许198.51.100.0/25访问TCP22"}, 200, &rejected)
	if rejected.Business == nil || rejected.Business.State != "needs_clarification" || rejected.Business.Draft != nil || rejected.Job != nil {
		t.Fatalf("negative declaration became a saved positive permission: %+v", rejected)
	}
	text := "HTTPS443必须保留，来源0.0.0.0/0；管理服务TCP22只允许198.51.100.0/25，允许集合完整"
	editRequest := app.IntentTurnRequest{ClientTurnID: "business-journey-edit", Text: text}
	var edited, confirmed httpapi.IntentResponse
	post(turnPath, "business-journey-edit", editRequest, 200, &edited)
	if edited.Business == nil || edited.Business.State != "preview_required" || edited.Business.Preview == nil {
		t.Fatalf("multi-service natural input lost its preview: %+v", edited)
	}
	post(turnPath, "business-journey-confirm", app.IntentTurnRequest{ClientTurnID: "business-journey-confirm", Text: "确认预览中的访问"}, 200, &confirmed)
	if confirmed.Business == nil || confirmed.Business.State != "confirmed" || confirmed.Business.Confirmation == nil {
		t.Fatalf("explicit section confirmation was not saved: %+v", confirmed)
	}
	after, _ := runAudit("business-journey-after")
	managementViolation := false
	for _, finding := range after.Audit.Result.Findings {
		if finding.RuleID == "CHK-03" && finding.ServiceRef == "svc-management" && finding.Verdict == "fail" {
			managementViolation = true
		}
	}
	if !managementViolation || !after.Audit.Input.Assets[0].Services[1].AllowedConfirmed || after.Audit.Input.Assets[0].Services[0].AllowedConfirmed {
		t.Fatal("the next audit did not use the correct service-specific authorization")
	}
	var original domain.Input
	get("/api/v1/jobs/"+beforeJob+"/input", &original)
	if original.Assets[0].Services[1].AllowedConfirmed || original.Assets[0].Services[0].RequiredConfirmed {
		t.Fatal("business confirmation changed the previous sealed task")
	}
	var replay httpapi.IntentResponse
	post(turnPath, "business-journey-edit", editRequest, 200, &replay)
	if !replay.Replayed || replay.Business == nil || replay.Business.Draft.ID != edited.Business.Draft.ID || replay.Business.Preview.ID != edited.Business.Preview.ID {
		t.Fatal("original HTTP receipt was not replayed after the business head changed")
	}
	var jobs httpapi.JobPage
	get("/api/v1/jobs?conversation_id="+conversation.ID, &jobs)
	if len(jobs.Items) != 2 {
		t.Fatal("business edits, confirmations or receipt replay created an extra execution")
	}
}
