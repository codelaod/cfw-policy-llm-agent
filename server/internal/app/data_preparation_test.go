package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"cfw-policy-llm-agent/server/internal/aliyuncollector"
	"cfw-policy-llm-agent/server/internal/audit"
	"cfw-policy-llm-agent/server/internal/domain"
	"cfw-policy-llm-agent/server/internal/report"
	"cfw-policy-llm-agent/server/internal/storage"
	"cfw-policy-llm-agent/server/internal/workload"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func preparationIntegration(t *testing.T) (*IntentService, *CloudService, storage.Conversation, *atomic.Int32) {
	t.Helper()
	physical := &atomic.Int32{}
	st, plans, cloud, _ := cloudIntegration(t, "internet", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		physical.Add(1)
		_ = r.ParseForm()
		action := r.Header.Get("x-acs-action")
		if action == "" {
			action = r.Form.Get("Action")
		}
		body := cloudProtocolFixture(action)
		if action == "DescribeInstances" {
			body = `{"NextToken":"","TotalCount":1,"Instances":{"Instance":[{"InstanceId":"i-runtime-synthetic","InstanceName":"new-web","RegionId":"cn-hangzhou","OSType":"linux","Tags":{"Tag":[{"TagKey":"app","TagValue":"example"}]},"SecurityGroupIds":{"SecurityGroupId":["sg-synthetic"]}}]}}`
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}))
	var base aliyuncollector.Config
	for _, cfg := range cloud.Scopes {
		base = cfg
	}
	cloud.Grant = storage.ConnectionGrant{SourceRef: cloud.SourceRef, ConfigRevision: plans.ConfigRevision, AccountRef: base.Scope.AccountRef, ControlPlaneRef: base.Scope.ControlPlaneRef, Regions: []string{"cn-hangzhou"}, Boundaries: []string{"internet/in"}}
	cloud.DiscoveryConfig = base
	cloud.DiscoveryConfig.SourceRef = cloud.SourceRef
	cloud.DiscoveryConfig.AllowedRegions = []string{"cn-hangzhou"}
	cloud.ResolveScope = func(boundary, direction string, rs []domain.Resource) (aliyuncollector.Config, error) {
		if boundary != "internet" || direction != "in" {
			return aliyuncollector.Config{}, ErrScopeDenied
		}
		cfg := base
		cfg.AllowedRegions = []string{"cn-hangzhou"}
		cfg.AssetRegions = map[string]string{}
		for _, r := range rs {
			if r.ResourceID != "i-runtime-synthetic" || r.Region != "cn-hangzhou" {
				return cfg, ErrScopeDenied
			}
			cfg.AssetRegions[r.ResourceID] = r.Region
		}
		return cfg, nil
	}
	if e := st.RegisterConnectionGrant(context.Background(), cloud.Grant); e != nil {
		t.Fatal(e)
	}
	conv, e := st.CreateConversation(context.Background(), "从自然目标自动准备资料", "")
	if e != nil {
		t.Fatal(e)
	}
	return &IntentService{Store: st, Plans: plans}, cloud, conv, physical
}
func executeGoal(t *testing.T, s *IntentService, cloud *CloudService, c storage.Conversation, key, text string) (storage.Job, storage.ExecutionPlan) {
	t.Helper()
	turn := goalTurn(t, s, c.ID, key, text)
	if turn.Proposal == nil || turn.Proposal.PlanID == "" {
		t.Fatalf("no accepted preparation proposal: %#v", turn.Intent)
	}
	p, e := s.Store.GetPlan(context.Background(), turn.Proposal.PlanID)
	if e != nil {
		t.Fatal(e)
	}
	accepted, e := s.Store.AcceptIntentProposal(context.Background(), c.ID, turn.Proposal.ID, key+"-accept", s.Plans.ConfigRevision, turn.Intent.Revision)
	if e != nil {
		t.Fatal(e)
	}
	job, found, e := s.Store.Claim(context.Background())
	if e != nil || !found || accepted.Job.ID != job.ID {
		t.Fatal(e)
	}
	w := NewWorkflow(s.Store)
	w.Cloud = cloud
	if e = w.Run(context.Background(), job); e != nil {
		t.Fatal(e)
	}
	return job, p
}

