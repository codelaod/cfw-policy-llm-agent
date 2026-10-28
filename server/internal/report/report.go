// Package report creates a bounded, public projection of a fixed audit result.
// It never serializes domain objects or user/model prose directly.
package report

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"cfw-policy-llm-agent/server/internal/domain"
)

var (
	ErrInvalidResult = errors.New("invalid report result or captured review")
	ErrFormat        = errors.New("unsupported report format")
	ErrTooLarge      = errors.New("report output exceeds its bound")
)

// The output is bounded separately from the imported artifact. Oversized
// projection is rejected, never truncated into an apparently complete report.
const MaxBytes = 5 << 20
const maxItems = 10000

type ReviewHead struct {
	FindingID string
	Revision  int64
	Plan      string
}
type Notice struct {
	Code string `json:"code"`
	Text string `json:"text"`
}
type Versions struct {
	Snapshot         string `json:"snapshot"`
	Profile          string `json:"profile"`
	Intent           string `json:"intent"`
	Baseline         string `json:"baseline"`
	Normalizer       string `json:"normalizer"`
	Kernel           string `json:"kernel"`
	SemanticsProfile string `json:"semantics_profile"`
	SemanticsVersion string `json:"semantics_version"`
}
type Scope struct {
	Alias             string `json:"alias"`
	Provider          string `json:"provider"`
	AccountAlias      string `json:"account_alias"`
	Boundary          string `json:"boundary"`
	Direction         string `json:"direction"`
	PolicySetAlias    string `json:"policy_set_alias"`
	GatewayAlias      string `json:"gateway_alias,omitempty"`
	ControlPlaneAlias string `json:"control_plane_alias"`
	IPFamily          string `json:"ip_family"`
}
type Coverage struct {
	Complete         bool     `json:"complete"`
	EvaluatedQueries int      `json:"evaluated_queries"`
	EvaluatedCells   int      `json:"evaluated_cells"`
	Limitations      []Notice `json:"limitations"`
}
type Query struct {
	Protocol          string             `json:"protocol,omitempty"`
	FixedAddressAlias string             `json:"fixed_address_alias,omitempty"`
	AddressAliases    []string           `json:"address_aliases"`
	Ports             []domain.PortRange `json:"ports"`
}
type Difference struct {
	Kind             string `json:"kind"`
	Explanation      string `json:"explanation"`
	AddressFromAlias string `json:"address_from_alias,omitempty"`
	AddressToAlias   string `json:"address_to_alias,omitempty"`
	PortFrom         *int   `json:"port_from,omitempty"`
	PortTo           *int   `json:"port_to,omitempty"`
	Action           string `json:"action,omitempty"`
	PolicyAlias      string `json:"policy_alias,omitempty"`
}
type Finding struct {
	Alias               string       `json:"alias"`
	RuleID              string       `json:"rule_id"`
	RuleVersionAlias    string       `json:"rule_version_alias"`
	Origin              string       `json:"origin"`
	SubjectAlias        string       `json:"subject_alias"`
	ServiceAlias        string       `json:"service_alias,omitempty"`
	IssueAlias          string       `json:"issue_alias"`
	Verdict             string       `json:"verdict"`
	AssessmentBasis     string       `json:"assessment_basis"`
	Severity            string       `json:"severity"`
	RemediationPriority string       `json:"remediation_priority"`
	Explanation         string       `json:"explanation"`
	Conditions          []Notice     `json:"conditions"`
	Query               Query        `json:"query"`
	Differences         []Difference `json:"differences"`
	EvidenceAliases     []string     `json:"evidence_aliases"`
	Limitations         []Notice     `json:"limitations"`
	AcceptedException   bool         `json:"accepted_exception"`
	ReviewRevision      int64        `json:"review_revision"`
	Plan                string       `json:"plan"`
}
type Evidence struct {
	Alias         string `json:"alias"`
	SnapshotAlias string `json:"snapshot_alias"`
	ScopeAlias    string `json:"scope_alias"`
	SubjectAlias  string `json:"subject_alias"`
	PolicyAlias   string `json:"policy_alias,omitempty"`
	ObservedAt    string `json:"observed_at,omitempty"`
	Explanation   string `json:"explanation"`
}
type Document struct {
	SchemaVersion     int         `json:"schema_version"`
	ProjectionVersion string      `json:"projection_version"`
	SourceKind        string      `json:"source_kind"`
	Qualification     string      `json:"qualification"`
	EvaluationAt      string      `json:"evaluation_at"`
	CollectedAt       string      `json:"collected_at"`
	Versions          Versions    `json:"versions"`
	Scope             Scope       `json:"scope"`
	Coverage          Coverage    `json:"coverage"`
	Findings          []Finding   `json:"findings"`
	Evidence          []Evidence  `json:"evidence"`
	Execution         *Execution  `json:"execution,omitempty"`
	Agent             *AgentLayer `json:"agent,omitempty"`
}

