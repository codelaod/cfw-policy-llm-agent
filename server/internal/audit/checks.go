package audit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"cfw-policy-llm-agent/server/internal/domain"
)

type runState struct {
	input    domain.Input
	result   domain.Result
	ctx      context.Context
	enabled  map[string]bool
	identity string
	findings map[string]int
	evidence map[string]bool
}

func stableID(prefix, s string) string {
	sum := sha256.Sum256([]byte(s))
	return prefix + hex.EncodeToString(sum[:12])
}
func validAssociation(in domain.Input, a domain.Asset) bool {
	_, err := time.Parse(time.RFC3339Nano, a.Association.ObservedAt)
	return a.Association.Status == "evidenced" && a.Association.GatewayRef == in.Scope.GatewayRef && a.Association.ObservationPoint != "" && a.Association.EvidenceRef != "" && err == nil
}
func manifest(in domain.Input) domain.InputManifest {
	return domain.InputManifest{SnapshotVersion: in.SnapshotVersion, ProfileVersion: in.ProfileVersion, IntentVersion: in.IntentVersion, BaselineVersion: in.Baseline.Version, NormalizerVersion: in.NormalizerVersion, KernelVersion: in.KernelVersion, ProviderSemantics: in.Semantics, PolicyScope: in.Scope, EvaluationAt: in.EvaluationAt, CollectedAt: in.CollectedAt, SourceKind: in.SourceKind}
}

// Run evaluates only frozen local inputs. It never alters the input and never
// obtains credentials, performs HTTP, or uses the current clock as a rule fact.
func Run(ctx context.Context, in domain.Input) (domain.Result, error) {
	ctx, cancel := context.WithTimeout(ctx, executionLimits(ctx).Timeout)
	defer cancel()
	if err := ValidateContext(ctx, in); err != nil {
		return domain.Result{}, err
	}
	r := &runState{input: in, ctx: ctx, enabled: map[string]bool{}, result: domain.Result{SchemaVersion: 1, Manifest: manifest(in), Findings: []domain.Finding{}, Evidence: []domain.Evidence{}, Coverage: domain.Coverage{Complete: true, Limitations: []string{"仅覆盖所选IPv4/TCP/UDP配置子域，不证明实际可达或运行时监听。"}}}}
	inputJSON, _ := json.Marshal(in)
	r.identity = stableID("input_", string(inputJSON))
	r.findings = map[string]int{}
	r.evidence = map[string]bool{}
	for _, id := range in.Baseline.EnabledChecks {
		r.enabled[id] = true
	}
	if len(r.enabled) == 0 {
		r.result.Coverage.Complete = false
		r.result.Coverage.Limitations = append(r.result.Coverage.Limitations, "NO_ENABLED_CHECKS")
		return r.result, nil
	}
	if in.Scope.Boundary == "nat" {
		if in.Selection.Kind == "gateway_policy" {
			if r.enabled["NAT-01"] {
				r.add("NAT-01", "", in.Scope.GatewayRef, "", "not_applicable", "网关策略集模式不推定任何资产到网关的路径。", domain.Query{}, nil, nil)
			}
			if r.enabled["NAT-02"] {
				r.duplicates("NAT-02", "CHK-04")
				r.coverage("NAT-02", "CHK-05", in.Scope.GatewayRef)
				for _, id := range []string{"OUT-01", "OUT-02"} {
					r.add("NAT-02", id, in.Scope.GatewayRef, "", "not_applicable", "未选择资产；资产级外联比较不适用。", domain.Query{}, nil, nil)
				}
			}
		} else {
			if r.enabled["NAT-02"] {
				r.duplicates("NAT-02", "CHK-04")
			}
			for _, a := range in.Assets {
				if ctx.Err() != nil {
					break
				}
				if r.enabled["NAT-01"] {
					v, s := "pass", "资产、网关和地址观察点具有同范围关联证据。"
					if !validAssociation(in, a) {
						v, s = "insufficient_data", "NAT资产关联、网关或地址观察点证据不足；不能推定转换路径。"
					}
					r.add("NAT-01", "", a.ID, "", v, s, domain.Query{}, nil, nil)
				}
				if r.enabled["NAT-02"] {
					r.coverage("NAT-02", "CHK-05", a.ID)
					r.outbound(a, true)
				}
			}
		}
	} else {
		if r.enabled["CHK-04"] {
			r.duplicates("CHK-04", "")
		}
		for _, a := range in.Assets {
			if ctx.Err() != nil {
				break
			}
			if r.enabled["CHK-05"] {
				r.coverage("CHK-05", "", a.ID)
			}
			if in.Scope.Direction == "in" {
				r.inbound(a)
			} else {
				r.outbound(a, false)
				for _, id := range []string{"CHK-01", "CHK-02", "CHK-03", "CHK-06", "CHK-07", "CHK-08"} {
					if r.enabled[id] {
						r.add(id, "", a.ID, "", "not_applicable", "入向服务／来源判据不直接应用到出向目标。", domain.Query{}, nil, nil)
					}
				}
			}
		}
	}
	if ctx.Err() != nil {
		r.result.Coverage.Complete = false
		r.result.Coverage.Limitations = append(r.result.Coverage.Limitations, "RULE_STAGE_INTERRUPTED")
		return r.result, ctx.Err()
	}
	r.result.Coverage.Limitations = uniqueStrings(r.result.Coverage.Limitations)
	return r.result, nil
}

