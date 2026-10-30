package app

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cfw-policy-llm-agent/server/internal/aliyuncollector"
	"cfw-policy-llm-agent/server/internal/audit"
	"cfw-policy-llm-agent/server/internal/domain"
	"cfw-policy-llm-agent/server/internal/outbound"
	"cfw-policy-llm-agent/server/internal/storage"
)

// These integration regressions originated in independent workflow review.
// Their inputs and model/reader implementations are public local fixtures;
// passing them does not establish real cloud compatibility or model quality.

func rootWorkflowReviewReport(t *testing.T) (*IntentService, storage.Conversation, storage.Job, storage.JobWorkspace) {
	t.Helper()
	ctx := context.Background()
	s, c := conversationalCase(t)
	first, e := s.Turn(ctx, c.ID, "review-initial", IntentTurnRequest{ClientTurnID: "review-initial", Text: "检查杭州 web-prod ECS"})
	if e != nil {
		t.Fatal(e)
	}
	if first.Proposal == nil || first.Proposal.PlanID == "" {
		t.Fatal("fixture did not prepare its initial audit")
	}
	accepted, e := s.Store.AcceptIntentProposal(ctx, c.ID, first.Proposal.ID, "review-initial-accept", s.Plans.ConfigRevision, first.Intent.Revision)
	if e != nil {
		t.Fatal(e)
	}
	job, found, e := s.Store.Claim(ctx)
	if e != nil || !found || job.ID != accepted.Job.ID {
		t.Fatal(e)
	}
	if e = NewWorkflow(s.Store).Run(ctx, job); e != nil {
		t.Fatal(e)
	}
	view, e := s.Store.ReadJobWorkspace(ctx, job.ID)
	if e != nil || view.Audit == nil || view.ReportRef == "" {
		t.Fatal(e)
	}
	return s, c, job, view
}
func TestRootWorkflowReviewTypedBusinessEmptyText(t *testing.T) {
	s, c, _, _ := rootWorkflowReviewReport(t)
	r, e := s.Turn(context.Background(), c.ID, "review-typed", IntentTurnRequest{ClientTurnID: "review-typed", Text: "", Business: &ConversationBusinessRequest{Action: "preview"}})
	if e != nil {
		t.Fatalf("TYPED_BUSINESS_REJECTED_BEFORE_HANDLER: %v", e)
	}
	if r.Business == nil || r.Business.Preview == nil {
		t.Fatal("typed empty-text action did not return the shared business preview")
	}
}
func TestRootWorkflowReviewCurrentQuestionNotHistory(t *testing.T) {
	s, c, _, view := rootWorkflowReviewReport(t)
	ctx := context.Background()
	old, e := s.Current(ctx, c.ID)
	if e != nil {
		t.Fatal(e)
	}
	r, e := s.Turn(ctx, c.ID, "review-now", IntentTurnRequest{ClientTurnID: "review-now", Text: "解释现在这台ECS的风险，只运行规则"})
	if e != nil {
		t.Fatal(e)
	}
	if r.Intent.Revision <= old.Revision || r.Intent.Action == "explain" || r.Intent.ContextReportID != "" {
		t.Fatalf("current-state question was routed to the old report: revision=%d action=%s context=%s reply=%q", r.Intent.Revision, r.Intent.Action, r.Intent.ContextReportID, r.Reply.PublicText)
	}
	if r.Proposal == nil || r.Proposal.PlanID == "" {
		t.Fatalf("current-state question lost its audit plan: %+v", r.Intent)
	}
	plan, e := s.Store.GetPlan(ctx, r.Proposal.PlanID)
	if e != nil || plan.Kind != "audit" || plan.ContextReportID != "" {
		t.Fatalf("current-state question did not prepare an audit: %+v %v", plan, e)
	}
	if strings.Contains(r.Reply.PublicText, view.ReportRef) && strings.Contains(r.Reply.PublicText, "没有查询当前云配置") {
		t.Fatal("current-state question only returned the old report explanation")
	}
}
func TestRootWorkflowReviewBusinessClauseNotSwallowedByHistoryQuestion(t *testing.T) {
	s, c, _, _ := rootWorkflowReviewReport(t)
	r, e := s.Turn(context.Background(), c.ID, "review-mixed-question", IntentTurnRequest{ClientTurnID: "review-mixed-question", Text: "确认Web服务，HTTPS443必须保留，管理来源未知，还需要补哪些业务信息？"})
	if e != nil {
		t.Fatal(e)
	}
	if r.Business == nil || r.Business.Draft == nil {
		t.Fatalf("EXPLICIT_BUSINESS_FIELDS_SWALLOWED_BY_HISTORY: business=%+v reply=%q", r.Business, r.Reply.PublicText)
	}
}
func TestRootWorkflowReviewBusinessReceiptRecoveryAfterRevisionChange(t *testing.T) {
	s, c, _, _ := rootWorkflowReviewReport(t)
	ctx := context.Background()
	edit, e := s.Turn(ctx, c.ID, "review-edit", IntentTurnRequest{ClientTurnID: "review-edit", Text: "确认Web服务"})
	if e != nil || edit.Business == nil || edit.Business.Draft == nil || edit.Business.Preview == nil {
		t.Fatal(e)
	}
	expected := edit.Intent.Revision
	req := IntentTurnRequest{ClientTurnID: "review-confirm", Text: "确认预览中的角色", ExpectedRevision: &expected, Business: &ConversationBusinessRequest{Action: "confirm", ExpectedTargetRef: edit.Business.Target.Ref, ExpectedDraftRevision: edit.Business.Draft.Revision, PreviewID: edit.Business.Preview.ID, Sections: []string{"roles"}}}
	if _, e = s.Store.SQL.Exec(`CREATE TRIGGER review_fail_intent_receipt BEFORE INSERT ON conversation_intent_requests WHEN NEW.request_key='review-confirm-key' BEGIN SELECT RAISE(ABORT,'review controlled receipt failure'); END`); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Turn(ctx, c.ID, "review-confirm-key", req); e == nil {
		t.Fatal("trigger did not reject outer receipt")
	}
	current, e := s.Store.GetProfileDraft(ctx, edit.Business.Draft.ID)
	if e != nil || current.Status != "confirmed" {
		t.Fatalf("fixture did not commit business before outer failure: %+v %v", current, e)
	}
	if _, e = s.Store.SQL.Exec(`DROP TRIGGER review_fail_intent_receipt`); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Turn(ctx, c.ID, "review-other-goal", IntentTurnRequest{ClientTurnID: "review-other-goal", Text: "只运行规则检查杭州 web-prod ECS"}); e != nil {
		t.Fatal(e)
	}
	retry, e := s.Turn(ctx, c.ID, "review-confirm-key", req)
	if errors.Is(e, storage.ErrVersionConflict) {
		t.Fatalf("BUSINESS_COMMITTED_BUT_ORIGINAL_KEY_CANNOT_RECOVER_AFTER_GOAL_REVISION: %v", e)
	}
	if e != nil || retry.Business == nil || retry.Business.Confirmation == nil {
		t.Fatalf("original confirmation receipt not recovered: %+v %v", retry.Business, e)
	}
}
func TestRootWorkflowReviewGuidanceBindsExistingSharedDraft(t *testing.T) {
	s, c, job, _ := rootWorkflowReviewReport(t)
	ctx := context.Background()
	edit, e := s.Turn(ctx, c.ID, "review-guidance-edit", IntentTurnRequest{ClientTurnID: "review-guidance-edit", Text: "确认Web服务"})
	if e != nil || edit.Business == nil || edit.Business.Draft == nil {
		t.Fatal(e)
	}
	catalog, e := s.Guidance(ctx, c.ID, job.ID, "", "profile")
	if e != nil {
		t.Fatal(e)
	}
	found := false
	for _, item := range catalog.Items {
		if item.ID == "GDE-06" {
			found = true
			if item.State != "available" || item.Action.Target.ID != edit.Business.Draft.ID {
				t.Fatalf("guidance does not open the existing shared draft: expected=%s target=%+v state=%s missing=%v", edit.Business.Draft.ID, item.Action.Target, item.State, item.MissingInputs)
			}
		}
	}
	if !found {
		t.Fatalf("business-gap guidance is missing: %+v", catalog)
	}
}
func TestRootWorkflowReviewCompletedHistoricalAssistantKeepsReportGuidance(t *testing.T) {
	s, c, _, view := rootWorkflowReviewReport(t)
	ctx := context.Background()
	s.Plans.ModelReady = true
	proposed, e := s.Turn(ctx, c.ID, "review-model-history", IntentTurnRequest{ClientTurnID: "review-model-history", Text: "使用模型解释这份旧报告", Mode: "rules_and_agent", ContextReportID: view.ReportRef})
	if e != nil {
		t.Fatal(e)
	}
	if proposed.Proposal == nil || proposed.Proposal.PlanID == "" {
		t.Fatal("historical model explanation did not prepare a plan")
	}
	if _, e = s.Store.AcceptIntentProposal(ctx, c.ID, proposed.Proposal.ID, "review-model-history-accept", s.Plans.ConfigRevision, proposed.Intent.Revision); e != nil {
		t.Fatal(e)
	}
	job, found, e := s.Store.Claim(ctx)
	if e != nil || !found {
		t.Fatal(e)
	}
	workflow := NewWorkflow(s.Store)
	workflow.Agent = NewAgentService(s.Store, appAgentFactory(false), s.Plans.ConfigRevision, nil)
	if e = workflow.Run(ctx, job); e != nil {
		t.Fatal(e)
	}
	catalog, e := s.Guidance(ctx, c.ID, "", "", "conversation")
	if e != nil {
		t.Fatal(e)
	}
	available := false
	for _, item := range catalog.Items {
		if item.ID == "GDE-05" && item.State == "available" && item.Action.Target.ID == view.ReportRef && item.Intent.ContextReportID == view.ReportRef {
			available = true
		}
	}
	if !available {
		t.Fatalf("COMPLETED_HISTORY_ASSISTANT_LOSES_ORIGINAL_REPORT_GUIDANCE: report=%s catalog=%+v", view.ReportRef, catalog)
	}
}
func TestRootWorkflowReviewBusinessAndBaselineKeepPreviewReachable(t *testing.T) {
	s, c, _, _ := rootWorkflowReviewReport(t)
	ctx := context.Background()
	r, e := s.Turn(ctx, c.ID, "review-business-and-baseline", IntentTurnRequest{ClientTurnID: "review-business-and-baseline", Text: "确认Web服务，HTTPS443必须保留，管理来源未知，先检查独立基线"})
	if e != nil || r.Business == nil || r.Business.Draft == nil {
		t.Fatal(e)
	}
	current, e := s.Store.ConversationBusinessContext(ctx, c.ID)
	if e != nil {
		t.Fatal(e)
	}
	if current.Draft == nil || current.Draft.ID != r.Business.Draft.ID {
		t.Fatalf("MIXED_BASELINE_REVISES_INTENT_AND_ORPHANS_SAVED_BUSINESS_PREVIEW: savedDraft=%s savedTarget=%s currentTarget=%s currentDraft=%+v", r.Business.Draft.ID, r.Business.Target.Ref, current.Target.Ref, current.Draft)
	}
}
func TestRootWorkflowReviewHistoricalModelPreviewUsesHistoricalObjects(t *testing.T) {
	s, c, _, view := rootWorkflowReviewReport(t)
	ctx := context.Background()
	moved, e := s.Turn(ctx, c.ID, "review-move-beijing", IntentTurnRequest{ClientTurnID: "review-move-beijing", Text: "改为北京名为 web-prod 的ECS，只运行规则"})
	if e != nil {
		t.Fatal(e)
	}
	if len(moved.Intent.Selection) != 1 || moved.Intent.Selection[0].Region != "cn-beijing" {
		t.Fatal("fixture target did not move", moved.Intent)
	}
	s.Plans.ModelReady = true
	history, e := s.Turn(ctx, c.ID, "review-old-report-model", IntentTurnRequest{ClientTurnID: "review-old-report-model", Text: "使用模型解释这份旧报告", Mode: "rules_and_agent", ContextReportID: view.ReportRef})
	if e != nil {
		t.Fatal(e)
	}
	if history.Proposal == nil || history.Proposal.PlanID == "" {
		t.Fatal("historical model explanation lost its original report plan")
	}
	plan, e := s.Store.GetPlan(ctx, history.Proposal.PlanID)
	if e != nil {
		t.Fatal(e)
	}
	if len(history.Intent.Selection) != 1 || len(plan.PreparedInput.Assets) != 1 || history.Intent.Selection[0].ResourceID != plan.PreparedInput.Assets[0].ProviderResourceID || history.Intent.Selection[0].Region != "cn-hangzhou" || plan.ContextReportID != view.ReportRef || plan.EffectCeiling.AllowCloudReads {
		t.Fatalf("historical model preview shows objects different from the executed report: intent=%+v plan=%+v", history.Intent, plan)
	}
	if _, e = s.Store.AcceptIntentProposal(ctx, c.ID, history.Proposal.ID, "review-old-report-model-accept", s.Plans.ConfigRevision, history.Intent.Revision); e != nil {
		t.Fatal(e)
	}
	job, found, e := s.Store.Claim(ctx)
	if e != nil || !found {
		t.Fatal(e)
	}
	workflow := NewWorkflow(s.Store)
	workflow.Agent = NewAgentService(s.Store, appAgentFactory(false), s.Plans.ConfigRevision, nil)
	if e = workflow.Run(ctx, job); e != nil {
		t.Fatal(e)
	}
	actual, e := s.Store.GetJobInput(ctx, job.ID)
	if e != nil || !reflect.DeepEqual(actual, view.Audit.Input) {
		t.Fatalf("historical assistant sealed different input from its original report: %v", e)
	}
}

