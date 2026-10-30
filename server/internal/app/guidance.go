package app

import "time"

// Guidance is a local presentation catalog. Entries describe the next user
// action; they neither grant effects nor execute a plan, cancellation or retry.
// Callers load and authorize references before constructing this context, and
// revalidate their current revisions when the user follows a suggestion.

const GuidanceCatalogVersion = "guidance-m0-v1"

type GuidanceRefs struct {
	ConversationID string `json:"conversation_id,omitempty"`
	IntentID       string `json:"intent_id,omitempty"`
	IntentRevision int64  `json:"intent_revision,omitempty"`
	ProposalID     string `json:"proposal_id,omitempty"`
	PlanID         string `json:"plan_id,omitempty"`
	SnapshotID     string `json:"snapshot_id,omitempty"`
	JobID          string `json:"job_id,omitempty"`
	ReportID       string `json:"report_id,omitempty"`
	FindingID      string `json:"finding_id,omitempty"`
	DraftID        string `json:"draft_id,omitempty"`
	SubmissionID   string `json:"submission_id,omitempty"`
}

// GuidanceContext contains state codes and local references, not user text,
// provider errors, credentials or authorization supplied by a browser/model.
// Upstream verification is informational: a ready, selected model may be
// tried within an accepted plan even when compatibility is not yet verified.
type GuidanceContext struct {
	Refs                    GuidanceRefs
	Surface                 string
	IntentStatus            string
	ProposalStatus          string
	JobStatus               string
	Phase                   string
	Mode                    string
	DataPolicy              string
	CloudReady              bool
	ModelReady              bool
	ModelUpstreamState      string
	FactsAvailable          bool
	RulesAvailable          bool
	BusinessMissing         bool
	DataIncomplete          bool
	Historical              bool
	ManualChange            bool
	FreshAfter              string
	SubmissionUnknown       bool
	ObservationDisconnected bool
	SaveState               string // unsaved, failed, conflict
	ImportState             string // invalid, partial
	Restriction             string // cloud_write, out_of_scope, unrelated, sensitive, external_instruction
	Boundary                string
	Direction               string
	ComparisonState         string // improved, remains, new, incomparable
	HasSavedExplanation     bool
	Batch                   bool
}

type GuidanceCatalog struct {
	CatalogVersion string         `json:"catalog_version"`
	State          string         `json:"state"`
	Items          []GuidanceItem `json:"items"`
}

type GuidanceIntent struct {
	SnapshotID      string `json:"snapshot_id,omitempty"`
	Action          string `json:"action"`
	Text            string `json:"text,omitempty"`
	Mode            string `json:"mode,omitempty"`
	DataPolicy      string `json:"data_policy,omitempty"`
	ContextReportID string `json:"context_report_id,omitempty"`
	RecheckOfJobID  string `json:"recheck_of_job_id,omitempty"`
	FreshAfter      string `json:"fresh_after,omitempty"`
}

type GuidanceEffects struct {
	AllowCloudReads    bool `json:"allow_cloud_reads"`
	AllowModelRequests bool `json:"allow_model_requests"`
}

type GuidanceTarget struct {
	Type           string `json:"type"`
	ID             string `json:"id,omitempty"`
	ConversationID string `json:"conversation_id,omitempty"`
	IntentRevision int64  `json:"intent_revision,omitempty"`
}

type GuidanceAction struct {
	Type   string         `json:"type"`
	Target GuidanceTarget `json:"target"`
}

type GuidanceConfirmation struct {
	Required   bool     `json:"required"`
	Conditions []string `json:"conditions"`
}

type GuidanceItem struct {
	ID             string               `json:"id"`
	CatalogVersion string               `json:"catalog_version"`
	DisplayText    string               `json:"display_text"`
	Intent         GuidanceIntent       `json:"intent"`
	Behavior       string               `json:"behavior"`
	RequiredRefs   []string             `json:"required_refs"`
	Effects        GuidanceEffects      `json:"effects"`
	State          string               `json:"state"`
	MissingInputs  []string             `json:"missing_inputs"`
	Action         GuidanceAction       `json:"action"`
	Confirmation   GuidanceConfirmation `json:"confirmation"`
}