func (r *runState) add(check, sub, subject, service, verdict, summary string, q domain.Query, differences []domain.Cell, reasons []string) {
	if differences == nil {
		differences = []domain.Cell{}
	}
	basis := map[string]string{"synthetic": "synthetic_assumption", "imported": "imported_assumption", "live_readonly": "live_observed"}[r.input.SourceKind]
	assumptions := []string{"只分析固定输入时点的本层配置，不证明端到端可达或业务故障。"}
	if basis == "imported_assumption" {
		assumptions = append(assumptions, "导入声明条件下：所提供覆盖及完整性声明尚未核实，不能称为真实账号全量。")
	}
	if basis == "synthetic_assumption" {
		assumptions = append(assumptions, "仅在此人工案例设定成立时有效，不证明真实云效果。")
	}
	key, _ := json.Marshal(struct {
		Check, Subcheck, Subject, Service string
		Query                             domain.Query
	}{check, sub, subject, service, q})
	family := check
	if sub != "" {
		family = sub
	}
	if family == "CHK-01" || family == "CHK-03" || family == "CHK-07" || family == "CHK-08" {
		family = "excess-inbound-service"
	}
	canonicalScope := r.input.Scope
	canonicalScope.Ref = ""
	issueKey, _ := json.Marshal(struct {
		Scope                              domain.Scope
		Subject, Service, Family, Protocol string
		Ports                              []domain.PortRange
	}{canonicalScope, subject, service, family, q.Protocol, q.Ports})
	f := domain.Finding{ID: stableID("finding_", r.identity+"/"+string(key)), RuleID: check, Subcheck: sub, RuleVersion: r.input.Baseline.Version, Origin: "rule", ScopeRef: r.input.Scope.Ref, SubjectRef: subject, ServiceRef: service, IssueKey: stableID("issue_", string(issueKey)), Verdict: verdict, AssessmentBasis: basis, Assumptions: assumptions, Severity: "information", RemediationPriority: "R3", Summary: summary, QueryScope: cloneQuery(q), Differences: differences, EvidenceRefs: []string{}, Limitations: uniqueStrings(reasons)}
	if verdict == "fail" {
		f.Severity = "medium"
		f.RemediationPriority = "R2"
		if check == "CHK-01" || check == "CHK-07" || check == "CHK-08" {
			f.Severity = "high"
			f.RemediationPriority = "R1"
		}
		if check == "CHK-04" || sub == "CHK-04" {
			f.Severity = "low"
			f.RemediationPriority = "R3"
		}
	}
	if verdict == "needs_confirmation" || verdict == "insufficient_data" || verdict == "unsupported" {
		f.Severity = "unassessed"
		f.RemediationPriority = "V0"
	}
	if verdict == "insufficient_data" || verdict == "unsupported" || len(reasons) > 0 {
		r.result.Coverage.Complete = false
	}
	pointers := map[string]string{"/baseline": "", "/provider_semantics": "", "/policies": ""}
	if check == "CHK-05" || sub == "CHK-05" {
		pointers["/protection"] = ""
	}
	if len(reasons) > 0 {
		pointers["/completeness"] = ""
	}
	for i, a := range r.input.Assets {
		if a.ID != subject {
			continue
		}
		pointer := "/assets/" + strconv.Itoa(i)
		pointers[pointer] = ""
		if a.Protection != nil && r.input.Scope.Boundary == "internet" {
			pointers[pointer+"/protection"] = ""
			if check == "CHK-05" || sub == "CHK-05" {
				delete(pointers, "/protection")
			}
		}
		if check == "NAT-01" {
			pointers[pointer+"/association"] = ""
		}
		for j, s := range a.Services {
			if s.ID == service {
				pointers[pointer+"/services/"+strconv.Itoa(j)] = ""
			}
		}
	}
	for _, c := range differences {
		if c.PolicyRef != "" {
			for i, p := range r.input.Policies {
				if p.ID == c.PolicyRef {
					pointers["/policies/"+strconv.Itoa(i)] = p.ID
				}
			}
		} else if c.Action == "accept" || c.Action == "drop" {
			pointers["/provider_semantics/default_action"] = ""
		}
	}
	fields := make([]string, 0, len(pointers))
	for pointer := range pointers {
		fields = append(fields, pointer)
	}
	sort.Strings(fields)
	for _, pointer := range fields {
		e := domain.Evidence{ID: stableID("evidence_", f.ID+"/"+pointer), SnapshotVersion: r.input.SnapshotVersion, ScopeRef: r.input.Scope.Ref, SubjectRef: subject, PolicyRef: pointers[pointer], Field: pointer, Summary: "判据、范围与来源绑定固定输入版本。", ObservedAt: r.input.CollectedAt}
		if !r.evidence[e.ID] {
			r.result.Evidence = append(r.result.Evidence, e)
			r.evidence[e.ID] = true
		}
		f.EvidenceRefs = append(f.EvidenceRefs, e.ID)
	}
	at, _ := time.Parse(time.RFC3339Nano, r.input.EvaluationAt)
	for _, x := range r.input.Baseline.Exceptions {
		expiry, _ := time.Parse(time.RFC3339Nano, x.ExpiresAt)
		if x.Confirmed && x.CheckID == check && (x.Subcheck == "" || x.Subcheck == sub) && x.ScopeRef == r.input.Scope.Ref && x.SubjectRef == subject && (x.ServiceRef == "" || x.ServiceRef == service) && expiry.After(at) {
			f.AcceptedException = true
			f.Limitations = uniqueStrings(append(f.Limitations, "已接受例外；原始事实仍保留，需按到期复核。"))
		}
	}
	if index, exists := r.findings[f.ID]; exists {
		old := r.result.Findings[index]
		chosen := old
		if verdictRank(f.Verdict) > verdictRank(old.Verdict) {
			chosen = f
		}
		chosen.Differences = uniqueCells(append(old.Differences, f.Differences...))
		chosen.EvidenceRefs = uniqueStrings(append(old.EvidenceRefs, f.EvidenceRefs...))
		chosen.Limitations = uniqueStrings(append(old.Limitations, f.Limitations...))
		chosen.Assumptions = uniqueStrings(append(old.Assumptions, f.Assumptions...))
		chosen.AcceptedException = old.AcceptedException || f.AcceptedException
		r.result.Findings[index] = chosen
	} else {
		r.findings[f.ID] = len(r.result.Findings)
		r.result.Findings = append(r.result.Findings, f)
	}
}

