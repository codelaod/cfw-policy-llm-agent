package app

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"cfw-policy-llm-agent/server/internal/audit"
	"cfw-policy-llm-agent/server/internal/domain"
	"cfw-policy-llm-agent/server/internal/storage"
	"cfw-policy-llm-agent/server/internal/workload"
)

var (
	ErrSourceNotConfigured  = errors.New("read-only data source is not configured")
	ErrScopeDenied          = errors.New("scope or object is outside the selected input")
	ErrModelNotConfigured   = errors.New("model is not configured or authorized")
	ErrRecheckNotComparable = errors.New("recheck not comparable: object_missing")
)

type PlanSelection struct {
	Kind       string   `json:"kind" enums:"assets,gateway_policy,identity_only"`
	AssetIDs   []string `json:"asset_ids,omitempty"`
	GatewayRef string   `json:"gateway_ref,omitempty"`
}
type DataPolicy struct {
	Mode               string `json:"mode" enums:"pinned_snapshot,prefer_fresh,force_refresh"`
	SnapshotID         string `json:"snapshot_id,omitempty"`
	AllowStaleFallback bool   `json:"allow_stale_fallback"`
	FreshAfter         string `json:"fresh_after,omitempty"`
}
type PlanRequest struct {
	Kind               string        `json:"kind" enums:"audit,collect,connection_check"`
	ConversationID     string        `json:"conversation_id"`
	DataSourceID       string        `json:"data_source_id"`
	PolicyScopeRef     string        `json:"policy_scope_ref"`
	Selection          PlanSelection `json:"selection"`
	DataPolicy         DataPolicy    `json:"data_policy"`
	ProfileVersionIDs  []string      `json:"profile_version_ids,omitempty"`
	IntentVersionIDs   []string      `json:"intent_version_ids,omitempty"`
	BaselineVersionID  string        `json:"baseline_version_id"`
	Mode               string        `json:"mode" enums:"rules_only,rules_and_agent"`
	DisclosurePolicyID string        `json:"disclosure_policy_id,omitempty"`
	EvaluationAt       string        `json:"evaluation_at,omitempty"`
	RecheckOfJobID     string        `json:"-"`
}
type PlanPreview struct {
	storage.ExecutionPlan
	Gaps     []string `json:"gaps"`
	Warnings []string `json:"warnings"`
}
type AssistantRequest struct {
	ClientMessageID  string `json:"client_message_id"`
	PolicyScopeRef   string `json:"policy_scope_ref"`
	SnapshotID       string `json:"snapshot_id"`
	ReportID         string `json:"report_id,omitempty"`
	Text             string `json:"text"`
	ModelUseAccepted bool   `json:"model_use_accepted"`
}
type RecheckRequest struct {
	SnapshotID        string `json:"snapshot_id"`
	ConversationID    string `json:"conversation_id"`
	ReuseEvaluationAt *bool  `json:"reuse_evaluation_at,omitempty"`
}

// PlanService previews and freezes local effects. Presence of credentials or a
// URL is never a permission source; availability is explicitly wired by runtime.
type PlanService struct {
	Workloads         workload.Config
	ModelTimeout      time.Duration
	ModelLogicalCalls *int
	Store             *storage.Store
	ConfigRevision    string
	BudgetRef         string
	ModelReady        bool
	ModelConfigured   bool
	ModelHTTPAttempts int
	ModelMode         string
	Cloud             *CloudService
}

func NewPlanService(store *storage.Store) *PlanService {
	return &PlanService{Store: store, ConfigRevision: "offline-v1", BudgetRef: "application-default", ModelMode: "unconfigured"}
}

