package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"sort"
	"strings"

	"cfw-policy-llm-agent/server/internal/aliyuncollector"
	"cfw-policy-llm-agent/server/internal/audit"
	"cfw-policy-llm-agent/server/internal/domain"
	"cfw-policy-llm-agent/server/internal/outbound"
	"cfw-policy-llm-agent/server/internal/storage"
)

// CloudReader is internal to accepted cloud jobs. A model or HTTP handler can
// never obtain this interface, credentials, an endpoint, or operation selection.
type CloudReader interface {
	aliyuncollector.Reader
	VerifyIdentity(context.Context) error
}

type CloudService struct {
	Store           *storage.Store
	SourceRef       string
	ConfigRevision  string
	Configured      bool
	Ready           bool
	MaxHTTPAttempts int
	SensitiveValues []string
	Scopes          map[string]aliyuncollector.Config
	NewReader       func(context.Context, string) (CloudReader, error)
	collect         func(context.Context, CloudReader, aliyuncollector.Config, domain.Selection) (aliyuncollector.Collection, error)
}

type CollectionAsset struct {
	ID     string `json:"id"`
	Region string `json:"region"`
}
type CollectionScope struct {
	Scope  domain.Scope      `json:"scope"`
	Assets []CollectionAsset `json:"assets"`
}
type CollectionOptions struct {
	SourceRef        string            `json:"source_ref"`
	Configured       bool              `json:"configured"`
	Available        bool              `json:"available"`
	IdentityVerified bool              `json:"identity_verified"`
	MaxHTTPAttempts  int               `json:"max_http_attempts"`
	Scopes           []CollectionScope `json:"scopes"`
}

// Options is a local configuration view. It does not check an identity, refresh
// credentials, enumerate products, or imply that a previous identity is current.
func (s *CloudService) Options() CollectionOptions {
	out := CollectionOptions{Scopes: []CollectionScope{}}
	if s == nil {
		return out
	}
	out.SourceRef, out.Configured, out.Available, out.MaxHTTPAttempts = s.SourceRef, s.Configured, s.Ready, s.MaxHTTPAttempts
	refs := make([]string, 0, len(s.Scopes))
	for ref := range s.Scopes {
		refs = append(refs, ref)
	}
	sort.Strings(refs)
	for _, ref := range refs {
		cfg := s.Scopes[ref]
		item := CollectionScope{Scope: cfg.Scope, Assets: []CollectionAsset{}}
		ids := make([]string, 0, len(cfg.AssetRegions))
		for id := range cfg.AssetRegions {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			item.Assets = append(item.Assets, CollectionAsset{ID: id, Region: cfg.AssetRegions[id]})
		}
		out.Scopes = append(out.Scopes, item)
	}
	return out
}

func (s *CloudService) selected(scopeRef string, selection PlanSelection) (aliyuncollector.Config, domain.Selection, error) {
	cfg, ok := s.Scopes[scopeRef]
	if !ok || cfg.Scope.Ref != scopeRef {
		return cfg, domain.Selection{}, ErrScopeDenied
	}
	selected := domain.Selection{Kind: selection.Kind, AssetIDs: append([]string(nil), selection.AssetIDs...)}
	if selection.Kind == "gateway_policy" {
		if cfg.Scope.Boundary != "nat" || len(selection.AssetIDs) != 0 || selection.GatewayRef != cfg.Scope.GatewayRef {
			return cfg, selected, ErrScopeDenied
		}
	} else if selection.Kind == "assets" {
		if len(selection.AssetIDs) == 0 || len(selection.AssetIDs) > 50 || selection.GatewayRef != "" {
			return cfg, selected, ErrScopeDenied
		}
		seen := map[string]bool{}
		for _, id := range selection.AssetIDs {
			if seen[id] || cfg.AssetRegions[id] == "" {
				return cfg, selected, ErrScopeDenied
			}
			seen[id] = true
		}
	} else {
		return cfg, selected, storage.ErrInputInvalid
	}
	sort.Strings(selected.AssetIDs)
	return cfg, selected, nil
}