func verdictRank(verdict string) int {
	return map[string]int{"not_applicable": 0, "pass": 1, "needs_confirmation": 2, "insufficient_data": 3, "unsupported": 4, "fail": 5}[verdict]
}
func uniqueCells(cells []domain.Cell) []domain.Cell {
	out := []domain.Cell{}
	seen := map[string]bool{}
	for _, c := range cells {
		key, _ := json.Marshal(c)
		if !seen[string(key)] {
			seen[string(key)] = true
			out = append(out, c)
		}
	}
	return out
}

func (r *runState) path(q domain.Query) domain.PathResult {
	p, err := Evaluate(r.ctx, r.input, q)
	if err != nil {
		p.Complete = false
		p.Reasons = append(p.Reasons, "QUERY_INTERRUPTED")
	}
	r.result.Coverage.EvaluatedQueries++
	r.result.Coverage.EvaluatedCells += p.EvaluatedCells
	if !p.Complete {
		r.result.Coverage.Complete = false
		r.result.Coverage.Limitations = append(r.result.Coverage.Limitations, p.Reasons...)
	}
	return p
}
func classify(p domain.PathResult, diffs []domain.Cell) string {
	if len(diffs) > 0 {
		return "fail"
	}
	if p.Complete {
		return "pass"
	}
	for _, reason := range p.Reasons {
		if strings.Contains(reason, "UNSUPPORTED") {
			return "unsupported"
		}
	}
	return "insufficient_data"
}
func serviceQuery(a domain.Asset, s domain.Service) domain.Query {
	return domain.Query{SubjectRef: a.ID, ServiceRef: s.ID, FixedAddress: a.Address, Protocol: s.Protocol, Addresses: []string{"0.0.0.0/0"}, Ports: s.Ports}
}
func (r *runState) upper(check, sub string, a domain.Asset, s domain.Service, confirmed, complete bool, allowed []domain.Access) {
	q := serviceQuery(a, s)
	if !confirmed || !complete {
		r.add(check, sub, a.ID, s.ID, "needs_confirmation", "允许上界尚未明确确认完整；不能把未填写项视为禁止。", q, nil, nil)
		return
	}
	for _, v := range allowed {
		if v.Applicability == "unknown" {
			r.add(check, sub, a.ID, s.ID, "insufficient_data", "业务访问是否属于所审边界尚未核实。", q, nil, []string{"BUSINESS_SCOPE_UNKNOWN"})
			return
		}
	}
	compiled, err := compileAccess(r.ctx, allowed)
	if err != nil {
		r.add(check, sub, a.ID, s.ID, "insufficient_data", "业务集合校验已取消；未完成范围不判通过。", q, nil, []string{"QUERY_CANCELLED"})
		return
	}
	p := r.path(q)
	diff := []domain.Cell{}
	for _, c := range p.Cells {
		if c.Action == "accept" {
			parts, complete, err := outsideAccess(r.ctx, c, compiled, q.Protocol)
			diff = append(diff, parts...)
			if !complete {
				p.Complete = false
				if err != nil {
					p.Reasons = append(p.Reasons, "QUERY_CANCELLED")
					break
				} else {
					p.Reasons = append(p.Reasons, "QUERY_CELL_LIMIT")
				}
			}
		}
	}
	diff = mergeCells(diff)
	v := classify(p, diff)
	summary := "已声明允许上界与本层配置允许范围一致（仅所审子域）。"
	if v == "fail" {
		summary = "本层配置允许范围超出已确认的完整业务允许上界。"
	}
	if v == "unsupported" || v == "insufficient_data" {
		summary = "存在未知／未支持范围，不能证明完整业务上界比较通过。"
	}
	r.add(check, sub, a.ID, s.ID, v, summary, q, diff, p.Reasons)
}

