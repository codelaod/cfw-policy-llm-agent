package report

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"cfw-policy-llm-agent/server/internal/agentquery"
	"cfw-policy-llm-agent/server/internal/domain"
)

const agentProjectionVersion = "agent-public-v1"
const agentQualification = "此层仅为已校验的模型候选结构；原始规则判定、严重程度和覆盖保持不变。详细模型解释在受保护任务记录中，公开报告不复制模型自由文案；省略文案不代表建议已执行。"
const usageQualification = "逻辑模型调用与物理HTTP尝试预记分别统计；预记可能包括未确认送达或预记后未发送的保守占用，不等于供应商已计费请求数。只汇总供应商明确报告的用量，未知不是零，不推导账单、费用或余额。"

func agentError(code string) *Notice {
	text := ""
	switch code {
	case "MODEL_NOT_CONFIGURED":
		text = "模型未配置或未获授权，模型补充未执行；规则结果保留。"
	case "AGENT_CONTEXT_REJECTED", "AGENT_CONTEXT_UNAVAILABLE", "MODEL_TEXT_PROJECTION_REJECTED":
		text = "模型上下文未满足当前范围或披露约束，模型补充未完成。"
	case "AGENT_OUTPUT_INVALID", "MODEL_OUTPUT_INVALID", "MODEL_OUTPUT_FILTERED", "MODEL_OUTPUT_TRUNCATED", "MODEL_PROTOCOL_INVALID", "MODEL_TOOL_CALL_INVALID", "TOOL_CALL_ID_INVALID", "TOOL_ARGUMENT_INVALID", "TOOL_REFERENCE_DENIED", "QUERY_INPUT_INVALID", "QUERY_REFERENCE_DENIED", "QUERY_TOOL_UNKNOWN", "QUERY_OUTPUT_TOO_LARGE":
		text = "模型或工具输出未通过受控结构、引用或范围校验；不将其作为已确认结论。"
	case "AGENT_MODEL_LIMIT", "AGENT_TOOL_LIMIT", "MODEL_CALL_LIMIT", "TOOL_CALL_LIMIT", "ATTEMPT_LIMIT", "BUDGET_NOT_CONFIGURED", "BUDGET_EXPIRED", "EFFECT_DENIED", "CONFIGURATION_STALE", "EXECUTION_CLOSED":
		text = "模型补充受执行准入、效果或预算边界限制，已有规则结果保留。"
	case "REQUEST_CANCELLED":
		text = "模型补充被取消，未完成部分不作为有效建议。"
	case "REQUEST_DEADLINE_EXCEEDED":
		text = "模型补充达到有界时限，未完成部分不作为有效建议。"
	case "AGENT_EXECUTION_FAILED", "AGENT_RESULT_SAVE_FAILED", "MODEL_CONFIGURATION_INVALID", "MODEL_INITIALIZATION_FAILED", "MODEL_REQUEST_FAILED", "MODEL_ADAPTER_FAILED", "MODEL_REQUEST_INVALID", "MODEL_REQUEST_TOO_LARGE", "MODEL_RESPONSE_READ_FAILED", "MODEL_RESPONSE_TOO_LARGE", "MODEL_STREAM_UNSUPPORTED", "TOOL_EXECUTION_FAILED", "ATTEMPT_RECORD_FAILED", "ADMISSION_UNAVAILABLE", "OUTBOUND_STOPPING", "DESTINATION_REFUSED", "UPSTREAM_TRANSPORT", "UPSTREAM_HTTP_ERROR", "UPSTREAM_READ", "UPSTREAM_RESPONSE_TOO_LARGE", "UPSTREAM_ENCODING_UNSUPPORTED", "UPSTREAM_PROTOCOL", "REDIRECT_REFUSED":
		text = "模型补充或受控执行阶段未完成；原始错误正文不进入公开报告。"
	}
	if text == "" {
		return nil
	}
	return &Notice{Code: code, Text: text}
}
func usageProjection(usage *AgentUsage, modelCalls int) (AgentUsageProjection, error) {
	p := AgentUsageProjection{Status: "unknown", Qualification: usageQualification}
	if usage == nil {
		return p, nil
	}
	if usage.PhysicalAttempts < 0 || usage.PhysicalAttempts > 6 || usage.KnownUsageResponses < 0 || usage.UnknownUsageAttempts < 0 || usage.KnownUsageResponses > usage.PhysicalAttempts || usage.UnknownUsageAttempts > usage.PhysicalAttempts || usage.KnownUsageResponses+usage.UnknownUsageAttempts != usage.PhysicalAttempts || modelCalls == 0 && usage.PhysicalAttempts > 0 {
		return p, ErrInvalidResult
	}
	if usage.UsageTotalsOverflow {
		if usage.KnownUsageResponses < 2 || usage.ReportedTokens != nil {
			return p, ErrInvalidResult
		}
		p.UsageTotalsOverflow = true
		p.Qualification += " 用量合计超过公开JSON安全整数边界，聚合总量保持未知；已报告响应数和未知尝试数仍保留。"
	} else if usage.KnownUsageResponses == 0 && usage.ReportedTokens != nil || usage.KnownUsageResponses > 0 && (usage.ReportedTokens == nil || !usage.ReportedTokens.Valid()) {
		return p, ErrInvalidResult
	}
	physical, known, unknown := usage.PhysicalAttempts, usage.KnownUsageResponses, usage.UnknownUsageAttempts
	p.PhysicalAttempts = &physical
	p.KnownUsageResponses = &known
	p.UnknownUsageAttempts = &unknown
	switch {
	case usage.UsageTotalsOverflow:
		p.Status = "overflow"
	case physical == 0:
		p.Status = "no_attempts"
	case known == 0:
		p.Status = "unknown"
	case unknown > 0:
		p.Status = "partial"
	default:
		p.Status = "reported"
	}
	if usage.ReportedTokens != nil {
		tokens := *usage.ReportedTokens
		p.ReportedTokens = &tokens
	}
	return p, nil
}
func emptySupplement(s AgentSupplement) bool {
	return s.Output == nil && len(s.Bindings) == 0 && s.ModelCalls == 0 && s.ToolCalls == 0 && s.ErrorCode == "" && s.Usage == nil
}
func validateSupplement(ctx context.Context, input domain.Input, result domain.Result, execution Execution, s AgentSupplement) error {
	if _, e := agentquery.New(input, &result); e != nil {
		return ErrInvalidResult
	}
	if e := ctx.Err(); e != nil {
		return e
	}
	if execution.Mode == "rules_only" {
		if execution.ModelStage != "not_requested" || !emptySupplement(s) {
			return ErrInvalidResult
		}
		return nil
	}
	if execution.Mode != "rules_and_agent" || !member(execution.ModelStage, "completed", "not_configured", "not_completed") || s.ModelCalls < 0 || s.ModelCalls > 6 || s.ToolCalls < 0 || s.ToolCalls > 12 || s.ToolCalls > 0 && s.ModelCalls == 0 {
		return ErrInvalidResult
	}
	if s.ToolCalls == 0 {
		for _, b := range s.Bindings {
			if b.Returned {
				return ErrInvalidResult
			}
		}
	}
	if s.Output != nil {
		if execution.ModelStage != "completed" || s.ErrorCode != "" || s.ModelCalls < 1 {
			return ErrInvalidResult
		}
		if e := agentquery.ValidateBoundOutput(input, &result, *s.Output, s.Bindings); e != nil {
			return ErrInvalidResult
		}
	} else {
		if execution.ModelStage == "completed" || agentError(s.ErrorCode) == nil {
			return ErrInvalidResult
		}
		if execution.ModelStage == "not_configured" && (s.ErrorCode != "MODEL_NOT_CONFIGURED" || s.ModelCalls != 0 || s.ToolCalls != 0) {
			return ErrInvalidResult
		}
		if len(s.Bindings) > 0 {
			if e := agentquery.ValidateLocalBindings(input, &result, s.Bindings); e != nil {
				return ErrInvalidResult
			}
		}
	}
	_, e := usageProjection(s.Usage, s.ModelCalls)
	return e
}