func TestRootWorkflowReviewBatchDoesNotDowngradeUnqualifiedLiveSnapshot(t *testing.T) {
	for _, n := range []int{1, 5} {
		t.Run(fmt.Sprintf("objects-%d", n), func(t *testing.T) {
			s, c := conversationalCase(t)
			s.Plans.Workloads = batchDialogProfile(t, 20)
			in, err := audit.SyntheticExample("internet-in")
			if err != nil {
				t.Fatal(err)
			}
			base := in.Assets[0]
			base.Services = []domain.Service{}
			base.Allowed = []domain.Access{}
			base.AllowedConfirmed = false
			base.AllowedComplete = false
			in.Assets = nil
			in.Selection.AssetIDs = nil
			in.SourceKind = "live_readonly"
			in.SnapshotVersion = "independent-old-live"
			in.CollectedAt = "2020-01-01T00:00:00Z"
			ids := []string{}
			for i := 0; i < n; i++ {
				a := base
				a.ID = fmt.Sprintf("asset-independent-live-%02d", i)
				a.ProviderResourceID = fmt.Sprintf("i-independent-live-%02d", i)
				a.Name = fmt.Sprintf("independent-old-live-%02d", i)
				a.Region = "cn-hangzhou"
				a.ResourceType = "ecs"
				a.Address = fmt.Sprintf("192.0.2.%d", i+50)
				in.Assets = append(in.Assets, a)
				in.Selection.AssetIDs = append(in.Selection.AssetIDs, a.ID)
				ids = append(ids, a.ProviderResourceID)
			}
			snap, err := s.Store.SaveSnapshot(context.Background(), in)
			if err != nil {
				t.Fatal(err)
			}
			text := "检查杭州 ECS " + strings.Join(ids, " ") + " 的当前入向，只运行规则"
			selected := goalTurn(t, s, c.ID, "independent-live-selected", text)
			if len(selected.Intent.Selection) != n {
				t.Fatalf("fixture not selected: %#v", selected.Intent)
			}
			if n > 1 {
				selected = goalTurn(t, s, c.ID, "independent-live-batch", "按完整对象集分批审计")
			}
			if selected.Intent.DataPolicy.Mode != "prefer_fresh" {
				t.Fatalf("fixture is not current policy: %#v", selected.Intent.DataPolicy)
			}
			if selected.Proposal != nil && selected.Proposal.PlanID != "" {
				p, err := s.Store.GetPlan(context.Background(), selected.Proposal.PlanID)
				if err != nil {
					t.Fatal(err)
				}
				t.Fatalf("unqualified old live data became executable: status=%s source=%s snapshot=%s old=%s preparation=%v assets=%d policy=%s", selected.Intent.Status, p.DataSourceID, p.SnapshotID, snap.ID, p.Preparation, len(p.PreparedInput.Assets), selected.Intent.DataPolicy.Mode)
			}
			if selected.Intent.Status != "needs_data" {
				t.Fatalf("current live data not blocked: %s", selected.Intent.Status)
			}
		})
	}
}