func TestCustomerAuditPreparesDataInSameAcceptedJobThenReusesWithoutHTTP(t *testing.T) {
	s, cloud, c, physical := preparationIntegration(t)
	turn := goalTurn(t, s, c.ID, "preview", "审计杭州名为 new-web 的ECS入向策略")
	if physical.Load() != 0 || turn.Proposal == nil || !turn.Proposal.RequiresDiscovery || !turn.Proposal.Effects.AllowCloudReads {
		t.Fatal("preview made requests or did not prepare", turn.Intent, physical.Load())
	}
	// Repeating the original customer turn remains local and idempotent.
	replay, e := s.Turn(context.Background(), c.ID, "preview", IntentTurnRequest{ClientTurnID: "preview", Text: "审计杭州名为 new-web 的ECS入向策略"})
	if e != nil || !replay.Replayed || replay.Proposal.PlanID != turn.Proposal.PlanID {
		t.Fatal(e)
	}
	accept, e := s.Store.AcceptIntentProposal(context.Background(), c.ID, turn.Proposal.ID, "accept", s.Plans.ConfigRevision, turn.Intent.Revision)
	if e != nil {
		t.Fatal(e)
	}
	job, found, e := s.Store.Claim(context.Background())
	if e != nil || !found || job.ID != accept.Job.ID {
		t.Fatal(e)
	}
	original, e := s.Store.GetPlan(context.Background(), job.PlanID)
	if e != nil {
		t.Fatal(e)
	}
	w := NewWorkflow(s.Store)
	w.Cloud = cloud
	if e = w.Run(context.Background(), job); e != nil {
		t.Fatal(e)
	}
	view, e := s.Store.ReadJobWorkspace(context.Background(), job.ID)
	if e != nil || view.Job.Status != "completed" || view.CloudResult == nil || view.CloudResult.Content.Kind != "audit" || view.Audit == nil || !view.InputSealed {
		t.Fatal("same job did not finish data and rules", view.Job, e)
	}
	after, e := s.Store.GetPlan(context.Background(), job.PlanID)
	if e != nil || !reflect.DeepEqual(original, after) || original.SnapshotID != "" {
		t.Fatal("original plan mutated", e)
	}
	if view.Audit.Input.Assets[0].ProviderResourceID != "i-runtime-synthetic" || view.Audit.Input.Assets[0].Name != "new-web" || len(view.Audit.Input.Assets[0].Services) != 0 {
		t.Fatal("metadata identity lost or business invented")
	}
	jobs, e := s.Store.ListJobs(context.Background(), c.ID, 0, 100)
	if e != nil || len(jobs) != 1 {
		t.Fatal("child collection was enqueued", len(jobs), e)
	}
	firstRequests := physical.Load()
	if firstRequests < 2 {
		t.Fatal("no actual loopback provider chain")
	}
	// Directory is now source-qualified; current prefer_fresh reuses only a
	// terminal producer and the original evidence window remains unchanged.
	warm, e := s.Store.CreateConversation(context.Background(), "合格复用", "")
	if e != nil {
		t.Fatal(e)
	}
	warmJob, _ := executeGoal(t, s, cloud, warm, "warm", "检查杭州 ECS i-runtime-synthetic 入向")
	if physical.Load() != firstRequests {
		t.Fatalf("qualified reuse made %d extra sends", physical.Load()-firstRequests)
	}
	reuseInput, e := s.Store.GetJobInput(context.Background(), warmJob.ID)
	if e != nil || reuseInput.CollectedAt != view.Audit.Input.CollectedAt || reuseInput.SnapshotVersion != view.Audit.Input.SnapshotVersion {
		t.Fatal("reuse retimestamped original data", e)
	}
	usage, e := s.Store.BudgetUsage(context.Background(), s.Plans.BudgetRef, "", warmJob.ID)
	if e != nil || usage.CloudAttempts != 0 || usage.ModelAttempts != 0 {
		t.Fatal("reuse consumed external budget", usage, e)
	}
}

func TestCustomerForceRefreshDoesNotReuseAndKeepsRuleReportAfterModelUnavailable(t *testing.T) {
	s, cloud, c, physical := preparationIntegration(t)
	_, _ = executeGoal(t, s, cloud, c, "initial", "检查杭州名为 new-web 的ECS")
	old := physical.Load()
	next, e := s.Store.CreateConversation(context.Background(), "人工改后强刷", "")
	if e != nil {
		t.Fatal(e)
	}
	turn, e := s.Turn(context.Background(), next.ID, "force", IntentTurnRequest{ClientTurnID: "force", Text: "已手动整改，重新采集杭州名为 new-web 的ECS入向，使用模型"})
	if e != nil || turn.Proposal == nil {
		t.Fatal(e)
	}
	accepted, e := s.Store.AcceptIntentProposal(context.Background(), next.ID, turn.Proposal.ID, "force-accept", s.Plans.ConfigRevision, turn.Intent.Revision)
	if e != nil {
		t.Fatal(e)
	}
	job, found, e := s.Store.Claim(context.Background())
	if e != nil || !found || job.ID != accepted.Job.ID {
		t.Fatal(e)
	}
	w := NewWorkflow(s.Store)
	w.Cloud = cloud
	e = w.Run(context.Background(), job)
	var failure JobFailure
	if !errors.As(e, &failure) || !failure.Partial || physical.Load() <= old {
		t.Fatal("force refresh or partial model behavior lost", e)
	}
	view, e := s.Store.ReadJobWorkspace(context.Background(), job.ID)
	if e != nil || view.Job.Status != "partial" || view.Audit == nil || !view.ReportPublished {
		t.Fatal("model unavailable discarded rules/report", e, view.Job.Status)
	}
}