// BuildWithAgent applies a second, public whitelist to already validated Agent
// output. Local bindings and model prose never enter either public encoding.
func BuildWithAgent(ctx context.Context, input domain.Input, result domain.Result, reviews []ReviewHead, format string, execution Execution, supplement AgentSupplement) ([]byte, error) {
	if ctx == nil {
		return nil, ErrInvalidResult
	}
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	if format != "json" && format != "markdown" {
		return nil, ErrFormat
	}
	// Reuse the complete existing rule projection before adding a separate layer.
	// Its aliases remain unchanged even when the Agent mentions new local facts.
	base, e := BuildWithExecution(ctx, result, reviews, "json", execution)
	if e != nil {
		return nil, e
	}
	if e = validateSupplement(ctx, input, result, execution, supplement); e != nil {
		return nil, e
	}
	if execution.Mode == "rules_only" {
		if format == "json" {
			return base, nil
		}
		var doc Document
		if json.Unmarshal(base, &doc) != nil {
			return nil, ErrInvalidResult
		}
		return renderDocument(ctx, doc, format)
	}
	var doc Document
	if json.Unmarshal(base, &doc) != nil {
		return nil, ErrInvalidResult
	}
	doc.Agent, e = projectAgent(ctx, input, result, doc, supplement, execution)
	if e != nil {
		return nil, e
	}
	return renderDocument(ctx, doc, format)
}
func renderDocument(ctx context.Context, doc Document, format string) ([]byte, error) {
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	var body []byte
	var e error
	if format == "json" {
		body, e = json.MarshalIndent(doc, "", "  ")
		body = append(body, '\n')
	} else {
		body, e = markdown(ctx, doc)
	}
	if e != nil {
		return nil, e
	}
	if len(body) > MaxBytes {
		return nil, ErrTooLarge
	}
	return body, nil
}

