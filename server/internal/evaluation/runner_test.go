package evaluation

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cfw-policy-llm-agent/server/internal/agentquery"
	"cfw-policy-llm-agent/server/internal/agentrun"
	"cfw-policy-llm-agent/server/internal/app"
	"cfw-policy-llm-agent/server/internal/modelprotocol"
	"cfw-policy-llm-agent/server/internal/outbound"
	"cfw-policy-llm-agent/server/internal/storage"
	"cfw-policy-llm-agent/server/internal/workload"
)

type protocolWire struct {
	Model       string  `json:"model"`
	Stream      bool    `json:"stream"`
	Maximum     int     `json:"max_completion_tokens"`
	Temperature float32 `json:"temperature"`
	TopP        float32 `json:"top_p"`
	Messages    []struct {
		Role       string `json:"role"`
		Content    string `json:"content"`
		Name       string `json:"name"`
		ToolCallID string `json:"tool_call_id"`
	} `json:"messages"`
}

func wireInitial(t *testing.T, wire protocolWire) agentquery.InitialView {
	t.Helper()
	for _, m := range wire.Messages {
		if strings.HasPrefix(m.Content, "固定公开任务资料") {
			var initial agentquery.InitialView
			if e := json.Unmarshal([]byte(m.Content[strings.Index(m.Content, "{"):]), &initial); e != nil {
				t.Error(e)
			}
			return initial
		}
	}
	t.Error("missing common initial context")
	return agentquery.InitialView{}
}
func finalReply(initial agentquery.InitialView) string {
	text, _ := json.Marshal(agentquery.Output{ScopeRef: initial.ScopeRef, RuleVersion: initial.RuleVersion, SourceKind: initial.SourceKind, AssessmentBasis: initial.AssessmentBasis, Explanation: "本机人工模型协议fixture，仅用于验证调用链。", Roles: []agentquery.RoleCandidate{}, Recommendations: []agentquery.Recommendation{}, Questions: []string{}})
	return string(text)
}
func writeReply(w http.ResponseWriter, message map[string]any, finish string, usage any) {
	w.Header().Set("Content-Type", "application/json")
	body := map[string]any{"id": "local-fixture", "choices": []any{map[string]any{"index": 0, "message": message, "finish_reason": finish}}}
	if usage != nil {
		body["usage"] = usage
	}
	_ = json.NewEncoder(w).Encode(body)
}
func writeTool(w http.ResponseWriter, id, name, key, ref string) {
	args, _ := json.Marshal(map[string]string{key: ref})
	writeReply(w, map[string]any{"role": "assistant", "content": "", "reasoning_content": "PRIVATE_EVAL_REASONING", "tool_calls": []any{map[string]any{"id": id, "type": "function", "function": map[string]any{"name": name, "arguments": string(args)}}}}, "tool_calls", nil)
}

type evaluationFixture struct {
	store       *storage.Store
	plans       *app.PlanService
	snapshot    storage.SnapshotRecord
	runner      *Runner
	protocol    Protocol
	caseData    FrozenCase
	journal     *FileJournal
	journalPath string
	requests    atomic.Int32
}