func (s *PlanService) previewCloud(ctx context.Context, req PlanRequest) (PlanPreview, error) {
	cloud := s.Cloud
	if cloud == nil || !cloud.Ready || cloud.NewReader == nil || req.DataSourceID != cloud.SourceRef {
		return PlanPreview{}, ErrSourceNotConfigured
	}
	if cloud.Store != s.Store || cloud.ConfigRevision != s.ConfigRevision || cloud.MaxHTTPAttempts < 1 || cloud.MaxHTTPAttempts > 100 {
		return PlanPreview{}, storage.ErrInputInvalid
	}
	if req.Mode != "rules_only" || req.DataPolicy.Mode != "force_refresh" || req.DataPolicy.SnapshotID != "" || req.DataPolicy.FreshAfter != "" || req.DataPolicy.AllowStaleFallback || req.EvaluationAt != "" || req.RecheckOfJobID != "" || req.DisclosurePolicyID != "" || len(req.ProfileVersionIDs) != 0 || len(req.IntentVersionIDs) != 0 || req.BaselineVersionID != audit.ProjectBaseline().Version {
		return PlanPreview{}, storage.ErrInputInvalid
	}
	conversation, err := s.Store.GetConversation(ctx, req.ConversationID)
	if err != nil {
		return PlanPreview{}, err
	}
	if conversation.Archived {
		return PlanPreview{}, storage.ErrConversationArchived
	}
	if conversation.PolicyScopeRef != req.PolicyScopeRef {
		return PlanPreview{}, ErrScopeDenied
	}
	cfg, selection, err := cloud.selected(req.PolicyScopeRef, req.Selection)
	if err != nil {
		return PlanPreview{}, err
	}
	limit := cloud.MaxHTTPAttempts
	if req.Kind == "connection_check" {
		limit = min(limit, 6)
	}
	body, err := json.Marshal(req)
	if err != nil {
		return PlanPreview{}, storage.ErrInputInvalid
	}
	prepared := domain.Input{Scope: cfg.Scope, Selection: selection, Baseline: audit.ProjectBaseline()}
	p, err := s.Store.SavePlan(ctx, storage.ExecutionPlan{Kind: req.Kind, ConversationID: conversation.ID, DataSourceID: cloud.SourceRef, PolicyScopeRef: cfg.Scope.Ref, Mode: "rules_only", ConversationRevision: conversation.Revision, Request: body, EffectCeiling: storage.EffectCeiling{AllowCloudReads: true, CloudHTTPAttempts: limit}, PreparedInput: prepared, ConfigRevision: s.ConfigRevision, BudgetRef: s.BudgetRef})
	warnings := []string{"仅对显式配置的账号与所选对象执行只读请求，云端策略仍由人工修改。", "配置目录尚未核验身份或产品权限；本次任务才执行身份核对。"}
	if req.Kind == "connection_check" {
		warnings = append(warnings, "本次仅验证调用身份；产品字段、权限与完整性须由采集任务另行验证。")
	}
	return PlanPreview{ExecutionPlan: p, Gaps: []string{}, Warnings: warnings}, err
}

func cloudFailure(ctx context.Context, err error, fallback string) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	code := outbound.ErrorCode(err)
	if code == "UPSTREAM_FAILURE" {
		code = fallback
	}
	return JobFailure{Code: code}
}