// Execution records only controlled workflow facts, separately from immutable
// rule coverage. It cannot carry model prose, upstream errors or private IDs.
type Execution struct {
	Mode       string `json:"mode"`
	ModelStage string `json:"model_stage"`
	JobStatus  string `json:"job_status,omitempty"`
}

var ruleText = map[string]string{
	"CHK-01": "管理来源与明确业务要求的本层配置比较。", "CHK-02": "协议及端口与完整业务允许集合的本层配置比较。", "CHK-03": "服务来源与明确授权集合的本层配置比较。",
	"CHK-04": "精确重复配置候选，需人工复核依赖，不自动删除。", "CHK-05": "当前云防火墙这一层的保护状态核查，其他防护层不据此推断。", "CHK-06": "明确必要访问与本层拒绝配置的比较，不证明实际网络故障。",
	"CHK-07": "已采用高风险端口基线下的配置允许范围核查，不证明实际监听或可达。", "CHK-08": "已确认数据库服务与公网访问要求的本层配置比较。",
	"OUT-01": "出向目标与完整明确依赖要求的本层配置比较。", "OUT-02": "明确必要外联与本层拒绝配置的比较。", "NAT-01": "NAT关联和地址观察点的证据充分性核查。", "NAT-02": "同一NAT网关策略分区内的适用规则一致性核查。",
}

var sources = map[string]struct{ basis, qualification, code, text string }{
	"synthetic":     {"synthetic_assumption", "仅人工构造案例，不证明真实云配置或数据面效果。", "synthetic_case_only", "结论仅在该人工案例设定成立时适用。"},
	"imported":      {"imported_assumption", "导入声明条件下：完整性与来源声明未经真实云观测核实。", "imported_declaration_unverified", "导入声明条件下，所提供配置及完整性声明必须成立。"},
	"live_readonly": {"live_observed", "仅该次只读观测范围与时点的配置结论，不证明数据面实际可达。", "configuration_observation_only", "限于固定观测范围与时点，需人工核实业务影响。"},
}