func newEvaluationFixture(t *testing.T, behavior string, ceiling *workload.AttemptCeiling) *evaluationFixture {
	t.Helper()
	f := &evaluationFixture{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.requests.Add(1)
		body, e := io.ReadAll(r.Body)
		if e != nil {
			t.Error(e)
			return
		}
		if strings.Contains(string(body), "PRIVATE_LABEL_SENTINEL") {
			t.Error("frozen labels leaked to model")
		}
		var wire protocolWire
		if json.Unmarshal(body, &wire) != nil || wire.Model != "fixture-model" || wire.Maximum != 512 || wire.Stream || wire.Temperature != 0.2 || wire.TopP != 1 {
			t.Error("changed model conditions", string(body))
			w.WriteHeader(500)
			return
		}
		if behavior == "http-error" {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, "PRIVATE_PROVIDER_ERROR")
			return
		}
		if behavior == "cancel" {
			<-r.Context().Done()
			return
		}
		initial := wireInitial(t, wire)
		if behavior == "invalid-output" {
			writeReply(w, map[string]any{"role": "assistant", "content": "invalid-final"}, "stop", nil)
			return
		}
		if strings.Contains(string(body), "predefined_pipeline") {
			for _, m := range wire.Messages {
				if m.Role == "tool" {
					t.Error("fixed prefetch forged model tool wire")
				}
			}
			writeReply(w, map[string]any{"role": "assistant", "content": finalReply(initial), "reasoning_content": "PRIVATE_EVAL_REASONING"}, "stop", nil)
			return
		}
		toolMessages := []string{}
		for _, m := range wire.Messages {
			if m.Role == "tool" {
				toolMessages = append(toolMessages, m.Content)
			}
		}
		if len(toolMessages) == 0 {
			writeTool(w, "adaptive_asset", "get_asset_context", "asset_ref", initial.Assets[0])
			return
		}
		if len(toolMessages) == 1 {
			var result struct {
				Data struct {
					BusinessRef string `json:"business_ref"`
				} `json:"data"`
			}
			if json.Unmarshal([]byte(toolMessages[0]), &result) != nil || result.Data.BusinessRef == "" {
				t.Error("no dependency returned", toolMessages[0])
				w.WriteHeader(500)
				return
			}
			writeTool(w, "adaptive_business", "get_business_intent", "business_ref", result.Data.BusinessRef)
			return
		}
		writeReply(w, map[string]any{"role": "assistant", "content": finalReply(initial), "reasoning_content": "PRIVATE_EVAL_REASONING"}, "stop", map[string]int{"prompt_tokens": 10, "completion_tokens": 3, "total_tokens": 13})
	}))
	t.Cleanup(server.Close)
	ctx := context.Background()
	store, e := storage.Open(filepath.Join(t.TempDir(), "evaluation.sqlite"), true)
	if e != nil {
		t.Fatal(e)
	}
	f.store = store
	t.Cleanup(func() { _ = store.Close() })
	f.snapshot, e = app.NewAuditService(store).LoadExample(ctx, "internet-in")
	if e != nil {
		t.Fatal(e)
	}
	f.plans = app.NewPlanService(store)
	f.plans.ModelReady = true
	f.plans.ConfigRevision = "evaluation-local-v1"
	f.plans.BudgetRef = "same-evaluation-grant"
	if e = store.ConfigureBudgetPolicy(ctx, storage.BudgetPolicy{Ref: f.plans.BudgetRef, ApplicationModelAttempts: 30, SessionModelAttempts: 30, ExpiresAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)}); e != nil {
		t.Fatal(e)
	}
	manager, e := outbound.NewManager(store, outbound.Config{Revision: f.plans.ConfigRevision, Sources: []outbound.Source{{Ref: "configured-model", Class: "model", Origin: server.URL, AllowLoopbackHTTP: true, Routes: []outbound.Route{{Method: "POST", Path: "/v1/chat/completions"}}, Timeout: 5 * time.Second, MaxResponseBytes: 256 << 10, Ceiling: ceiling}}})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(manager.Close)
	factory, e := modelprotocol.New(manager, modelprotocol.Config{BaseURL: server.URL + "/v1", APIKey: "synthetic-evaluation-key", Model: "fixture-model", ProviderProfile: "openai-compatible", MaxOutputTokens: 512, AllowLoopbackFixture: true})
	if e != nil {
		t.Fatal(e)
	}
	f.journalPath = filepath.Join(t.TempDir(), "runs.jsonl")
	f.journal, e = OpenFileJournal(f.journalPath, "synthetic-evaluation-key", "PRIVATE_LABEL_SENTINEL")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = f.journal.Close() })
	f.runner, e = NewRunner(Binding{Store: store, Factory: factory, Journal: f.journal, RuntimeRevision: f.plans.ConfigRevision, BudgetRef: f.plans.BudgetRef, ModelConfigRef: "same-local-fixture-v1", Sampling: Sampling{Temperature: 0.2, TopP: 1}, SensitiveValues: []string{"synthetic-evaluation-key", "PRIVATE_LABEL_SENTINEL"}})
	if e != nil {
		t.Fatal(e)
	}
	profile, e := workload.Normalize("audit", workload.Resolved{})
	if e != nil {
		t.Fatal(e)
	}
	d := CaseDefinition{ID: "case-one", ScenarioRef: "scenario-one", Split: "development", Repetitions: 1, LabelVersionRef: "labels-one", LabelReviewState: "proposed"}
	f.caseData, e = FreezeCase(ctx, d, f.snapshot.Input, profile)
	if e != nil {
		t.Fatal(e)
	}
	f.protocol = Protocol{SchemaVersion: 1, Version: "local-protocol-v1", DatasetUse: "development", Cases: []CaseDefinition{d}, CommonBriefVersion: "brief-v1", CommonBrief: "依据共同固定资料核对配置和业务缺项。", ModelConfigRef: "same-local-fixture-v1", Sampling: Sampling{Temperature: 0.2, TopP: 1}, Workload: profile, ToolProjectionVersion: ToolProjectionVersion, FinalSchemaVersion: FinalSchemaVersion, AgentPromptVersion: AgentPromptVersion, FixedPromptVersion: FixedPromptVersion, FixedPipeline: agentrun.FixedPipeline{Version: "pipeline-v1", Steps: []agentrun.FixedStep{{Tool: "get_asset_context", Selector: "assets", MaxItems: 1}, {Tool: "get_business_intent", Selector: "business", MaxItems: 4}}}}
	return f
}
func (f *evaluationFixture) accepted(t *testing.T, arm Arm, id string) Assignment {
	t.Helper()
	ctx := context.Background()
	conversation, e := f.store.CreateConversation(ctx, "本机人工评价fixture", f.snapshot.ScopeRef)
	if e != nil {
		t.Fatal(e)
	}
	mode := "rules_and_agent"
	if arm == RulesOnly {
		mode = "rules_only"
	}
	plan, e := f.plans.Preview(ctx, app.PlanRequest{Kind: "audit", ConversationID: conversation.ID, DataSourceID: "local-offline", PolicyScopeRef: f.snapshot.ScopeRef, Selection: app.PlanSelection{Kind: "assets", AssetIDs: f.snapshot.Input.Selection.AssetIDs}, DataPolicy: app.DataPolicy{Mode: "pinned_snapshot", SnapshotID: f.snapshot.ID}, BaselineVersionID: "B2-v1", Mode: mode})
	if e != nil {
		t.Fatal(e)
	}
	_, _, e = f.store.EnqueuePlan(ctx, plan.ID, id, f.plans.ConfigRevision)
	if e != nil {
		t.Fatal(e)
	}
	j, found, e := f.store.Claim(ctx)
	if e != nil || !found {
		t.Fatal(e, found)
	}
	return Assignment{RunID: id, CaseID: f.caseData.Definition.ID, Arm: arm, Repetition: 1, JobID: j.ID}
}
func TestThreeArmsUseRealManagerLedgerAndDistinctSelectionPaths(t *testing.T) {
	f := newEvaluationFixture(t, "success", nil)
	var outcomes []RunOutcome
	for i, arm := range AllArms() {
		a := f.accepted(t, arm, []string{"run-rules", "run-fixed", "run-agent"}[i])
		before := f.requests.Load()
		out, e := f.runner.Run(context.Background(), f.protocol, f.caseData, a)
		if e != nil || out.Record.Status != "completed" || out.Record.OutputAbsent || out.Record.ReportRef == "" || out.Record.RuleAuditRef == "" {
			t.Fatal(out.Record, e)
		}
		if arm == RulesOnly && (f.requests.Load() != before || out.Record.ModelCalls != 0 || !out.Record.ModelOutputAbsent) {
			t.Fatal("rules arm called model", out.Record)
		}
		if arm == FixedModel && (out.Record.ModelCalls != 1 || out.Record.ToolCalls != 5 || out.Record.Usage.Attempts.UnknownUsageAttempts != 1 || out.Record.Usage.Attempts.ReportedTokens != nil) {
			t.Fatal("fixed counts", out.Record)
		}
		if arm == AgentSelection && (out.Record.ModelCalls != 3 || out.Record.ToolCalls != 2 || len(out.Record.Traces) != 2 || len(out.Record.Traces[1].DependsOn) != 1 || out.Record.Traces[1].DependsOn[0] != "adaptive_asset" || out.Record.Usage.Attempts.KnownUsageResponses != 1 || out.Record.Usage.Attempts.UnknownUsageAttempts != 2 || out.Record.Usage.Attempts.ReportedTokens == nil) {
			t.Fatal("adaptive path/counts", out.Record)
		}
		for _, trace := range out.Record.Traces {
			want := "model"
			if arm == FixedModel {
				want = "predefined_pipeline"
			}
			if trace.SelectionOrigin != want || len(trace.InputRefs) != 1 {
				t.Fatal("trace origin/arguments", trace)
			}
		}
		if arm == FixedModel {
			if len(out.Record.Traces[0].DependsOn) != 0 || len(out.Record.Traces[1].DependsOn) != 1 {
				t.Fatal("fixed dependencies", out.Record.Traces)
			}
		}
		attempts, e := f.store.JobAttempts(context.Background(), a.JobID)
		if e != nil || len(attempts) != out.Record.ModelCalls {
			t.Fatal("logical vs physical actual fixture count", len(attempts), e)
		}
		for _, attempt := range attempts {
			if attempt.BudgetRef != f.plans.BudgetRef || attempt.Class != "model" {
				t.Fatal("invented new budget", attempt)
			}
		}
		networkBefore := f.requests.Load()
		replayed, e := f.runner.Run(context.Background(), f.protocol, f.caseData, a)
		if e != nil || !replayed.Record.Replayed || replayed.Record.ReportRef != out.Record.ReportRef || f.requests.Load() != networkBefore {
			t.Fatal("replay spent external request", replayed.Record, e)
		}
		outcomes = append(outcomes, out)
	}
	records := []RunRecord{outcomes[0].Record, outcomes[1].Record, outcomes[2].Record}
	summary, e := Aggregate(f.protocol, []FrozenCase{f.caseData}, records)
	if e != nil || summary.Total.Total != 3 || summary.Total.TerminalStates["completed"] != 3 || summary.Total.ModelHTTPAttempts != 4 || summary.Total.UnknownUsageAttempts != 3 {
		t.Fatal(summary, e)
	}
	if _, e = Aggregate(f.protocol, []FrozenCase{f.caseData}, records[:2]); !errors.Is(e, ErrScoring) {
		t.Fatal("incomplete matrix accepted")
	}
	raw, e := os.ReadFile(f.journalPath)
	if e != nil || strings.Contains(string(raw), "PRIVATE_EVAL_REASONING") || strings.Contains(string(raw), "PRIVATE_LABEL_SENTINEL") || strings.Contains(string(raw), "synthetic-evaluation-key") || strings.Contains(string(raw), "127.0.0.1") || strings.Contains(string(raw), "模型协议fixture") {
		t.Fatal("journal leaked protected content", e)
	}
	usage, e := f.store.BudgetUsage(context.Background(), f.plans.BudgetRef, "", "")
	if e != nil || usage.ModelAttempts != 4 {
		t.Fatal("not a shared ledger", usage, e)
	}
}