func (r *runState) necessary(check, sub string, a domain.Asset, s domain.Service) {
	q := serviceQuery(a, s)
	if !s.RequiredConfirmed {
		r.add(check, sub, a.ID, s.ID, "needs_confirmation", "必要访问尚未明确确认，不从角色或历史流量推定。", q, nil, nil)
		return
	}
	if len(s.Required) == 0 {
		r.add(check, sub, a.ID, s.ID, "not_applicable", "没有已声明的必要访问项。", q, nil, nil)
		return
	}
	applicable := false
	for _, need := range s.Required {
		if need.Applicability == "not_applicable" {
			continue
		}
		applicable = true
		if need.Applicability == "unknown" {
			r.add(check, sub, a.ID, s.ID, "insufficient_data", "必要访问的边界归属未核实。", q, nil, []string{"BUSINESS_SCOPE_UNKNOWN"})
			continue
		}
		protocols := []string{need.Protocol}
		if need.Protocol == "any" {
			protocols = []string{"tcp", "udp"}
		}
		for _, protocol := range protocols {
			nq := q
			nq.Protocol = protocol
			nq.Addresses = need.Addresses
			nq.Ports = need.Ports
			p := r.path(nq)
			diff := []domain.Cell{}
			for _, c := range p.Cells {
				if c.Action == "drop" {
					diff = append(diff, c)
				}
			}
			v := classify(p, diff)
			summary := "已声明必要访问未被所审本层配置拒绝；不代表端到端可达。"
			if v == "fail" {
				summary = "明确必要访问与本层配置拒绝范围相交；不据此证明实际业务故障。"
			}
			r.add(check, sub, a.ID, s.ID, v, summary, nq, diff, p.Reasons)
		}
	}
	if !applicable {
		r.add(check, sub, a.ID, s.ID, "not_applicable", "已声明必要访问明确不属于当前互联网／NAT边界。", q, nil, nil)
	}
}