type guidanceBuilder struct {
	context GuidanceContext
	catalog GuidanceCatalog
}

// BuildGuidance returns at most three deterministic suggestions. Surface does
// not change semantics: conversation, five views and future terminal clients
// can render the same typed actions without duplicating state decisions.
func BuildGuidance(context GuidanceContext) GuidanceCatalog {
	context.Refs = cleanGuidanceRefs(context.Refs)
	b := guidanceBuilder{context: context, catalog: GuidanceCatalog{CatalogVersion: GuidanceCatalogVersion, Items: []GuidanceItem{}}}
	if b.restricted() || b.recovery() || b.active() {
		return b.catalog
	}
	if context.ManualChange {
		b.catalog.State = "manual_change"
		b.refresh()
		b.report()
		b.history()
	} else if context.Historical || context.Surface == "report" && context.Refs.ReportID != "" {
		b.catalog.State = "historical"
		b.explainHistory()
		b.currentAudit()
		b.review()
	} else if guidanceTerminal(context.JobStatus) {
		b.terminal()
	} else if context.ProposalStatus == "stale" || context.ProposalStatus == "expired" || context.ProposalStatus == "superseded" || context.IntentStatus == "stale" {
		b.catalog.State = "preview_stale"
		b.repreview("原计划需要重新核对，预览当前目标", "current_intent_revision", "review_changed_scope_and_versions")
		b.facts()
		b.history()
	} else if context.IntentStatus == "ready_for_confirmation" || context.IntentStatus == "ready" {
		b.catalog.State = "ready_for_confirmation"
		b.add("GDE-PREVIEW", "核对本次计划的对象、资料与效果", "review_plan", "navigate_local", "plan_preview", context.Refs.PlanID, []string{"conversation_id", "intent_id", "intent_revision", "proposal_id", "plan_id"}, GuidanceEffects{}, true, "accept_current_proposal_revision")
		b.facts()
		if context.BusinessMissing {
			b.business()
		} else {
			b.rulesOnly()
		}
	} else if context.IntentStatus == "needs_batch" {
		b.catalog.State = "needs_batch"
		b.fill("GDE-BATCH", "按完整对象集准备串行分批计划", "audit", "按完整对象集分批审计", b.mode(), b.policy())
		b.fill("GDE-NARROW", "先说明一个明确ID收窄这次范围", "clarify", "我先收窄对象，请核对明确ID", b.mode(), b.policy())
		b.facts()
	} else if context.IntentStatus == "needs_clarification" || context.IntentStatus == "ambiguous" || context.IntentStatus == "clarification_required" {
		b.catalog.State = "needs_clarification"
		b.add("GDE-CLARIFY", "核对候选或补充影响执行的条件", "clarify", "navigate_local", "intent_candidates", context.Refs.IntentID, []string{"conversation_id", "intent_id", "intent_revision"}, GuidanceEffects{}, false)
		b.facts()
		b.fill("GDE-07", "只检查规则，不用模型", "audit", "只检查规则，不用模型", "rules_only", "")
	} else if context.IntentStatus == "needs_discovery" || context.IntentStatus == "needs_data" || context.IntentStatus == "blocked" || context.IntentStatus == "intent_draft" || context.IntentStatus == "draft" {
		b.catalog.State = "needs_preparation"
		if context.CloudReady {
			b.repreview("核对有限发现、资料准备与后续审计", "finite_regions_and_selector", "review_effect_ceiling", "accept_current_proposal_revision")
		} else {
			b.offline()
		}
		b.fill("GDE-07", "只检查规则，不用模型", "audit", "只检查规则，不用模型", "rules_only", "")
		b.sample()
	} else if context.ImportState == "invalid" {
		b.catalog.State = "import_invalid"
		b.add("GDE-IMPORT-RETRY", "修正输入结构或版本后重新导入，原目标保留", "open_offline", "navigate_local", "offline_import", "", nil, GuidanceEffects{}, false)
		b.sample()
		b.fill("GDE-CONTINUE", "继续澄清原来的非敏感目标", "clarify", "继续澄清已有审计目标", b.mode(), "")
	} else if context.Surface == "profile" && (context.FactsAvailable || context.Refs.SnapshotID != "") {
		b.catalog.State = "business_context"
		if context.Direction == "out" {
			b.add("GDE-13", "核对这些资产的外部依赖", "edit_outbound_dependencies", "navigate_local", "business_draft", context.Refs.DraftID, []string{"conversation_id", "snapshot_id"}, GuidanceEffects{}, false, "keep_outbound_intent_separate_from_inbound_sources")
		} else {
			b.inferPurpose()
		}
		b.business()
		b.fill("GDE-04", "用途未知，先检查基础风险", "audit", "用途未知，先按独立基线检查基础风险", "rules_only", b.policy())
	} else if context.FactsAvailable || context.Refs.SnapshotID != "" {
		b.catalog.State = "facts_available"
		b.facts()
		b.pinned()
		if context.BusinessMissing {
			b.business()
		} else if context.Boundary == "nat" && context.Refs.FindingID != "" {
			b.natGap()
		} else {
			b.rulesOnly()
		}
	} else {
		b.catalog.State = "blank"
		b.sample()
		b.fill("GDE-START", "描述要核查的名称、ID、地域与方向", "audit", "按明确的名称或资源ID、地域与方向核查云防火墙策略", "rules_only", "prefer_fresh")
		if context.CloudReady {
			b.fill("GDE-14", "检查一个NAT网关", "audit", "检查一个明确NAT网关的出向策略", "rules_only", "prefer_fresh")
		} else {
			b.offline()
		}
	}
	return b.catalog
}