func TestRootWorkflowReviewBatchStopsAfterPermanentAccountMismatch(t *testing.T) {
	s, cloud, c, physical := preparationIntegration(t)
	s.Plans.Workloads = batchDialogProfile(t, 20)
	base := cloud.DiscoveryConfig
	cloud.ResolveScope = func(boundary, direction string, rs []domain.Resource) (aliyuncollector.Config, error) {
		if boundary != "internet" || direction != "in" {
			return aliyuncollector.Config{}, ErrScopeDenied
		}
		cfg := base
		cfg.AssetRegions = map[string]string{}
		cfg.AllowedRegions = []string{"cn-hangzhou"}
		for _, r := range rs {
			if r.Region != "cn-hangzhou" || r.ResourceType != "ecs" {
				return cfg, ErrScopeDenied
			}
			cfg.AssetRegions[r.ResourceID] = r.Region
		}
		return cfg, nil
	}
	reader := &cloudReaderFixture{identityErr: outbound.Failure("CLOUD_ACCOUNT_MISMATCH")}
	cloud.NewReader = func(context.Context, string) (CloudReader, error) { return reader, nil }
	resources := []domain.Resource{}
	ids := []string{}
	for i := 0; i < 5; i++ {
		id := fmt.Sprintf("i-permanent-review-%d", i)
		ids = append(ids, id)
		resources = append(resources, domain.Resource{Provider: "aliyun", AccountRef: cloud.Grant.AccountRef, ResourceID: id, Region: "cn-hangzhou", ResourceType: "ecs", Name: id, Source: "live_readonly", SourceRef: cloud.SourceRef, ObservedAt: "2026-10-29T00:00:00Z"})
	}
	if err := s.Store.UpsertResources(context.Background(), cloud.SourceRef, resources, domain.CatalogueCoverage{Source: cloud.SourceRef, AccountRef: cloud.Grant.AccountRef, Complete: false, Limitations: []string{"independent_local_fixture"}}); err != nil {
		t.Fatal(err)
	}
	selected := goalTurn(t, s, c.ID, "independent-account-select", "检查杭州 ECS "+strings.Join(ids, " ")+" 的入向，只运行规则")
	if len(selected.Intent.Selection) != 5 {
		t.Fatalf("fixture selection failed: %#v", selected.Intent)
	}
	offer := goalTurn(t, s, c.ID, "independent-account-batch", "按完整对象集分批审计")
	if offer.Proposal == nil || offer.Proposal.PlanID == "" {
		t.Fatalf("fixture batch failed: %#v", offer.Intent)
	}
	plan, err := s.Store.GetPlan(context.Background(), offer.Proposal.PlanID)
	if err != nil || plan.Preparation == nil || plan.Batch == nil {
		t.Fatalf("fixture live plan invalid: %#v %v", plan, err)
	}
	if _, err = s.Store.AcceptIntentProposal(context.Background(), c.ID, offer.Proposal.ID, "independent-account-accept", s.Plans.ConfigRevision, offer.Intent.Revision); err != nil {
		t.Fatal(err)
	}
	parent, found, err := s.Store.Claim(context.Background())
	if err != nil || !found {
		t.Fatal(err)
	}
	w := NewWorkflow(s.Store)
	w.Cloud = cloud
	runErr := w.Run(context.Background(), parent)
	if runErr == nil {
		t.Fatal("permanent account mismatch succeeded")
	}
	batch, err := s.Store.GetBatchRequest(context.Background(), parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	started := 0
	for _, seg := range batch.Segments {
		if seg.ChildJobID != "" {
			started++
		}
	}
	if physical.Load() != 0 {
		t.Fatal("fixture called loopback transport", physical.Load())
	}
	if started != 1 || reader.verified != 1 || len(batch.Unprocessed) != 3 {
		t.Fatalf("continued after permanent shared identity mismatch: started=%d identity_checks=%d covered=%d failed=%d unprocessed=%d error=%v segments=%#v", started, reader.verified, len(batch.Covered), len(batch.Failed), len(batch.Unprocessed), runErr, batch.Segments)
	}
}

func TestRootWorkflowReviewAcceptedGroupBatchKeepsOriginalReceiptAndMembers(t *testing.T) {
	s, c, _ := selectedGroupDialog(t, 5)
	oldGroup := confirmDialogGroup(t, s, c)
	s.Plans.Workloads = batchDialogProfile(t, 20)
	other, err := s.Store.CreateConversation(context.Background(), "独立组批量回执复核", "")
	if err != nil {
		t.Fatal(err)
	}
	selected := goalTurn(t, s, other.ID, "independent-group-select", "审计客户甲的全部ECS入向，只运行规则")
	if len(selected.Intent.Selection) != 5 {
		t.Fatal("full group not retained")
	}
	offered := goalTurn(t, s, other.ID, "independent-group-batch", "按完整对象集分批审计")
	if offered.Proposal == nil || offered.Proposal.PlanID == "" {
		t.Fatal("no batch proposal")
	}
	accepted, err := s.Store.AcceptIntentProposal(context.Background(), other.ID, offered.Proposal.ID, "independent-group-original-accept", s.Plans.ConfigRevision, offered.Intent.Revision)
	if err != nil {
		t.Fatal(err)
	}
	source, err := s.Current(context.Background(), c.ID)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := s.Store.PreviewResourceGroup(context.Background(), c.ID, "客户甲", []string{oldGroup.Members[0].ResourceRef}, "工单SEC-89变更成员", source.Revision, "independent-group-new-preview")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Store.ConfirmResourceGroup(context.Background(), c.ID, preview.ID, source.Revision, "independent-group-new-confirm"); err != nil {
		t.Fatal(err)
	}
	replay, err := s.Store.AcceptIntentProposal(context.Background(), other.ID, offered.Proposal.ID, "independent-group-original-accept", "newer-configuration-revision", offered.Intent.Revision)
	if err != nil || !replay.Replayed || replay.Job.ID != accepted.Job.ID {
		t.Fatalf("old acceptance receipt lost: %#v %v", replay, err)
	}
	parent, found, err := s.Store.Claim(context.Background())
	if err != nil || !found || parent.ID != accepted.Job.ID {
		t.Fatal(err)
	}
	if err = NewWorkflow(s.Store).Run(context.Background(), parent); err != nil {
		t.Fatal("accepted group head was requalified during execution", err)
	}
	batch, err := s.Store.GetBatchRequest(context.Background(), parent.ID)
	if err != nil || len(batch.Covered) != 5 || len(batch.Unprocessed) != 0 {
		t.Fatalf("accepted old members were replaced: %#v %v", batch, err)
	}
}
func TestRootWorkflowReviewCancelChildCancelsRootAndPreventsLaterSegments(t *testing.T) {
	s, c, _ := selectedGroupDialog(t, 5)
	s.Plans.Workloads = batchDialogProfile(t, 20)
	offer := goalTurn(t, s, c.ID, "independent-cancel-batch", "按完整对象集分批审计")
	if offer.Proposal == nil || offer.Proposal.PlanID == "" {
		t.Fatal("no cancel fixture plan")
	}
	accepted, err := s.Store.AcceptIntentProposal(context.Background(), c.ID, offer.Proposal.ID, "independent-cancel-accept", s.Plans.ConfigRevision, offer.Intent.Revision)
	if err != nil {
		t.Fatal(err)
	}
	parent, found, err := s.Store.Claim(context.Background())
	if err != nil || !found {
		t.Fatal(err)
	}
	started := make(chan struct{})
	calls := &atomic.Int32{}
	w := NewWorkflow(s.Store)
	w.rules = func(ctx context.Context, in domain.Input) (domain.Result, error) {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-ctx.Done()
		return domain.Result{}, ctx.Err()
	}
	engine := New(s.Store, w.Run)
	done := make(chan struct{})
	t.Cleanup(func() {
		engine.Stop()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("cancel fixture worker did not exit during cleanup")
		}
	})
	go func() { defer close(done); engine.execute(parent) }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("rules did not start")
	}
	batch, err := s.Store.GetBatchRequest(context.Background(), parent.ID)
	if err != nil || len(batch.Segments) != 3 || batch.Segments[0].ChildJobID == "" {
		t.Fatal("no active internal segment", err)
	}
	returned, err := engine.Cancel(context.Background(), batch.Segments[0].ChildJobID)
	if err != nil || returned.ID != accepted.Job.ID {
		t.Fatal("child cancel did not map to root", returned, err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		engine.Stop()
		t.Fatal("batch did not stop after child cancellation")
	}
	saved, err := s.Store.Get(context.Background(), parent.ID)
	if err != nil || saved.Status != "cancelled" {
		t.Fatal("root did not finish cancelled", saved.Status, err)
	}
	batch, err = s.Store.GetBatchRequest(context.Background(), parent.ID)
	if err != nil || len(batch.Unprocessed) != 3 || calls.Load() != 1 {
		t.Fatalf("cancel started more segments: calls=%d batch=%#v err=%v", calls.Load(), batch, err)
	}
}

