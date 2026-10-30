package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"regexp"
	"strings"
	"unicode/utf8"

	"cfw-policy-llm-agent/server/internal/domain"
	"cfw-policy-llm-agent/server/internal/sensitive"
	"cfw-policy-llm-agent/server/internal/storage"
)

type ConversationBusinessRequest = storage.ConversationBusinessRequest
type ConversationBusinessResult = storage.ConversationBusinessResult

var businessSignal = regexp.MustCompile(`(?i)(?:用途|角色|必须保留|必要访问|允许集合|不允许任何|管理(?:入口|来源|白名单).*(?:未知|不知道)|(?:来源|白名单).*(?:未知|不知道)|业务草稿|业务资料|风险例外|(?:记|添加|新增|登记).*例外|例外.*(?:理由|授权|到期)|拒绝.*候选|接受.*候选|(?:确认|它是|这是|改为|改成).*(?:web(?:服务)?|数据库|管理服务|管理接口|缓存服务|dns服务)|^(?:允许|该服务允许|这个服务允许).*(?:[0-9]|https|http|tcp|udp))`)
var businessConfirmation = regexp.MustCompile(`^确认(?:当前|本次|刚才)?预览(?:中的?|的)?(角色|访问|角色和访问|角色与访问)(?:部分|项目)?[。！!]?\s*$`)
var businessAddress = regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}(?:/\d{1,2})?\b`)
var businessPort = regexp.MustCompile(`(?i)(?:tcp|udp|端口)\s*[:：/]?\s*([0-9]{1,5})\b`)

// HandleBusiness is local only. Natural language produces a bounded draft and
// preview; saving a business version requires an explicit current preview and
// sections. Forms and this router read/update the same ProfileDraft record.
func (s *IntentService) HandleBusiness(ctx context.Context, conversation, key, text string, typed ...ConversationBusinessRequest) (handled bool, result ConversationBusinessResult, err error) {
	if s == nil || s.Store == nil || len(typed) > 1 || len(text) > 16000 || !utf8.ValidString(text) || key == "" || len(key) > 128 {
		return false, result, storage.ErrInputInvalid
	}
	text, _ = sensitive.Redact(strings.TrimSpace(text), s.SensitiveValues)
	if len(typed) == 0 && !businessSignal.MatchString(businessSemanticText(text)) && !businessConfirmation.MatchString(text) {
		return false, result, nil
	}
	handled = true
	request := ConversationBusinessRequest{}
	if len(typed) == 1 {
		request = typed[0]
		encoded, e := json.Marshal(request)
		if e != nil {
			return true, result, storage.ErrInputInvalid
		}
		if _, changed := sensitive.Redact(string(encoded), s.SensitiveValues); changed {
			return true, ConversationBusinessResult{State: "sensitive_input_blocked", Reply: "业务资料不接收凭据。请移除敏感内容，再继续原来的非敏感目标。", Candidates: []storage.BusinessRoleCandidate{}, UnknownFields: []string{}, MissingInputs: []string{}}, nil
		}
	}
	clientDigest := intentDigest(struct {
		Conversation, Text string
		Typed              []ConversationBusinessRequest
	}{conversation, text, typed})
	if replay, found, e := s.Store.ConversationBusinessReplay(ctx, conversation, key, clientDigest); e != nil || found {
		replay.Reply = businessReply(replay)
		return true, replay, e
	}
	current, e := s.Store.ConversationBusinessContext(ctx, conversation)
	if errors.Is(e, storage.ErrBusinessTargetUnresolved) {
		return true, ConversationBusinessResult{State: "needs_clarification", Reply: "请先明确本会话的对象与方向，并取得已封存的资产资料。现成事实会自动带入，角色和访问要求再分别确认。", Candidates: []storage.BusinessRoleCandidate{}, UnknownFields: []string{}, MissingInputs: []string{"sealed_asset_target"}}, nil
	}
	if e != nil {
		return true, result, e
	}
	if len(typed) == 0 {
		var missing []string
		request, missing = evaluateBusinessText(current, text)
		if request.Action == "" {
			return true, businessClarification(current, missing), nil
		}
		request.MissingInputs = missing
	}
	if request.ExpectedTargetRef == "" {
		request.ExpectedTargetRef = current.Target.Ref
	}
	request.ClientDigest = clientDigest
	result, err = s.Store.ApplyConversationBusiness(ctx, conversation, key, request)
	if errors.Is(err, storage.ErrVersionConflict) {
		result = businessClarification(current, []string{"current_draft_revision_and_preview"})
		result.State = "conflict"
		result.Reply = "原草稿与已确认版本保留。对象、草稿或基版本已经变化，请读取最新草稿并重新核对差异；旧预览不能继续确认。"
		return true, result, nil
	}
	if err != nil {
		result.State = "save_failed"
		result.Reply = "本次保存尚未确认成功，原草稿与提交标识应保留。请查询原保存结果后沿同一标识重试。"
		return true, result, err
	}
	result.Reply = businessReply(result)
	return true, result, nil
}

func businessClarification(current storage.ConversationBusinessContext, missing []string) ConversationBusinessResult {
	return ConversationBusinessResult{State: "needs_clarification", Reply: "请明确当前资产／服务及要修改的业务项目；可查看同一份草稿后分项预览和确认。没有明确声明的来源与允许范围保持未知。", Target: current.Target, Draft: current.Draft, Candidates: current.Candidates, UnknownFields: []string{}, MissingInputs: append([]string{}, missing...)}
}

func businessReply(result ConversationBusinessResult) string {
	switch result.State {
	case "preview_required":
		if result.Preview != nil && len(result.Preview.ValidationErrors) > 0 {
			return "业务草稿已保存，部分项目仍缺少有效确认条件；可先核对可确认部分。新例外须补齐授权来源、理由、对象、检查与到期。角色不代表访问授权。"
		}
		if len(result.UnknownFields) > 0 || len(result.MissingInputs) > 0 {
			return "已保存明确的业务草稿并生成差异预览；角色与必要访问分别列出，未声明的来源仍未知。请按预览明确选择角色或访问部分确认，独立基线可继续。"
		}
		return "业务草稿已保存并生成差异预览。请核对对象、服务及变化，再明确选择角色或访问部分确认；角色不代表允许来源，本地确认不会修改云策略。"
	case "confirmed":
		return "已保存本次明确选择的业务确认。未选部分继续保留在草稿；历史任务和报告仍使用原版本，后续审计才使用新的确认版本。"
	case "candidate_rejected":
		return "已记录拒绝该角色候选；原始证据、其他草稿与旧确认保留。可以人工修改用途，或保留未知继续独立基线。"
	case "unknown_preserved":
		return "已保留业务未知项，未修改旧确认，也未补造来源或白名单；不依赖这些资料的独立基线仍可继续。"
	case "rejected":
		return "已记录拒绝这份草稿；原始事实和既有确认版本保留。"
	default:
		if result.Draft == nil {
			return "当前目标和已有事实已读取，尚无业务草稿。可明确服务用途、必要访问或保留未知，再预览确认。"
		}
		return "已读取当前共用业务草稿与候选。对话和表单使用相同修订；修改后需重新预览，再明确分项确认。"
	}
}

func evaluateBusinessText(current storage.ConversationBusinessContext, text string) (request ConversationBusinessRequest, missing []string) {
	request.UnknownFields = []string{}
	missing = []string{}
	if match := businessConfirmation.FindStringSubmatch(text); len(match) > 0 {
		if current.Draft == nil || current.Draft.Status != "previewed" || current.Draft.PreviewID == "" {
			return request, []string{"current_preview"}
		}
		request.Action, request.PreviewID, request.ExpectedDraftRevision = "confirm", current.Draft.PreviewID, current.Draft.Revision
		if strings.Contains(match[1], "角色") {
			request.Sections = append(request.Sections, "roles")
		}
		if strings.Contains(match[1], "访问") {
			request.Sections = append(request.Sections, "access")
		}
		return request, missing
	}
	if strings.Contains(text, "业务草稿") && (strings.Contains(text, "查看") || strings.Contains(text, "读取")) {
		request.Action = "read"
		return request, missing
	}
	if strings.Contains(text, "预览") || strings.Contains(text, "保存这次业务资料") {
		if current.Draft == nil {
			return request, []string{"business_draft_changes"}
		}
		request.Action = "preview"
		return request, missing
	}
	if strings.Contains(text, "候选") && (strings.Contains(text, "接受") || strings.Contains(text, "拒绝")) {
		if len(current.Candidates) != 1 {
			return request, []string{"role_candidate_selection"}
		}
		request.CandidateRef = current.Candidates[0].Ref
		request.Action = "accept_candidate"
		if strings.Contains(text, "拒绝") {
			request.Action = "reject_candidate"
		}
		return request, missing
	}
	if strings.Contains(text, "管理") && (strings.Contains(text, "未知") || strings.Contains(text, "不知道")) {
		request.UnknownFields = append(request.UnknownFields, "management_sources")
	}
	if strings.Contains(text, "白名单") && (strings.Contains(text, "未知") || strings.Contains(text, "不知道")) {
		request.UnknownFields = append(request.UnknownFields, "allowed_sources")
	}
	if (strings.Contains(text, "角色") || strings.Contains(text, "用途")) && (strings.Contains(text, "未知") || strings.Contains(text, "不知道")) {
		request.UnknownFields = append(request.UnknownFields, "role")
		if strings.Contains(text, "候选") && len(current.Candidates) == 1 {
			request.CandidateRef = current.Candidates[0].Ref
		}
	}
	if len(request.UnknownFields) > 0 && !strings.Contains(text, "确认") && !strings.Contains(text, "必须保留") && !strings.Contains(text, "必要访问") && !strings.Contains(text, "不允许任何") && !strings.Contains(text, "允许集合") && !businessAddress.MatchString(text) {
		request.Action = "keep_unknown"
		return request, missing
	}
	assetID := businessTextAsset(current, text)
	if assetID == "" {
		return request, []string{"asset_selection"}
	}
	services := businessCurrentServices(current, assetID)
	addresses := []string{}
	for _, raw := range businessAddress.FindAllString(text, -1) {
		if prefix, e := netip.ParsePrefix(raw); e == nil && prefix.Addr().Is4() {
			addresses = append(addresses, prefix.Masked().String())
		} else if address, e := netip.ParseAddr(raw); e == nil && address.Is4() {
			addresses = append(addresses, address.String())
		} else {
			return request, []string{"valid_explicit_business_addresses"}
		}
	}
	protocol, ports := businessProtocolPorts(text)
	roles := []string{}
	for _, clause := range strings.FieldsFunc(businessSemanticText(text), func(r rune) bool { return strings.ContainsRune("，,；;。", r) }) {
		if strings.Contains(clause, "来源") || strings.Contains(clause, "白名单") || strings.Contains(clause, "未知") || strings.Contains(clause, "不知道") {
			continue
		}
		declared := strings.Contains(clause, "确认") || strings.Contains(clause, "它是") || strings.Contains(clause, "这是") || strings.Contains(clause, "角色") || strings.Contains(clause, "用途") || strings.Contains(clause, "改为") || strings.Contains(clause, "改成") || len(roles) > 0 && (strings.Contains(clause, "服务") || strings.Contains(clause, "接口"))
		if !declared {
			continue
		}
		lower := strings.ToLower(clause)
		for _, role := range []struct {
			Words []string
			Value string
		}{{[]string{"web", "网站"}, "web"}, {[]string{"数据库"}, "database"}, {[]string{"管理服务", "管理接口"}, "management"}, {[]string{"缓存"}, "cache"}, {[]string{"dns服务"}, "dns"}} {
			for _, word := range role.Words {
				if strings.Contains(lower, word) && !hasString(roles, role.Value) {
					roles = append(roles, role.Value)
					break
				}
			}
		}
	}
	if (strings.Contains(text, "改成") || strings.Contains(text, "改为")) && len(roles) > 1 {
		return request, []string{"service_to_modify"}
	}
	for _, role := range roles {
		rp, rports := "unknown", []domain.PortRange{}
		if len(roles) == 1 {
			rp, rports = protocol, ports
		}
		serviceID, ambiguous := businessTextService(services, text, role, rp, rports)
		if ambiguous {
			return request, []string{"service_selection"}
		}
		if serviceID != "" {
			for _, service := range services {
				if service.ID == serviceID && rp == "unknown" {
					rp, rports = service.Protocol, service.Ports
				}
			}
		}
		request.Patches = append(request.Patches, storage.ConversationBusinessPatch{AssetID: assetID, ServiceID: serviceID, Role: &storage.RoleChange{Role: role, Protocol: rp, Ports: rports}})
	}
	if strings.Contains(text, "必须保留") || strings.Contains(text, "必要访问") {
		if len(ports) == 0 || protocol == "unknown" {
			missing = append(missing, "required_protocol_and_ports")
		} else {
			index := -1
			for i, patch := range request.Patches {
				if patch.Role != nil && patch.Role.Protocol == protocol && businessSamePorts(patch.Role.Ports, ports) {
					index = i
					break
				}
			}
			if index < 0 {
				serviceID, ambiguous := businessTextService(services, text, "", protocol, ports)
				if ambiguous {
					return request, []string{"service_selection"}
				}
				patch := storage.ConversationBusinessPatch{AssetID: assetID, ServiceID: serviceID}
				if serviceID == "" {
					patch.Role = &storage.RoleChange{Role: "unknown", Protocol: protocol, Ports: ports}
				}
				request.Patches = append(request.Patches, patch)
				index = len(request.Patches) - 1
			}
			applicability := "applicable"
			if len(addresses) == 0 {
				applicability = "unknown"
				field := "required_sources"
				if current.Target.Scope.Direction == "out" {
					field = "required_targets"
				}
				request.UnknownFields = append(request.UnknownFields, field)
				missing = append(missing, field)
			}
			required := []domain.Access{{ScopeRef: current.Target.Scope.Ref, Protocol: protocol, Addresses: addresses, Ports: ports, Applicability: applicability}}
			request.Patches[index].Required = &required
		}
	}
	if strings.Contains(text, "不允许任何") || strings.Contains(text, "允许集合就是空") {
		role := ""
		if strings.Contains(text, "管理") {
			role = "management"
		}
		serviceID, ambiguous := businessTextService(services, text, role, protocol, ports)
		if ambiguous || serviceID == "" {
			return request, []string{"service_for_explicit_empty_permissions"}
		}
		request.Patches = append(request.Patches, storage.ConversationBusinessPatch{AssetID: assetID, ServiceID: serviceID, Allowed: &storage.BusinessAllowedChange{Complete: true, DenyAll: true, Allowed: []domain.Access{}, PublicAccess: "not_required"}})
	} else if strings.Contains(text, "允许") && len(addresses) > 0 {
		serviceID, ambiguous := businessTextService(services, text, "", protocol, ports)
		if ambiguous || serviceID == "" || len(ports) == 0 {
			return request, []string{"service_and_ports_for_permissions"}
		}
		request.Patches = append(request.Patches, storage.ConversationBusinessPatch{AssetID: assetID, ServiceID: serviceID, Allowed: &storage.BusinessAllowedChange{Complete: strings.Contains(text, "完整"), Allowed: []domain.Access{{ScopeRef: current.Target.Scope.Ref, Protocol: protocol, Addresses: addresses, Ports: ports, Applicability: "applicable"}}, PublicAccess: "unknown"}})
	}
	if strings.Contains(text, "例外") {
		missing = append(missing, "exception_check_and_scope", "exception_reason", "exception_authorization_source", "exception_expiry")
	}
	if len(request.Patches) > 0 {
		request.Action = "edit"
	} else if len(request.UnknownFields) > 0 {
		request.Action = "keep_unknown"
	}
	return request, missing
}

// Resource locators are data, even when their names contain role words such as
// web-prod. Keep the original text for identity matching and explicit address
// parsing, but exclude locator names/IDs from business semantic recognition.
func businessSemanticText(text string) string {
	text = explicitResourceName.ReplaceAllString(text, " ")
	text = quotedResourceName.ReplaceAllString(text, " ")
	text = resourceIdentifier.ReplaceAllString(text, " ")
	return identifierToken.ReplaceAllStringFunc(text, func(token string) string {
		if strings.ContainsAny(token, "-_.") {
			return " "
		}
		return token
	})
}

func businessTextAsset(current storage.ConversationBusinessContext, text string) string {
	matched := []string{}
	for _, asset := range current.Input.Assets {
		if strings.Contains(text, asset.ID) || asset.ProviderResourceID != "" && strings.Contains(text, asset.ProviderResourceID) || asset.Name != "" && strings.Contains(text, asset.Name) {
			matched = append(matched, asset.ID)
		}
	}
	if len(matched) == 1 {
		return matched[0]
	}
	if len(matched) == 0 && len(current.Target.AssetIDs) == 1 {
		return current.Target.AssetIDs[0]
	}
	return ""
}
func businessCurrentServices(current storage.ConversationBusinessContext, assetID string) []domain.Service {
	if current.Draft != nil && (current.Draft.Status == "draft" || current.Draft.Status == "previewed") {
		for _, change := range current.Draft.Changes {
			if change.AssetID == assetID {
				out := []domain.Service{}
				for _, role := range change.Roles {
					out = append(out, domain.Service{ID: role.ServiceID, Role: role.Role, Protocol: role.Protocol, Ports: role.Ports})
				}
				return out
			}
		}
	}
	for _, asset := range current.Input.Assets {
		if asset.ID == assetID {
			return asset.Services
		}
	}
	return nil
}
func businessTextService(services []domain.Service, text, role, protocol string, ports []domain.PortRange) (string, bool) {
	ids := []string{}
	for _, service := range services {
		if strings.Contains(text, service.ID) {
			ids = append(ids, service.ID)
		}
	}
	if len(ids) == 1 {
		return ids[0], false
	}
	if len(ids) > 1 {
		return "", true
	}
	// Explicit endpoint information identifies a service before its proposed
	// role. Otherwise confirming an unknown HTTPS service can create a duplicate,
	// or change an existing HTTP service merely because both are called web.
	if protocol != "" && protocol != "unknown" && len(ports) > 0 {
		matches := []domain.Service{}
		for _, service := range services {
			if service.Protocol == protocol && businessSamePorts(service.Ports, ports) {
				matches = append(matches, service)
			}
		}
		if len(matches) == 1 {
			service := matches[0]
			if role != "" && service.Role != "" && service.Role != "unknown" && service.Role != role {
				return "", true
			}
			return service.ID, false
		}
		return "", len(matches) > 1
	}
	for _, service := range services {
		if role != "" && service.Role == role {
			ids = append(ids, service.ID)
		}
	}
	if len(ids) == 1 {
		return ids[0], false
	}
	return "", len(ids) > 1
}
func businessProtocolPorts(text string) (string, []domain.PortRange) {
	lower := strings.ToLower(text)
	if strings.Contains(lower, "https") {
		return "tcp", []domain.PortRange{{From: 443, To: 443}}
	}
	if strings.Contains(lower, "http") {
		return "tcp", []domain.PortRange{{From: 80, To: 80}}
	}
	if match := businessPort.FindStringSubmatch(text); len(match) > 0 {
		n := 0
		for _, r := range match[1] {
			n = n*10 + int(r-'0')
		}
		if n >= 1 && n <= 65535 {
			protocol := "tcp"
			if strings.Contains(lower, "udp") {
				protocol = "udp"
			}
			return protocol, []domain.PortRange{{From: n, To: n}}
		}
	}
	return "unknown", []domain.PortRange{}
}
func businessSamePorts(a, b []domain.PortRange) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