func (b *guidanceBuilder) add(id, text, intent, behavior, targetType, targetID string, required []string, effects GuidanceEffects, confirm bool, conditions ...string) *GuidanceItem {
	if len(b.catalog.Items) == 3 {
		return nil
	}
	item := GuidanceItem{ID: id, CatalogVersion: GuidanceCatalogVersion, DisplayText: text, Intent: GuidanceIntent{Action: intent}, Behavior: behavior, RequiredRefs: append([]string{}, required...), Effects: effects, State: "available", MissingInputs: []string{}, Action: GuidanceAction{Type: behavior, Target: GuidanceTarget{Type: targetType, ID: targetID, ConversationID: b.context.Refs.ConversationID, IntentRevision: b.context.Refs.IntentRevision}}, Confirmation: GuidanceConfirmation{Required: confirm, Conditions: append([]string{}, conditions...)}}
	for _, ref := range required {
		if !b.hasRef(ref) {
			item.State = "needs_input"
			item.MissingInputs = append(item.MissingInputs, ref)
		}
	}
	b.catalog.Items = append(b.catalog.Items, item)
	return &b.catalog.Items[len(b.catalog.Items)-1]
}

func (b *guidanceBuilder) hasRef(name string) bool {
	r := b.context.Refs
	if name == "intent_revision" {
		return r.IntentRevision > 0
	}
	return map[string]string{"conversation_id": r.ConversationID, "intent_id": r.IntentID, "proposal_id": r.ProposalID, "plan_id": r.PlanID, "snapshot_id": r.SnapshotID, "job_id": r.JobID, "report_id": r.ReportID, "finding_id": r.FindingID, "draft_id": r.DraftID, "submission_id": r.SubmissionID}[name] != ""
}

func (b *guidanceBuilder) fill(id, text, action, input, mode, policy string) {
	if item := b.add(id, text, action, "fill_input", "composer", "", nil, GuidanceEffects{}, false); item != nil {
		item.Intent.Text, item.Intent.Mode, item.Intent.DataPolicy = input, mode, policy
	}
}