func TestWorkflowUsesConfiguredRulesAndFinalizationWindows(t *testing.T) {
	st, plans, _, _, req := planCase(t)
	preview, e := plans.Preview(context.Background(), req)
	if e != nil {
		t.Fatal(e)
	}
	p := preview.ExecutionPlan
	p.ID = ""
	p.ConsumedJobID = ""
	limits := workload.Default("audit")
	limits.ExecutionSeconds = 600
	limits.CollectionSeconds = 180
	limits.RulesSeconds = 45
	limits.ModelSeconds = 300
	limits.FinalizeSeconds = 25
	p.Workload = workload.Resolved{ProfileRef: "configured-workflow", Limits: limits}
	saved, e := st.SavePlan(context.Background(), p)
	if e != nil {
		t.Fatal(e)
	}
	if _, _, e = st.EnqueuePlan(context.Background(), saved.ID, "configured-windows", plans.ConfigRevision); e != nil {
		t.Fatal(e)
	}
	job, found, e := st.Claim(context.Background())
	if e != nil || !found {
		t.Fatal(e)
	}
	w := NewWorkflow(st)
	called := false
	w.rules = func(ctx context.Context, in domain.Input) (domain.Result, error) {
		called = true
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) < 40*time.Second || time.Until(deadline) > 45*time.Second {
			t.Fatal("rules retain hidden 20-second ceiling")
		}
		return audit.Run(ctx, in)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 600*time.Second)
	defer cancel()
	if e = w.Run(ctx, job); e != nil || !called {
		t.Fatal(e)
	}
	view, e := st.ReadJobWorkspace(ctx, job.ID)
	if e != nil {
		t.Fatal(e)
	}
	reports, e := st.ListReports(ctx, view.Audit.ID, 10)
	if e != nil || len(reports) != 1 {
		t.Fatal(e)
	}
	var document report.Document
	if e = json.Unmarshal(reports[0].Content, &document); e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(string(reports[0].Content), "configured-workflow") {
		t.Fatal("report omitted executed protection profile")
	}
	root, cancelRoot := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelRoot()
	phase, cancelPhase := reserveFinalization(root, 25*time.Second)
	defer cancelPhase()
	d, _ := phase.Deadline()
	if time.Until(d) > 5*time.Second || time.Until(d) < 4*time.Second {
		t.Fatal("finalization reserve extended or erased root")
	}
}

func TestNewSingularGoalRevokesOldMultipleSelectorAndMissingIDsKeepWholeSet(t *testing.T) {
	s, cloud, c, physical := preparationIntegration(t)
	all := goalTurn(t, s, c.ID, "many", "审计杭州所有名为 old-web 的ECS")
	if all.Proposal == nil {
		t.Fatal(all.Intent)
	}
	one := goalTurn(t, s, c.ID, "one", "改查这台杭州名为 new-web 的ECS")
	p, e := s.Store.GetPlan(context.Background(), one.Proposal.PlanID)
	if e != nil || p.Preparation.AllowMultiple || p.Preparation.ResultLimit != 1 {
		t.Fatal("singular target inherited broad set", p.Preparation, e)
	}
	ids := goalTurn(t, s, c.ID, "ids", "改查杭州 ECS i-runtime-synthetic 和 i-missing-two")
	if ids.Proposal == nil || ids.Proposal.PlanID == "" {
		t.Fatal("missing IDs left no finite preparation", ids.Intent)
	}
	p, e = s.Store.GetPlan(context.Background(), ids.Proposal.PlanID)
	if e != nil || len(queryIDs(p.Preparation.Query)) != 2 || len(p.Preparation.Selected) != 0 || !p.Preparation.AllowMultiple {
		t.Fatal("missing IDs silently reduced to known subset", p.Preparation, e)
	}
	if physical.Load() != 0 {
		t.Fatal("goal edits made external requests")
	}
	_ = cloud
}