func TestFailuresAndPrivateCapsRemainInRunPopulation(t *testing.T) {
	for _, behavior := range []string{"http-error", "invalid-output", "private-zero", "fixed-selection"} {
		t.Run(behavior, func(t *testing.T) {
			var ceiling *workload.AttemptCeiling
			if behavior == "private-zero" {
				zero := int64(0)
				ceiling = &workload.AttemptCeiling{ApplicationAttempts: &zero}
			}
			f := newEvaluationFixture(t, behavior, ceiling)
			if behavior == "fixed-selection" {
				f.protocol.FixedPipeline.Steps[1].MaxItems = 1
			}
			a := f.accepted(t, FixedModel, "failure-run")
			out, e := f.runner.Run(context.Background(), f.protocol, f.caseData, a)
			if e != nil || out.Record.Status != "partial" || !out.Record.ModelOutputAbsent || out.Record.ModelLayer != "not_produced" || out.Record.RuleAuditRef == "" || out.Record.ErrorCode == "" {
				t.Fatal(out.Record, e)
			}
			if behavior == "private-zero" && (f.requests.Load() != 0 || out.Record.Usage.Attempts.ModelAttempts != 0) {
				t.Fatal("private zero expanded", out.Record)
			}
			if behavior == "fixed-selection" && (f.requests.Load() != 0 || out.Record.ModelCalls != 0 || out.Record.ToolCalls != 1 || len(out.Record.Traces) != 1) {
				t.Fatal("prefetch failure fabricated model call", out.Record)
			}
			if behavior == "http-error" && out.Record.Usage.Attempts.ModelAttempts != 1 {
				t.Fatal("failed HTTP disappeared", out.Record)
			}
			score, e := ScoreModel(out, nil)
			if e != nil || score.Status != "not_produced" {
				t.Fatal(score, e)
			}
		})
	}
}