func (s *PlanService) Preview(ctx context.Context, req PlanRequest) (PlanPreview, error) {
	bad := func() (PlanPreview, error) { return PlanPreview{}, storage.ErrInputInvalid }
	if req.Kind != "audit" {
		if req.Kind == "collect" || req.Kind == "connection_check" {
			return s.previewCloud(ctx, req)
		}
		return bad()
	}
	if req.DataSourceID != "local-offline" {
		return PlanPreview{}, ErrSourceNotConfigured
	}
	if req.Mode != "rules_only" && req.Mode != "rules_and_agent" {
		return bad()
	}
	if req.BaselineVersionID != audit.ProjectBaseline().Version || req.DataPolicy.SnapshotID == "" || req.DataPolicy.AllowStaleFallback || req.DataPolicy.FreshAfter != "" {
		return bad()
	}
	if req.DataPolicy.Mode != "pinned_snapshot" {
		if req.DataPolicy.Mode == "prefer_fresh" || req.DataPolicy.Mode == "force_refresh" {
			return PlanPreview{}, ErrSourceNotConfigured
		}
		return bad()
	}
	if req.EvaluationAt != "" && req.RecheckOfJobID == "" {
		return bad()
	}
	if req.DisclosurePolicyID != "" && req.DisclosurePolicyID != "minimal-v1" {
		return bad()
	}
	conversation, err := s.Store.GetConversation(ctx, req.ConversationID)
	if err != nil {
		return PlanPreview{}, err
	}
	if conversation.Archived {
		return PlanPreview{}, storage.ErrConversationArchived
	}
	snapshot, err := s.Store.GetSnapshot(ctx, req.DataPolicy.SnapshotID)
	if err != nil {
		return PlanPreview{}, err
	}
	if req.PolicyScopeRef != snapshot.Input.Scope.Ref || conversation.PolicyScopeRef != req.PolicyScopeRef {
		return PlanPreview{}, ErrScopeDenied
	}
	resolved, err := s.ResolveWorkload("audit")
	if err != nil {
		return PlanPreview{}, err
	}
	if req.Selection.Kind == "assets" {
		if len(req.Selection.AssetIDs) == 0 || len(req.Selection.AssetIDs) > resolved.Limits.AssetsPerBatch || req.Selection.GatewayRef != "" {
			return bad()
		}
		seen := map[string]bool{}
		known := map[string]bool{}
		for _, asset := range snapshot.Input.Assets {
			known[asset.ID] = true
		}
		for _, id := range req.Selection.AssetIDs {
			if seen[id] || !known[id] {
				return PlanPreview{}, ErrScopeDenied
			}
			seen[id] = true
		}
	} else if req.Selection.Kind == "gateway_policy" {
		if snapshot.Input.Scope.Boundary != "nat" || len(req.Selection.AssetIDs) != 0 || (req.Selection.GatewayRef != "" && req.Selection.GatewayRef != snapshot.Input.Scope.GatewayRef) {
			return bad()
		}
	} else {
		return bad()
	}
	resolvedInputLimits, inputPolicyErr := s.ResolveWorkload("audit")
	if inputPolicyErr != nil {
		return PlanPreview{}, inputPolicyErr
	}
	var objectErr error
	ctx, objectErr = audit.WithInputObjectLimits(ctx, resolvedInputLimits.Limits.AssetsPerBatch, resolvedInputLimits.Limits.MaxPolicies, resolvedInputLimits.Limits.MaxQueryCells)
	if objectErr != nil {
		return bad()
	}
	in, heads, err := s.Store.FreezeSelection(ctx, snapshot.ID, domain.Selection{Kind: req.Selection.Kind, AssetIDs: append([]string(nil), req.Selection.AssetIDs...)})
	if err != nil {
		return PlanPreview{}, err
	}
	if err = audit.ValidateContext(ctx, in); err != nil {
		return PlanPreview{}, err
	}
	if !requestedVersions(req.ProfileVersionIDs, in.ProfileVersion, heads, true) || !requestedVersions(req.IntentVersionIDs, in.IntentVersion, heads, false) {
		return PlanPreview{}, storage.ErrPlanStale
	}
	effects := storage.EffectCeiling{}
	gaps := []string{}
	warnings := []string{"仅分析固定输入时点的配置，不证明实际监听或端到端可达。"}
	if in.SourceKind == "imported" {
		warnings = append(warnings, "导入声明条件下：来源与完整性未经真实云观测核实。")
	}
	if req.Mode == "rules_and_agent" {
		if s.ModelReady {
			allowance := resolved.Limits.ModelHTTPAttempts
			effects.AllowModelRequests = allowance > 0
			effects.ModelHTTPAttempts = allowance
			if allowance == 0 {
				gaps = append(gaps, "MODEL_BUDGET_BLOCKED")
			}
		} else {
			gaps = append(gaps, "MODEL_NOT_CONFIGURED")
			warnings = append(warnings, "模型未配置或未获授权；将保留规则结果并标记部分完成。")
		}
	}
	encoded, err := json.Marshal(req)
	if err != nil {
		return PlanPreview{}, err
	}
	plan, err := s.Store.SavePlan(ctx, storage.ExecutionPlan{Workload: resolved, Kind: "audit", ConversationID: conversation.ID, DataSourceID: "local-offline", PolicyScopeRef: in.Scope.Ref, SnapshotID: snapshot.ID, Mode: req.Mode, ConversationRevision: conversation.Revision, RecheckOfJobID: req.RecheckOfJobID, EvaluationAt: req.EvaluationAt, Request: encoded, EffectCeiling: effects, PreparedInput: in, Heads: heads, ConfigRevision: s.ConfigRevision, BudgetRef: s.BudgetRef})
	return PlanPreview{ExecutionPlan: plan, Gaps: gaps, Warnings: warnings}, err
}

