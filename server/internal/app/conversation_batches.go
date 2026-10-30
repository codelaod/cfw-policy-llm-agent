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

func (s *IntentService) prepareBatchProposal(ctx context.Context, c storage.Conversation, in *domain.ConversationIntent, resolved workload.Resolved) (string, *storage.ExecutionPlan, error) {
	return s.prepareBatchResources(ctx, c, in, resolved, in.Selection)
}

// Explicit IDs with one region are finite user selectors, not observed catalog
// rows. Keep them in the accepted preparation plan until provider observations
// establish their identities; do not manufacture directory membership.
func (s *IntentService) prepareExplicitBatchProposal(ctx context.Context, c storage.Conversation, in *domain.ConversationIntent, resolved workload.Resolved) (string, *storage.ExecutionPlan, error) {
	cloud := s.Plans.Cloud
	regions := queryRegions(in.Query)
	if len(regions) == 0 && cloud != nil {
		regions = append([]string(nil), cloud.Grant.Regions...)
	}
	if cloud == nil || in.DataPolicy.Mode == "pinned_snapshot" || in.Query.ResourceType != "ecs" || len(regions) != 1 || in.Query.Name != "" || len(in.Query.Tags) != 0 || in.Query.Group != "" {
		in.Status = "needs_clarification"
		return "完整ID集合尚未定位到唯一地域。请明确这些ID所在的一个地域，或先按地域分别定位；不会将多地域和ID任意组合后分批。", nil, nil
	}
	requested := []domain.Resource{}
	for _, id := range queryIDs(in.Query) {
		requested = append(requested, domain.Resource{SourceRef: cloud.SourceRef, Source: "live_readonly", AccountRef: cloud.Grant.AccountRef, ResourceID: id, Region: regions[0], ResourceType: "ecs"})
	}
	reply, plan, err := s.prepareBatchResources(ctx, c, in, resolved, requested)
	if plan != nil && in.Proposal != nil {
		in.Proposal.RequiresDiscovery = true
		in.Proposal.Warnings = append([]string{"这些是对话指定的ID及地域，尚非已观测目录结果；各批必须先核验返回身份、范围和资料，缺失或不完整不会当作通过。"}, in.Proposal.Warnings...)
	}
	return reply, plan, err
}