func TestRootWorkflowReviewProductNaturalCurrentPlanConfirmationStartsOriginalPlan(t *testing.T) {
	s, c := conversationalCase(t)
	s.Engine = New(s.Store, nil)
	t.Cleanup(s.Engine.Stop)
	offered := goalTurn(t, s, c.ID, "product-natural-offer", "检查杭州 web-prod ECS，只运行规则")
	if offered.Proposal == nil || offered.Proposal.PlanID == "" {
		t.Fatal("fixture missing plan")
	}
	expected := offered.Intent.Revision
	accepted, err := s.Turn(context.Background(), c.ID, "product-natural-confirm", IntentTurnRequest{ClientTurnID: "product-natural-confirm", Text: "确认按当前计划开始审计", ExpectedRevision: &expected})
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := s.Store.ListJobs(context.Background(), c.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || accepted.Job == nil || jobs[0].PlanID != offered.Proposal.PlanID {
		nextPlan := ""
		if accepted.Proposal != nil {
			nextPlan = accepted.Proposal.PlanID
		}
		t.Fatalf("explicit natural confirmation did not consume the reviewed plan: jobs=%d returned_job=%v old_plan=%s next_plan=%s old_revision=%d next_revision=%d reply=%q", len(jobs), accepted.Job, offered.Proposal.PlanID, nextPlan, offered.Intent.Revision, accepted.Intent.Revision, accepted.Reply.PublicText)
	}
}

func rootWorkflowReviewRecheckAB(t *testing.T) (*IntentService, storage.Conversation, storage.Job, storage.JobWorkspace) {
	t.Helper()
	s, cloud, c, _ := preparationIntegration(t)
	oldJob, _ := executeGoal(t, s, cloud, c, "product-recheck-original", "审计杭州名为 new-web 的ECS入向策略，只运行规则")
	old, err := s.Store.ReadJobWorkspace(context.Background(), oldJob.ID)
	if err != nil || old.Audit == nil || old.ReportRef == "" {
		t.Fatal("fixture original report missing", err)
	}
	cfg := cloud.DiscoveryConfig
	cloud.ResolveScope = func(boundary, direction string, rs []domain.Resource) (aliyuncollector.Config, error) {
		if boundary != "internet" || direction != "in" {
			return cfg, ErrScopeDenied
		}
		return cfg, nil
	}
	b := domain.Resource{Provider: "aliyun", AccountRef: cloud.Grant.AccountRef, SourceRef: cloud.SourceRef, Source: "live_readonly", ResourceID: "i-product-current-b", Region: "cn-hangzhou", ResourceType: "ecs", Name: "current-b", ObservedAt: "2026-10-29T00:00:00Z"}
	if err = s.Store.UpsertResources(context.Background(), cloud.SourceRef, []domain.Resource{b}, domain.CatalogueCoverage{AccountRef: cloud.Grant.AccountRef, Source: cloud.SourceRef, Complete: false, Limitations: []string{"independent_public_fixture"}}); err != nil {
		t.Fatal(err)
	}
	moved := goalTurn(t, s, c.ID, "product-recheck-move-b", "改查杭州 ECS i-product-current-b，只运行规则")
	if len(moved.Intent.Selection) != 1 || moved.Intent.Selection[0].ResourceID != b.ResourceID {
		t.Fatal("fixture did not move to B", moved.Intent)
	}
	return s, c, oldJob, old
}

func TestRootWorkflowReviewProductRecheckReportADoesNotReadCurrentObjectB(t *testing.T) {
	s, c, oldJob, old := rootWorkflowReviewRecheckAB(t)
	recheck, err := s.Turn(context.Background(), c.ID, "product-recheck-report-a", IntentTurnRequest{ClientTurnID: "product-recheck-report-a", Text: "我已修改，重新采集复审，只运行规则", ContextReportID: old.ReportRef})
	if err != nil {
		t.Fatal(err)
	}
	if recheck.Proposal == nil || recheck.Proposal.PlanID == "" {
		t.Fatal("explicit report-only recheck did not prepare its original objects", recheck.Intent)
	}
	plan, err := s.Store.GetPlan(context.Background(), recheck.Proposal.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	expected := old.Audit.Input.Assets[0].ProviderResourceID
	if plan.RecheckOfJobID != oldJob.ID || plan.Preparation == nil || len(plan.Preparation.Selected) != 1 || plan.Preparation.Selected[0].ResourceID != expected {
		t.Fatalf("report A recheck silently reads current B: original_job=%s linked=%s original_resource=%s shown=%#v prepared=%#v", oldJob.ID, plan.RecheckOfJobID, expected, recheck.Intent.Selection, plan.Preparation)
	}
}

func TestRootWorkflowReviewProductTrustedGroupBeyondOnePageCanBatchWholeSet(t *testing.T) {
	s, c, _ := selectedGroupDialog(t, 101)
	group := confirmDialogGroup(t, s, c)
	other, err := s.Store.CreateConversation(context.Background(), "独立可信组分页", "")
	if err != nil {
		t.Fatal(err)
	}
	selected := goalTurn(t, s, other.ID, "product-group-page", "审计客户甲的全部ECS入向，只运行规则")
	batch := goalTurn(t, s, other.ID, "product-group-whole-batch", "按完整对象集分批审计")
	if len(batch.Intent.Selection) != len(group.Members) || batch.Proposal == nil || batch.Proposal.PlanID == "" {
		t.Fatalf("finite trusted full group is stuck at first catalogue page: full=%d first_candidates=%d first_selection=%d first_status=%s after_batch_selection=%d after_batch_status=%s reply=%q", len(group.Members), len(selected.Intent.Candidates), len(selected.Intent.Selection), selected.Intent.Status, len(batch.Intent.Selection), batch.Intent.Status, batch.Reply.PublicText)
	}
}

func TestRootWorkflowReviewProductPinnedOldSnapshotRestoresOriginalBusinessDraft(t *testing.T) {
	s, c, _, old := rootWorkflowReviewReport(t)
	original := goalTurn(t, s, c.ID, "product-draft-original", "确认Web服务，HTTPS443必须保留，管理来源未知")
	if original.Business == nil || original.Business.Draft == nil {
		t.Fatal("fixture draft missing")
	}
	oldDraft := original.Business.Draft.ID
	newerInput := old.Audit.Input
	newerInput.SnapshotVersion = "product-draft-new-snapshot"
	newerInput.CollectedAt = "2026-10-29T00:00:00Z"
	newer, err := s.Store.SaveSnapshot(context.Background(), newerInput)
	if err != nil {
		t.Fatal(err)
	}
	next, err := s.Turn(context.Background(), c.ID, "product-draft-new-plan", IntentTurnRequest{ClientTurnID: "product-draft-new-plan", Text: "检查杭州 web-prod ECS，只运行规则", DataPolicy: &domain.IntentDataPolicy{Mode: "pinned_snapshot", SnapshotID: newer.ID}})
	if err != nil || next.Proposal == nil {
		t.Fatal("fixture new plan missing", err)
	}
	if _, err = s.Store.AcceptIntentProposal(context.Background(), c.ID, next.Proposal.ID, "product-draft-new-accept", s.Plans.ConfigRevision, next.Intent.Revision); err != nil {
		t.Fatal(err)
	}
	job, found, err := s.Store.Claim(context.Background())
	if err != nil || !found {
		t.Fatal(err)
	}
	if err = NewWorkflow(s.Store).Run(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	newest, err := s.Store.ConversationBusinessContext(context.Background(), c.ID)
	if err != nil || newest.Target.SnapshotID != newer.ID {
		t.Fatal("fixture did not produce new data target", err)
	}
	restored, err := s.Turn(context.Background(), c.ID, "product-draft-restore-old", IntentTurnRequest{ClientTurnID: "product-draft-restore-old", Text: "检查杭州 web-prod ECS，使用固定历史资料，只运行规则", DataPolicy: &domain.IntentDataPolicy{Mode: "pinned_snapshot", SnapshotID: old.Audit.SnapshotID}})
	if err != nil {
		t.Fatal(err)
	}
	if restored.Intent.DataPolicy.SnapshotID != old.Audit.SnapshotID {
		t.Fatal("fixture did not choose original historical snapshot")
	}
	current, err := s.Store.ConversationBusinessContext(context.Background(), c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Target.SnapshotID != old.Audit.SnapshotID || current.Draft == nil || current.Draft.ID != oldDraft {
		t.Fatalf("explicit old snapshot still resolves newer business target and hides original draft: requested_snapshot=%s got_snapshot=%s old_draft=%s current_draft=%#v", old.Audit.SnapshotID, current.Target.SnapshotID, oldDraft, current.Draft)
	}
	preview := handleBusiness(t, s, c.ID, "product-draft-restored-preview", "预览业务资料")
	if preview.Draft == nil || preview.Draft.ID != oldDraft || preview.Preview == nil {
		t.Fatal("restored historical draft could not generate a current preview", preview)
	}
}

func TestRootWorkflowReviewProductRecheckRejectsExplicitConflictingObjectsOrBoundary(t *testing.T) {
	for _, text := range []string{"我已修改，重新采集复审 i-product-current-b，只运行规则", "我已修改，重新采集复审出向，只运行规则"} {
		t.Run(text, func(t *testing.T) {
			s, c, _, old := rootWorkflowReviewRecheckAB(t)
			r, err := s.Turn(context.Background(), c.ID, "product-recheck-explicit-conflict", IntentTurnRequest{ClientTurnID: "product-recheck-explicit-conflict", Text: text, ContextReportID: old.ReportRef})
			if err != nil {
				t.Fatal(err)
			}
			if r.Intent.Status != "needs_clarification" || r.Proposal != nil && r.Proposal.PlanID != "" {
				t.Fatalf("conflicting recheck became executable: %+v", r.Intent)
			}
		})
	}
}
func TestRootWorkflowReviewProductRefreshingCurrentBDoesNotAttachUnrelatedReportA(t *testing.T) {
	s, c, _, _ := rootWorkflowReviewRecheckAB(t)
	r := goalTurn(t, s, c.ID, "product-refresh-current-b", "当前这台重新采集复审，只运行规则")
	if r.Proposal == nil || r.Proposal.PlanID == "" {
		t.Fatal("current B refresh did not prepare a plan", r.Intent)
	}
	p, err := s.Store.GetPlan(context.Background(), r.Proposal.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	if p.RecheckOfJobID != "" || p.Preparation == nil || len(p.Preparation.Selected) != 1 || p.Preparation.Selected[0].ResourceID != "i-product-current-b" {
		t.Fatalf("current B refresh borrowed an unrelated A comparison: %+v", p)
	}
}

func TestRootWorkflowReviewNaturalAcceptanceReceiptSurvivesNewGoalAndStoppedEngine(t *testing.T) {
	s, c := conversationalCase(t)
	s.Engine = New(s.Store, nil)
	t.Cleanup(s.Engine.Stop)
	offer := goalTurn(t, s, c.ID, "independent-natural-history-offer", "检查杭州 web-prod ECS，只运行规则")
	revision := offer.Intent.Revision
	req := IntentTurnRequest{ClientTurnID: "independent-natural-history-confirm", Text: "就按这个计划执行", ExpectedRevision: &revision}
	accepted, err := s.Turn(context.Background(), c.ID, "independent-natural-history-confirm", req)
	if err != nil || accepted.Job == nil || accepted.Message == nil || accepted.Reply == nil {
		t.Fatal("fixture did not accept the original reviewed plan", err)
	}
	newGoal := goalTurn(t, s, c.ID, "independent-natural-history-next", "改查北京 web-prod ECS，只运行规则")
	s.Engine.Stop()
	replay, err := s.Turn(context.Background(), c.ID, "independent-natural-history-confirm", req)
	if err != nil || !replay.Replayed || replay.Job == nil || replay.Job.ID != accepted.Job.ID || replay.Message.ID != accepted.Message.ID || replay.Reply.ID != accepted.Reply.ID {
		t.Fatal("old natural acceptance lost its original durable receipt", err)
	}
	current, err := s.Current(context.Background(), c.ID)
	if err != nil || current.Revision != newGoal.Intent.Revision || current.Selection[0].Region != "cn-beijing" {
		t.Fatal("replaying an original acceptance rolled the current target back", err)
	}
	jobs, err := s.Store.ListJobs(context.Background(), c.ID, 0, 100)
	if err != nil || len(jobs) != 1 || jobs[0].PlanID != offer.Proposal.PlanID {
		t.Fatal("receipt recovery created a second execution", err)
	}
}

func TestRootWorkflowReviewTrustedGroupTraversesThreeCatalogPages(t *testing.T) {
	s, c, _ := selectedGroupDialog(t, 201)
	group := confirmDialogGroup(t, s, c)
	other, err := s.Store.CreateConversation(context.Background(), "独立三页可信组", "")
	if err != nil {
		t.Fatal(err)
	}
	selected := goalTurn(t, s, other.ID, "independent-three-pages", "审计客户甲的全部ECS入向，只运行规则")
	if len(selected.Intent.Selection) != len(group.Members) || !selected.Intent.Catalogue.Complete || selected.Intent.Status != "needs_batch" {
		t.Fatal("trusted finite membership was truncated or remained incomplete", selected.Intent)
	}
	offer := goalTurn(t, s, other.ID, "independent-three-pages-batch", "按完整对象集分批审计")
	if offer.Proposal == nil || offer.Proposal.PlanID == "" {
		t.Fatal("full three-page group could not preview a batch")
	}
	plan, err := s.Store.GetPlan(context.Background(), offer.Proposal.PlanID)
	if err != nil || plan.Batch == nil || len(plan.Batch.Resources) != 201 || plan.ResourceGroupVersionID != group.ID {
		t.Fatal("batch lost the immutable complete group", err)
	}
	actual := map[string]bool{}
	for _, resource := range plan.Batch.Resources {
		if actual[resource.ResourceID] {
			t.Fatal("catalog pages duplicated a member", resource.ResourceID)
		}
		actual[resource.ResourceID] = true
	}
	for _, member := range group.Members {
		if !actual[member.ResourceID] {
			t.Fatal("catalog traversal omitted an accepted member", member.ResourceID)
		}
	}
}

func TestRootWorkflowReviewQueuedBatchCancelPublishesOrderedTerminalWithoutStartingChildren(t *testing.T) {
	s, c, _ := selectedGroupDialog(t, 5)
	s.Plans.Workloads = batchDialogProfile(t, 20)
	offer := goalTurn(t, s, c.ID, "independent-queued-stop-offer", "按完整对象集分批审计")
	accepted, err := s.Store.AcceptIntentProposal(context.Background(), c.ID, offer.Proposal.ID, "independent-queued-stop-accept", s.Plans.ConfigRevision, offer.Intent.Revision)
	if err != nil || accepted.Job.Status != "queued" {
		t.Fatal("fixture did not remain queued", err)
	}
	engine := New(s.Store, nil)
	t.Cleanup(engine.Stop)
	cancelled, err := engine.Cancel(context.Background(), accepted.Job.ID)
	if err != nil || cancelled.Status != "cancelled" {
		t.Fatal(err)
	}
	page, _, err := s.Store.Page(context.Background(), cancelled.ID, accepted.Job.ViewSeq, 100)
	if err != nil || len(page.Events) != 2 || page.Events[0].Type != "stage_changed" || page.Events[0].Payload["stage"] != "batch_request_stopped" || page.Events[1].Type != "job_terminal" || page.Events[1].Seq != page.Events[0].Seq+1 || page.LastSeq != page.Events[1].Seq {
		t.Fatal("queued cancellation did not commit distinct ordered stop and terminal events", page, err)
	}
	if _, err = engine.Cancel(context.Background(), cancelled.ID); err != nil {
		t.Fatal(err)
	}
	after, _, err := s.Store.Page(context.Background(), cancelled.ID, page.LastSeq, 100)
	if err != nil || len(after.Events) != 0 || after.LastSeq != page.LastSeq {
		t.Fatal("repeated cancellation republished a terminal event", after, err)
	}
	batch, err := s.Store.GetBatchRequest(context.Background(), cancelled.ID)
	if err != nil || len(batch.Unprocessed) != 5 || len(batch.Covered) != 0 || len(batch.Failed) != 0 {
		t.Fatal("unstarted cancellation invented executed coverage", batch, err)
	}
	for _, segment := range batch.Segments {
		if segment.ChildJobID != "" || segment.Status != "unprocessed" || segment.Manifest != nil {
			t.Fatal("queued cancellation created a child or input", segment)
		}
	}
	guidance, err := s.Guidance(context.Background(), c.ID, cancelled.ID, "", "conversation")
	if err != nil || len(guidance.Items) != 1 || guidance.Items[0].ID != "GDE-BATCH-RESULTS" || guidance.Items[0].Action.Target.ID != cancelled.ID {
		t.Fatal("cancelled batch guidance lost the original parent", guidance, err)
	}
	if _, found, err := s.Store.Claim(context.Background()); err != nil || found {
		t.Fatal("queued cancellation left executable work", err)
	}
}