var limits = map[string]string{
	"configuration_scope_only":           "仅覆盖所选IPv4/TCP/UDP配置子域，不证明实际可达或运行时监听。",
	"accepted_exception_requires_review": "已接受的例外仍需按原范围与有效期复核，原始事实保留。",
	"data_incomplete":                    "输入数据枚举或覆盖声明不完整。", "observation_unstable": "观测一致性不足或已知配置变化。", "protection_unconfirmed": "本层保护状态尚未核实。", "endpoint_unconfirmed": "端点地址或关联尚未核实。",
	"nat_association_incomplete": "NAT关联或观察点证据不足。", "endpoint_outside_selection": "端点不属于本次明确选择范围。", "order_unknown": "相关策略顺序未知。", "enabled_unknown": "相关策略启用状态未知。",
	"reference_or_match_unknown": "相关引用或匹配条件无法确定。", "condition_unsupported": "存在相关且不支持的匹配条件。", "action_unsupported": "存在不支持的策略动作。", "default_unconfirmed": "默认行为依据尚未核实。",
	"protocol_unsupported": "协议超出已支持语义。", "query_cell_limit": "集合计算达到单查询单元上限。", "query_cancelled": "查询被取消，未完成范围不能判为通过。", "query_interrupted": "查询被中断，未完成范围不能判为通过。",
	"reference_incomplete": "相关地址或端口引用未完整展开。", "state_unsupported": "相关策略状态超出已支持语义。", "action_unconfirmed": "相关策略动作尚未核实。",
	"query_dimension_unknown": "查询包含尚未确定的必要维度。", "duplicate_fields_incomplete": "精确重复比较所需的匹配或生效字段不完整。", "public_address_classification_unknown": "部分地址的公网分类未知，所涉检查覆盖尚未穷尽。",
	"business_scope_unknown": "业务授权的适用范围未知。", "duplicate_fields_unsupported": "重复配置比较存在未知或不支持字段。", "rule_stage_interrupted": "规则阶段中断，保留已得结果。", "no_enabled_checks": "未启用可执行检查，不能视为全部通过。",
	"enumeration_incomplete": "输入枚举不完整。", "coverage_incomplete": "受影响查询未完整检查。", "unknown_reference": "引用无法完整解析。", "unresolved_reference": "引用无法完整解析。",
	"unsupported_condition": "存在相关且不支持的匹配条件。", "unknown_default": "默认行为依据不足。", "cell_limit": "集合计算达到单元上限。", "calculation_limit": "集合计算达到上限。",
	"deadline_exceeded": "有界执行达到时限。", "configuration_changed": "观测期间配置有变化。", "observation_unverified": "观测一致性未经核实。", "business_unconfirmed": "业务要求尚未确认。", "association_unverified": "对象关联或观察点证据不足。",
	"unclassified_gap": "存在未公开细节的信息缺口，请在受保护的本地证据视图核对。",
}

func notices(raw []string) []Notice {
	seen := map[string]bool{}
	out := []Notice{}
	for _, value := range raw {
		code := strings.ToLower(strings.TrimSpace(value))
		switch code {
		case "仅覆盖所选ipv4/tcp/udp配置子域，不证明实际可达或运行时监听。":
			code = "configuration_scope_only"
		case "已接受例外；原始事实仍保留，需按到期复核。":
			code = "accepted_exception_requires_review"
		}
		if _, ok := limits[code]; !ok {
			code = "unclassified_gap"
		}
		if !seen[code] {
			seen[code] = true
			out = append(out, Notice{code, limits[code]})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Code < out[j].Code })
	return out
}
func member(value string, allowed ...string) bool {
	for _, a := range allowed {
		if value == a {
			return true
		}
	}
	return false
}
func canonicalTime(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	v, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return "", ErrInvalidResult
	}
	return v.UTC().Format(time.RFC3339Nano), nil
}
func validPorts(ports []domain.PortRange) bool {
	for _, p := range ports {
		if p.From < 0 || p.To < p.From || p.To > 65535 {
			return false
		}
	}
	return true
}

func duplicateCandidate(f domain.Finding, d domain.Cell) bool {
	check := f.Origin == "rule" && ((f.RuleID == "CHK-04" && f.Subcheck == "") || (f.RuleID == "NAT-02" && f.Subcheck == "CHK-04"))
	return check && d.Action == "candidate" && d.Reason == "EXACT_DUPLICATE_CANDIDATE" && d.PolicyRef != "" && d.AddressFrom == "" && d.AddressTo == "" && d.PortFrom == 0 && d.PortTo == 0 && !d.Broad
}

func validDifference(f domain.Finding, d domain.Cell) bool {
	if f.RuleID == "CHK-04" || (f.RuleID == "NAT-02" && f.Subcheck == "CHK-04") || d.Action == "candidate" {
		return duplicateCandidate(f, d)
	}
	return d.PortFrom >= 0 && d.PortTo >= d.PortFrom && d.PortTo <= 65535 && member(d.Action, "accept", "drop", "unknown")
}