type rejectedJournal struct{}

func (rejectedJournal) Begin(context.Context, RunStart) (*RunRecord, bool, error) {
	return nil, false, ErrJournal
}
func (rejectedJournal) End(context.Context, RunRecord) error { return ErrJournal }
func TestAdmissionMismatchAndJournalFailureMakeNoRequest(t *testing.T) {
	for _, change := range []string{"sampling", "input", "workload", "journal", "runtime"} {
		t.Run(change, func(t *testing.T) {
			f := newEvaluationFixture(t, "success", nil)
			a := f.accepted(t, FixedModel, "unadmitted-run")
			switch change {
			case "sampling":
				f.protocol.Sampling.Temperature = 0.5
			case "input":
				f.caseData.input.Assets[0].Name = "changed"
			case "workload":
				f.protocol.Workload.Limits.ToolCalls--
			case "journal":
				f.runner.binding.Journal = rejectedJournal{}
			case "runtime":
				f.runner.binding.RuntimeRevision = "other-revision"
			}
			_, e := f.runner.Run(context.Background(), f.protocol, f.caseData, a)
			if e == nil || f.requests.Load() != 0 {
				t.Fatal("invalid admission spent request", e)
			}
			usage, e := f.store.BudgetUsage(context.Background(), f.plans.BudgetRef, "", a.JobID)
			if e != nil || usage.ModelAttempts != 0 {
				t.Fatal("invalid admission spent ledger", usage, e)
			}
		})
	}
}