func (b *guidanceBuilder) sample() {
	b.add("GDE-01", "从公开人工样例开始", "open_sample", "navigate_local", "sample_catalog", "", nil, GuidanceEffects{}, false)
}

func (b *guidanceBuilder) offline() {
	b.add("GDE-11", "选择人工样例或导入离线资料", "open_offline", "navigate_local", "offline_import", "", nil, GuidanceEffects{}, false)
}

func (b *guidanceBuilder) facts() {
	if !b.context.FactsAvailable && b.context.Refs.SnapshotID == "" {
		return
	}
	text := "看这些对象已有资料的来源、时点与缺口"
	if b.context.DataIncomplete || b.context.ImportState == "partial" {
		text = "查看局部资料与受影响检查，保留信息不足项"
	}
	b.add("GDE-02", text, "inspect_facts", "navigate_local", "snapshot", b.context.Refs.SnapshotID, []string{"snapshot_id"}, GuidanceEffects{}, false)
}

func (b *guidanceBuilder) rulesOnly() {
	b.fill("GDE-07", "只检查规则，不用模型", "audit", "用途未知，先检查独立基线；只运行规则", "rules_only", b.context.DataPolicy)
}

func (b *guidanceBuilder) pinned() {
	item := b.add("GDE-08", "沿用这份固定时点资料预览规则检查", "audit", "open_plan_preview", "snapshot", b.context.Refs.SnapshotID, []string{"snapshot_id"}, GuidanceEffects{}, true, "review_fixed_time_and_source", "accept_new_plan")
	if item != nil {
		item.Intent.Mode, item.Intent.DataPolicy = "rules_only", "pinned_snapshot"
	}
}

func (b *guidanceBuilder) business() {
	b.add("GDE-06", "哪些判断还需要业务信息", "inspect_business_gaps", "navigate_local", "business_draft", b.context.Refs.DraftID, []string{"conversation_id"}, GuidanceEffects{}, false)
}

func (b *guidanceBuilder) report() {
	if b.context.Refs.ReportID != "" {
		b.add("GDE-REPORT", "查看原报告的证据、时点与限制", "open_report", "navigate_local", "report", b.context.Refs.ReportID, []string{"report_id"}, GuidanceEffects{}, false)
	} else if b.context.RulesAvailable {
		b.add("GDE-RULES", "查看已保存的规则结果与未完成部分", "open_rules", "navigate_local", "job_rules", b.context.Refs.JobID, []string{"job_id"}, GuidanceEffects{}, false)
	}
}

func (b *guidanceBuilder) history() {
	b.add("GDE-10", "继续历史会话和本地待办", "open_history", "navigate_local", "conversation_history", b.context.Refs.ConversationID, []string{"conversation_id"}, GuidanceEffects{}, false)
}

func (b *guidanceBuilder) review() {
	b.add("GDE-REVIEW", "审阅原发现并记录本地待办", "review_report", "navigate_local", "report_review", b.context.Refs.ReportID, []string{"report_id"}, GuidanceEffects{}, true, "local_review_does_not_apply_cloud_changes")
}

func (b *guidanceBuilder) natGap() {
	b.add("GDE-15", "这条NAT结论缺什么", "inspect_finding_gaps", "navigate_local", "finding", b.context.Refs.FindingID, []string{"finding_id"}, GuidanceEffects{}, false)
}

func (b *guidanceBuilder) repreview(text string, conditions ...string) {
	required := []string{"conversation_id", "intent_id", "intent_revision"}
	if b.policy() == "pinned_snapshot" {
		required = append(required, "snapshot_id")
	}
	item := b.add("GDE-PREPARE", text, "audit", "open_plan_preview", "intent", b.context.Refs.IntentID, required, GuidanceEffects{AllowCloudReads: b.context.CloudReady && b.policy() != "pinned_snapshot", AllowModelRequests: b.context.Mode == "rules_and_agent" && b.context.ModelReady}, true, conditions...)
	if item != nil {
		item.Intent.Mode, item.Intent.DataPolicy = b.mode(), b.policy()
		b.modelCondition(item)
	}
}

