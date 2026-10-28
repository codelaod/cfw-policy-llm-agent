package app

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"cfw-policy-llm-agent/server/internal/agentquery"
	"cfw-policy-llm-agent/server/internal/audit"
	"cfw-policy-llm-agent/server/internal/report"
	"cfw-policy-llm-agent/server/internal/storage"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type appFixtureModel struct {
	generate func(context.Context, []*schema.Message) (*schema.Message, error)
}

func (m *appFixtureModel) Generate(ctx context.Context, in []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	return m.generate(ctx, in)
}
func (m *appFixtureModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	panic("nonstream only")
}

func appAgentFactory(invalid bool) AgentModelFactoryFunc {
	return func(context.Context, string) (model.BaseChatModel, error) {
		calls := 0
		return &appFixtureModel{generate: func(ctx context.Context, messages []*schema.Message) (*schema.Message, error) {
			calls++
			var initial agentquery.InitialView
			for _, message := range messages {
				if message.Role == schema.User && strings.HasPrefix(message.Content, "固定公开任务资料") {
					if i := strings.IndexByte(message.Content, '{'); i >= 0 {
						json.Unmarshal([]byte(message.Content[i:]), &initial)
					}
				}
			}
			if calls == 1 {
				args, _ := json.Marshal(map[string]string{"asset_ref": initial.Assets[0]})
				return &schema.Message{Role: schema.Assistant, ReasoningContent: "PRIVATE_TRANSIENT_REASONING", ToolCalls: []schema.ToolCall{{ID: "fixture-asset-call", Type: "function", Function: schema.FunctionCall{Name: "get_asset_context", Arguments: string(args)}}}, ResponseMeta: &schema.ResponseMeta{FinishReason: "tool_calls"}}, nil
			}
			var refs []string
			for _, message := range messages {
				if message.Role == schema.Tool {
					var result agentquery.ToolResult
					json.Unmarshal([]byte(message.Content), &result)
					refs = result.EvidenceRefs
				}
			}
			output := agentquery.Output{ScopeRef: initial.ScopeRef, RuleVersion: initial.RuleVersion, SourceKind: initial.SourceKind, AssessmentBasis: initial.AssessmentBasis, Explanation: "模型补充解释；业务资料仍需人工确认。", Roles: []agentquery.RoleCandidate{}, Recommendations: []agentquery.Recommendation{{Kind: "confirm_information", TargetKind: "asset", TargetRef: initial.Assets[0], BasisKind: "unknown_followup", EvidenceRefs: refs, Action: "confirm_intent", Conditions: []string{"需操作者核对业务资料"}, Impact: "业务资料影响判据", ManualNextStep: "在资产页面创建并预览业务草稿"}}, Questions: []string{}}
			if invalid {
				output.SourceKind = "live_readonly"
			}
			data, _ := json.Marshal(output)
			return &schema.Message{Role: schema.Assistant, Content: string(data), ResponseMeta: &schema.ResponseMeta{FinishReason: "stop"}}, nil
		}}, nil
	}
}