func TestRulesRunNeedsNoFactoryAndZeroFinalizeUsesFiniteRoot(t *testing.T) {
	f := newEvaluationFixture(t, "success", nil)
	ctx := context.Background()
	conversation, e := f.store.CreateConversation(ctx, "共享根截止时间测试", f.snapshot.ScopeRef)
	if e != nil {
		t.Fatal(e)
	}
	preview, e := f.plans.Preview(ctx, app.PlanRequest{Kind: "audit", ConversationID: conversation.ID, DataSourceID: "local-offline", PolicyScopeRef: f.snapshot.ScopeRef, Selection: app.PlanSelection{Kind: "assets", AssetIDs: f.snapshot.Input.Selection.AssetIDs}, DataPolicy: app.DataPolicy{Mode: "pinned_snapshot", SnapshotID: f.snapshot.ID}, BaselineVersionID: "B2-v1", Mode: "rules_only"})
	if e != nil {
		t.Fatal(e)
	}
	plan := preview.ExecutionPlan
	plan.ID = ""
	plan.Workload.Limits.FinalizeSeconds = 0
	plan, e = f.store.SavePlan(ctx, plan)
	if e != nil {
		t.Fatal(e)
	}
	_, _, e = f.store.EnqueuePlan(ctx, plan.ID, "zero-finalize", f.plans.ConfigRevision)
	if e != nil {
		t.Fatal(e)
	}
	job, found, e := f.store.Claim(ctx)
	if e != nil || !found {
		t.Fatal(found, e)
	}
	f.protocol.Workload = plan.Workload
	f.caseData, e = FreezeCase(ctx, f.caseData.Definition, f.snapshot.Input, plan.Workload)
	if e != nil {
		t.Fatal(e)
	}
	f.runner.binding.Factory = nil
	out, e := f.runner.Run(ctx, f.protocol, f.caseData, Assignment{RunID: "zero-finalize", CaseID: f.caseData.Definition.ID, Arm: RulesOnly, Repetition: 1, JobID: job.ID})
	if e != nil || out.Record.Status != "completed" || out.Record.ReportRef == "" || out.Record.ModelCalls != 0 || f.requests.Load() != 0 {
		t.Fatal(out.Record, e)
	}
}
