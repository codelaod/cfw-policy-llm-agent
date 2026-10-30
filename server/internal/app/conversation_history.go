package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"cfw-policy-llm-agent/server/internal/domain"
	"cfw-policy-llm-agent/server/internal/storage"
)

var recheckEvaluationWords = regexp.MustCompile(`(不要|不用|不能|不想|不再|别|不)?\s*(沿用|复用|保持|使用|采用|改用|换用|用|按)?\s*(原报告的|原报告|原|旧|当前|现在|新的|新)\s*评估(?:时点|时间|时刻)`)

func recheckEvaluationChoice(text string) (current, conflict bool) {
	old, next := false, false
	for _, phrase := range recheckEvaluationWords.FindAllStringSubmatch(text, -1) {
		original := strings.HasPrefix(phrase[3], "原") || phrase[3] == "旧"
		if phrase[1] != "" {
			original = !original
		}
		if original {
			old = true
		} else {
			next = true
		}
	}
	return next, old && next
}

func (s *IntentService) selectedReport(ctx context.Context, conversation, ref string) (storage.ReportRecord, storage.AuditRecord, storage.Job, error) {
	if ref != "" {
		r, e := s.Store.GetReport(ctx, ref)
		if e != nil {
			return r, storage.AuditRecord{}, storage.Job{}, e
		}
		a, e := s.Store.GetAudit(ctx, r.AuditID)
		if e != nil {
			return r, a, storage.Job{}, e
		}
		j, found, e := s.Store.JobForAudit(ctx, a.ID)
		if e != nil {
			return r, a, j, e
		}
		if !found || j.ConversationID != conversation {
			return r, a, j, ErrScopeDenied
		}
		return r, a, j, nil
	}
	jobs, e := s.Store.ListJobs(ctx, conversation, 0, 100)
	if e != nil {
		return storage.ReportRecord{}, storage.AuditRecord{}, storage.Job{}, e
	}
	for _, j := range jobs {
		if j.Kind != "audit" || j.ResultRef == "" || !storage.Terminal(j.Status) {
			continue
		}
		view, e := s.Store.ReadJobWorkspace(ctx, j.ID)
		if e != nil {
			return storage.ReportRecord{}, storage.AuditRecord{}, j, e
		}
		if !view.ReportPublished {
			continue
		}
		r, e := s.Store.GetReport(ctx, view.ReportRef)
		if e != nil {
			return r, storage.AuditRecord{}, j, e
		}
		return r, *view.Audit, j, nil
	}
	return storage.ReportRecord{}, storage.AuditRecord{}, storage.Job{}, storage.ErrNotFound
}

func historicalReply(r storage.ReportRecord, a storage.AuditRecord, finding string) string {
	out := fmt.Sprintf("以下引用固定报告 %s 的原始证据，采集／构造时点 %s；没有查询当前云配置。\n", r.ID, a.Input.CollectedAt)
	seen := 0
	for _, f := range a.Findings {
		if finding != "" && finding != f.ID && finding != f.Finding.ID {
			continue
		}
		if seen >= 3 && finding == "" {
			break
		}
		seen++
		basis := f.Finding
		out += fmt.Sprintf("%s：%s；%s。证据：%s。\n", basis.RuleID, basis.Verdict, basis.Summary, strings.Join(basis.EvidenceRefs, "、"))
		if len(basis.Limitations) > 0 {
			out += "限制：" + strings.Join(basis.Limitations, "、") + "。\n"
		}
	}
	if seen == 0 {
		out += "该报告没有匹配发现，请核对所选发现引用。\n"
	}
	if finding == "" && len(a.Findings) > seen {
		out += "这里只列出部分发现，完整结果与定位证据可在该固定报告查看。\n"
	}
	return out + "配置允许不证明实际可达；角色与端口线索不等于业务授权。询问当前状态或说明人工修改后，会先生成新的资料计划。"
}