func (s *IntentService) prepareBatchResources(ctx context.Context, c storage.Conversation, in *domain.ConversationIntent, resolved workload.Resolved, selected []domain.Resource) (string, *storage.ExecutionPlan, error) {
	if in.Boundary != "internet" || in.Action == "recheck" || in.RecheckOfJobID != "" {
		in.Status = "needs_clarification"
		return "首版分批只处理一个互联网策略分区的明确ECS集合。NAT请选一个网关；复审请从原批段报告选择对应对象，不会把一个旧审计硬套所有新批段。", nil, nil
	}
	resources := []storage.PreparedResource{}
	sameSource := ""
	account := ""
	cloud := s.Plans.Cloud
	live := in.DataPolicy.Mode != "pinned_snapshot" && cloud != nil && cloud.Configured
	for _, r := range selected {
		if account != "" && r.AccountRef != account || sameSource != "" && r.SourceRef != sameSource {
			return "", nil, ErrScopeDenied
		}
		account, sameSource = r.AccountRef, r.SourceRef
		live = live && r.SourceRef == cloud.SourceRef && r.Source == "live_readonly"
		resources = append(resources, storage.PreparedResource{ResourceID: r.ResourceID, Region: r.Region, ResourceType: r.ResourceType, AssetID: r.LocalAssetID})
	}
	if len(resources) <= resolved.Limits.AssetsPerBatch {
		return "", nil, storage.ErrInputInvalid
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
	p := &storage.ExecutionPlan{Batch: &storage.BatchSpec{Resources: resources, Size: resolved.Limits.AssetsPerBatch}, ResourceGroupVersionID: in.ResourceGroupVersionID, Workload: resolved, Kind: "audit", ConversationID: c.ID, Mode: in.Mode, ConfigRevision: s.Plans.ConfigRevision, BudgetRef: s.Plans.BudgetRef, EffectCeiling: effects}
	if live {
		if !cloud.Ready || resolved.Limits.CloudHTTPAttempts <= 0 {
			in.Status = "needs_data"
			return "当前不能为完整集合准备所有批次。请恢复连接／额度，或收窄到已有合格固定资料的对象。", nil, nil
		}
		cfg, _, e := cloud.preparationConfig(in.Boundary, in.Direction, selected)
		if e != nil {
			return "", nil, e
		}
		ids, regions := []string{}, []string{}
		for i, r := range selected {
			if r.ResourceID == "" || r.Region == "" || r.ResourceType != "ecs" {
				return "", nil, ErrScopeDenied
			}
			if p.Batch.Resources[i].AssetID == "" {
				p.Batch.Resources[i].AssetID = r.ResourceID
			}
			ids = append(ids, r.ResourceID)
			if !hasString(regions, r.Region) {
				regions = append(regions, r.Region)
			}
		}
		sort.Strings(regions)
		p.DataSourceID = cloud.SourceRef
		p.PolicyScopeRef = cfg.Scope.Ref
		p.PreparedInput = domain.Input{Scope: cfg.Scope, Baseline: audit.ProjectBaseline()}
		p.EffectCeiling.AllowCloudReads = true
		p.EffectCeiling.CloudHTTPAttempts = resolved.Limits.CloudHTTPAttempts
		p.Preparation = &storage.PreparationSpec{SourceRef: cloud.SourceRef, ConfigRevision: p.ConfigRevision, Query: domain.ResourceQuery{ResourceIDs: ids, Regions: regions, ResourceType: "ecs"}, Selected: resources, Boundary: in.Boundary, Direction: in.Direction, DataPolicy: in.DataPolicy.Mode, FreshAfter: in.DataPolicy.FreshAfter, AllowMultiple: true, ResultLimit: len(resources)}
	} else {
		if in.DataPolicy.Mode == "force_refresh" {
			in.Status = "needs_data"
			return "人工或导入资料不能伪装成强刷新。请核对当前云连接中的明确对象，或选择固定历史资料。", nil, nil
		}
		refs := []string{}
		for _, r := range selected {
			refs = append(refs, r.ID)
		}
		bindings, e := s.Store.ResourceBindings(ctx, refs, in.Boundary, in.Direction)
		if e != nil {
			return "", nil, e
		}
		bySnapshot := map[string]map[string]string{}
		order := []string{}
		for _, b := range bindings {
			if bySnapshot[b.SnapshotID] == nil {
				bySnapshot[b.SnapshotID] = map[string]string{}
				order = append(order, b.SnapshotID)
			}
			bySnapshot[b.SnapshotID][b.ResourceRef] = b.AssetID
		}
		chosen := ""
		for _, id := range order {
			if in.DataPolicy.Mode == "pinned_snapshot" && id != in.DataPolicy.SnapshotID {
				continue
			}
			if len(bySnapshot[id]) == len(refs) {
				chosen = id
				break
			}
		}
		if chosen == "" {
			in.Status = "needs_data"
			return "尚无覆盖完整集合的固定资料。请先核对对象或准备当前连接中的资料，不会只审已找到的部分。", nil, nil
		}
		snapshot, e := s.Store.GetSnapshot(ctx, chosen)
		if e != nil {
			return "", nil, e
		}
		if snapshot.SourceKind == "live_readonly" && in.DataPolicy.Mode != "pinned_snapshot" {
			in.Status = "needs_data"
			return "这份云资料尚未通过本次当前状态的资格检查，不能改称离线资料来绕过。请核对当前连接，或明确选择固定历史资料。", nil, nil
		}
		selection := domain.Selection{Kind: "assets", AssetIDs: []string{}}
		for i, ref := range refs {
			asset := bySnapshot[chosen][ref]
			selection.AssetIDs = append(selection.AssetIDs, asset)
			p.Batch.Resources[i].AssetID = asset
			p.Batch.Resources[i].ResourceType = "ecs"
		}
		prepared, heads, e := s.Store.FreezeSelection(ctx, chosen, selection)
		if e != nil {
			return "", nil, e
		}
		bounded, e := audit.WithInputObjectLimits(ctx, len(resources), resolved.Limits.MaxPolicies, resolved.Limits.MaxQueryCells)
		if e != nil {
			return "", nil, e
		}
		if e = audit.ValidateContext(bounded, prepared); e != nil {
			return "", nil, e
		}
		p.DataSourceID = "local-offline"
		p.PolicyScopeRef = prepared.Scope.Ref
		p.SnapshotID = chosen
		p.PreparedInput = prepared
		p.Heads = heads
	}
	p.Request, _ = json.Marshal(struct {
		Kind, Conversation, Mode string
		DataPolicy               domain.IntentDataPolicy
		Resources                []storage.PreparedResource
		BatchSize                int
	}{"audit", c.ID, in.Mode, in.DataPolicy, p.Batch.Resources, p.Batch.Size})
	sourceWarning := "资料将在当前连接的明确对象范围内逐批准备，各批段记录实际原始时点与来源。"
	if p.Preparation == nil {
		sourceWarning = fmt.Sprintf("固定资料来源%s，原采集／构造时点%s；固定历史或人工声明不等当前云配置，复用不改原时点。", p.PreparedInput.SourceKind, p.PreparedInput.CollectedAt)
	}
	count := (len(resources) + p.Batch.Size - 1) / p.Batch.Size
	in.Status = "ready_for_confirmation"
	in.Proposal = &domain.ActionProposal{Summary: fmt.Sprintf("完整选择%d个ECS，在同一%s/%s分区内串行处理%d批，每批最多%d个。", len(resources), in.Boundary, in.Direction, count, p.Batch.Size), Gaps: gaps, Warnings: []string{sourceWarning, "全部批段共用这次接受的总期限、模型／云额度及一个会话活动位，不按批复制额度。", "各批段保留独立时点、固定输入与报告；父请求明确列出已有审计证据、失败与尚未覆盖对象，不合成一个采集时点。", "任一阶段耗尽或不可恢复失败时停止新增批段，已有报告仍可查看。"}, Effects: domain.ProposalEffects{AllowCloudReads: p.EffectCeiling.AllowCloudReads, AllowModelRequests: p.EffectCeiling.AllowModelRequests}, Status: "ready"}
	return "已准备完整范围的串行分批计划，请核对所有对象、批数、资料与总效果后接受。接受一次后自动推进，不需要逐批点击。", p, nil
}