func (r *runState) coverage(check, sub, subject string) {
	p := r.input.ProtectionFor(subject)
	v, summary := "pass", "有来源的状态表明当前该CFW层已开启；不证明数据面或其他防护层。"
	if !p.Applicable || !p.Required {
		v, summary = "not_applicable", "没有已采用的本层保护要求或本层不适用。"
	} else if p.EvidenceRef == "" || p.State == "unknown" || p.State == "transitioning" || p.State == "" {
		v, summary = "insufficient_data", "当前该CFW层状态未知／切换中，需要核对来源与覆盖。"
	} else if p.State == "off" {
		v, summary = "fail", "当前该CFW层明确未保护；其他防护层状态不由此推断。"
	} else if p.State != "on" {
		v, summary = "insufficient_data", "未知保护状态不能当成已启用或缺失。"
	}
	r.add(check, sub, subject, "", v, summary, domain.Query{}, nil, nil)
}

func (r *runState) inbound(a domain.Asset) {
	if r.enabled["CHK-02"] {
		if !a.AllowedConfirmed || !a.AllowedComplete {
			r.add("CHK-02", "", a.ID, "", "needs_confirmation", "资产整体协议／端口允许集合未确认完整，不能由服务列表推断。", domain.Query{}, nil, nil)
		} else {
			for _, proto := range []string{"tcp", "udp"} {
				s := domain.Service{Protocol: proto, Ports: []domain.PortRange{{From: 1, To: 65535}}}
				r.upper("CHK-02", "", a, s, true, true, a.Allowed)
			}
		}
	}
	for _, s := range a.Services {
		if r.ctx.Err() != nil {
			return
		}
		if r.enabled["CHK-01"] {
			if s.RoleStatus == "confirmed" && s.Role == "management" {
				r.upper("CHK-01", "", a, s, s.AllowedConfirmed, s.AllowedComplete, s.Allowed)
			} else {
				r.add("CHK-01", "", a.ID, s.ID, "not_applicable", "该服务未被确认为管理服务；不从端口或标签自动确认角色。", serviceQuery(a, s), nil, nil)
			}
		}
		if r.enabled["CHK-03"] {
			r.upper("CHK-03", "", a, s, s.AllowedConfirmed, s.AllowedComplete, s.Allowed)
		}
		if r.enabled["CHK-06"] {
			r.necessary("CHK-06", "", a, s)
		}
		if r.enabled["CHK-08"] {
			q := serviceQuery(a, s)
			if s.Role == "database" && s.RoleStatus != "confirmed" {
				r.add("CHK-08", "", a.ID, s.ID, "needs_confirmation", "数据库角色仅是候选／冲突，不能作为已确认业务依据。", q, nil, nil)
			} else if s.Role != "database" {
				r.add("CHK-08", "", a.ID, s.ID, "not_applicable", "数据库角色判据不适用于此服务。", q, nil, nil)
			} else if s.PublicAccess != "not_required" {
				r.add("CHK-08", "", a.ID, s.ID, "needs_confirmation", "数据库角色本身不证明无需公网访问，需明确该服务访问要求。", q, nil, nil)
			} else {
				q.Addresses = publicAddresses()
				p := r.publicPath(q, false)
				diff := []domain.Cell{}
				for _, c := range p.Cells {
					if c.Action == "accept" {
						diff = append(diff, c)
					}
				}
				r.add("CHK-08", "", a.ID, s.ID, classify(p, diff), "按已确认数据库服务的实际端口检查无需公网访问要求；不影响同机其他服务。", q, diff, p.Reasons)
			}
		}
	}
	if r.enabled["CHK-07"] {
		for _, risk := range r.input.Baseline.RiskPorts {
			service := ""
			for _, s := range a.Services {
				if s.Protocol == risk.Protocol && contains(portRanges(s.Ports), uint64(risk.Port)) {
					service = s.ID
					break
				}
			}
			q := domain.Query{SubjectRef: a.ID, ServiceRef: service, FixedAddress: a.Address, Protocol: risk.Protocol, Addresses: publicAddresses(), Ports: []domain.PortRange{{From: risk.Port, To: risk.Port}}}
			p := r.publicPath(q, true)
			diff := []domain.Cell{}
			for _, c := range p.Cells {
				if c.Action == "accept" && c.Broad {
					diff = append(diff, c)
				}
			}
			r.add("CHK-07", "", a.ID, service, classify(p, diff), "仅核对公共单播配置子域内，所选风险端口由全地址条目或默认放行形成的剩余允许范围。", q, diff, p.Reasons)
		}
	}
}

