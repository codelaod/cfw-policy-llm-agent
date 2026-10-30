package app

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"cfw-policy-llm-agent/server/internal/sensitive"
	"cfw-policy-llm-agent/server/internal/storage"
)

type ConversationGroupResult struct {
	Handled  bool                          `json:"handled"`
	Reply    string                        `json:"reply"`
	Preview  *storage.ResourceGroupPreview `json:"preview,omitempty"`
	Version  *storage.ResourceGroupVersion `json:"version,omitempty"`
	Missing  []string                      `json:"missing"`
	Replayed bool                          `json:"replayed"`
}

var groupAssignment = regexp.MustCompile(`^(?:(?:请|把|将)\s*)?(?:(?:这些对象|这些资源|所选对象|所选资源|这些机器|这几台(?:机器|主机|ECS)?|这台(?:机器|主机|ECS|资源)?)\s*(?:都)?\s*)?(?:归属|归入|属于)\s*[:：]?\s*(.*)$`)
var groupConfirmation = regexp.MustCompile(`^(?:我)?确认(?:本次|当前|这些对象的)?归属(?:预览)?[。.!！]?$`)

func groupReply(result storage.ResourceGroupResult) ConversationGroupResult {
	out := ConversationGroupResult{Handled: true, Preview: result.Preview, Version: result.Version, Missing: append([]string{}, result.Missing...), Replayed: result.Replayed}
	if result.Preview != nil {
		out.Reply = fmt.Sprintf("已预览%d个明确对象归属“%s”，请核对对象、来源和授权说明后明确确认归属。预览不会改变审计对象、访问授权或云标签。", len(result.Preview.Members), result.Preview.GroupName)
	}
	if result.Version != nil {
		out.Reply = fmt.Sprintf("已保存“%s”的可信业务归属版本%d，共%d个对象。仅后续目标可按此映射检索，旧任务与计划保持原对象；角色和访问要求仍需分别确认。", result.Version.GroupName, result.Version.Revision, len(result.Version.Members))
	}
	return out
}

// HandleGroup routes a local membership attestation through the same service as
// typed forms. It never accepts a plan, changes a role or calls a provider.
func (s *IntentService) HandleGroup(ctx context.Context, conversation, key, text string, typed ...*storage.ResourceGroupRequest) (out ConversationGroupResult, err error) {
	if s == nil || s.Store == nil || len(typed) > 1 {
		return out, storage.ErrInputInvalid
	}
	var original *storage.ResourceGroupRequest
	if len(typed) == 1 && typed[0] != nil {
		copy := *typed[0]
		copy.ClientDigest = ""
		copy.ResourceRefs = append([]string(nil), copy.ResourceRefs...)
		original = &copy
	}
	spoken := strings.TrimSpace(text)
	confirm := groupConfirmation.MatchString(spoken)
	assignment := groupAssignment.FindStringSubmatch(spoken)
	if original == nil && !confirm && len(assignment) == 0 {
		return out, nil
	}
	out = ConversationGroupResult{Handled: true, Missing: []string{}}
	digest := intentDigest(struct {
		Text  string
		Typed *storage.ResourceGroupRequest
	}{text, original})
	if saved, found, e := s.Store.ResourceGroupReplay(ctx, conversation, key, digest); e != nil || found {
		if e != nil {
			return out, e
		}
		return groupReply(saved), nil
	}
	safe, _ := sensitive.Redact(text, s.SensitiveValues)
	typedSensitive := false
	if original != nil {
		for _, value := range []string{original.GroupName, original.AuthorizationSource} {
			projected, _ := sensitive.Redact(value, s.SensitiveValues)
			typedSensitive = typedSensitive || projected != value
		}
	}
	if safe != text || typedSensitive {
		out.Missing = []string{"non_sensitive_group_input"}
		out.Reply = "检测到可能凭据，未建立归属预览。请通过受控配置处理凭据，再提供非敏感业务归属说明。"
		return out, nil
	}
	req := storage.ResourceGroupRequest{}
	if original != nil {
		req = *original
	} else if confirm {
		req.Action = "confirm"
	} else {
		req.Action = "preview"
		head := assignment[1]
		if index := strings.Index(head, "授权来源"); index >= 0 {
			req.AuthorizationSource = strings.Trim(strings.TrimPrefix(head[index+len("授权来源"):], "："), " \t，。；:：=\"“”")
			head = head[:index]
		}
		req.GroupName = strings.Trim(head, " \t，。；:：\"“”")
	}
	if req.Action == "preview" {
		if req.GroupName == "" || strings.Contains(req.GroupName, "哪个") || strings.Contains(req.GroupName, "什么") || strings.Contains(req.GroupName, "不知道") {
			out.Missing = append(out.Missing, "group_name")
		}
		if !storage.ResourceGroupAuthorityDeclared(req.AuthorizationSource) {
			out.Missing = append(out.Missing, "authorization_source")
		}
		if len(out.Missing) > 0 {
			out.Reply = "归属仍需明确组名和授权来源；普通名称或标签不会自动成为可信业务归属。当前目标保持原样。"
			return out, nil
		}
	} else if req.Action == "confirm" {
		if req.PreviewID == "" {
			preview, e := s.Store.LatestResourceGroupPreview(ctx, conversation)
			if errors.Is(e, storage.ErrNotFound) {
				out.Missing = []string{"group_preview"}
				out.Reply = "还没有本会话的归属预览，请先核对当前已选对象、组名与授权来源。"
				return out, nil
			}
			if e != nil {
				return out, e
			}
			req.PreviewID = preview.ID
		}
	} else {
		return out, storage.ErrInputInvalid
	}
	target, e := s.Store.ResourceGroupTarget(ctx, conversation)
	if errors.Is(e, storage.ErrResourceGroupTarget) || errors.Is(e, storage.ErrNotFound) {
		out.Missing = []string{"selected_resources"}
		out.Reply = "当前没有已明确选定的对象。请先在同一会话解析和选择资源；不会凭客户名称或标签猜成员。"
		return out, nil
	}
	if e != nil {
		return out, e
	}
	// Spoken explicit IDs must belong to the server-selected set; no text can add
	// a new member. Natural 'these objects' and typed empty refs use that set.
	if original == nil && req.Action == "preview" {
		targetText := text
		if index := strings.Index(targetText, "授权来源"); index >= 0 {
			targetText = targetText[:index]
		}
		tokens := resourceIdentifier.FindAllString(targetText, -1)
		if len(tokens) > 0 {
			// ResourceGroupTarget exposes opaque source-qualified refs only. A natural
			// provider ID is not an opaque member reference; require an explicit chosen
			// set instead of silently reinterpreting it as all selected resources.
			out.Missing = []string{"use_selected_resource_refs"}
			out.Reply = "请用当前明确选定的对象预览归属，或在表单选择其已发放引用；不会从自由文字扩充或替换成员。"
			return out, nil
		}
	}
	if req.ExpectedRevision == 0 {
		req.ExpectedRevision = target.IntentRevision
	}
	if req.Action == "preview" && len(req.ResourceRefs) == 0 {
		req.ResourceRefs = append([]string{}, target.ResourceRefs...)
	}
	req.ClientDigest = digest
	result, e := s.Store.ApplyResourceGroup(ctx, conversation, key, req)
	if e != nil {
		return out, e
	}
	return groupReply(result), nil
}