func (s *IntentService) reportResources(ctx context.Context, a storage.AuditRecord) ([]domain.Resource, error) {
	locators, e := s.Store.SnapshotResources(ctx, a.SnapshotID)
	if e != nil {
		return nil, e
	}
	selected := []domain.Resource{}
	for _, locator := range locators {
		if a.Input.Selection.Kind == "gateway_policy" {
			if locator.ResourceType != "nat_gateway" {
				continue
			}
			if a.Input.GatewayMetadata != nil {
				locator.Name = a.Input.GatewayMetadata.Name
				locator.SourceNames = a.Input.GatewayMetadata.SourceNames
			}
			locator.ObservedAt = a.Input.CollectedAt
			selected = append(selected, locator)
			continue
		}
		for _, asset := range a.Input.Assets {
			if locator.LocalAssetID != asset.ID {
				continue
			}
			locator.Name = asset.Name
			if locator.Name == "" {
				locator.Name = asset.ID
			}
			locator.Region, locator.ResourceType, locator.ResourceID = asset.Region, asset.ResourceType, asset.ProviderResourceID
			locator.Tags, locator.OperatingSystem = asset.Tags, asset.OperatingSystem
			locator.ObservedAt = a.Input.CollectedAt
			selected = append(selected, locator)
		}
	}
	if len(selected) == 0 || len(selected) != len(a.Input.Assets) && a.Input.Selection.Kind == "assets" || a.Input.Selection.Kind == "gateway_policy" && len(selected) != 1 {
		return nil, storage.ErrInputInvalid
	}
	return selected, nil
}

// reportRecheck binds an explicit report action to its immutable objects. A
// later conversation target is not authority to refresh a different resource
// under the original report's recheck relationship.
func (s *IntentService) reportRecheck(ctx context.Context, c storage.Conversation, in *domain.ConversationIntent, text, reportRef string, query domain.ResourceQuery, explicitTarget, explicitRegion bool) (string, *storage.ExecutionPlan, bool, error) {
	originalReport, previous, producer, err := s.selectedReport(ctx, c.ID, reportRef)
	if errors.Is(err, storage.ErrNotFound) {
		return "", nil, false, nil
	}
	if err != nil {
		return "", nil, false, err
	}
	resources, err := s.reportResources(ctx, previous)
	if err != nil {
		return "", nil, false, err
	}
	scope := previous.Input.Scope
	if reportRef == "" {
		// Without an explicit report, a different current selection is a new
		// refresh request. Do not attach an unrelated latest audit to it.
		if len(in.Selection) != len(resources) || in.Boundary != scope.Boundary || in.Direction != scope.Direction {
			return "", nil, false, nil
		}
		for _, selected := range in.Selection {
			found := false
			for _, original := range resources {
				found = found || selected.ID == original.ID
			}
			if !found {
				return "", nil, false, nil
			}
		}
	}
	in.ContextReportID = originalReport.ID
	conflict := explicitRegion && !recheckQueryMatches(resources, domain.ResourceQuery{Region: query.Region, Regions: query.Regions})
	targetQuery := query
	if !explicitRegion {
		targetQuery.Region, targetQuery.Regions = "", nil
	}
	conflict = conflict || explicitTarget && !recheckQueryMatches(resources, targetQuery)
	lower := strings.ToLower(text)
	if strings.Contains(text, "入向") || strings.Contains(text, "出向") || strings.Contains(text, "入站") || strings.Contains(text, "出站") || strings.Contains(lower, "ingress") || strings.Contains(lower, "egress") || strings.Contains(lower, "inbound") || strings.Contains(lower, "outbound") {
		conflict = conflict || in.Direction != scope.Direction
	}
	if strings.Contains(text, "互联网") || strings.Contains(lower, "nat") {
		conflict = conflict || in.Boundary != scope.Boundary
	}
	if conflict {
		in.Status = "needs_clarification"
		in.Proposal = nil
		in.Clarification = "本次另写的对象或边界与原报告不同。请按原报告对象复审，或明确为当前对象新建独立审计；不会把另一对象作为原报告的复审输入。"
		return in.Clarification, nil, true, nil
	}
	in.Selection = resources
	in.Candidates = []domain.Resource{}
	in.Query = domain.ResourceQuery{ResourceType: resources[0].ResourceType}
	in.ResourceGroupVersionID = ""
	in.AllowMultiple = len(resources) > 1
	in.Boundary, in.Direction = scope.Boundary, scope.Direction
	in.RecheckOfJobID = producer.ID
	changedInstant, conflictingInstants := recheckEvaluationChoice(text)
	if conflictingInstants {
		in.Status = "needs_clarification"
		in.Proposal = nil
		in.Clarification = "本次同时指定原评估时点与新的当前评估时点，请明确采用一个。资料会保留实际新采集时间，不会因时点选择混淆原报告条件。"
		return in.Clarification, nil, true, nil
	}
	reply, plan, err := s.resolvePlan(ctx, c, in)
	if plan != nil && err == nil {
		// Match the existing report recheck form: hold the evaluation instant
		// constant while recording the new data's actual collection time.
		plan.EvaluationAt = previous.Input.EvaluationAt
		if changedInstant {
			plan.EvaluationAt = time.Now().UTC().Format(time.RFC3339Nano)
		}
		if plan.Preparation == nil {
			plan.PreparedInput.EvaluationAt = plan.EvaluationAt
		}
		if in.Proposal != nil {
			warning := "复审沿用原评估时点 " + plan.EvaluationAt + "；资料仍保留新的实际采集时点，不是重建历史云状态。"
			if changedInstant {
				warning = "本次明确使用新的评估时点 " + plan.EvaluationAt + "；与原报告评估条件不同，比较须保留不可比原因，不能直接算作改善。"
			}
			in.Proposal.Warnings = append(in.Proposal.Warnings, warning)
		}
	}
	return reply, plan, true, err
}