func TestExplicitDataPolicyCannotBeChangedByGenericCurrentTimeWord(t *testing.T) {
	s, _, c, physical := preparationIntegration(t)
	r, e := s.Turn(context.Background(), c.ID, "typed-force", IntentTurnRequest{ClientTurnID: "typed-force", Text: "现在审计杭州名为 new-web 的ECS", DataPolicy: &domain.IntentDataPolicy{Mode: "force_refresh"}})
	if e != nil || r.Proposal == nil {
		t.Fatal(r, e)
	}
	p, e := s.Store.GetPlan(context.Background(), r.Proposal.PlanID)
	if e != nil || p.Preparation.DataPolicy != "force_refresh" || p.Preparation.FreshAfter == "" {
		t.Fatal("generic now erased explicit refresh", p.Preparation, e)
	}
	conflict, e := s.Turn(context.Background(), c.ID, "conflict-policy", IntentTurnRequest{ClientTurnID: "conflict-policy", Text: "复用同一台的资料", DataPolicy: &domain.IntentDataPolicy{Mode: "force_refresh"}})
	if e != nil || conflict.Intent.Status != "needs_clarification" || conflict.Proposal != nil {
		t.Fatal("conflicting explicit data modes executed", conflict, e)
	}
	if physical.Load() != 0 {
		t.Fatal("data policy parsing made external requests")
	}
}

func TestExplicitNamesDoNotBecomeRawIDsOrRegionsAndSecretsAreRemoved(t *testing.T) {
	s, _, c, _ := preparationIntegration(t)
	for i, name := range []string{"i-display-name", "nat-prod", "cn-beijing-example"} {
		key := fmt.Sprintf("name-%d", i)
		r := goalTurn(t, s, c.ID, key, "检查杭州名为 "+name+" 的ECS")
		if r.Proposal == nil {
			t.Fatal(r.Intent)
		}
		p, e := s.Store.GetPlan(context.Background(), r.Proposal.PlanID)
		if e != nil || p.Preparation.Query.Name != name || p.Preparation.Query.ResourceID != "" || p.Preparation.Query.Region != "cn-hangzhou" || p.Preparation.Query.ResourceType != "ecs" {
			t.Fatal("display name changed locator identity", p.Preparation, e)
		}
	}
	s.SensitiveValues = []string{"SYNTHETIC_KNOWN_SECRET_VALUE"}
	r := goalTurn(t, s, c.ID, "mixed-secret", "SYNTHETIC_KNOWN_SECRET_VALUE；sk-UNKNOWN_ARTIFICIAL_123456，检查杭州名为 new-web 的ECS")
	if r.Proposal == nil || r.Message == nil {
		t.Fatal("mixed safe target was discarded", r.Intent)
	}
	messages, e := s.Store.ConversationMessages(context.Background(), c.ID, 0, 100)
	if e != nil {
		t.Fatal(e)
	}
	for _, m := range messages.Items {
		if strings.Contains(m.PublicText, "SYNTHETIC_KNOWN_SECRET_VALUE") || strings.Contains(m.PublicText, "sk-UNKNOWN_ARTIFICIAL_123456") {
			t.Fatal("secret persisted in public conversation")
		}
	}
}

func TestGuidanceFollowsNewReadyGoalInsteadOfOldTerminalJob(t *testing.T) {
	s, cloud, c, _ := preparationIntegration(t)
	_, _ = executeGoal(t, s, cloud, c, "old-complete", "检查杭州名为 new-web 的ECS")
	current := goalTurn(t, s, c.ID, "new-goal", "改查杭州名为 next-web 的ECS")
	if current.Proposal == nil {
		t.Fatal(current.Intent)
	}
	g, e := s.Guidance(context.Background(), c.ID, "", "", "conversation")
	if e != nil || g.State != "ready_for_confirmation" {
		t.Fatal("old job displaced new plan", g, e)
	}
	item := guidanceItem(t, g, "GDE-PREVIEW")
	if item.Action.Target.ID != current.Proposal.PlanID {
		t.Fatal("guidance points at old plan")
	}
}