func (b *guidanceBuilder) currentAudit() {
	item := b.add("GDE-16", "以此边界预览新的当前核查", "audit", "open_plan_preview", "report", b.context.Refs.ReportID, []string{"report_id"}, GuidanceEffects{AllowCloudReads: b.context.CloudReady, AllowModelRequests: b.context.Mode == "rules_and_agent" && b.context.ModelReady}, true, "resolve_current_objects_and_scope", "evaluate_data_eligibility", "accept_new_plan")
	if item != nil {
		item.Intent.Mode, item.Intent.DataPolicy, item.Intent.RecheckOfJobID = b.mode(), "prefer_fresh", b.context.Refs.JobID
		b.modelCondition(item)
		if !b.context.CloudReady {
			item.State = "needs_input"
			item.MissingInputs = append(item.MissingInputs, "current_data_or_readonly_connection")
		}
	}
}

func (b *guidanceBuilder) refresh() {
	item := b.add("GDE-09", "我已人工修改，预览刷新后的复审", "recheck", "open_plan_preview", "report", b.context.Refs.ReportID, []string{"report_id"}, GuidanceEffects{AllowCloudReads: b.context.CloudReady, AllowModelRequests: b.context.Mode == "rules_and_agent" && b.context.ModelReady}, true, "confirm_manual_change_completed_at", "resolve_same_scope_and_objects", "accept_new_plan")
	if item != nil {
		item.Intent.Mode, item.Intent.DataPolicy, item.Intent.RecheckOfJobID = b.mode(), "force_refresh", b.context.Refs.JobID
		b.modelCondition(item)
		if !b.context.CloudReady {
			item.State = "needs_input"
			item.MissingInputs = append(item.MissingInputs, "readonly_connection")
		}
		// The planner validates the user's change timestamp. Guidance does not
		// substitute the current clock or treat an unvalidated value as saved.
		if at, err := time.Parse(time.RFC3339Nano, b.context.FreshAfter); err == nil {
			item.Intent.FreshAfter = at.UTC().Format(time.RFC3339Nano)
		} else {
			item.State = "needs_input"
			item.MissingInputs = append(item.MissingInputs, "change_completed_at")
		}
	}
}

func (b *guidanceBuilder) explainHistory() {
	model := b.context.Mode == "rules_and_agent" && b.context.ModelReady && !b.context.HasSavedExplanation
	if model {
		item := b.add("GDE-05", "解释这份旧报告的发现与原时点限制", "explain_report", "fill_input", "composer", "", []string{"conversation_id", "report_id", "snapshot_id"}, GuidanceEffects{}, false, "send_preview_then_accept", "keep_original_input_and_time")
		if item != nil {
			item.Intent.Text = "请使用模型解释固定报告 " + b.context.Refs.ReportID + " 的发现、依据和原时点限制。"
			item.Intent.ContextReportID = b.context.Refs.ReportID
			item.Intent.Mode, item.Intent.DataPolicy, item.Intent.SnapshotID = "rules_and_agent", "pinned_snapshot", b.context.Refs.SnapshotID
		}
		return
	}
	item := b.add("GDE-05", "解释这份旧报告的发现与原时点限制", "explain_report", "navigate_local", "report", b.context.Refs.ReportID, []string{"report_id"}, GuidanceEffects{}, false, "keep_original_input_and_time")
	if item != nil {
		item.Intent.ContextReportID = b.context.Refs.ReportID
		item.Intent.DataPolicy = "pinned_snapshot"
	}
}