// ResolveWorkload combines the explicit profile with private connection ceilings.
// ModelConfigured distinguishes a deliberate zero allowance from old callers
// that supplied no model configuration at all.
func (s *PlanService) ResolveWorkload(kind string) (workload.Resolved, error) {
	ceilings := workload.Ceilings{ModelLogicalCalls: s.ModelLogicalCalls}
	if s.Cloud != nil && s.Cloud.Configured {
		n := s.Cloud.MaxHTTPAttempts
		ceilings.CloudHTTPAttempts = &n
	}
	if s.ModelConfigured || s.ModelHTTPAttempts != 0 {
		value := s.ModelHTTPAttempts
		ceilings.ModelHTTPAttempts = &value
	}
	if s.ModelTimeout < 0 {
		return workload.Resolved{}, storage.ErrInputInvalid
	}
	if s.ModelTimeout > 0 {
		seconds := int(s.ModelTimeout / time.Second)
		if s.ModelTimeout%time.Second != 0 {
			seconds++
		}
		ceilings.ModelRequestSeconds = &seconds
	}
	resolved, err := s.Workloads.Resolve(kind, ceilings)
	if err != nil {
		return workload.Resolved{}, storage.ErrInputInvalid
	}
	return resolved, nil
}

// modelAllowance retains old internal call sites while resolving the profile;
// the former numeric argument no longer supplies a hidden 4/6 ceiling.
func (s *PlanService) modelAllowance(_ int) (int, error) {
	resolved, err := s.ResolveWorkload("audit")
	if err != nil {
		return 0, err
	}
	return resolved.Limits.ModelHTTPAttempts, nil
}

func requestedVersions(ids []string, aggregate string, heads []storage.AssetHeadVersion, profile bool) bool {
	if len(ids) == 0 {
		return true
	}
	if len(ids) == 1 && ids[0] == aggregate {
		return true
	}
	expected := []string{}
	for _, head := range heads {
		value := head.IntentVersionID
		if profile {
			value = head.ProfileVersionID
		}
		if value != "" {
			expected = append(expected, value)
		}
	}
	if len(expected) != len(ids) {
		return false
	}
	actual := append([]string(nil), ids...)
	sort.Strings(actual)
	sort.Strings(expected)
	for i := range actual {
		if actual[i] != expected[i] || (i > 0 && actual[i] == actual[i-1]) {
			return false
		}
	}
	return true
}

func (s *PlanService) RecheckPreview(ctx context.Context, oldJobID string, request RecheckRequest) (PlanPreview, error) {
	job, old, err := s.Store.ReadJobAudit(ctx, oldJobID)
	if err != nil {
		return PlanPreview{}, err
	}
	if old == nil || job.Kind != "audit" {
		return PlanPreview{}, storage.ErrInputInvalid
	}
	if request.ConversationID == "" {
		request.ConversationID = job.ConversationID
	}
	snapshot, err := s.Store.GetSnapshot(ctx, request.SnapshotID)
	if err != nil {
		return PlanPreview{}, err
	}
	if old.Input.Selection.Kind == "assets" {
		known := map[string]bool{}
		for _, asset := range snapshot.Input.Assets {
			known[asset.ID] = true
		}
		for _, id := range old.Input.Selection.AssetIDs {
			if !known[id] {
				// Missing old objects are a comparability limit. Do not silently
				// shrink the selection or replace it with all current objects.
				return PlanPreview{}, ErrRecheckNotComparable
			}
		}
	}
	previous, err := s.Store.GetPlan(ctx, job.PlanID)
	if err != nil {
		return PlanPreview{}, err
	}
	selection := PlanSelection{Kind: old.Input.Selection.Kind, AssetIDs: append([]string(nil), old.Input.Selection.AssetIDs...)}
	if selection.Kind == "gateway_policy" {
		selection.GatewayRef = snapshot.Input.Scope.GatewayRef
	}
	req := PlanRequest{Kind: "audit", ConversationID: request.ConversationID, DataSourceID: "local-offline", PolicyScopeRef: snapshot.Input.Scope.Ref, Selection: selection, DataPolicy: DataPolicy{Mode: "pinned_snapshot", SnapshotID: snapshot.ID}, BaselineVersionID: audit.ProjectBaseline().Version, Mode: previous.Mode, RecheckOfJobID: job.ID}
	if request.ReuseEvaluationAt == nil || *request.ReuseEvaluationAt {
		req.EvaluationAt = old.Input.EvaluationAt
	}
	return s.Preview(ctx, req)
}