func recheckQueryMatches(resources []domain.Resource, query domain.ResourceQuery) bool {
	if query.Group != "" {
		return false // current membership cannot replace a historical fixed set.
	}
	ids := queryIDs(query)
	if len(ids) > 0 && len(ids) != len(resources) {
		return false
	}
	for _, resource := range resources {
		if len(ids) > 0 && !hasString(ids, resource.ResourceID) || query.Name != "" && query.Name != resource.Name || query.ResourceType != "" && query.ResourceType != resource.ResourceType || (query.Region != "" || len(query.Regions) > 0) && !queryRegionMatches(query, resource.Region) {
			return false
		}
		for key, value := range query.Tags {
			actual, exists := resource.Tags[key]
			if !exists || value != "" && actual != value {
				return false
			}
		}
	}
	return true
}

func (s *IntentService) historyProposal(ctx context.Context, c storage.Conversation, in *domain.ConversationIntent, text, reportRef, findingRef string) (string, *storage.ExecutionPlan, error) {
	r, a, _, e := s.selectedReport(ctx, c.ID, reportRef)
	if e != nil {
		if e == storage.ErrNotFound {
			return "本会话还没有可解释的固定报告。可以先明确审计对象，或打开已有报告所在的会话。", nil, nil
		}
		return "", nil, e
	}
	reply := historicalReply(r, a, findingRef)
	if in.Mode != "rules_and_agent" || !s.Plans.ModelReady {
		return reply, nil, nil
	}
	resolved, e := s.Plans.ResolveWorkload("assistant_turn")
	if e != nil {
		return "", nil, e
	}
	if resolved.Limits.ModelHTTPAttempts == 0 || resolved.Limits.ModelLogicalCalls == 0 {
		return reply + "\n当前模型请求额度不可用，已保留规则证据解释。", nil, nil
	}
	selected, e := s.reportResources(ctx, a)
	if e != nil {
		return "", nil, e
	}
	in.Selection = selected
	in.Candidates = []domain.Resource{}
	in.Query = domain.ResourceQuery{}
	in.ResourceGroupVersionID = ""
	in.DataPolicy = domain.IntentDataPolicy{Mode: "pinned_snapshot", SnapshotID: a.SnapshotID}
	in.Action = "explain"
	in.Boundary, in.Direction = a.Input.Scope.Boundary, a.Input.Scope.Direction
	in.ContextReportID = r.ID
	in.Status = "ready_for_confirmation"
	effects := storage.EffectCeiling{AllowModelRequests: true, ModelHTTPAttempts: resolved.Limits.ModelHTTPAttempts}
	req, _ := json.Marshal(struct{ Text, ReportID string }{text, r.ID})
	p := &storage.ExecutionPlan{Kind: "assistant_turn", Workload: resolved, ConversationID: c.ID, PolicyScopeRef: a.Input.Scope.Ref, SnapshotID: a.SnapshotID, DataSourceID: "local-offline", Mode: "assistant", Request: req, ContextReportID: r.ID, EvaluationAt: a.Input.EvaluationAt, PreparedInput: a.Input, Heads: []storage.AssetHeadVersion{}, ConfigRevision: s.Plans.ConfigRevision, BudgetRef: s.Plans.BudgetRef, EffectCeiling: effects}
	in.Proposal = &domain.ActionProposal{Summary: "使用这份固定报告的原证据，接受后由受限模型解释依据、影响与缺项。", Gaps: []string{}, Warnings: []string{"固定报告解释始终零云读取，不把旧时点结论当当前状态。", "模型失败仍可查看原规则发现及证据。"}, Effects: domain.ProposalEffects{AllowModelRequests: true}, Status: "ready"}
	return reply + "\n已准备固定报告的模型解释计划，请核对原报告与模型效果后接受。", p, nil
}