func acceptedAgentAudit(t *testing.T) (*storage.Store, *PlanService, storage.Job) {
	t.Helper()
	store, plans, _, _, request := planCase(t)
	plans.ModelReady = true
	request.Mode = "rules_and_agent"
	preview, err := plans.Preview(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	job, _, err := store.EnqueuePlan(context.Background(), preview.ID, "agent-audit", plans.ConfigRevision)
	if err != nil {
		t.Fatal(err)
	}
	job, found, err := store.Claim(context.Background())
	if err != nil || !found {
		t.Fatal(err)
	}
	return store, plans, job
}

func TestE3WorkflowPersistsAgentSupplementWithoutOverwritingRuleFacts(t *testing.T) {
	store, plans, job := acceptedAgentAudit(t)
	workflow := NewWorkflow(store)
	workflow.Agent = NewAgentService(store, appAgentFactory(false), plans.ConfigRevision, nil)
	if err := workflow.Run(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	current, run, err := store.ReadJobAudit(context.Background(), job.ID)
	if err != nil || current.Status != "completed" || run == nil {
		t.Fatal("agent audit did not complete", err, current.Status)
	}
	computed, err := audit.Run(context.Background(), run.Input)
	if err != nil || !reflect.DeepEqual(computed, run.Result) {
		t.Fatal("model changed raw deterministic rule facts", err)
	}
	agent, err := store.GetJobAgentResult(context.Background(), job.ID)
	if err != nil || agent == nil || agent.Content.Output == nil || agent.Content.ModelCalls != 2 || agent.Content.ToolCalls != 1 {
		t.Fatal("bounded agent work was not saved", err)
	}
	encoded, _ := json.Marshal(agent)
	if strings.Contains(string(encoded), "PRIVATE_TRANSIENT_REASONING") {
		t.Fatal("internal reasoning entered persistence")
	}
	page, err := store.ConversationMessages(context.Background(), job.ConversationID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	last := page.Items[len(page.Items)-1]
	if last.Role != "assistant" || last.Origin != "model" || last.PublicText != agent.Content.Output.Explanation {
		t.Fatal("validated model answer was not published with its report")
	}
	reports, err := store.ListReports(context.Background(), run.ID, 100)
	if err != nil || len(reports) != 1 {
		t.Fatal("fixed report missing", err)
	}
	var document report.Document
	if json.Unmarshal(reports[0].Content, &document) != nil || document.Agent == nil || document.Agent.ModelStage != "completed" || len(document.Agent.Recommendations) != 1 {
		t.Fatal("report omitted separately projected Agent supplement")
	}
	if strings.Contains(string(reports[0].Content), agent.Content.Output.Explanation) || strings.Contains(string(reports[0].Content), agent.Content.Output.ScopeRef) {
		t.Fatal("public report copied private model prose or task nonce")
	}
	manual, err := NewAuditService(store).Report(context.Background(), run.ID, "markdown", "manual-agent-report")
	if err != nil || !strings.Contains(string(manual.Content), "Agent") {
		t.Fatal("manual export lost saved Agent stage", err)
	}
}

func TestE3InvalidAgentOutputKeepsL2AndProducesPartialReport(t *testing.T) {
	store, plans, job := acceptedAgentAudit(t)
	workflow := NewWorkflow(store)
	workflow.Agent = NewAgentService(store, appAgentFactory(true), plans.ConfigRevision, nil)
	if err := workflow.Run(context.Background(), job); err == nil {
		t.Fatal("invalid model source upgrade was accepted")
	}
	current, run, err := store.ReadJobAudit(context.Background(), job.ID)
	if err != nil || current.Status != "partial" || run == nil {
		t.Fatal("agent failure discarded L2", err, current.Status)
	}
	agent, err := store.GetJobAgentResult(context.Background(), job.ID)
	if err != nil || agent == nil || agent.Content.Output != nil || agent.Content.ErrorCode == "" {
		t.Fatal("failed agent state was not preserved", err)
	}
	reports, err := store.ListReports(context.Background(), run.ID, 100)
	if err != nil || len(reports) != 1 {
		t.Fatal("partial result has no fixed report", err)
	}
}

func TestE3AssistantAtomicResultMessageAndTerminalUseFrozenInput(t *testing.T) {
	store, plans, snapshot, conversation, _ := planCase(t)
	plans.ModelReady = true
	engine := New(store, nil)
	defer engine.Stop()
	job, _, _, err := plans.StartAssistant(context.Background(), engine, conversation.ID, AssistantRequest{ClientMessageID: "agent-assistant", PolicyScopeRef: snapshot.ScopeRef, SnapshotID: snapshot.ID, Text: "解释当前固定资料", ModelUseAccepted: true})
	if err != nil {
		t.Fatal(err)
	}
	job, found, err := store.Claim(context.Background())
	if err != nil || !found {
		t.Fatal(err)
	}
	workflow := NewWorkflow(store)
	workflow.Agent = NewAgentService(store, appAgentFactory(false), plans.ConfigRevision, nil)
	if err = workflow.Run(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	current, err := store.Get(context.Background(), job.ID)
	if err != nil || current.Status != "completed" {
		t.Fatal("assistant task did not finalize", err)
	}
	agent, err := store.GetJobAgentResult(context.Background(), job.ID)
	if err != nil || agent == nil || agent.Content.Output == nil || agent.Content.Manifest.PolicyScope.Ref != snapshot.ScopeRef {
		t.Fatal("assistant lost fixed context", err)
	}
	page, err := store.ConversationMessages(context.Background(), conversation.ID, 0, 100)
	if err != nil || len(page.Items) != 2 || page.Items[1].Origin != "model" {
		t.Fatal("assistant answer/message/terminal were not atomically visible", err)
	}
}

func TestE3AgentServiceRetainsCancellationBeforePreparation(t *testing.T) {
	store, plans, job := acceptedAgentAudit(t)
	service := NewAgentService(store, appAgentFactory(false), plans.ConfigRevision, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := service.Run(ctx, job); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled preparation became a different failure: %v", err)
	}
}
