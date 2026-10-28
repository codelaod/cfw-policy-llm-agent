//go:build e3browserfixture

package httpapi_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"cfw-policy-llm-agent/server/internal/agentquery"
	"cfw-policy-llm-agent/server/internal/app"
	"cfw-policy-llm-agent/server/internal/httpapi"
	"cfw-policy-llm-agent/server/internal/modelprotocol"
	"cfw-policy-llm-agent/server/internal/outbound"
	"cfw-policy-llm-agent/server/internal/storage"
)

const e3FixtureNotice = "仅本机模型协议人工模拟：真实Store/Eino/工具/HTTP/SSE链，非真实LLM或云验收；固定模拟usage不代表供应商用量或账单。"
const e3FixtureModelKey = "synthetic-e3-browser-model-token"

type e3BrowserFixture struct {
	store     *storage.Store
	plans     *app.PlanService
	engine    *app.Engine
	manager   *outbound.Manager
	upstream  *httptest.Server
	api       *httptest.Server
	token     string
	dbPath    string
	snapshots map[string]storage.SnapshotRecord
}

type e3FixtureMessage struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"`
	ToolCallID string          `json:"tool_call_id"`
	ToolCalls  []struct {
		ID       string `json:"id"`
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	} `json:"tool_calls"`
}

func e3FixtureText(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) == nil {
		for _, part := range parts {
			if part.Type == "text" {
				text += part.Text
			}
		}
	}
	return text
}

type e3FixtureToolResult struct {
	EvidenceRefs []string `json:"evidence_refs"`
	Data         struct {
		AssetRef       string `json:"asset_ref"`
		AssociationRef string `json:"association_ref"`
		Status         string `json:"status"`
		Services       []struct {
			ServiceRef string `json:"service_ref"`
			Role       string `json:"role"`
		} `json:"services"`
	} `json:"data"`
}

