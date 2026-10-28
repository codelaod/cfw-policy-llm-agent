package agentquery

import "encoding/json"

var toolArgument = map[string]string{"get_partition_context": "scope_ref", "get_asset_context": "asset_ref", "get_nat_association": "association_ref", "get_security_group_rules": "asset_ref", "get_business_intent": "business_ref", "get_policy": "policy_ref", "resolve_reference": "reference_ref", "get_finding_evidence": "finding_ref", "get_rule_definition": "rule_id"}
var ruleDefinitions = map[string]string{
	"CHK-01": "管理来源须有已确认业务依据；端口不证明授权。", "CHK-02": "按完整已确认允许上界比较协议和端口。", "CHK-03": "服务来源与明确业务授权比较，不生成可信来源。", "CHK-04": "精确重复配置候选需人工复核，不自动删除。", "CHK-05": "仅核对当前云防火墙层的保护状态，不推断其他防护层。", "CHK-06": "必要访问与本层拒绝配置比较，不证明实际故障。", "CHK-07": "采用高风险端口基线核对公网配置允许范围，不证明监听。", "CHK-08": "已确认数据库角色与公网访问要求分别核对。", "OUT-01": "出向目标与完整明确依赖上界比较。", "OUT-02": "已确认必要外联与本层拒绝比较。", "NAT-01": "资产与当前网关、观察点必须有证据；不足时未知。", "NAT-02": "当前网关分区内的适用出向检查，不扩为转换路径审计。"}
var componentTopics = []string{"network_access", "cfw_boundary", "intrusion_prevention", "logging_audit", "waf", "database_segmentation", "ddos_assessment"}

func (s *Service) Tools() []ToolDefinition {
	names := []string{"get_partition_context", "get_asset_context", "get_nat_association", "get_security_group_rules", "get_business_intent", "get_policy", "resolve_reference", "get_finding_evidence", "get_rule_definition"}
	out := make([]ToolDefinition, 0, len(names))
	for _, name := range names {
		key := toolArgument[name]
		schema, _ := json.Marshal(map[string]any{"type": "object", "additionalProperties": false, "required": []string{key}, "properties": map[string]any{key: map[string]any{"type": "string", "minLength": 1, "maxLength": 128}}})
		out = append(out, ToolDefinition{name, "仅查当前封存资料；引用须已由服务器在先前模型轮次返回，缺口不触发外呼。", schema})
	}
	return out
}