func (r *runState) outbound(a domain.Asset, nat bool) {
	services := a.Services
	if len(services) == 0 {
		services = []domain.Service{{ID: "", Protocol: "unknown"}}
	}
	for _, s := range services {
		if nat {
			r.upper("NAT-02", "OUT-01", a, s, s.AllowedConfirmed, s.AllowedComplete, s.Allowed)
			r.necessary("NAT-02", "OUT-02", a, s)
		} else {
			if r.enabled["OUT-01"] {
				r.upper("OUT-01", "", a, s, s.AllowedConfirmed, s.AllowedComplete, s.Allowed)
			}
			if r.enabled["OUT-02"] {
				r.necessary("OUT-02", "", a, s)
			}
		}
	}
}

func (r *runState) duplicates(check, sub string) {
	policies := compilePolicies(r.input, true)
	groups := map[string][]string{}
	missing, unsupported := false, false
	for _, p := range policies {
		if r.ctx.Err() != nil {
			return
		}
		if !p.sourceKnown || !p.destinationKnown || !p.portsKnown || !p.p.OrderKnown || p.p.Enabled == "unknown" || p.p.Enabled == "" || p.p.Action == "unknown" || p.p.Action == "" {
			missing = true
			continue
		}
		proto := strings.ToLower(p.p.Protocol)
		if (p.p.Enabled != "enabled" && p.p.Enabled != "disabled") || len(p.p.Conditions) > 0 || (p.p.Action != "accept" && p.p.Action != "drop") || (proto != "tcp" && proto != "udp" && proto != "any") {
			unsupported = true
			continue
		}
		key := struct {
			Source, Destination, Ports [][2]uint64
			Protocol, Action, Enabled  string
		}{rangePairs(p.source), rangePairs(p.destination), rangePairs(p.ports), strings.ToLower(p.p.Protocol), p.p.Action, p.p.Enabled}
		b, _ := json.Marshal(key)
		groups[string(b)] = append(groups[string(b)], p.p.ID)
	}
	diff := []domain.Cell{}
	keys := make([]string, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		ids := groups[key]
		if len(ids) > 1 {
			sort.Strings(ids)
			for _, id := range ids {
				diff = append(diff, domain.Cell{PolicyRef: id, Reason: "EXACT_DUPLICATE_CANDIDATE", Action: "candidate"})
			}
		}
	}
	v := "pass"
	summary := "在完整可比较字段中未发现精确重复候选。"
	reasons := []string{}
	if missing {
		v = "insufficient_data"
		summary = "部分条目引用／顺序／状态证据缺失，无法穷尽比较。"
		reasons = append(reasons, "DUPLICATE_FIELDS_INCOMPLETE")
	}
	if unsupported {
		v = "unsupported"
		summary = "部分条目条件／引用／状态无法完整比较，不宣称等价。"
		reasons = append(reasons, "DUPLICATE_FIELDS_UNSUPPORTED")
	}
	if len(diff) > 0 {
		v = "fail"
		summary = "发现精确重复配置候选；需人工核对位置与依赖，不自动删除。"
	}
	if !r.input.Completeness.Declared || !r.input.Completeness.EnumerationComplete || len(r.input.Completeness.KnownGaps) > 0 {
		if len(diff) == 0 {
			v = "insufficient_data"
		}
		reasons = append(reasons, "DATA_INCOMPLETE")
	}
	if r.input.Completeness.ObservationConsistency != "fixed_artifact" && r.input.Completeness.ObservationConsistency != "observed_stable" {
		if len(diff) == 0 {
			v = "insufficient_data"
		}
		reasons = append(reasons, "OBSERVATION_UNSTABLE")
	}
	r.add(check, sub, r.input.Scope.PolicySetRef, "", v, summary, domain.Query{}, diff, reasons)
}