type aliases map[string]map[string]string

func newAliases() aliases { return aliases{} }
func (a aliases) add(kind, value string) {
	if value == "" {
		return
	}
	if a[kind] == nil {
		a[kind] = map[string]string{}
	}
	a[kind][value] = ""
}
func (a aliases) seal() {
	for kind, values := range a {
		keys := make([]string, 0, len(values))
		for key := range values {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for n, key := range keys {
			values[key] = fmt.Sprintf("%s-%03d", kind, n+1)
		}
	}
}
func (a aliases) get(kind, value string) string {
	if value == "" {
		return ""
	}
	return a[kind][value]
}

// Build returns an independent byte slice for a captured result/review snapshot.
// Fields not explicitly projected below cannot enter either public format.
func Build(ctx context.Context, result domain.Result, reviews []ReviewHead, format string) ([]byte, error) {
	return build(ctx, result, reviews, format, nil)
}

func BuildWithExecution(ctx context.Context, result domain.Result, reviews []ReviewHead, format string, execution Execution) ([]byte, error) {
	if !member(execution.JobStatus, "", "completed", "partial", "failed", "cancelled", "interrupted") {
		return nil, ErrInvalidResult
	}
	if (execution.Mode != "rules_only" || execution.ModelStage != "not_requested") &&
		(execution.Mode != "rules_and_agent" || !member(execution.ModelStage, "not_configured", "not_completed", "completed")) {
		return nil, ErrInvalidResult
	}
	return build(ctx, result, reviews, format, &execution)
}

func build(ctx context.Context, result domain.Result, reviews []ReviewHead, format string, execution *Execution) ([]byte, error) {
	if ctx == nil {
		return nil, ErrInvalidResult
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if format != "json" && format != "markdown" {
		return nil, ErrFormat
	}
	if result.SchemaVersion != 1 || len(result.Findings) > maxItems || len(result.Evidence) > maxItems || len(reviews) > maxItems {
		return nil, ErrInvalidResult
	}
	// Bound aggregate work before allocating aliases or the projected Cartesian
	// detail lists. The final byte limit is checked independently below.
	parts := int64(len(result.Findings) + len(result.Evidence) + len(reviews))
	for _, f := range result.Findings {
		if len(f.Differences) > maxItems || len(f.EvidenceRefs) > maxItems || len(f.QueryScope.Addresses) > maxItems || len(f.QueryScope.Ports) > maxItems || len(f.Limitations) > maxItems {
			return nil, ErrTooLarge
		}
		parts += int64(len(f.Differences))*8 + int64(len(f.EvidenceRefs)) + int64(len(f.QueryScope.Addresses)) + int64(len(f.QueryScope.Ports)) + int64(len(f.Limitations))
		if parts > MaxBytes/32 {
			return nil, ErrTooLarge
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	source, ok := sources[result.Manifest.SourceKind]
	if !ok {
		return nil, ErrInvalidResult
	}
	m := result.Manifest
	s := m.PolicyScope
	if s.Ref == "" || s.Provider != "aliyun" || s.ControlPlaneRef == "" || s.IPFamily != "ipv4" || !((s.Boundary == "internet" && member(s.Direction, "in", "out")) || (s.Boundary == "nat" && s.Direction == "out" && s.GatewayRef != "")) {
		return nil, ErrInvalidResult
	}
	if result.Coverage.EvaluatedCells < 0 || result.Coverage.EvaluatedQueries < 0 {
		return nil, ErrInvalidResult
	}
	evaluation, err := canonicalTime(m.EvaluationAt)
	if err != nil || evaluation == "" {
		return nil, ErrInvalidResult
	}
	collected, err := canonicalTime(m.CollectedAt)
	if err != nil || collected == "" {
		return nil, ErrInvalidResult
	}
	a := newAliases()
	for _, entry := range []struct{ kind, value string }{{"snapshot", m.SnapshotVersion}, {"profile", m.ProfileVersion}, {"intent", m.IntentVersion}, {"baseline", m.BaselineVersion}, {"normalizer", m.NormalizerVersion}, {"kernel", m.KernelVersion}, {"semantics", m.ProviderSemantics.ProfileID}, {"semantics-version", m.ProviderSemantics.Version}, {"scope", s.Ref}, {"account", s.AccountRef}, {"policy-set", s.PolicySetRef}, {"gateway", s.GatewayRef}, {"control-plane", s.ControlPlaneRef}} {
		a.add(entry.kind, entry.value)
	}
	evidenceByID := map[string]domain.Evidence{}
	for _, e := range result.Evidence {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if e.ID == "" || e.SnapshotVersion != m.SnapshotVersion || e.ScopeRef != s.Ref {
			return nil, ErrInvalidResult
		}
		if _, exists := evidenceByID[e.ID]; exists {
			return nil, ErrInvalidResult
		}
		if _, err = canonicalTime(e.ObservedAt); err != nil {
			return nil, err
		}
		evidenceByID[e.ID] = e
		a.add("evidence", e.ID)
		a.add("object", e.SubjectRef)
		a.add("policy", e.PolicyRef)
	}
	findingByID := map[string]bool{}
	for _, f := range result.Findings {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if f.ID == "" || f.ScopeRef != s.Ref || f.RuleVersion == "" || f.SubjectRef == "" || f.IssueKey == "" || findingByID[f.ID] {
			return nil, ErrInvalidResult
		}
		findingByID[f.ID] = true
		if _, ok := ruleText[f.RuleID]; !ok {
			return nil, ErrInvalidResult
		}
		if !member(f.Origin, "rule", "model") || !member(f.Verdict, "pass", "fail", "needs_confirmation", "insufficient_data", "unsupported", "not_applicable") || f.AssessmentBasis != source.basis || !member(f.Severity, "", "high", "medium", "low", "info", "informational", "information", "unassessed") || !member(f.RemediationPriority, "", "R1", "R2", "R3", "V0") {
			return nil, ErrInvalidResult
		}
		if f.Origin == "model" && f.Verdict != "needs_confirmation" {
			return nil, ErrInvalidResult
		}
		if !validPorts(f.QueryScope.Ports) {
			return nil, ErrInvalidResult
		}
		if len(f.EvidenceRefs) > maxItems || len(f.Differences) > maxItems || len(f.Limitations) > maxItems {
			return nil, ErrInvalidResult
		}
		for _, ref := range f.EvidenceRefs {
			e, exists := evidenceByID[ref]
			if !exists || e.ScopeRef != f.ScopeRef {
				return nil, ErrInvalidResult
			}
		}
		a.add("finding", f.ID)
		a.add("rule-version", f.RuleVersion)
		a.add("object", f.SubjectRef)
		a.add("service", f.ServiceRef)
		a.add("issue", f.IssueKey)
		a.add("address", f.QueryScope.FixedAddress)
		for _, address := range f.QueryScope.Addresses {
			a.add("address", address)
		}
		for _, d := range f.Differences {
			if !validDifference(f, d) {
				return nil, ErrInvalidResult
			}
			if d.Action == "candidate" {
				policyEvidence := false
				for _, ref := range f.EvidenceRefs {
					if evidenceByID[ref].PolicyRef == d.PolicyRef {
						policyEvidence = true
						break
					}
				}
				if !policyEvidence {
					return nil, ErrInvalidResult
				}
			}
			a.add("address", d.AddressFrom)
			a.add("address", d.AddressTo)
			a.add("policy", d.PolicyRef)
		}
	}
	byReview := map[string]ReviewHead{}
	for _, r := range reviews {
		if !findingByID[r.FindingID] || r.Revision < 0 || (r.Revision == 0 && r.Plan != "") || !member(r.Plan, "", "todo", "deferred") {
			return nil, ErrInvalidResult
		}
		if _, exists := byReview[r.FindingID]; exists {
			return nil, ErrInvalidResult
		}
		byReview[r.FindingID] = r
	}
	a.seal()
	doc := Document{SchemaVersion: 1, ProjectionVersion: "public-v1", SourceKind: m.SourceKind, Qualification: source.qualification, EvaluationAt: evaluation, CollectedAt: collected,
		Versions: Versions{a.get("snapshot", m.SnapshotVersion), a.get("profile", m.ProfileVersion), a.get("intent", m.IntentVersion), a.get("baseline", m.BaselineVersion), a.get("normalizer", m.NormalizerVersion), a.get("kernel", m.KernelVersion), a.get("semantics", m.ProviderSemantics.ProfileID), a.get("semantics-version", m.ProviderSemantics.Version)},
		Scope:    Scope{a.get("scope", s.Ref), s.Provider, a.get("account", s.AccountRef), s.Boundary, s.Direction, a.get("policy-set", s.PolicySetRef), a.get("gateway", s.GatewayRef), a.get("control-plane", s.ControlPlaneRef), s.IPFamily},
		Coverage: Coverage{result.Coverage.Complete, result.Coverage.EvaluatedQueries, result.Coverage.EvaluatedCells, notices(result.Coverage.Limitations)}, Findings: []Finding{}, Evidence: []Evidence{}}
	doc.Execution = execution
	if !doc.Coverage.Complete && len(doc.Coverage.Limitations) == 0 {
		doc.Coverage.Limitations = notices([]string{"coverage_incomplete"})
	}
	findings := append([]domain.Finding(nil), result.Findings...)
	sort.Slice(findings, func(i, j int) bool { return findings[i].ID < findings[j].ID })
	for _, f := range findings {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		conditions := []Notice{{source.code, source.text}}
		if f.AcceptedException {
			conditions = append(conditions, Notice{"accepted_exception_requires_review", "已接受的例外仍需按原范围与有效期复核，不表示无风险。"})
		}
		q := Query{Protocol: f.QueryScope.Protocol, FixedAddressAlias: a.get("address", f.QueryScope.FixedAddress), AddressAliases: []string{}, Ports: append([]domain.PortRange{}, f.QueryScope.Ports...)}
		if !member(q.Protocol, "", "any", "tcp", "udp", "icmp") {
			q.Protocol = "unsupported"
		}
		for _, address := range f.QueryScope.Addresses {
			q.AddressAliases = append(q.AddressAliases, a.get("address", address))
		}
		p := Finding{Alias: a.get("finding", f.ID), RuleID: f.RuleID, RuleVersionAlias: a.get("rule-version", f.RuleVersion), Origin: f.Origin, SubjectAlias: a.get("object", f.SubjectRef), ServiceAlias: a.get("service", f.ServiceRef), IssueAlias: a.get("issue", f.IssueKey), Verdict: f.Verdict, AssessmentBasis: f.AssessmentBasis, Severity: f.Severity, RemediationPriority: f.RemediationPriority, Explanation: ruleText[f.RuleID], Conditions: conditions, Query: q, Differences: []Difference{}, EvidenceAliases: []string{}, Limitations: notices(f.Limitations), AcceptedException: f.AcceptedException}
		if f.Origin == "model" {
			p.Explanation = "关联该检查的模型疑点，仅供人工核实，不改变原始规则结论。"
		}
		for _, d := range f.Differences {
			if d.Action == "candidate" {
				p.Differences = append(p.Differences, Difference{Kind: "duplicate_policy_candidate", Explanation: "精确重复配置维护候选，需人工复核策略位置及业务依赖。", PolicyAlias: a.get("policy", d.PolicyRef)})
				continue
			}
			from, to := d.PortFrom, d.PortTo
			p.Differences = append(p.Differences, Difference{Kind: "flow", Explanation: "所审配置查询中的地址及端口差异，不证明实际可达。", AddressFromAlias: a.get("address", d.AddressFrom), AddressToAlias: a.get("address", d.AddressTo), PortFrom: &from, PortTo: &to, Action: d.Action, PolicyAlias: a.get("policy", d.PolicyRef)})
		}
		for _, ref := range f.EvidenceRefs {
			p.EvidenceAliases = append(p.EvidenceAliases, a.get("evidence", ref))
		}
		if r, ok := byReview[f.ID]; ok {
			p.ReviewRevision = r.Revision
			p.Plan = r.Plan
		}
		doc.Findings = append(doc.Findings, p)
	}
	evidence := append([]domain.Evidence(nil), result.Evidence...)
	sort.Slice(evidence, func(i, j int) bool { return evidence[i].ID < evidence[j].ID })
	for _, e := range evidence {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		observed, _ := canonicalTime(e.ObservedAt)
		doc.Evidence = append(doc.Evidence, Evidence{a.get("evidence", e.ID), a.get("snapshot", m.SnapshotVersion), a.get("scope", s.Ref), a.get("object", e.SubjectRef), a.get("policy", e.PolicyRef), observed, "固定输入内的证据引用；原字段位置仅在受保护的本地视图提供。"})
	}
	var body []byte
	if format == "json" {
		body, err = json.MarshalIndent(doc, "", "  ")
		body = append(body, '\n')
	} else {
		body, err = markdown(ctx, doc)
	}
	if err != nil {
		return nil, err
	}
	if len(body) > MaxBytes {
		return nil, ErrTooLarge
	}
	return body, nil
}

func markdown(ctx context.Context, d Document) ([]byte, error) {
	var out bytes.Buffer
	fmt.Fprintf(&out, "# 云防火墙策略审计报告\n\n%s\n\n", d.Qualification)
	if d.Execution != nil {
		stageText := map[string]string{"not_requested": "仅规则模式；未请求模型。", "not_configured": "模型未配置或未获授权，模型阶段未执行；规则结果保留。", "not_completed": "模型阶段未完成；已保存规则结果保留。", "completed": "模型阶段流程已结束；模型补充仍需人工核实。"}[d.Execution.ModelStage]
		fmt.Fprintf(&out, "执行模式：%s。%s\n\n", d.Execution.Mode, stageText)
		if d.Execution.JobStatus != "" {
			jobText := map[string]string{"completed": "整体任务流程已结束；这不表示所有检查通过或风险已修复。", "partial": "整体任务部分完成；已保存规则或模型结果保留，未完成阶段仍需核对。", "failed": "整体任务未完成；已有阶段结果不代表全流程成功。", "cancelled": "整体任务已取消；已有阶段结果保留，未自动恢复执行。", "interrupted": "整体任务中断；模型阶段完成不表示整个流程已完成。"}[d.Execution.JobStatus]
			fmt.Fprintf(&out, "任务终态：%s。%s\n\n", d.Execution.JobStatus, jobText)
		}
	}
	if d.Agent != nil {
		if err := agentMarkdown(ctx, &out, d.Agent); err != nil {
			return nil, err
		}
	}
	fmt.Fprintf(&out, "评估时点：%s。范围：%s / %s（%s）。\n\n", d.EvaluationAt, d.Scope.Boundary, d.Scope.Direction, d.Scope.Alias)
	fmt.Fprintf(&out, "采集／构造时点：%s。\n\n", d.CollectedAt)
	fmt.Fprintf(&out, "来源：%s；provider=%s；account=%s；control_plane=%s；ip_family=%s；policy_set=%s；gateway=%s。\n\n", d.SourceKind, d.Scope.Provider, d.Scope.AccountAlias, d.Scope.ControlPlaneAlias, d.Scope.IPFamily, d.Scope.PolicySetAlias, d.Scope.GatewayAlias)
	fmt.Fprintf(&out, "固定版本：snapshot=%s；profile=%s；intent=%s；baseline=%s；normalizer=%s；kernel=%s；semantics=%s/%s。\n\n", d.Versions.Snapshot, d.Versions.Profile, d.Versions.Intent, d.Versions.Baseline, d.Versions.Normalizer, d.Versions.Kernel, d.Versions.SemanticsProfile, d.Versions.SemanticsVersion)
	fmt.Fprintf(&out, "检查覆盖：complete=%t；queries=%d；cells=%d。\n\n", d.Coverage.Complete, d.Coverage.EvaluatedQueries, d.Coverage.EvaluatedCells)
	for _, n := range d.Coverage.Limitations {
		fmt.Fprintf(&out, "- %s：%s\n", n.Code, n.Text)
	}
	for _, f := range d.Findings {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		fmt.Fprintf(&out, "\n## %s · %s\n\n%s\n\n对象：%s；服务：%s；问题族：%s。\n\n", f.Alias, f.RuleID, f.Explanation, f.SubjectAlias, f.ServiceAlias, f.IssueAlias)
		fmt.Fprintf(&out, "判定：%s；来源：%s；严重程度：%s；整改顺序：%s；origin=%s。\n\n", f.Verdict, f.AssessmentBasis, f.Severity, f.RemediationPriority, f.Origin)
		for _, n := range f.Conditions {
			fmt.Fprintf(&out, "- 条件 %s：%s\n", n.Code, n.Text)
		}
		for _, n := range f.Limitations {
			fmt.Fprintf(&out, "- 缺口 %s：%s\n", n.Code, n.Text)
		}
		fmt.Fprintf(&out, "\n证据：%s。规则版本：%s。\n\n", strings.Join(f.EvidenceAliases, ", "), f.RuleVersionAlias)
		if f.Query.Protocol != "" || f.Query.FixedAddressAlias != "" || len(f.Query.AddressAliases) > 0 || len(f.Query.Ports) > 0 {
			fmt.Fprintf(&out, "查询：protocol=%s；fixed_address=%s；addresses=%s。\n\n", f.Query.Protocol, f.Query.FixedAddressAlias, strings.Join(f.Query.AddressAliases, ", "))
			for _, p := range f.Query.Ports {
				fmt.Fprintf(&out, "- 查询端口：%d–%d。\n", p.From, p.To)
			}
		}
		for _, diff := range f.Differences {
			if diff.Kind == "duplicate_policy_candidate" {
				fmt.Fprintf(&out, "- 精确重复配置维护候选（duplicate_policy_candidate）：%s。%s\n", diff.PolicyAlias, diff.Explanation)
				continue
			}
			if diff.Kind != "flow" || diff.PortFrom == nil || diff.PortTo == nil {
				return nil, ErrInvalidResult
			}
			fmt.Fprintf(&out, "- 配置差异（flow）：%s 至 %s；端口 %d–%d；动作 %s；策略 %s。%s\n", diff.AddressFromAlias, diff.AddressToAlias, *diff.PortFrom, *diff.PortTo, diff.Action, diff.PolicyAlias, diff.Explanation)
		}
		fmt.Fprintf(&out, "\n本地审阅：review_revision=%d；plan=%s。待办／暂缓仅为本地处理记录。\n", f.ReviewRevision, f.Plan)
		if out.Len() > MaxBytes {
			return nil, ErrTooLarge
		}
	}
	fmt.Fprint(&out, "\n## 证据索引\n\n")
	for _, e := range d.Evidence {
		fmt.Fprintf(&out, "- %s：snapshot=%s，scope=%s，object=%s，policy=%s，observed_at=%s。%s\n", e.Alias, e.SnapshotAlias, e.ScopeAlias, e.SubjectAlias, e.PolicyAlias, e.ObservedAt, e.Explanation)
		if out.Len() > MaxBytes {
			return nil, ErrTooLarge
		}
	}
	return out.Bytes(), nil
}