func TestConfiguredSevenModelTurnsPersistAndExportWithoutLegacySixCap(t *testing.T) {
	st, plans, _, _, req := planCase(t)
	plans.Workloads = explicitWorkloads(t)
	plans.ModelReady = true
	req.Mode = "rules_and_agent"
	preview, e := plans.Preview(context.Background(), req)
	if e != nil {
		t.Fatal(e)
	}
	if _, _, e = st.EnqueuePlan(context.Background(), preview.ID, "seven-model-turns", plans.ConfigRevision); e != nil {
		t.Fatal(e)
	}
	job, found, e := st.Claim(context.Background())
	if e != nil || !found {
		t.Fatal(e)
	}
	factory := AgentModelFactoryFunc(func(ctx context.Context, id string) (model.BaseChatModel, error) {
		base, e := appAgentFactory(false).NewModel(ctx, id)
		if e != nil {
			return nil, e
		}
		calls := 0
		var original schema.ToolCall
		return &appFixtureModel{generate: func(ctx context.Context, messages []*schema.Message) (*schema.Message, error) {
			calls++
			if calls == 1 {
				out, e := base.Generate(ctx, messages)
				if e == nil {
					original = out.ToolCalls[0]
				}
				return out, e
			}
			if calls <= 6 {
				call := original
				call.ID = fmt.Sprintf("extended-tool-%d", calls)
				return &schema.Message{Role: schema.Assistant, ToolCalls: []schema.ToolCall{call}, ResponseMeta: &schema.ResponseMeta{FinishReason: "tool_calls"}}, nil
			}
			return base.Generate(ctx, messages)
		}}, nil
	})
	w := NewWorkflow(st)
	w.Agent = NewAgentService(st, factory, plans.ConfigRevision, nil)
	if e = w.Run(context.Background(), job); e != nil {
		t.Fatal("larger profile failed downstream storage/report", e)
	}
	saved, e := st.GetJobAgentResult(context.Background(), job.ID)
	if e != nil || saved == nil || saved.Content.ModelCalls != 7 || saved.Content.ToolCalls != 6 {
		t.Fatal("logical calls were not persisted", e)
	}
	view, e := st.ReadJobWorkspace(context.Background(), job.ID)
	if e != nil || view.Job.Status != "completed" || !view.ReportPublished {
		t.Fatal("larger model profile lost report", e)
	}
	exported, e := NewAuditService(st).Report(context.Background(), view.Audit.ID, "markdown", "seven-export")
	if e != nil || len(exported.Content) == 0 {
		t.Fatal("manual report restored legacy six cap", e)
	}
}

func TestHistoryExplanationUsesOriginalReportAndAcceptedModelPlanHasZeroCloud(t *testing.T) {
	s, c := conversationalCase(t)
	initial := goalTurn(t, s, c.ID, "audit", "检查杭州 web-prod ECS")
	accepted, e := s.Store.AcceptIntentProposal(context.Background(), c.ID, initial.Proposal.ID, "audit-accept", s.Plans.ConfigRevision, initial.Intent.Revision)
	if e != nil {
		t.Fatal(e)
	}
	job, found, e := s.Store.Claim(context.Background())
	if e != nil || !found || job.ID != accepted.Job.ID {
		t.Fatal(e)
	}
	if e = NewWorkflow(s.Store).Run(context.Background(), job); e != nil {
		t.Fatal(e)
	}
	view, e := s.Store.ReadJobWorkspace(context.Background(), job.ID)
	if e != nil {
		t.Fatal(e)
	}
	oldRevision := initial.Intent.Revision
	local := goalTurn(t, s, c.ID, "explain-local", "解释原报告的依据、影响和还缺什么业务信息")
	if local.Proposal == nil || local.Intent.Revision != oldRevision || !strings.Contains(local.Reply.PublicText, view.ReportRef) || !strings.Contains(local.Reply.PublicText, view.Audit.Input.CollectedAt) {
		t.Fatal("history did not retain original evidence and goal", local.Intent)
	}
	jobs, e := s.Store.ListJobs(context.Background(), c.ID, 0, 100)
	if e != nil || len(jobs) != 1 {
		t.Fatal("local explanation created work")
	}
	s.Plans.ModelReady = true
	modelTurn, e := s.Turn(context.Background(), c.ID, "explain-model", IntentTurnRequest{ClientTurnID: "explain-model", Text: "使用模型解释这份旧报告的依据", Mode: "rules_and_agent", ContextReportID: view.ReportRef})
	if e != nil || modelTurn.Proposal == nil {
		t.Fatal(e)
	}
	p, e := s.Store.GetPlan(context.Background(), modelTurn.Proposal.PlanID)
	if e != nil || p.Kind != "assistant_turn" || p.ContextReportID != view.ReportRef || p.EffectCeiling.AllowCloudReads || !p.EffectCeiling.AllowModelRequests || p.PreparedInput.EvaluationAt != view.Audit.Input.EvaluationAt {
		t.Fatal("history plan changed original input or effects", p, e)
	}
	_, e = s.Store.AcceptIntentProposal(context.Background(), c.ID, modelTurn.Proposal.ID, "explain-model-accept", s.Plans.ConfigRevision, modelTurn.Intent.Revision)
	if e != nil {
		t.Fatal(e)
	}
	modelJob, found, e := s.Store.Claim(context.Background())
	if e != nil || !found {
		t.Fatal(e)
	}
	w := NewWorkflow(s.Store)
	w.Agent = NewAgentService(s.Store, appAgentFactory(false), s.Plans.ConfigRevision, nil)
	if e = w.Run(context.Background(), modelJob); e != nil {
		t.Fatal(e)
	}
	modelInput, e := s.Store.GetJobInput(context.Background(), modelJob.ID)
	if e != nil || !reflect.DeepEqual(modelInput, view.Audit.Input) {
		t.Fatal("old report explanation re-froze current business heads", e)
	}
	unchanged, e := s.Store.GetAudit(context.Background(), view.Audit.ID)
	if e != nil || !reflect.DeepEqual(unchanged.Result, view.Audit.Result) {
		t.Fatal("history modified rule conclusions", e)
	}
}