func rangePairs(values []interval) [][2]uint64 {
	out := make([][2]uint64, 0, len(values))
	for _, v := range values {
		out = append(out, [2]uint64{v.lo, v.hi})
	}
	return out
}

// ExplainRule returns bounded, versioned rule metadata for the query-only agent.
func ExplainRule(id string) (string, error) {
	definitions := map[string]string{"CHK-01": "管理服务来源与明确完整业务上界比较；未知白名单保持待确认。", "CHK-02": "整资产已确认完整协议/端口上界；只覆盖TCP/UDP，不声称其他协议安全。", "CHK-03": "逐服务比较明确来源授权；与管理来源问题关联但保留原始判定。", "CHK-04": "完整受支持字段的精确重复候选，不自动删除。", "CHK-05": "当前该CFW层的保护状态；未知不等于关闭，关闭不等于所有防护缺失。", "CHK-06": "必要访问与本层拒绝交集；不证明实际业务故障。", "CHK-07": "风险端口上全地址/默认允许路径的剩余范围；缺业务白名单仍可检查。", "CHK-08": "已确认数据库服务及明确无需公网访问要求；使用实际端口。", "OUT-01": "当前分区出向目标允许集合与已确认完整业务上界比较。", "OUT-02": "明确必要外联与当前分区拒绝范围比较。", "NAT-01": "核对当前网关、资产、观察点与范围关联，不推定SNAT/DNAT。", "NAT-02": "复用出向、重复与当前层状态；gateway_policy不生成资产矩阵。"}
	value, ok := definitions[id]
	if !ok {
		return "", fmt.Errorf("%w: rule not enabled in M0", ErrUnsupported)
	}
	return value, nil
}