func (b *guidanceBuilder) inferPurpose() {
	item := b.add("GDE-03", "根据已有资料预览可能用途的有界分析", "infer_purpose", "open_plan_preview", "snapshot", b.context.Refs.SnapshotID, []string{"snapshot_id"}, GuidanceEffects{AllowModelRequests: b.context.ModelReady && b.context.Mode == "rules_and_agent"}, true, "select_model_mode", "review_external_projection", "accept_new_plan")
	if item != nil {
		item.Intent.Mode, item.Intent.DataPolicy = "rules_and_agent", "pinned_snapshot"
		if b.context.Mode != "rules_and_agent" {
			item.State = "needs_input"
			item.MissingInputs = append(item.MissingInputs, "model_mode_selection")
		}
		b.modelCondition(item)
	}
}

func (b *guidanceBuilder) modelCondition(item *GuidanceItem) {
	if item.Intent.Mode == "rules_and_agent" && !b.context.ModelReady {
		item.State = "needs_input"
		item.MissingInputs = append(item.MissingInputs, "model_available")
	}
}

func (b *guidanceBuilder) active() bool {
	if b.context.JobStatus != "queued" && b.context.JobStatus != "running" && b.context.JobStatus != "cancelling" {
		return false
	}
	b.catalog.State = b.context.JobStatus
	if b.context.ObservationDisconnected {
		b.add("GDE-RECONNECT", "恢复原任务观察，任务继续使用原额度", "observe_job", "navigate_local", "job_observation", b.context.Refs.JobID, []string{"job_id"}, GuidanceEffects{}, false)
	}
	b.add("GDE-12", "看本次阶段、等待和已知用量", "inspect_job", "navigate_local", "job", b.context.Refs.JobID, []string{"job_id"}, GuidanceEffects{}, false)
	if b.context.JobStatus != "cancelling" {
		b.add("GDE-CANCEL", "核对并取消当前任务", "cancel_job", "navigate_local", "job_cancel_confirmation", b.context.Refs.JobID, []string{"conversation_id", "job_id"}, GuidanceEffects{}, true, "confirm_this_job_only")
	}
	if b.context.RulesAvailable {
		b.report()
	}
	return true
}

func (b *guidanceBuilder) terminal() {
	b.catalog.State = b.context.JobStatus
	if b.context.Batch {
		b.add("GDE-BATCH-RESULTS", "查看各批段报告、实际覆盖与未执行对象；复审时选择原批段报告", "inspect_batch", "navigate_local", "job", b.context.Refs.JobID, []string{"conversation_id", "job_id"}, GuidanceEffects{}, false)
		return
	}
	if b.context.JobStatus == "completed" {
		if b.context.ComparisonState == "incomparable" {
			b.add("GDE-INCOMPARABLE", "查看不可比较的对象与原因，差异消失尚未获证", "inspect_comparison", "navigate_local", "report_comparison", b.context.Refs.ReportID, []string{"report_id"}, GuidanceEffects{}, false)
		}
		b.report()
		if b.context.BusinessMissing {
			b.business()
		} else if b.context.Boundary == "nat" && b.context.Refs.FindingID != "" {
			b.natGap()
		} else {
			b.review()
		}
		b.currentAudit()
		return
	}
	b.add("GDE-TERMINAL", "查看原任务终态、已得资料与未执行部分", "inspect_job", "navigate_local", "job", b.context.Refs.JobID, []string{"job_id"}, GuidanceEffects{}, false)
	b.report()
	b.repreview("保留原结果，核对范围后预览新的有界尝试", "inspect_original_terminal_state", "accept_new_plan")
}