// This model is a stateless artificial protocol script. Every next tool is
// selected from the current request's earlier real tool results, including the
// previously unavailable NAT association reference; no raw cloud IDs are used.
func e3FixtureModel(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" || r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer "+e3FixtureModelKey {
		http.Error(w, "invalid synthetic fixture request", 422)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 128<<10+1))
	r.Body.Close()
	if err != nil || len(body) > 128<<10 {
		http.Error(w, "synthetic fixture request too large", 422)
		return
	}
	var request struct {
		Model    string             `json:"model"`
		Stream   bool               `json:"stream"`
		Messages []e3FixtureMessage `json:"messages"`
	}
	if json.Unmarshal(body, &request) != nil || request.Stream || request.Model != "synthetic-browser-model" {
		http.Error(w, "invalid synthetic model envelope", 422)
		return
	}
	var initial agentquery.InitialView
	currentQuestion := ""
	for _, message := range request.Messages {
		if message.Role != "user" {
			continue
		}
		text := e3FixtureText(message.Content)
		if initial.ScopeRef == "" && strings.HasPrefix(text, "固定公开任务资料") {
			if start := strings.IndexByte(text, '{'); start >= 0 {
				json.Unmarshal([]byte(text[start:]), &initial)
			}
			continue
		}
		currentQuestion = text
	}
	if initial.ScopeRef == "" {
		http.Error(w, "missing synthetic task context", 422)
		return
	}
	if strings.HasPrefix(currentQuestion, "历史应用记录") {
		currentQuestion = ""
	}
	names := map[string]string{}
	for _, message := range request.Messages {
		for _, call := range message.ToolCalls {
			names[call.ID] = call.Function.Name
		}
	}
	var asset, nat, partition *e3FixtureToolResult
	for _, message := range request.Messages {
		if message.Role != "tool" {
			continue
		}
		var value e3FixtureToolResult
		if json.Unmarshal([]byte(e3FixtureText(message.Content)), &value) != nil {
			continue
		}
		switch names[message.ToolCallID] {
		case "get_asset_context":
			asset = &value
		case "get_nat_association":
			nat = &value
		case "get_partition_context":
			partition = &value
		}
	}
	select {
	case <-r.Context().Done():
		return
	case <-time.After(200 * time.Millisecond):
	}
	message := map[string]any{"role": "assistant"}
	finish := "stop"
	tool := func(name, key, ref, id string) {
		args, _ := json.Marshal(map[string]string{key: ref})
		message["content"] = nil
		message["tool_calls"] = []any{map[string]any{"id": id, "type": "function", "function": map[string]any{"name": name, "arguments": string(args)}}}
		finish = "tool_calls"
	}
	switch {
	case len(initial.Assets) > 0 && asset == nil:
		tool("get_asset_context", "asset_ref", initial.Assets[0], "fixture_asset_"+strings.TrimPrefix(initial.ScopeRef, "scope_"))
	case asset != nil && asset.Data.AssociationRef != "" && nat == nil:
		tool("get_nat_association", "association_ref", asset.Data.AssociationRef, "fixture_nat_"+strings.TrimPrefix(initial.ScopeRef, "scope_"))
	case len(initial.Assets) == 0 && partition == nil:
		tool("get_partition_context", "scope_ref", initial.ScopeRef, "fixture_partition_"+strings.TrimPrefix(initial.ScopeRef, "scope_"))
	default:
		targetKind, targetRef := "scope", initial.ScopeRef
		evidence := []string{}
		if asset != nil {
			targetKind, targetRef = "asset", asset.Data.AssetRef
			evidence = append(evidence, asset.EvidenceRefs...)
		}
		if nat != nil {
			evidence = append(evidence, nat.EvidenceRefs...)
		}
		if partition != nil {
			evidence = append(evidence, partition.EvidenceRefs...)
		}
		out := agentquery.Output{ScopeRef: initial.ScopeRef, RuleVersion: initial.RuleVersion, SourceKind: initial.SourceKind, AssessmentBasis: initial.AssessmentBasis, Explanation: "本轮为本机模型协议人工模拟，仅展示固定资料的只读补查和受控候选；业务角色与授权需人工确认。", Roles: []agentquery.RoleCandidate{}, Recommendations: []agentquery.Recommendation{{Kind: "confirm_information", TargetKind: targetKind, TargetRef: targetRef, BasisKind: "unknown_followup", EvidenceRefs: evidence, Action: "confirm_intent", Conditions: []string{"需由操作者核对实际业务资料与当前范围。"}, Impact: "业务资料及关联证据会影响审计判据，配置允许不证明实际可达。", ManualNextStep: "在资产资料页核对候选内容，预览差异后由操作者明确确认。"}}, Questions: []string{"请确认所选对象的实际用途及访问要求。"}}
		if asset != nil && len(asset.Data.Services) > 0 {
			service := asset.Data.Services[0]
			out.Roles = append(out.Roles, agentquery.RoleCandidate{AssetRef: asset.Data.AssetRef, ServiceRef: service.ServiceRef, Role: service.Role, SupportingEvidence: asset.EvidenceRefs, ContradictingEvidence: []string{}, Unknowns: []string{"该候选仍需核实，配置端口不证明实际监听。"}, Questions: []string{"请操作者确认该服务的实际用途。"}})
		}
		if nat != nil && nat.Data.Status == "unknown" {
			out.Explanation = "本轮为本机模型协议人工模拟；工具返回的NAT关联证据仍需核实，仅保留条件性提示。"
		}
		if strings.Contains(currentQuestion, "无效输出") {
			out.SourceKind = "live_readonly"
			out.AssessmentBasis = "live_observed"
		}
		encoded, _ := json.Marshal(out)
		message["content"] = string(encoded)
	}
	response := map[string]any{"id": "synthetic-browser-response", "object": "chat.completion", "model": "synthetic-browser-model", "choices": []any{map[string]any{"index": 0, "message": message, "finish_reason": finish}}}
	if !strings.Contains(currentQuestion, "未知用量") {
		response["usage"] = map[string]int{"prompt_tokens": 3, "completion_tokens": 2, "total_tokens": 5}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

func startE3BrowserFixture(t *testing.T, privateDir string) *e3BrowserFixture {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal("fixture root unavailable")
	}
	fixture := &e3BrowserFixture{token: rand.Text(), dbPath: filepath.Join(privateDir, "e3-browser-"+rand.Text()+".sqlite"), snapshots: map[string]storage.SnapshotRecord{}}
	t.Cleanup(func() {
		if fixture.engine != nil {
			fixture.engine.Stop()
		}
		if fixture.api != nil {
			fixture.api.CloseClientConnections()
			fixture.api.Close()
		}
		if fixture.manager != nil {
			fixture.manager.Close()
		}
		if fixture.upstream != nil {
			fixture.upstream.CloseClientConnections()
			fixture.upstream.Close()
		}
		if fixture.store != nil {
			fixture.store.Close()
		}
	})
	fixture.store, err = storage.Open(fixture.dbPath, true)
	if err != nil {
		t.Fatal("fixture storage initialization failed")
	}
	fixture.plans = app.NewPlanService(fixture.store)
	fixture.plans.ConfigRevision = "e3-browser-fixture-v1"
	fixture.plans.BudgetRef = "e3-browser-period-" + rand.Text()
	fixture.plans.ModelReady = true
	fixture.plans.ModelConfigured = true
	fixture.plans.ModelMode = "local_protocol_fixture"
	fixture.plans.ModelHTTPAttempts = 6
	if err = fixture.store.ConfigureBudgetPolicy(context.Background(), storage.BudgetPolicy{Ref: fixture.plans.BudgetRef, ApplicationModelAttempts: 80, SessionModelAttempts: 80, ExpiresAt: time.Now().Add(25 * time.Minute).UTC().Format(time.RFC3339Nano)}); err != nil {
		t.Fatal("fixture budget setup failed")
	}
	fixture.upstream = httptest.NewServer(http.HandlerFunc(e3FixtureModel))
	fixture.manager, err = outbound.NewManager(fixture.store, outbound.Config{Revision: fixture.plans.ConfigRevision, Sources: []outbound.Source{{Ref: "configured-model", Class: "model", Origin: fixture.upstream.URL, AllowLoopbackHTTP: true, MaxResponseBytes: modelprotocol.MaxResponseBytes, Routes: []outbound.Route{{Method: "POST", Path: "/v1/chat/completions"}}}}})
	if err != nil {
		t.Fatal("fixture outbound setup failed")
	}
	factory, err := modelprotocol.New(fixture.manager, modelprotocol.Config{BaseURL: fixture.upstream.URL + "/v1", APIKey: e3FixtureModelKey, Model: "synthetic-browser-model", ProviderProfile: "openai-compatible", MaxOutputTokens: 512, SensitiveValues: []string{fixture.token, fixture.upstream.URL}, AllowLoopbackFixture: true})
	if err != nil {
		t.Fatal("fixture model adapter setup failed")
	}
	workflow := app.NewWorkflow(fixture.store)
	privateValues := []string{fixture.token, e3FixtureModelKey, fixture.upstream.URL}
	workflow.Agent = app.NewAgentService(fixture.store, factory, fixture.plans.ConfigRevision, privateValues)
	fixture.engine = app.New(fixture.store, workflow.Run)
	router, err := httpapi.New(fixture.engine, fixture.token, httpapi.Options{Plans: fixture.plans, WebDir: filepath.Join(root, "web", "dist"), SensitiveValues: privateValues})
	if err != nil {
		t.Fatal("fixture API/static bundle setup failed")
	}
	for _, name := range []string{"internet-in", "internet-out", "nat-out"} {
		snapshot, err := app.NewAuditService(fixture.store).LoadExample(context.Background(), name)
		if err != nil {
			t.Fatal("public synthetic input unavailable")
		}
		fixture.snapshots[name] = snapshot
	}
	fixture.api = httptest.NewUnstartedServer(router)
	fixture.api.Config.ReadHeaderTimeout = 5 * time.Second
	fixture.api.Config.ReadTimeout = 10 * time.Second
	fixture.api.Config.IdleTimeout = 60 * time.Second
	fixture.api.Config.MaxHeaderBytes = 16 << 10
	fixture.api.Start()
	fixture.engine.Start()
	return fixture
}