func TestManualChangeConversationRefreshLinksNewAuditToOriginalReport(t *testing.T) {
	s, cloud, c, physical := preparationIntegration(t)
	first, _ := executeGoal(t, s, cloud, c, "first-recheck", "检查杭州名为 new-web 的ECS")
	old, e := s.Store.ReadJobWorkspace(context.Background(), first.ID)
	if e != nil {
		t.Fatal(e)
	}
	before := physical.Load()
	next := goalTurn(t, s, c.ID, "changed", "我已人工修改，重新采集这台并复审")
	if next.Proposal == nil {
		t.Fatal(next.Intent)
	}
	p, e := s.Store.GetPlan(context.Background(), next.Proposal.PlanID)
	if e != nil || p.RecheckOfJobID != first.ID || p.Preparation.DataPolicy != "force_refresh" || p.Preparation.FreshAfter == "" {
		t.Fatal("manual change lost original audit or refresh boundary", p, e)
	}
	_, e = s.Store.AcceptIntentProposal(context.Background(), c.ID, next.Proposal.ID, "changed-accept", s.Plans.ConfigRevision, next.Intent.Revision)
	if e != nil {
		t.Fatal(e)
	}
	job, found, e := s.Store.Claim(context.Background())
	if e != nil || !found {
		t.Fatal(e)
	}
	w := NewWorkflow(s.Store)
	w.Cloud = cloud
	if e = w.Run(context.Background(), job); e != nil {
		t.Fatal(e)
	}
	newer, e := s.Store.ReadJobWorkspace(context.Background(), job.ID)
	if e != nil || newer.Recheck == nil || newer.Recheck.OldAuditID != old.Audit.ID || physical.Load() <= before {
		t.Fatal("refresh produced no linked comparison", e)
	}
	historical, e := s.Store.GetAudit(context.Background(), old.Audit.ID)
	if e != nil || !reflect.DeepEqual(historical.Result, old.Audit.Result) {
		t.Fatal("recheck rewrote original conclusions", e)
	}
}

type gatedIdentityReader struct {
	CloudReader
	started chan<- struct{}
	release <-chan struct{}
}