type agentAliases struct {
	fixed         aliases
	extra         aliases
	input         domain.Input
	result        domain.Result
	bindings      map[string]agentquery.LocalBinding
	serviceCounts map[string]int
}

func newAgentAliases(input domain.Input, result domain.Result, doc Document, bindings map[string]agentquery.LocalBinding) *agentAliases {
	p := &agentAliases{fixed: newAliases(), extra: newAliases(), input: input, result: result, bindings: bindings, serviceCounts: map[string]int{}}
	for _, asset := range input.Assets {
		for _, service := range asset.Services {
			p.serviceCounts[service.ID]++
		}
	}
	set := func(kind, raw, alias string) {
		if raw == "" || alias == "" {
			return
		}
		if p.fixed[kind] == nil {
			p.fixed[kind] = map[string]string{}
		}
		p.fixed[kind][raw] = alias
	}
	set("scope", input.Scope.Ref, doc.Scope.Alias)
	set("policy-set", input.Scope.PolicySetRef, doc.Scope.PolicySetAlias)
	set("gateway", input.Scope.GatewayRef, doc.Scope.GatewayAlias)
	set("baseline", input.Baseline.Version, doc.Versions.Baseline)
	findings := append([]domain.Finding(nil), result.Findings...)
	sort.Slice(findings, func(i, j int) bool { return findings[i].ID < findings[j].ID })
	for i, f := range findings {
		public := doc.Findings[i]
		set("finding", f.ID, public.Alias)
		set("object", f.SubjectRef, public.SubjectAlias)
		set("service", f.ServiceRef, public.ServiceAlias)
		for j, c := range f.Differences {
			set("policy", c.PolicyRef, public.Differences[j].PolicyAlias)
		}
	}
	evidence := append([]domain.Evidence(nil), result.Evidence...)
	sort.Slice(evidence, func(i, j int) bool { return evidence[i].ID < evidence[j].ID })
	for i, e := range evidence {
		public := doc.Evidence[i]
		set("evidence", e.ID, public.Alias)
		set("object", e.SubjectRef, public.SubjectAlias)
		set("policy", e.PolicyRef, public.PolicyAlias)
	}
	return p
}
func bindingIndex(field, prefix string) int {
	if !strings.HasPrefix(field, prefix) {
		return -1
	}
	raw := strings.TrimPrefix(field, prefix)
	if strings.Contains(raw, "/") {
		return -1
	}
	n, e := strconv.Atoi(raw)
	if e != nil || n < 0 {
		return -1
	}
	return n
}
func (p *agentAliases) serviceKey(b agentquery.LocalBinding) string {
	if b.ServiceRef == "" {
		return ""
	}
	if p.serviceCounts[b.ServiceRef] == 1 && p.fixed.get("service", b.ServiceRef) != "" {
		return b.ServiceRef
	}
	return b.AssetRef + "\x00" + b.ServiceRef
}
func (p *agentAliases) identity(b agentquery.LocalBinding) (string, string) {
	switch b.Kind {
	case "scope":
		return "scope", p.input.Scope.Ref
	case "policy_set":
		return "policy-set", p.input.Scope.PolicySetRef
	case "gateway":
		return "gateway", p.input.Scope.GatewayRef
	case "asset":
		return "object", b.AssetRef
	case "service":
		return "service", p.serviceKey(b)
	case "policy":
		i := bindingIndex(b.Field, "/policies/")
		if i >= 0 && i < len(p.input.Policies) {
			return "policy", p.input.Policies[i].ID
		}
	case "finding":
		i := bindingIndex(b.Field, "/findings/")
		if b.Document == "result" && i >= 0 && i < len(p.result.Findings) {
			return "finding", p.result.Findings[i].ID
		}
	case "evidence":
		if b.Document == "result" {
			i := bindingIndex(b.Field, "/evidence/")
			if i >= 0 && i < len(p.result.Evidence) {
				return "evidence", p.result.Evidence[i].ID
			}
		}
		return "evidence", b.Document + ":evidence:" + b.Field
	case "business":
		return "business", b.Document + ":business:" + b.Field
	case "access":
		return "access", b.Document + ":access:" + b.Field
	}
	return "", ""
}
func (p *agentAliases) add(kind, key string) {
	if kind != "" && key != "" && p.fixed.get(kind, key) == "" {
		p.extra.add("agent-"+kind, key)
	}
}
func (p *agentAliases) get(kind, key string) string {
	if fixed := p.fixed.get(kind, key); fixed != "" {
		return fixed
	}
	return p.extra.get("agent-"+kind, key)
}
func (p *agentAliases) addRef(ref string) {
	if ref == "" {
		return
	}
	b := p.bindings[ref]
	kind, key := p.identity(b)
	p.add(kind, key)
	if b.AssetRef != "" {
		p.add("object", b.AssetRef)
	}
	if b.ServiceRef != "" {
		p.add("service", p.serviceKey(b))
	}
	if b.Kind == "evidence" && b.Document == "input" {
		i := bindingIndex(b.Field, "/policies/")
		if i >= 0 && i < len(p.input.Policies) {
			p.add("policy", p.input.Policies[i].ID)
		}
	}
}
func (p *agentAliases) alias(ref string) string {
	if ref == "" {
		return ""
	}
	kind, key := p.identity(p.bindings[ref])
	return p.get(kind, key)
}
func (p *agentAliases) target(kind, ref string) AgentTarget {
	b := p.bindings[ref]
	return AgentTarget{Kind: kind, Alias: p.alias(ref), AssetAlias: p.get("object", b.AssetRef), ServiceAlias: p.get("service", p.serviceKey(b))}
}
func (p *agentAliases) evidenceTarget(ref string) AgentTarget {
	b := p.bindings[ref]
	if b.ServiceRef != "" {
		return AgentTarget{Kind: "asset_service", Alias: p.get("service", p.serviceKey(b)), AssetAlias: p.get("object", b.AssetRef), ServiceAlias: p.get("service", p.serviceKey(b))}
	}
	if b.AssetRef != "" {
		return AgentTarget{Kind: "asset", Alias: p.get("object", b.AssetRef), AssetAlias: p.get("object", b.AssetRef)}
	}
	if b.Document == "input" {
		if i := bindingIndex(b.Field, "/policies/"); i >= 0 && i < len(p.input.Policies) {
			return AgentTarget{Kind: "policy", Alias: p.get("policy", p.input.Policies[i].ID)}
		}
		if b.Field == "/baseline" {
			return AgentTarget{Kind: "baseline", Alias: p.fixed.get("baseline", p.input.Baseline.Version)}
		}
	}
	return AgentTarget{Kind: "scope", Alias: p.fixed.get("scope", p.input.Scope.Ref)}
}
func (p *agentAliases) evidenceAliases(refs []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, ref := range refs {
		alias := p.alias(ref)
		if alias != "" && !seen[alias] {
			seen[alias] = true
			out = append(out, alias)
		}
	}
	return out
}

