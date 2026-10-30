package app

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"cfw-policy-llm-agent/server/internal/audit"
	"cfw-policy-llm-agent/server/internal/domain"
	"cfw-policy-llm-agent/server/internal/storage"
	"cfw-policy-llm-agent/server/internal/workload"
)

// prepareProposal is local only. Discovery is one accepted effect of the audit;
// unknown/ambiguous results return to clarification rather than choose a match.
func (s *IntentService) prepareProposal(ctx context.Context, c storage.Conversation, in *domain.ConversationIntent) (string, *storage.ExecutionPlan, error) {
	cloud := s.Plans.Cloud
	if in.DataPolicy.Mode == "pinned_snapshot" {
		in.Status = "needs_data"
		return "指定的历史资料未覆盖所选对象，请核对快照和分区。", nil, nil
	}
	if cloud == nil || !cloud.Configured || cloud.ResolveScope == nil || !hasString(cloud.Grant.Boundaries, in.Boundary+"/"+in.Direction) {
		in.Status = "needs_data"
		in.Proposal = &domain.ActionProposal{Summary: "目标已保留，当前缺少所选分区的数据连接或固定资料。", Gaps: []string{"CLOUD_SOURCE_UNAVAILABLE"}, Warnings: []string{"不会凭空生成资源或配置。"}, Status: "blocked"}
		return "当前没有可用资料连接。可以继续核对对象，或导入明确标源的固定资料；连接配置只需账号凭据与权限上界，不要求逐台登记资产。", nil, nil
	}
	resolved, e := s.Plans.ResolveWorkload("audit")
	if e != nil {
		return "", nil, e
	}
	q := in.Query
	if q.ResourceType == "" {
		if in.Boundary == "nat" {
			q.ResourceType = "nat_gateway"
		} else {
			q.ResourceType = "ecs"
		}
	}
	if q.Group != "" && in.ResourceGroupVersionID == "" {
		in.Status = "needs_clarification"
		return "业务组必须有已确认归属；请先确认组成员，或列出名称／ID。", nil, nil
	}
	regions := queryRegions(q)
	if len(regions) == 0 {
		regions = append([]string(nil), cloud.Grant.Regions...)
	}
	for _, region := range regions {
		if !hasString(cloud.Grant.Regions, region) {
			in.Status = "needs_clarification"
			return "所述地域超出当前连接范围，请收窄到已连接地域。", nil, nil
		}
	}
	selected := []storage.PreparedResource{}
	resources := []domain.Resource{}
	for _, r := range in.Selection {
		if r.SourceRef != cloud.SourceRef || r.AccountRef != cloud.Grant.AccountRef || r.ResourceID == "" || r.Region == "" {
			in.Status = "needs_clarification"
			return "已选对象没有当前连接下的云身份，不能将本地资料ID当云ID发送。请提供云资源ID或使用明确历史资料。", nil, nil
		}
		selected = append(selected, storage.PreparedResource{ResourceID: r.ResourceID, Region: r.Region, ResourceType: r.ResourceType, AssetID: r.LocalAssetID})
		if in.Boundary == "nat" {
			selected[len(selected)-1].AssetID = ""
		}
		resources = append(resources, r)
	}
	if len(resources) > 0 {
		q.Name, q.Group, q.Tags = "", "", nil
		q.ResourceID, q.ResourceIDs = "", []string{}
		regions = []string{}
		for _, r := range resources {
			q.ResourceIDs = append(q.ResourceIDs, r.ResourceID)
			if !hasString(regions, r.Region) {
				regions = append(regions, r.Region)
			}
		}
		if len(q.ResourceIDs) == 1 {
			q.ResourceID = q.ResourceIDs[0]
			q.ResourceIDs = nil
		}
	}
	sort.Strings(regions)
	q.Region, q.Regions = "", nil
	if len(regions) == 1 {
		q.Region = regions[0]
	} else {
		q.Regions = regions
	}
	if q.Name == "" && len(queryIDs(q)) == 0 && len(q.Tags) == 0 && !(in.AllowMultiple && q.ResourceType == "ecs" && len(queryRegions(in.Query)) > 0) {
		in.Status = "needs_clarification"
		return "请给出资源名称、ID或有限标签条件，不能直接把整个账号当成本次审计对象。", nil, nil
	}
	multiple := in.AllowMultiple || len(selected) > 1 || len(queryIDs(q)) > 1
	limit := 1
	if multiple {
		limit = resolved.Limits.AssetsPerBatch
	}
	if in.Boundary == "nat" && multiple {
		in.Status = "needs_clarification"
		return "NAT首版一次分析一个网关，请从候选中选择一个。", nil, nil
	}
	if len(selected) > limit || len(queryIDs(q)) > limit {
		if in.BatchRequested && len(selected) == 0 && len(queryIDs(q)) > limit {
			in.Query = q
			return s.prepareExplicitBatchProposal(ctx, c, in, resolved)
		}
		in.Status = "needs_batch"
		return "本次对象数超过单批保护值，请核对同分区分批范围。不会只审前几个。", nil, nil
	}
	scope := domain.Scope{AccountRef: cloud.Grant.AccountRef, ControlPlaneRef: cloud.Grant.ControlPlaneRef, Boundary: in.Boundary, Direction: in.Direction}
	if in.Boundary == "internet" || len(resources) > 0 {
		cfg, _, err := cloud.preparationConfig(in.Boundary, in.Direction, resources)
		if err != nil {
			in.Status = "needs_clarification"
			return "对象或边界超出当前连接范围，请核对名称、地域与类型。", nil, nil
		}
		scope = cfg.Scope
	}
	if !cloud.Ready || resolved.Limits.CloudHTTPAttempts <= 0 {
		// A blocked adapter still permits a qualified local product; no new effect
		// is introduced when its existing snapshot already meets current policy.
		if len(resources) > 0 && in.DataPolicy.Mode == "prefer_fresh" {
			cfg, sel, err := cloud.preparationConfig(in.Boundary, in.Direction, resources)
			if err == nil {
				reuse, err := FindEligibleSnapshot(ctx, s.Store, cloud.reuseRequest(cfg.Scope, sel, "prefer_fresh", ""), 100)
				if err != nil {
					return "", nil, err
				}
				if reuse.Reuse {
					return s.fixedResourcePlan(ctx, c, in, reuse.SnapshotID, sel, resolved)
				}
			}
		}
		in.Status = "needs_data"
		in.Proposal = &domain.ActionProposal{Summary: "需要新资料，但当前连接或云请求授权不可用。", Gaps: []string{"CLOUD_READ_BLOCKED"}, Warnings: []string{"旧资料不会被当作刷新成功。"}, Status: "blocked"}
		return "已保留目标；所需资料不合格或缺失，当前不能刷新。请在连接恢复后重试，或明确选择历史快照分析。", nil, nil
	}
	effects := storage.EffectCeiling{AllowCloudReads: true, CloudHTTPAttempts: resolved.Limits.CloudHTTPAttempts}
	gaps := []string{}
	if in.Mode == "rules_and_agent" {
		if s.Plans.ModelReady && resolved.Limits.ModelHTTPAttempts > 0 {
			effects.AllowModelRequests = true
			effects.ModelHTTPAttempts = resolved.Limits.ModelHTTPAttempts
		} else {
			gaps = append(gaps, "MODEL_NOT_AVAILABLE")
		}
	}
	spec := &storage.PreparationSpec{SourceRef: cloud.SourceRef, ConfigRevision: s.Plans.ConfigRevision, Query: q, Selected: selected, Boundary: in.Boundary, Direction: in.Direction, DataPolicy: in.DataPolicy.Mode, FreshAfter: in.DataPolicy.FreshAfter, AllowMultiple: multiple, ResultLimit: limit}
	request, _ := json.Marshal(struct {
		Kind, ConversationID, Mode string
		Query                      domain.ResourceQuery
		DataPolicy                 domain.IntentDataPolicy
	}{"audit", c.ID, in.Mode, q, in.DataPolicy})
	p := &storage.ExecutionPlan{ResourceGroupVersionID: in.ResourceGroupVersionID, Workload: resolved, Preparation: spec, Kind: "audit", RecheckOfJobID: in.RecheckOfJobID, ConversationID: c.ID, DataSourceID: cloud.SourceRef, PolicyScopeRef: scope.Ref, Mode: in.Mode, Request: request, EffectCeiling: effects, PreparedInput: domain.Input{Scope: scope, Baseline: audit.ProjectBaseline()}, ConfigRevision: s.Plans.ConfigRevision, BudgetRef: s.Plans.BudgetRef}
	in.Status = "ready_for_confirmation"
	summary := fmt.Sprintf("在%d个明确地域内定位%s，准备%s/%s资料并审计。", len(regions), q.ResourceType, in.Boundary, in.Direction)
	in.Proposal = &domain.ActionProposal{Summary: summary, Gaps: gaps, Warnings: []string{"接受后才可能进行只读目录查询／采集；歧义或结果未穷尽时停止并请你澄清。", "采集与审计属于同一任务，共用原请求预算和期限。", "配置允许不证明实际监听或端到端可达。"}, RequiresDiscovery: len(selected) == 0, Effects: domain.ProposalEffects{AllowCloudReads: true, AllowModelRequests: effects.AllowModelRequests}, Status: "ready"}
	return "请核对本次对象条件、地域、边界、资料策略与模型模式。开始后会自动检查资料资格，必要时只读获取所需资料；不会要求你后台逐台预配。", p, nil
}