func (f *e3BrowserFixture) request(t *testing.T, method, path string, body any, key string) []byte {
	t.Helper()
	var data []byte
	if body != nil {
		var err error
		data, err = json.Marshal(body)
		if err != nil {
			t.Fatal("fixture request encode failed")
		}
	}
	request, err := http.NewRequest(method, f.api.URL+path, strings.NewReader(string(data)))
	if err != nil {
		t.Fatal("fixture request initialization failed")
	}
	request.Header.Set("X-App-Token", f.token)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		request.Header.Set("Idempotency-Key", key)
	}
	response, err := (&http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}}).Do(request)
	if err != nil {
		t.Fatal("fixture application request failed")
	}
	defer response.Body.Close()
	data, err = io.ReadAll(io.LimitReader(response.Body, 10<<20))
	if err != nil || response.StatusCode >= 300 {
		t.Fatalf("fixture application status=%d", response.StatusCode)
	}
	return data
}

func (f *e3BrowserFixture) runSmoke(t *testing.T, example, question string, assistant bool) storage.Job {
	t.Helper()
	snapshot := f.snapshots[example]
	var conversation storage.Conversation
	if json.Unmarshal(f.request(t, "POST", "/api/v1/conversations", map[string]string{"title": "本机模型协议人工烟测", "policy_scope_ref": snapshot.ScopeRef}, ""), &conversation) != nil {
		t.Fatal("fixture conversation decode failed")
	}
	var job storage.Job
	if assistant {
		var value httpapi.SendResponse
		if json.Unmarshal(f.request(t, "POST", "/api/v1/conversations/"+conversation.ID+"/messages", app.AssistantRequest{ClientMessageID: "fixture-message-" + rand.Text(), SnapshotID: snapshot.ID, PolicyScopeRef: snapshot.ScopeRef, Text: question, ModelUseAccepted: true}, ""), &value) != nil {
			t.Fatal("fixture assistant admission decode failed")
		}
		job = value.Job
	} else {
		var plan app.PlanPreview
		if json.Unmarshal(f.request(t, "POST", "/api/v1/execution-plans", app.PlanRequest{Kind: "audit", ConversationID: conversation.ID, DataSourceID: "local-offline", PolicyScopeRef: snapshot.ScopeRef, Selection: app.PlanSelection{Kind: "assets", AssetIDs: snapshot.Input.Selection.AssetIDs}, DataPolicy: app.DataPolicy{Mode: "pinned_snapshot", SnapshotID: snapshot.ID}, BaselineVersionID: "B2-v1", Mode: "rules_and_agent"}, ""), &plan) != nil {
			t.Fatal("fixture plan decode failed")
		}
		if json.Unmarshal(f.request(t, "POST", "/api/v1/jobs", httpapi.StartRequest{PlanID: plan.ID}, "fixture-accept-"+rand.Text()), &job) != nil {
			t.Fatal("fixture execution admission decode failed")
		}
	}
	if job.ID == "" {
		t.Fatal("fixture admission did not return a job")
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		if json.Unmarshal(f.request(t, "GET", "/api/v1/jobs/"+job.ID, nil, ""), &job) != nil {
			t.Fatal("fixture state decode failed")
		}
		if storage.Terminal(job.Status) {
			return job
		}
		if time.Now().After(deadline) {
			t.Fatal("fixture job did not finish")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Explicit tagged invocation only; normal tests never start this waiting server.
func TestE3BrowserFixtureServe(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal("fixture root unavailable")
	}
	privateDir := filepath.Join(root, "runtime")
	if err = os.MkdirAll(privateDir, 0700); err != nil {
		t.Fatal("fixture runtime unavailable")
	}
	tokenPath, statePath, stopPath := filepath.Join(privateDir, "e3-browser-token"), filepath.Join(privateDir, "e3-browser-state.json"), filepath.Join(privateDir, "e3-browser-stop")
	if _, err = os.Stat(statePath); err == nil {
		t.Fatal("existing browser fixture state must be resolved before another fixture starts")
	}
	fixture := startE3BrowserFixture(t, privateDir)
	token, err := os.OpenFile(tokenPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal("browser fixture token path is already in use")
	}
	if _, err = token.WriteString(fixture.token); err != nil {
		token.Close()
		os.Remove(tokenPath)
		t.Fatal("fixture token could not be saved")
	}
	token.Close()
	t.Cleanup(func() { os.Remove(tokenPath); os.Remove(statePath) })
	os.Remove(stopPath)
	state := struct {
		APIURL    string `json:"api_url"`
		TokenPath string `json:"token_path"`
		DBPath    string `json:"db_path"`
		Notice    string `json:"fixture_notice"`
	}{fixture.api.URL, tokenPath, fixture.dbPath, e3FixtureNotice}
	encoded, _ := json.MarshalIndent(state, "", "  ")
	if err = os.WriteFile(statePath, append(encoded, '\n'), 0600); err != nil {
		t.Fatal("fixture state could not be saved")
	}
	fmt.Printf("E3 local protocol fixture ready; application URL=%s; token path=%s; state path=%s; stop marker=%s\n", fixture.api.URL, tokenPath, statePath, stopPath)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	limit := time.NewTimer(20 * time.Minute)
	defer limit.Stop()
	for {
		select {
		case <-limit.C:
			return
		case <-ticker.C:
			if _, err = os.Stat(stopPath); err == nil {
				return
			}
		}
	}
}

func TestE3BrowserFixtureSmoke(t *testing.T) {
	fixture := startE3BrowserFixture(t, t.TempDir())
	for _, scenario := range []struct {
		name, example, question string
		assistant               bool
		status                  string
		calls, tools            int
		unknownUsage            bool
	}{
		{"internet-audit", "internet-in", "", false, "completed", 2, 1, false},
		{"nat-dependent-audit", "nat-out", "", false, "completed", 3, 2, false},
		{"invalid-output", "internet-in", "请演示无效输出", true, "failed", 2, 1, false},
		{"unknown-usage", "internet-in", "请演示未知用量", true, "completed", 2, 1, true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			job := fixture.runSmoke(t, scenario.example, scenario.question, scenario.assistant)
			if job.Status != scenario.status {
				t.Fatalf("fixture status=%s code=%s", job.Status, job.ErrorCode)
			}
			result, err := fixture.store.GetJobAgentResult(context.Background(), job.ID)
			if err != nil || result == nil || result.Content.ModelCalls != scenario.calls || result.Content.ToolCalls != scenario.tools {
				t.Fatal("fixture did not execute actual bounded model/tool rounds", err)
			}
			if scenario.status == "completed" && result.Content.Output == nil || scenario.status == "failed" && result.Content.Output != nil {
				t.Fatal("fixture output did not pass through real validation")
			}
			if scenario.example == "nat-out" {
				page, _, err := fixture.store.Page(context.Background(), job.ID, 0, 100)
				if err != nil {
					t.Fatal(err)
				}
				names := []string{}
				for _, event := range page.Events {
					if event.Type == "tool_started" {
						name, _ := event.Payload["tool"].(string)
						names = append(names, name)
					}
				}
				if !slices.Equal(names, []string{"get_asset_context", "get_nat_association"}) {
					t.Fatal("NAT fixture did not follow the earlier tool's association reference", names)
				}
			}
			usage, err := fixture.store.BudgetUsage(context.Background(), fixture.plans.BudgetRef, "", job.ID)
			if err != nil || usage.ModelAttempts != int64(scenario.calls) || (usage.ReportedTokens == nil) != scenario.unknownUsage {
				t.Fatal("fixture usage was not recorded from its HTTP response", err)
			}
		})
	}
}