func (r *gatedIdentityReader) VerifyIdentity(ctx context.Context) error {
	select {
	case r.started <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-r.release:
		return r.CloudReader.VerifyIdentity(ctx)
	case <-ctx.Done():
		return ctx.Err()
	}
}
func TestThreeConversationPreparationsRunWithFourthQueuedAndOneSharedLedger(t *testing.T) {
	s, cloud, c, physical := preparationIntegration(t)
	ctx := context.Background()
	started := make(chan struct{}, 4)
	release := make(chan struct{})
	original := cloud.NewReader
	cloud.NewReader = func(ctx context.Context, id string) (CloudReader, error) {
		r, e := original(ctx, id)
		return &gatedIdentityReader{CloudReader: r, started: started, release: release}, e
	}
	w := NewWorkflow(s.Store)
	w.Cloud = cloud
	engine := New(s.Store, w.Run)
	s.Engine = engine
	var once sync.Once
	defer func() { once.Do(func() { close(release) }); engine.Stop() }()
	jobs := []storage.Job{}
	conversations := []storage.Conversation{c}
	for i := 1; i < 4; i++ {
		other, e := s.Store.CreateConversation(ctx, fmt.Sprintf("并发%d", i), "")
		if e != nil {
			t.Fatal(e)
		}
		conversations = append(conversations, other)
	}
	for i, conv := range conversations {
		key := fmt.Sprintf("concurrent-%d", i)
		turn := goalTurn(t, s, conv.ID, key, "审计杭州名为 new-web 的ECS入向")
		accept, e := engine.AcceptIntentProposal(ctx, conv.ID, turn.Proposal.ID, key+"-accept", s.Plans.ConfigRevision, turn.Intent.Revision)
		if e != nil {
			t.Fatal(e)
		}
		jobs = append(jobs, accept.Job)
	}
	engine.Start()
	for i := 0; i < 3; i++ {
		select {
		case <-started:
		case <-time.After(3 * time.Second):
			t.Fatal("three different conversations did not progress")
		}
	}
	for i, j := range jobs {
		current, e := s.Store.Get(ctx, j.ID)
		if e != nil {
			t.Fatal(e)
		}
		want := "running"
		if i == 3 {
			want = "queued"
		}
		if current.Status != want {
			t.Fatalf("job %d status=%s want=%s", i, current.Status, want)
		}
	}
	// A new preview in a running conversation cannot consume a second slot.
	next := goalTurn(t, s, c.ID, "same-session-new", "检查杭州名为 new-web 的ECS")
	if _, e := engine.AcceptIntentProposal(ctx, c.ID, next.Proposal.ID, "same-session-accept", s.Plans.ConfigRevision, next.Intent.Revision); !errors.Is(e, storage.ErrBusy) {
		t.Fatal("one conversation acquired two active jobs", e)
	}
	once.Do(func() { close(release) })
	deadline := time.Now().Add(5 * time.Second)
	for _, j := range jobs {
		for {
			view, e := s.Store.ReadJobWorkspace(ctx, j.ID)
			if e != nil {
				t.Fatal(e)
			}
			if storage.Terminal(view.Job.Status) {
				if view.Job.Status != "completed" || view.Audit == nil || view.Job.ConversationID != j.ConversationID {
					t.Fatal("concurrent product lost conversation binding", view.Job)
				}
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("queued preparation did not finish")
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	usage, e := s.Store.BudgetUsage(ctx, s.Plans.BudgetRef, "", "")
	if e != nil || usage.CloudAttempts != int64(physical.Load()) || usage.ModelAttempts != 0 || usage.CloudAttempts <= 0 || usage.CloudAttempts > 100 {
		t.Fatal("conversations duplicated shared authorization or lost physical accounting", usage, e)
	}
}

func TestCancelledPreparationKeepsObservedDirectoryWithoutSealingAudit(t *testing.T) {
	s, cloud, c, _ := preparationIntegration(t)
	turn := goalTurn(t, s, c.ID, "cancel-prepare", "检查杭州名为 new-web 的ECS")
	_, e := s.Store.AcceptIntentProposal(context.Background(), c.ID, turn.Proposal.ID, "cancel-accept", s.Plans.ConfigRevision, turn.Intent.Revision)
	if e != nil {
		t.Fatal(e)
	}
	job, found, e := s.Store.Claim(context.Background())
	if e != nil || !found {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cloud.collect = func(context.Context, CloudReader, aliyuncollector.Config, domain.Selection) (aliyuncollector.Collection, error) {
		cancel()
		return aliyuncollector.Collection{}, context.Canceled
	}
	w := NewWorkflow(s.Store)
	w.Cloud = cloud
	if e = w.Run(ctx, job); !errors.Is(e, context.Canceled) {
		t.Fatal("cancelled collection completed", e)
	}
	view, e := s.Store.ReadJobWorkspace(context.Background(), job.ID)
	if e != nil || view.PreparationDiscovery == nil || len(view.PreparationDiscovery.Resources) != 1 || view.InputSealed || view.Audit != nil {
		t.Fatal("cancel discarded observed metadata or invented sealed data", e)
	}
	before, e := s.Store.BudgetUsage(context.Background(), s.Plans.BudgetRef, "", job.ID)
	if e != nil {
		t.Fatal(e)
	}
	if _, _, e = s.Store.Page(context.Background(), job.ID, 0, 100); e != nil {
		t.Fatal(e)
	}
	after, e := s.Store.BudgetUsage(context.Background(), s.Plans.BudgetRef, "", job.ID)
	if e != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("observation replay resumed requests", e)
	}
}

func TestNaturalBusinessDialogSharesDraftAndConfirmsSectionsWithoutInventingSources(t *testing.T) {
	s, cloud, c, physical := preparationIntegration(t)
	job, _ := executeGoal(t, s, cloud, c, "business-data", "检查杭州名为 new-web 的ECS")
	before := physical.Load()
	oldInput, e := s.Store.GetJobInput(context.Background(), job.ID)
	if e != nil {
		t.Fatal(e)
	}
	edit := goalTurn(t, s, c.ID, "business-edit", "确认Web服务，HTTPS443必须保留，管理来源未知")
	if edit.Business == nil || edit.Business.Draft == nil || edit.Business.Preview == nil || edit.Business.State != "preview_required" {
		t.Fatal("natural fields did not enter shared draft", edit.Business)
	}
	if len(edit.Business.Draft.Changes) != 1 || len(edit.Business.Draft.Changes[0].Roles) != 1 || edit.Business.Draft.Changes[0].Roles[0].Role != "web" {
		t.Fatal("explicit service role lost")
	}
	change := edit.Business.Draft.Changes[0]
	if change.Access == nil || len(change.Access.Services) != 1 || len(change.Access.Services[0].Required) != 1 || change.Access.Services[0].AllowedDeclared == nil || *change.Access.Services[0].AllowedDeclared {
		t.Fatal("required-only statement declared allowed sources", change)
	}
	role := goalTurn(t, s, c.ID, "business-role-confirm", "确认预览中的角色")
	if role.Business == nil || role.Business.Confirmation == nil || role.Business.State != "confirmed" {
		t.Fatal("role confirmation failed", role.Business)
	}
	frozen, _, e := s.Store.FreezeInput(context.Background(), edit.Business.Target.SnapshotID)
	if e != nil || len(frozen.Assets[0].Services) != 1 || frozen.Assets[0].Services[0].RoleStatus != "confirmed" || frozen.Assets[0].Services[0].AllowedConfirmed || frozen.Assets[0].Services[0].RequiredConfirmed {
		t.Fatal("role confirmation also granted access", e)
	}
	preview := goalTurn(t, s, c.ID, "business-repreview", "预览业务草稿")
	if preview.Business == nil || preview.Business.Draft.ID != edit.Business.Draft.ID || preview.Business.Preview == nil {
		t.Fatal("next section created a separate draft", preview.Business)
	}
	access := goalTurn(t, s, c.ID, "business-access-confirm", "确认预览中的访问")
	if access.Business == nil || access.Business.Confirmation == nil {
		t.Fatal("access section not confirmable", access.Business)
	}
	frozen, _, e = s.Store.FreezeInput(context.Background(), edit.Business.Target.SnapshotID)
	if e != nil || !frozen.Assets[0].Services[0].RequiredConfirmed || frozen.Assets[0].Services[0].AllowedConfirmed || len(frozen.Assets[0].Services[0].Required[0].Addresses) != 0 {
		t.Fatal("unknown sources were invented or sections coupled", e)
	}
	again := goalTurn(t, s, c.ID, "business-access-confirm", "确认预览中的访问")
	if !again.Replayed || again.Business == nil || again.Business.Confirmation.ActionID != access.Business.Confirmation.ActionID {
		t.Fatal("business ACK replay made duplicate confirmation")
	}
	currentInput, e := s.Store.GetJobInput(context.Background(), job.ID)
	if e != nil || !reflect.DeepEqual(currentInput, oldInput) || physical.Load() != before {
		t.Fatal("local business dialog rewrote old sealed task or sent HTTP", e)
	}
	jobs, e := s.Store.ListJobs(context.Background(), c.ID, 0, 100)
	if e != nil || len(jobs) != 1 {
		t.Fatal("local business confirmation started another audit", e)
	}
}

func TestUnknownBusinessMixedWithBaselineStillProducesAuditProposal(t *testing.T) {
	s, cloud, c, _ := preparationIntegration(t)
	_, _ = executeGoal(t, s, cloud, c, "mixed-data", "检查杭州名为 new-web 的ECS")
	r := goalTurn(t, s, c.ID, "unknown-baseline", "管理来源未知，先检查独立基线")
	if r.Business == nil || r.Business.State != "unknown_preserved" || r.Proposal == nil || r.Proposal.PlanID == "" || r.Intent.Status != "ready_for_confirmation" {
		t.Fatal("unknown business swallowed legal baseline request", r.Intent, r.Business)
	}
}

func TestTypedBusinessEmptyTextPreviewAndConfirmUseSameReceiptPath(t *testing.T) {
	s, cloud, c, physical := preparationIntegration(t)
	_, _ = executeGoal(t, s, cloud, c, "typed-data", "检查杭州名为 new-web 的ECS")
	edit := goalTurn(t, s, c.ID, "typed-edit", "确认Web服务，HTTPS443必须保留，管理来源未知")
	if edit.Business == nil || edit.Business.Draft == nil {
		t.Fatal(edit.Business)
	}
	before := physical.Load()
	preview, e := s.Turn(context.Background(), c.ID, "typed-preview", IntentTurnRequest{ClientTurnID: "typed-preview", Business: &ConversationBusinessRequest{Action: "preview", ExpectedTargetRef: edit.Business.Target.Ref, ExpectedDraftRevision: edit.Business.Draft.Revision}})
	if e != nil || preview.Business == nil || preview.Business.Preview == nil {
		t.Fatal("typed preview with empty text failed", e, preview.Business)
	}
	request := IntentTurnRequest{ClientTurnID: "typed-confirm", Business: &ConversationBusinessRequest{Action: "confirm", ExpectedTargetRef: preview.Business.Target.Ref, ExpectedDraftRevision: preview.Business.Draft.Revision, PreviewID: preview.Business.Preview.ID, Sections: []string{"roles"}}}
	confirm, e := s.Turn(context.Background(), c.ID, "typed-confirm", request)
	if e != nil || confirm.Business == nil || confirm.Business.State != "confirmed" {
		t.Fatal("typed confirm failed", e, confirm.Business)
	}
	replay, e := s.Turn(context.Background(), c.ID, "typed-confirm", request)
	if e != nil || !replay.Replayed || replay.Business.Confirmation.ActionID != confirm.Business.Confirmation.ActionID {
		t.Fatal("empty-text typed confirm lost original receipt", e)
	}
	if physical.Load() != before {
		t.Fatal("typed business work made provider request")
	}
}