func (s *IntentService) fixedResourcePlan(ctx context.Context, c storage.Conversation, in *domain.ConversationIntent, snapshot string, selection domain.Selection, resolved workload.Resolved) (string, *storage.ExecutionPlan, error) {
	prepared, heads, e := s.Store.FreezeSelection(ctx, snapshot, selection)
	if e != nil {
		return "", nil, e
	}
	effects := storage.EffectCeiling{}
	gaps := []string{}
	if in.Mode == "rules_and_agent" {
		if s.Plans.ModelReady && resolved.Limits.ModelHTTPAttempts > 0 {
			effects.AllowModelRequests = true
			effects.ModelHTTPAttempts = resolved.Limits.ModelHTTPAttempts
		} else {
			gaps = append(gaps, "MODEL_NOT_AVAILABLE")
		}
	}
	request := PlanRequest{Kind: "audit", RecheckOfJobID: in.RecheckOfJobID, ConversationID: c.ID, DataSourceID: "local-offline", PolicyScopeRef: prepared.Scope.Ref, Selection: PlanSelection{Kind: selection.Kind, AssetIDs: selection.AssetIDs}, DataPolicy: DataPolicy{Mode: "pinned_snapshot", SnapshotID: snapshot}, Mode: in.Mode, BaselineVersionID: audit.ProjectBaseline().Version}
	raw, _ := json.Marshal(request)
	p := &storage.ExecutionPlan{ResourceGroupVersionID: in.ResourceGroupVersionID, Workload: resolved, Kind: "audit", RecheckOfJobID: in.RecheckOfJobID, ConversationID: c.ID, DataSourceID: "local-offline", PolicyScopeRef: prepared.Scope.Ref, SnapshotID: snapshot, Mode: in.Mode, Request: raw, EffectCeiling: effects, PreparedInput: prepared, Heads: heads, ConfigRevision: s.Plans.ConfigRevision, BudgetRef: s.Plans.BudgetRef}
	in.Status = "ready_for_confirmation"
	in.Proposal = &domain.ActionProposal{Summary: "已有资料通过当前时效与来源检查；使用固定输入执行审计。", Gaps: gaps, Warnings: []string{"复用保留原始采集时点，不是本次重新采集。", "当前云连接不可刷新，开始前仍请核对资料时点。"}, Effects: domain.ProposalEffects{AllowModelRequests: effects.AllowModelRequests}, Status: "ready"}
	return "所选对象已有合格资料，本次不产生云请求。请核对原始资料时点与模型模式后开始。", p, nil
}