func (s *CloudService) Run(ctx context.Context, job storage.Job) error {
	if s == nil || !s.Ready || s.Store == nil || s.NewReader == nil {
		return JobFailure{Code: "CLOUD_NOT_CONFIGURED"}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	p, err := s.Store.GetPlan(ctx, job.PlanID)
	if err != nil || p.ConsumedJobID != job.ID || p.BudgetRef != job.BudgetRef || p.Kind != job.Kind || (job.Kind != "collect" && job.Kind != "connection_check") || p.ConversationID != job.ConversationID || p.DataSourceID != s.SourceRef || p.ConfigRevision != s.ConfigRevision || !p.EffectCeiling.AllowCloudReads || p.EffectCeiling.AllowModelRequests || p.EffectCeiling.ModelHTTPAttempts != 0 || p.EffectCeiling.CloudHTTPAttempts < 1 || p.EffectCeiling.CloudHTTPAttempts > s.MaxHTTPAttempts {
		return workflowFailure(ctx, "CLOUD_PLAN_INVALID", false)
	}
	var request PlanRequest
	if json.Unmarshal(p.Request, &request) != nil || request.Kind != p.Kind || request.PolicyScopeRef != p.PolicyScopeRef || request.DataSourceID != p.DataSourceID {
		return workflowFailure(ctx, "CLOUD_PLAN_INVALID", false)
	}
	cfg, selection, err := s.selected(p.PolicyScopeRef, request.Selection)
	if err != nil || !reflect.DeepEqual(cfg.Scope, p.PreparedInput.Scope) || !reflect.DeepEqual(selection, p.PreparedInput.Selection) {
		return workflowFailure(ctx, "CLOUD_PLAN_INVALID", false)
	}
	reader, err := s.NewReader(ctx, job.ID)
	if err != nil {
		return cloudFailure(ctx, err, "CLOUD_CONFIGURATION_FAILED")
	}
	if err = reader.VerifyIdentity(ctx); err != nil {
		return cloudFailure(ctx, err, "CLOUD_IDENTITY_FAILED")
	}
	content := storage.CloudContent{JobID: job.ID, Kind: job.Kind, ScopeRef: p.PolicyScopeRef, SourceRef: p.DataSourceID, ConfigRevision: p.ConfigRevision, IdentityVerified: true, Observations: []storage.CloudObservation{}, Limitations: []string{}}
	if job.Kind == "connection_check" {
		content.Limitations = []string{"IDENTITY_ONLY"}
		_, err = s.Store.FinalizeCloud(ctx, job.ID, content, nil)
		if err != nil {
			return cloudFailure(ctx, err, "CLOUD_RESULT_SAVE_FAILED")
		}
		return nil
	}
	cfg.MaxAttempts = min(cfg.MaxAttempts, p.EffectCeiling.CloudHTTPAttempts-1)
	if cfg.MaxAttempts < 1 {
		return JobFailure{Code: "ATTEMPT_LIMIT"}
	}
	var collected aliyuncollector.Collection
	if s.collect != nil {
		collected, err = s.collect(ctx, reader, cfg, selection)
	} else {
		var collector *aliyuncollector.Collector
		collector, err = aliyuncollector.New(reader, cfg)
		if err == nil {
			collected, err = collector.Collect(ctx, selection)
		}
	}
	if err != nil {
		return cloudFailure(ctx, err, "COLLECTION_FAILED")
	}
	if err = audit.ValidateContext(ctx, collected.Input); err != nil {
		return workflowFailure(ctx, "COLLECTION_INPUT_INVALID", false)
	}
	encodedInput, err := json.Marshal(collected.Input)
	if err != nil || cloudContainsSecret(encodedInput, s.SensitiveValues) {
		return workflowFailure(ctx, "CLOUD_RESPONSE_CONTAINS_CREDENTIAL", false)
	}
	for _, artifact := range collected.Artifacts {
		query, err := json.Marshal(artifact.Query)
		if err != nil || cloudContainsSecret(query, s.SensitiveValues) || cloudContainsSecret(artifact.Body, s.SensitiveValues) {
			return workflowFailure(ctx, "CLOUD_RESPONSE_CONTAINS_CREDENTIAL", false)
		}
	}
	content.ProductReady = true
	content.Limitations = append(content.Limitations, collected.Input.Completeness.KnownGaps...)
	if !collected.Input.Completeness.EnumerationComplete {
		content.Limitations = append(content.Limitations, "COLLECTION_INCOMPLETE")
	}
	if collected.Input.Completeness.ObservationConsistency != "observed_stable" {
		content.Limitations = append(content.Limitations, "CONFIGURATION_NOT_STABLE")
	}
	for _, observation := range collected.Observations {
		content.Observations = append(content.Observations, storage.CloudObservation{Round: observation.Round, Operation: observation.Operation, Pages: observation.Pages, StartedAt: observation.StartedAt, FinishedAt: observation.FinishedAt, EnumerationComplete: observation.EnumerationComplete, SemanticSHA256: observation.SemanticSHA256})
	}
	artifacts := make([]storage.CloudRawArtifact, 0, len(collected.Artifacts))
	for _, artifact := range collected.Artifacts {
		artifacts = append(artifacts, storage.CloudRawArtifact{Round: artifact.Round, Operation: artifact.Operation, Query: artifact.Query, Body: []byte(artifact.Body), SHA256: artifact.SHA256})
	}
	content.RawArtifacts, err = json.Marshal(artifacts)
	if err != nil || len(content.RawArtifacts) > 32<<20 {
		return workflowFailure(ctx, "COLLECTION_RAW_LIMIT", false)
	}
	if _, err = s.Store.FinalizeCloud(ctx, job.ID, content, &collected.Input); err != nil {
		return cloudFailure(ctx, err, "CLOUD_RESULT_SAVE_FAILED")
	}
	return nil
}

// Inspect decoded strings and keys so JSON escaping cannot hide a known server
// credential. The original provider body remains private and is never logged.
func cloudContainsSecret(body []byte, values []string) bool {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if decoder.Decode(&value) != nil {
		return true
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return true
	}
	var contains func(any) bool
	contains = func(value any) bool {
		switch v := value.(type) {
		case json.Number:
			return contains(v.String())
		case string:
			for _, secret := range values {
				if secret != "" && strings.Contains(v, secret) {
					return true
				}
			}
		case []any:
			for _, child := range v {
				if contains(child) {
					return true
				}
			}
		case map[string]any:
			for key, child := range v {
				if contains(key) || contains(child) {
					return true
				}
			}
		}
		return false
	}
	return contains(value)
}