func fixedRecommendation(kind, action string) (string, string, string) {
	switch kind {
	case "policy_change":
		impact := "配置变更可能影响已确认的必要访问，须核对依赖和回退条件。"
		if action == "conditional_allow_required" {
			impact = "只核对固定的已确认必要访问，不得扩大业务允许集合或免除人工验证。"
		}
		return "基于原发现与固定业务引用的条件性策略复核候选。", impact, "人工核对原策略、当前业务要求和影响后决定是否修改；之后重新采集并复审。"
	case "coverage_followup":
		return "核对当前层保护目标和覆盖，未知不等于未部署。", "当前层缺口不代表其他所有防护层失效，也不证明真实网络路径。", "人工核对该层保护状态与范围，以补充资料进行新审计。"
	case "component_consideration":
		return "基于固定组件主题与已知条件进行适用性评估。", "实际协议、现有覆盖、部署位置和成本仍须核实，不能据此判定产品缺失。", "人工评估该组件主题是否适用，不自动采购、开通或调整产品。"
	default:
		return "请求补充或核实当前范围内的业务资料。", "候选和追问不自动成为角色事实或业务授权。", "在本地业务草稿中核对资料和差异，再由操作者明确确认。"
	}
}
func projectAgent(ctx context.Context, input domain.Input, result domain.Result, doc Document, s AgentSupplement, execution Execution) (*AgentLayer, error) {
	usage, e := usageProjection(s.Usage, s.ModelCalls)
	if e != nil {
		return nil, e
	}
	source := sources[input.SourceKind]
	layer := &AgentLayer{ProjectionVersion: agentProjectionVersion, ModelStage: execution.ModelStage, SourceKind: input.SourceKind, AssessmentBasis: source.basis, ScopeAlias: doc.Scope.Alias, RuleVersionAlias: doc.Versions.Baseline, ModelCalls: s.ModelCalls, ToolCalls: s.ToolCalls, Qualification: agentQualification, Usage: usage, Roles: []AgentRole{}, Recommendations: []AgentRecommendation{}, Evidence: []AgentEvidence{}, Limitations: []Notice{{"model_prose_private", "详细模型解释在受保护任务记录中；本公开层仅保留受控候选结构与服务器固定说明。"}, {"manual_review_required", "模型候选不覆盖原始规则，不表示配置已修改或真实风险已修复。"}}}
	if s.Output == nil {
		layer.Error = agentError(s.ErrorCode)
		return layer, nil
	}
	aliases := newAgentAliases(input, result, doc, s.Bindings)
	usedEvidence := map[string]bool{}
	addEvidence := func(refs []string) {
		for _, ref := range refs {
			aliases.addRef(ref)
			usedEvidence[ref] = true
		}
	}
	for _, role := range s.Output.Roles {
		aliases.addRef(role.AssetRef)
		aliases.addRef(role.ServiceRef)
		addEvidence(role.SupportingEvidence)
		addEvidence(role.ContradictingEvidence)
	}
	for _, rec := range s.Output.Recommendations {
		for _, ref := range []string{rec.TargetRef, rec.BusinessRef, rec.AccessRef, rec.FindingRef} {
			aliases.addRef(ref)
		}
		addEvidence(rec.EvidenceRefs)
	}
	aliases.extra.seal()
	conditions := func(kind, basis string) []Notice {
		return []Notice{{source.code, source.text}, {"fixed_input_only", "仅在本报告固定输入、业务版本和检查条件成立时适用。"}, {"candidate_" + kind, "此项为待人工核实的候选，不能据此自动改变已确认资料或云端配置。"}, {"basis_" + basis, map[string]string{"known_gap": "依据已有规则发现核对已知配置缺口，原判定保持不变。", "applicability_assessment": "这是适用性评估，不表示已读取或确认其他产品状态。", "unknown_followup": "资料或运行条件仍需核实，未知不能改写为缺失或已修复。"}[basis]}}
	}
	for i, role := range s.Output.Roles {
		if e := ctx.Err(); e != nil {
			return nil, e
		}
		layer.Roles = append(layer.Roles, AgentRole{Alias: fmt.Sprintf("agent-role-%03d", i+1), Target: aliases.target("asset_service", role.ServiceRef), Role: role.Role, Status: "candidate", SupportingEvidence: aliases.evidenceAliases(role.SupportingEvidence), ContradictingEvidence: aliases.evidenceAliases(role.ContradictingEvidence), Conditions: conditions("role", "unknown_followup"), Impact: "角色只是业务候选，不证明实际监听、可达性或访问授权。", ManualNextStep: "人工核对实际用途与证据后，在本地业务草稿中预览并明确确认。"})
		layer.PendingQuestions += len(role.Questions)
	}
	for i, rec := range s.Output.Recommendations {
		if e := ctx.Err(); e != nil {
			return nil, e
		}
		explanation, impact, next := fixedRecommendation(rec.Kind, rec.Action)
		layer.Recommendations = append(layer.Recommendations, AgentRecommendation{Alias: fmt.Sprintf("agent-recommendation-%03d", i+1), Kind: rec.Kind, Status: "candidate", Target: aliases.target(rec.TargetKind, rec.TargetRef), BasisKind: rec.BasisKind, FindingAlias: aliases.alias(rec.FindingRef), BusinessAlias: aliases.alias(rec.BusinessRef), AccessAlias: aliases.alias(rec.AccessRef), EvidenceAliases: aliases.evidenceAliases(rec.EvidenceRefs), Action: rec.Action, Field: rec.Field, ComponentTopic: rec.ComponentTopic, Explanation: explanation, Conditions: conditions(rec.Kind, rec.BasisKind), Impact: impact, ManualNextStep: next})
	}
	layer.PendingQuestions += len(s.Output.Questions)
	byAlias := map[string]string{}
	for ref := range usedEvidence {
		byAlias[aliases.alias(ref)] = ref
	}
	keys := []string{}
	for alias := range byAlias {
		keys = append(keys, alias)
	}
	sort.Strings(keys)
	for _, alias := range keys {
		layer.Evidence = append(layer.Evidence, AgentEvidence{Alias: alias, Target: aliases.evidenceTarget(byAlias[alias]), Explanation: "来自固定输入或原规则证据的受控引用；原始身份与字段位置只在受保护任务记录中查看。"})
	}
	return layer, nil
}