func (b *guidanceBuilder) recovery() bool {
	if b.context.SubmissionUnknown {
		b.catalog.State = "submission_unknown"
		b.add("GDE-LOOKUP", "查询原提交的受理结果", "lookup_submission", "navigate_local", "submission", b.context.Refs.SubmissionID, []string{"conversation_id", "submission_id"}, GuidanceEffects{}, false)
		b.add("GDE-SAME-REQUEST", "核对后沿原标识重试同一次提交", "retry_submission", "navigate_local", "submission_retry_confirmation", b.context.Refs.SubmissionID, []string{"conversation_id", "submission_id"}, GuidanceEffects{}, true, "lookup_original_first", "reuse_submission_id", "keep_same_request_body")
		return true
	}
	if b.context.SaveState == "conflict" {
		b.catalog.State = "save_conflict"
		b.add("GDE-DIFF", "保留草稿，核对新基版本的差异", "compare_draft", "navigate_local", "draft_diff", b.context.Refs.DraftID, []string{"draft_id"}, GuidanceEffects{}, false)
		b.add("GDE-DRAFT-PREVIEW", "按当前对象重新预览业务确认", "preview_draft", "navigate_local", "draft_preview", b.context.Refs.DraftID, []string{"draft_id"}, GuidanceEffects{}, true, "confirm_current_objects_and_base_versions")
		return true
	}
	if b.context.SaveState == "failed" || b.context.SaveState == "unsaved" {
		b.catalog.State = "save_not_confirmed"
		b.add("GDE-SAVE-LOOKUP", "核对保存结果，保留尚未确认保存的草稿", "lookup_save", "navigate_local", "draft_save_status", b.context.Refs.DraftID, []string{"draft_id"}, GuidanceEffects{}, false)
		b.add("GDE-SAVE-RETRY", "核对后重试保存同一份草稿", "retry_save", "navigate_local", "draft_retry_confirmation", b.context.Refs.DraftID, []string{"draft_id"}, GuidanceEffects{}, true, "lookup_original_save_first", "keep_original_draft_and_request")
		return true
	}
	return false
}

func (b *guidanceBuilder) restricted() bool {
	switch b.context.Restriction {
	case "sensitive":
		b.catalog.State = "sensitive_input_blocked"
		b.add("GDE-CONFIG", "移除凭据，查看服务端受控配置说明", "configuration_help", "navigate_local", "configuration_help", "", nil, GuidanceEffects{}, false)
		b.fill("GDE-CONTINUE", "去除敏感内容后继续原目标", "clarify", "继续核对先前的非敏感审计目标", b.mode(), "")
	case "cloud_write":
		b.catalog.State = "readonly_boundary"
		b.fill("GDE-READONLY", "保留只读分析与人工变更建议", "audit", "只分析已有证据并说明人工调整的条件和影响", "rules_only", "")
		b.report()
	case "out_of_scope":
		b.catalog.State = "scope_restricted"
		b.fill("GDE-SCOPE", "改为连接允许范围内的明确对象", "clarify", "选择当前连接范围内的对象、地域和方向", b.mode(), "")
		b.offline()
	case "unrelated":
		b.catalog.State = "audit_goal_needed"
		b.fill("GDE-START", "按名称或ID描述云防火墙核查目标", "audit", "按明确资源名称或ID、地域和方向核查云防火墙策略", "rules_only", "prefer_fresh")
		b.sample()
	case "external_instruction":
		b.catalog.State = "external_content_is_data"
		b.facts()
		b.offline()
	default:
		return false
	}
	return true
}

func (b *guidanceBuilder) mode() string {
	if b.context.Mode == "rules_and_agent" {
		return "rules_and_agent"
	}
	return "rules_only"
}

func (b *guidanceBuilder) policy() string {
	if b.context.DataPolicy == "force_refresh" || b.context.DataPolicy == "pinned_snapshot" {
		return b.context.DataPolicy
	}
	return "prefer_fresh"
}

func guidanceTerminal(status string) bool {
	return status == "completed" || status == "partial" || status == "failed" || status == "cancelled" || status == "interrupted"
}

func cleanGuidanceRefs(refs GuidanceRefs) GuidanceRefs {
	for _, value := range []*string{&refs.ConversationID, &refs.IntentID, &refs.ProposalID, &refs.PlanID, &refs.SnapshotID, &refs.JobID, &refs.ReportID, &refs.FindingID, &refs.DraftID, &refs.SubmissionID} {
		valid := len(*value) <= 128
		for _, r := range *value {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == ':' || r == '.') {
				valid = false
			}
		}
		if !valid {
			*value = ""
		}
	}
	if refs.IntentRevision < 0 {
		refs.IntentRevision = 0
	}
	return refs
}