func (s *PlanService) StartAssistant(ctx context.Context, engine *Engine, conversationID string, req AssistantRequest) (storage.Job, storage.ConversationMessage, bool, error) {
	invalid := func(err error) (storage.Job, storage.ConversationMessage, bool, error) {
		return storage.Job{}, storage.ConversationMessage{}, false, err
	}
	if strings.TrimSpace(req.Text) == "" || len(req.Text) > 16<<10 || req.ClientMessageID == "" || len(req.ClientMessageID) > 128 {
		return invalid(storage.ErrInputInvalid)
	}
	request, err := json.Marshal(struct {
		SnapshotID string `json:"snapshot_id"`
		ReportID   string `json:"report_id,omitempty"`
		Accepted   bool   `json:"model_use_accepted"`
	}{req.SnapshotID, req.ReportID, req.ModelUseAccepted})
	if err != nil {
		return invalid(storage.ErrInputInvalid)
	}
	plan := storage.ExecutionPlan{Kind: "assistant_turn", ConversationID: conversationID, PolicyScopeRef: req.PolicyScopeRef, SnapshotID: req.SnapshotID, Mode: "assistant", Request: request, ContextReportID: req.ReportID}
	message := storage.MessageInput{ClientMessageID: req.ClientMessageID, Role: "user", Origin: "user", PublicText: req.Text, PolicyScopeRef: req.PolicyScopeRef, RefKind: "snapshot", RefID: req.SnapshotID}
	if job, saved, found, replayErr := s.Store.FindAssistantReplay(ctx, plan, message); replayErr != nil || found {
		return job, saved, found, replayErr
	}
	if !req.ModelUseAccepted {
		return invalid(storage.ErrInputInvalid)
	}
	if !s.ModelReady || engine == nil {
		return invalid(ErrModelNotConfigured)
	}
	resolved, err := s.ResolveWorkload("assistant_turn")
	if err != nil {
		return invalid(err)
	}
	allowance := resolved.Limits.ModelHTTPAttempts
	if allowance == 0 || resolved.Limits.ModelLogicalCalls == 0 {
		return invalid(ErrModelNotConfigured)
	}
	plan.Workload = resolved
	conversation, err := s.Store.GetConversation(ctx, conversationID)
	if err != nil {
		return invalid(err)
	}
	if conversation.Archived {
		return invalid(storage.ErrConversationArchived)
	}
	var input domain.Input
	var heads []storage.AssetHeadVersion
	if req.ReportID != "" {
		report, err := s.Store.GetReport(ctx, req.ReportID)
		if err != nil {
			return invalid(err)
		}
		run, err := s.Store.GetAudit(ctx, report.AuditID)
		if err != nil {
			return invalid(err)
		}
		if run.SnapshotID != req.SnapshotID || run.Input.Scope.Ref != req.PolicyScopeRef {
			return invalid(ErrScopeDenied)
		}
		input = run.Input
	} else {
		if conversation.PolicyScopeRef != req.PolicyScopeRef {
			return invalid(ErrScopeDenied)
		}
		snapshot, err := s.Store.GetSnapshot(ctx, req.SnapshotID)
		if err != nil {
			return invalid(err)
		}
		if snapshot.Input.Scope.Ref != req.PolicyScopeRef {
			return invalid(ErrScopeDenied)
		}
		input, heads, err = s.Store.FreezeSelection(ctx, snapshot.ID, snapshot.Input.Selection)
		if err != nil {
			return invalid(err)
		}
	}
	plan.DataSourceID = "local-offline"
	plan.ConversationRevision = conversation.Revision
	plan.PreparedInput = input
	plan.Heads = heads
	plan.ConfigRevision = s.ConfigRevision
	plan.BudgetRef = s.BudgetRef
	plan.EffectCeiling = storage.EffectCeiling{AllowModelRequests: true, ModelHTTPAttempts: allowance}
	if req.ReportID != "" {
		plan.EvaluationAt = input.EvaluationAt
	}
	return engine.EnqueueAssistant(ctx, plan, message)
}
