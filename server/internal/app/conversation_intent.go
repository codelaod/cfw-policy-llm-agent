package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"cfw-policy-llm-agent/server/internal/audit"
	"cfw-policy-llm-agent/server/internal/domain"
	"cfw-policy-llm-agent/server/internal/sensitive"
	"cfw-policy-llm-agent/server/internal/storage"
)

type IntentTurnRequest struct {
	Group *storage.ResourceGroupRequest `json:"group,omitempty"`

	Business *ConversationBusinessRequest `json:"business,omitempty"`

	ContextReportID string `json:"context_report_id,omitempty"`
	FindingID       string `json:"finding_id,omitempty"`

	ClientTurnID     string                   `json:"client_turn_id"`
	Text             string                   `json:"text"`
	ExpectedRevision *int64                   `json:"expected_revision,omitempty"`
	Mode             string                   `json:"mode,omitempty"`
	DataPolicy       *domain.IntentDataPolicy `json:"data_policy,omitempty"`
}
type IntentChoiceRequest struct {
	IntentID         string   `json:"intent_id"`
	ExpectedRevision int64    `json:"expected_revision"`
	CandidateIDs     []string `json:"candidate_ids"`
}
type IntentAcceptRequest struct {
	ExpectedIntentRevision int64 `json:"expected_intent_revision"`
}
type IntentService struct {
	SensitiveValues []string
	Store           *storage.Store
	Plans           *PlanService
	Engine          *Engine
}

var resourceIdentifier = regexp.MustCompile(`\b(?:i-|ngw-|nat-|asset-)[A-Za-z0-9_-]+\b`)
var identifierToken = regexp.MustCompile(`[A-Za-z][A-Za-z0-9_.-]{1,127}`)
var explicitResourceName = regexp.MustCompile(`(?:名为|名称为?|叫作?|名字是?|资源名为?)\s*["“]?([^\s，。；、"”]{1,128})`)
var explicitQuotedResourceName = regexp.MustCompile(`(?:名为|名称为?|叫作?|名字是?|资源名为?)\s*["“]([^"”\r\n]{1,128})["”]`)
var quotedResourceName = regexp.MustCompile(`["“]([^"”\r\n]{1,128})["”]`)
var resourceTagSelector = regexp.MustCompile(`(?i)(?:标签|tag)\s*[:：]?\s*([A-Za-z0-9_.-]{1,128})\s*(?:=|为|:|：)\s*([^\s，。；]{0,256})`)
var regionToken = regexp.MustCompile(`\b(?:cn|ap|eu|us|me|sa|af)-[a-z]+(?:-[a-z0-9]+)*\b`)
var statusWords = regexp.MustCompile(`进度|状态|到哪|多久|耗时`)
var cancelWords = regexp.MustCompile(`取消(?:当前)?(?:任务|审计)|停止(?:任务|审计)|^取消$`)
var writeWords = regexp.MustCompile(`(?:直接|自动|帮我).*(?:删除.*策略|下发|云端修改|修复)|扩大权限|切换.*账号|其他账号`)
var unsupportedWords = regexp.MustCompile(`端到端|全账号合规|证明.*可达`)
var rulesWords = regexp.MustCompile(`只(?:做|运行|查|要).*规则|不用模型|不使用模型|仅规则`)
var modelWords = regexp.MustCompile(`使用模型|模型补充|启用模型`)
var refreshWords = regexp.MustCompile(`已.*(?:改好|修改|整改)|重新采集|强制刷新|刷新.*复审`)
var acceptPlanWords = regexp.MustCompile(`^(?:确认按当前计划开始审计|确认(?:执行|审计|本次|当前)?计划|确认(?:开始|执行)(?:审计)?|接受(?:本次|当前)?计划(?:并开始)?|开始吧|开始审计|就按(?:这个|当前)计划执行)[。！!\s]*$`)

func localActionText(text string) string {
	// An explicitly named resource is data, even when its name contains status,
	// cancellation or write words. Preserve byte positions for later extraction.
	masked := []byte(text)
	for _, pattern := range []*regexp.Regexp{explicitQuotedResourceName, explicitResourceName, resourceTagSelector} {
		for _, span := range pattern.FindAllStringIndex(string(masked), -1) {
			for i := span[0]; i < span[1]; i++ {
				masked[i] = ' '
			}
		}
	}
	return string(masked)
}

func intentDigest(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func initialIntent() domain.ConversationIntent {
	return domain.ConversationIntent{Status: "intent_draft", Action: "audit", Boundary: "internet", Direction: "in", Mode: "rules_only", DataPolicy: domain.IntentDataPolicy{Mode: "prefer_fresh"}, Selection: []domain.Resource{}, Candidates: []domain.Resource{}, Catalogue: domain.CatalogueCoverage{Limitations: []string{}}}
}
func (s *IntentService) Current(ctx context.Context, conversation string) (domain.ConversationIntent, error) {
	intent, err := s.Store.CurrentIntent(ctx, conversation)
	if errors.Is(err, storage.ErrNotFound) {
		if _, e := s.Store.GetConversation(ctx, conversation); e != nil {
			return intent, e
		}
		intent = initialIntent()
		intent.ConversationID = conversation
		return intent, nil
	}
	if err != nil {
		return intent, err
	}
	if intent.Proposal != nil && intent.Proposal.PlanID != "" {
		p, e := s.Store.GetPlan(ctx, intent.Proposal.PlanID)
		if e == nil && p.ConsumedJobID == "" {
			at, parseErr := time.Parse(time.RFC3339Nano, p.ExpiresAt)
			if parseErr != nil {
				return intent, storage.ErrInputInvalid
			}
			if !time.Now().Before(at) {
				intent.Status = "stale"
				intent.Proposal.Status = "expired"
			}
			if s.Plans != nil && p.ConfigRevision != s.Plans.ConfigRevision {
				intent.Status = "stale"
				intent.Proposal.Status = "stale"
			}
		}
		if e == nil && p.ConsumedJobID == "" && p.ResourceGroupVersionID != "" {
			v, e := s.Store.ResourceGroupVersion(ctx, p.ResourceGroupVersionID)
			if e != nil {
				return intent, e
			}
			current, e := s.Store.ResourceGroup(ctx, v.SourceRef, v.AccountRef, v.GroupName)
			if e != nil {
				return intent, e
			}
			if current.ID != v.ID {
				intent.Status = "stale"
				intent.Proposal.Status = "stale"
			}
		}
		if e == nil && p.ConsumedJobID != "" {
			intent.Status = "accepted"
			intent.Proposal.Status = "accepted"
			if p.Preparation != nil && len(intent.Selection) == 0 {
				binding, e := s.Store.GetPreparedAuditBinding(ctx, p.ConsumedJobID)
				if e == nil {
					resources := []domain.Resource{}
					if binding.Input.GatewayMetadata != nil {
						resources = append(resources, *binding.Input.GatewayMetadata)
					}
					for _, a := range binding.Input.Assets {
						resources = append(resources, domain.Resource{Provider: binding.Input.Scope.Provider, AccountRef: binding.Input.Scope.AccountRef, ResourceID: a.ProviderResourceID, Region: a.Region, ResourceType: a.ResourceType, LocalAssetID: a.ID, Name: a.Name, Tags: a.Tags, OperatingSystem: a.OperatingSystem})
					}
					for i := range resources {
						r := &resources[i]
						r.SourceRef = p.DataSourceID
						r.Source = binding.Input.SourceKind
						r.ObservedAt = binding.Input.CollectedAt
						page, e := s.Store.SearchResources(ctx, storage.ResourceFilter{SourceRef: p.DataSourceID, AccountRef: r.AccountRef, ResourceID: r.ResourceID, Region: r.Region, ResourceType: r.ResourceType}, "", 2)
						if e != nil {
							return intent, e
						}
						if len(page.Items) != 1 {
							return intent, storage.ErrInputInvalid
						}
						r.ID, r.IdentityRef, r.IdentityKind = page.Items[0].ID, page.Items[0].IdentityRef, page.Items[0].IdentityKind
					}
					intent.Selection = resources
				} else if !errors.Is(e, storage.ErrNotFound) {
					return intent, e
				}
			}
		}
	}
	return intent, nil
}
func (s *IntentService) Turn(ctx context.Context, conversation, key string, req IntentTurnRequest) (storage.IntentTurnResult, error) {
	if key == "" || len(key) > 128 || req.ClientTurnID == "" || len(req.ClientTurnID) > 128 || strings.TrimSpace(req.Text) == "" && req.Business == nil && req.Group == nil || len(req.Text) > 16000 || !utf8.ValidString(req.Text) {
		return storage.IntentTurnResult{}, storage.ErrInputInvalid
	}
	if strings.TrimSpace(req.Text) == "" && req.Business != nil {
		captions := map[string]string{"read": "查看业务草稿", "edit": "修改业务草稿", "preview": "预览业务草稿", "confirm": "确认预览中的选定业务项目", "reject_draft": "拒绝这份业务草稿", "accept_candidate": "接受所选角色候选", "reject_candidate": "拒绝所选角色候选", "keep_unknown": "保留选定业务资料为未知"}
		req.Text = captions[req.Business.Action]
		if req.Text == "" {
			return storage.IntentTurnResult{}, storage.ErrInputInvalid
		}
	}
	if req.Business != nil && req.Group != nil {
		return storage.IntentTurnResult{}, storage.ErrInputInvalid
	}
	if strings.TrimSpace(req.Text) == "" && req.Group != nil {
		if req.Group.Action == "preview" {
			req.Text = "预览本次资源归属"
		} else if req.Group.Action == "confirm" {
			req.Text = "确认归属"
		} else {
			return storage.IntentTurnResult{}, storage.ErrInputInvalid
		}
	}
	originalText := req.Text
	text, _ := sensitive.Redact(req.Text, s.SensitiveValues)
	actionText := strings.TrimSpace(localActionText(text))
	req.Text = text
	digest := intentDigest(struct {
		Conversation string
		Request      IntentTurnRequest
	}{conversation, req})
	if old, found, e := s.Store.IntentReplay(ctx, conversation, key, digest); e != nil || found {
		return old, e
	}
	c, err := s.Store.GetConversation(ctx, conversation)
	if err != nil {
		return storage.IntentTurnResult{}, err
	}
	old, err := s.Current(ctx, conversation)
	if err != nil {
		return storage.IntentTurnResult{}, err
	}

	in := old
	in.Proposal = nil
	in.Clarification = ""
	var plan *storage.ExecutionPlan
	var businessResult *ConversationBusinessResult
	var groupResult *storage.ResourceGroupResult
	var acceptance *storage.IntentTurnAcceptance
	businessPrefix := ""
	restrictionPrefix := ""
	reply := ""
	localOnly := false
	save := func() (storage.IntentTurnResult, error) {
		if restrictionPrefix != "" && !strings.HasPrefix(reply, restrictionPrefix) {
			reply = restrictionPrefix + "\n" + reply
		}
		if businessPrefix != "" && !strings.HasPrefix(reply, businessPrefix) {
			reply = businessPrefix + "\n" + reply
		}
		turn := storage.SaveIntentTurn{Acceptance: acceptance, Group: groupResult, Business: businessResult, LocalOnly: localOnly, RequestKey: key, RequestDigest: digest, ConversationID: conversation, ExpectedConversationRevision: c.Revision, ExpectedIntentRevision: old.Revision, Intent: in, Text: text, Reply: reply, Plan: plan}
		if acceptance != nil && s.Engine != nil {
			return s.Engine.AcceptIntentTurn(ctx, turn)
		}
		return s.Store.SaveIntentRevision(ctx, turn)
	}
	businessArgsForReplay := []ConversationBusinessRequest{}
	if req.Business != nil {
		businessArgsForReplay = append(businessArgsForReplay, *req.Business)
	}
	businessDigest := intentDigest(struct {
		Conversation, Text string
		Typed              []ConversationBusinessRequest
	}{conversation, actionText, businessArgsForReplay})
	if stored, found, e := s.Store.ConversationBusinessReplay(ctx, conversation, key, businessDigest); e != nil {
		return storage.IntentTurnResult{}, e
	} else if found {
		stored.Reply = businessReply(stored)
		businessResult = &stored
		localOnly = true
		reply = stored.Reply + " 当前目标不回退；这份回执对应原请求中的对象与业务项目。"
		return save()
	}
	groupDigest := intentDigest(struct {
		Text  string
		Typed *storage.ResourceGroupRequest
	}{actionText, req.Group})
	if stored, found, e := s.Store.ResourceGroupReplay(ctx, conversation, key, groupDigest); e != nil {
		return storage.IntentTurnResult{}, e
	} else if found {
		groupResult = &stored
		localOnly = true
		reply = groupReply(stored).Reply + " 当前目标保持不变，此回执仍绑定原归属预览。"
		return save()
	}
	if req.ExpectedRevision != nil && *req.ExpectedRevision != old.Revision {
		return storage.IntentTurnResult{}, storage.ErrVersionConflict
	}
	if originalText != text && !strings.ContainsAny(text, "审查核检") {
		text = ""
		localOnly = true
		reply = "检测到可能的凭据，原值没有进入消息或模型。请使用服务端受控配置；不能保证识别所有秘密。当前审计目标仍保留。"
		return save()
	}
	if req.Mode != "" {
		if req.Mode != "rules_only" && req.Mode != "rules_and_agent" {
			return storage.IntentTurnResult{}, storage.ErrInputInvalid
		}
		in.Mode = req.Mode
	}
	if req.DataPolicy != nil {
		p := *req.DataPolicy
		if p.Mode != "prefer_fresh" && p.Mode != "force_refresh" && p.Mode != "pinned_snapshot" {
			return storage.IntentTurnResult{}, storage.ErrInputInvalid
		}
		if p.Mode == "pinned_snapshot" && (p.SnapshotID == "" || p.FreshAfter != "") || p.Mode != "pinned_snapshot" && p.SnapshotID != "" || p.Mode == "prefer_fresh" && p.FreshAfter != "" {
			return storage.IntentTurnResult{}, storage.ErrInputInvalid
		}
		if p.FreshAfter != "" {
			at, e := time.Parse(time.RFC3339Nano, p.FreshAfter)
			if e != nil || at.After(time.Now()) {
				return storage.IntentTurnResult{}, storage.ErrInputInvalid
			}
			p.FreshAfter = at.UTC().Format(time.RFC3339Nano)
		}
		in.DataPolicy = p
	}
	if req.Business == nil && req.Group == nil && acceptPlanWords.MatchString(strings.TrimSpace(text)) {
		localOnly = true
		switch {
		case old.Proposal == nil || old.Proposal.PlanID == "":
			reply = "目前没有可接受的执行计划。请先明确对象并核对服务端预览中的范围、资料和效果。"
		case old.Status == "accepted" || old.Proposal.Status == "accepted":
			reply = "当前计划已经接受，请查看原任务进度；不会重复启动同一计划。"
		case old.Status != "ready_for_confirmation" || old.Proposal.Status != "ready":
			reply = "当前计划已经过期或需要重新核对，请按原目标重新预览后再开始。"
		case req.ExpectedRevision == nil:
			reply = "开始前需要绑定你核对过的计划版本。请使用当前计划的接受入口，或从当前会话发送确认。"
		case in.Mode != old.Mode || in.DataPolicy != old.DataPolicy || req.ContextReportID != "" && req.ContextReportID != old.ContextReportID || req.FindingID != "":
			reply = "本次选项与已预览计划不一致，请先按明确对象重新核对模式、资料与效果，再接受新计划。"
		default:
			acceptance = &storage.IntentTurnAcceptance{ProposalID: old.Proposal.ID, ExpectedRevision: old.Revision, ConfigRevision: s.Plans.ConfigRevision}
			reply = "已接受当前已核对的计划，按原对象、资料策略与总额度推进。可以查询进度或明确取消。"
		}
		return save()
	}
	if statusWords.MatchString(actionText) {
		jobs, e := s.Store.ListJobs(ctx, conversation, 0, 1)
		if e != nil {
			return storage.IntentTurnResult{}, e
		}
		reply = "本会话尚无执行任务，可以继续说明审计目标。"
		if len(jobs) > 0 {
			reply = fmt.Sprintf("当前任务%s，状态%s。查询进度不会重建任务或产生模型／云请求。", jobs[0].ID, jobs[0].Status)
		}
		localOnly = true
		return save()
	}
	if cancelWords.MatchString(actionText) {
		jobs, e := s.Store.ListJobs(ctx, conversation, 0, 100)
		if e != nil {
			return storage.IntentTurnResult{}, e
		}
		reply = "当前没有未结束任务需要取消。"
		for _, j := range jobs {
			if !storage.Terminal(j.Status) {
				if s.Engine == nil {
					return storage.IntentTurnResult{}, ErrStopping
				}
				if _, e = s.Engine.Cancel(ctx, j.ID); e != nil {
					return storage.IntentTurnResult{}, e
				}
				reply = "已请求取消该任务；保留已得证据，已送达上游的请求不保证撤回。"
				break
			}
		}
		localOnly = true
		return save()
	}
	groupOutcome, groupErr := s.HandleGroup(ctx, conversation, key, actionText, req.Group)
	if groupErr != nil {
		return storage.IntentTurnResult{}, groupErr
	}
	if groupOutcome.Handled {
		state := "needs_clarification"
		if groupOutcome.Preview != nil {
			state = "preview_required"
		} else if groupOutcome.Version != nil {
			state = "confirmed"
		}
		groupResult = &storage.ResourceGroupResult{State: state, Preview: groupOutcome.Preview, Version: groupOutcome.Version, Missing: groupOutcome.Missing, Replayed: groupOutcome.Replayed}
		localOnly = true
		reply = groupOutcome.Reply
		return save()
	}
	reportRef := req.ContextReportID
	if reportRef == "" {
		reportRef = regexp.MustCompile(`\breport_[a-f0-9]{32}\b`).FindString(actionText)
	}
	asksHistory := regexp.MustCompile("解释|依据|影响|优先级|未检查|缺什么|需要.*业务信息").MatchString(actionText)
	asksRoleInference := regexp.MustCompile("判断.*用途|推断.*角色|分析.*可能.*服务").MatchString(actionText)
	asksCurrent := regexp.MustCompile("现在|当前状态|最新|当前配置").MatchString(actionText)
	businessIntent := req.Business != nil || businessSignal.MatchString(actionText)
	if asksHistory && req.DataPolicy != nil && req.DataPolicy.Mode == "force_refresh" && !refreshWords.MatchString(actionText) {
		in.Status = "needs_clarification"
		reply = "固定报告解释沿原时点且零云，强刷新用于新的当前审计。请明确本次要做哪一项。"
		in.Clarification = reply
		return save()
	}
	if asksCurrent && reportRef != "" && asksHistory {
		in.Status = "needs_clarification"
		reply = "本次同时提到固定报告和当前状态，请明确解释原报告时点，还是按当前资料策略重新检查。"
		in.Clarification = reply
		return save()
	}
	if asksCurrent && req.DataPolicy == nil && in.DataPolicy.Mode == "pinned_snapshot" {
		in.DataPolicy.Mode = "prefer_fresh"
		in.DataPolicy.SnapshotID = ""
	}
	if ((asksHistory && !businessIntent && !asksCurrent) || asksRoleInference && in.Mode == "rules_and_agent" && s.Plans.ModelReady) && !refreshWords.MatchString(actionText) {
		mode := in.Mode
		if rulesWords.MatchString(actionText) {
			in.Mode = "rules_only"
		} else if modelWords.MatchString(actionText) {
			in.Mode = "rules_and_agent"
		}
		if req.Mode != "" && req.Mode != in.Mode && in.Mode != mode {
			in.Status = "needs_clarification"
			reply = "话语与选项的模型模式不一致，请核对后继续。"
			in.Clarification = reply
			return save()
		}
		reply, plan, err = s.historyProposal(ctx, c, &in, text, reportRef, req.FindingID)
		if err != nil {
			return storage.IntentTurnResult{}, err
		}
		localOnly = plan == nil
		return save()
	}
	if writeWords.MatchString(actionText) {
		restrictionPrefix = "当前只支持已选账号范围内只读审计，不能下发、删除、切换账号或扩大权限。"
		if !regexp.MustCompile("审计|检查|核查|基线").MatchString(text) {
			localOnly = true
			reply = restrictionPrefix
			return save()
		}
	}
	if unsupportedWords.MatchString(actionText) {
		restrictionPrefix += "本系统只提供所选边界的配置层审计，不能证明端到端可达或整个账号合规。"
		if !regexp.MustCompile("审计|检查|核查|基线").MatchString(text) {
			localOnly = true
			reply = restrictionPrefix
			return save()
		}
	}
	businessArgs := []ConversationBusinessRequest{}
	if req.Business != nil {
		businessArgs = append(businessArgs, *req.Business)
	}
	handled, business, businessErr := s.HandleBusiness(ctx, conversation, key, actionText, businessArgs...)
	if businessErr != nil {
		return storage.IntentTurnResult{}, businessErr
	}
	if handled {
		businessResult = &business
		if !regexp.MustCompile("审计|检查|核查|先查|基线|风险").MatchString(text) || business.State == "conflict" {
			localOnly = true
			reply = business.Reply
			return save()
		}
		businessPrefix = business.Reply
	}
	scopeText := text
	scopeActions := localActionText(scopeText)
	for _, word := range []string{"改查", "改为", "换成", "这次查", "现在查"} {
		if at := strings.LastIndex(scopeActions, word); at >= 0 {
			scopeText = scopeText[at+len(word):]
			scopeActions = scopeActions[at+len(word):]
		}
	}
	in.Action = "audit"
	in.ContextReportID = ""
	in.RecheckOfJobID = ""
	explicitTarget := false
	newQuery := in.Query
	locatorText := scopeText
	nameSpan := explicitQuotedResourceName.FindStringSubmatchIndex(scopeText)
	if len(nameSpan) < 4 {
		nameSpan = explicitResourceName.FindStringSubmatchIndex(scopeText)
	}
	if len(nameSpan) < 4 {
		nameSpan = quotedResourceName.FindStringSubmatchIndex(scopeText)
	}
	if len(nameSpan) >= 4 {
		newQuery.Name = scopeText[nameSpan[2]:nameSpan[3]]
		newQuery.ResourceID = ""
		newQuery.ResourceIDs = nil
		newQuery.Group = ""
		explicitTarget = true
		locatorText = scopeText[:nameSpan[0]] + strings.Repeat(" ", nameSpan[1]-nameSpan[0]) + scopeText[nameSpan[1]:]
	}
	// Tag keys and values are selector data. Exclude them from independent name,
	// ID, region, type and action extraction, retaining this turn's exact tags.
	tagMatches := resourceTagSelector.FindAllStringSubmatch(locatorText, -1)
	maskedLocator := []byte(locatorText)
	for _, span := range resourceTagSelector.FindAllStringIndex(locatorText, -1) {
		for i := span[0]; i < span[1]; i++ {
			maskedLocator[i] = ' '
		}
	}
	locatorText = string(maskedLocator)
	explicitRegion := false
	regions := []string{}
	addRegion := func(v string) {
		for _, existing := range regions {
			if existing == v {
				return
			}
		}
		regions = append(regions, v)
	}
	for _, region := range regionToken.FindAllString(locatorText, -1) {
		addRegion(region)
	}
	for _, pair := range [][2]string{{"杭州", "cn-hangzhou"}, {"北京", "cn-beijing"}, {"上海", "cn-shanghai"}} {
		if strings.Contains(locatorText, pair[0]) {
			addRegion(pair[1])
		}
	}
	if len(regions) > 0 {
		explicitRegion = true
		newQuery.Region = ""
		newQuery.Regions = nil
		if len(regions) == 1 {
			newQuery.Region = regions[0]
		} else {
			newQuery.Regions = regions
		}
	}
	if strings.Contains(strings.ToLower(locatorText), "ecs") || strings.Contains(locatorText, "服务器") || strings.Contains(locatorText, "机器") {
		newQuery.ResourceType = "ecs"
	}
	if strings.Contains(strings.ToLower(locatorText), "nat") || strings.Contains(locatorText, "网关") {
		newQuery.ResourceType = "nat_gateway"
		in.Boundary = "nat"
		in.Direction = "out"
	}
	if strings.Contains(locatorText, "出向") || strings.Contains(locatorText, "外连") || strings.Contains(locatorText, "出站") {
		in.Direction = "out"
		if newQuery.ResourceType != "nat_gateway" {
			in.Boundary = "internet"
		}
	}
	if strings.Contains(locatorText, "入向") || strings.Contains(locatorText, "入站") {
		in.Boundary = "internet"
		in.Direction = "in"
	}
	if regexp.MustCompile("入向|入站").MatchString(locatorText) && regexp.MustCompile("出向|出站|外连").MatchString(locatorText) {
		in.Status = "needs_clarification"
		in.Query = newQuery
		reply = "本次包含入向和出向两个策略分区。已保留原话，请先明确这轮要执行的一个分区；另一分区需独立计划，不会只取其中一个冒充全部。"
		in.Clarification = reply
		in.Selection = []domain.Resource{}
		return save()
	}
	idsInText := []string{}
	for _, id := range resourceIdentifier.FindAllString(locatorText, -1) {
		found := false
		for _, previous := range idsInText {
			if previous == id {
				found = true
			}
		}
		if !found {
			idsInText = append(idsInText, id)
		}
	}
	if len(idsInText) > 0 {
		newQuery.ResourceID = ""
		newQuery.ResourceIDs = idsInText
		newQuery.Name = ""
		newQuery.Group = ""
		explicitTarget = true
		if len(idsInText) == 1 {
			newQuery.ResourceID = idsInText[0]
			newQuery.ResourceIDs = nil
		}
		if !explicitRegion {
			newQuery.Region = ""
			newQuery.Regions = nil
		}
		if strings.HasPrefix(idsInText[0], "ngw-") || strings.HasPrefix(idsInText[0], "nat-") {
			newQuery.ResourceType = "nat_gateway"
			in.Boundary = "nat"
			in.Direction = "out"
		}
	}
	if !explicitTarget {
		if m := explicitResourceName.FindStringSubmatch(scopeText); len(m) > 1 {
			newQuery.Name = m[1]
			newQuery.ResourceID = ""
			newQuery.ResourceIDs = nil
			newQuery.Group = ""
			explicitTarget = true
		} else if m := quotedResourceName.FindStringSubmatch(scopeText); len(m) > 1 {
			newQuery.Name = m[1]
			newQuery.ResourceID = ""
			newQuery.ResourceIDs = nil
			explicitTarget = true
		}
	}
	if !explicitTarget {
		for _, token := range identifierToken.FindAllString(locatorText, -1) {
			if strings.EqualFold(token, "ecs") || strings.EqualFold(token, "nat") || regionToken.MatchString(token) {
				continue
			}
			p, e := s.Store.SearchResources(ctx, storage.ResourceFilter{Name: token}, "", 1)
			if e != nil {
				return storage.IntentTurnResult{}, e
			}
			if len(p.Items) > 0 {
				newQuery.Name = token
				newQuery.ResourceID = ""
				newQuery.ResourceIDs = nil
				newQuery.Group = ""
				explicitTarget = true
				break
			}
		}
	}
	if !explicitTarget && (strings.ContainsAny(scopeText, "审查检核") || old.Query.Name == "") {
		for _, token := range identifierToken.FindAllString(locatorText, -1) {
			if strings.EqualFold(token, "ecs") || strings.EqualFold(token, "nat") || regionToken.MatchString(token) {
				continue
			}
			if strings.ContainsAny(token, "-_.") {
				newQuery.Name = token
				newQuery.ResourceID = ""
				newQuery.ResourceIDs = nil
				newQuery.Group = ""
				explicitTarget = true
				break
			}
		}
	}
	if !explicitTarget && (strings.Contains(locatorText, "所有") || strings.Contains(locatorText, "全部")) && !regexp.MustCompile("这些|选定|已选|这组|组内").MatchString(locatorText) {
		// An explicit whole-region/type request replaces an earlier single target.
		newQuery.Name, newQuery.ResourceID = "", ""
		newQuery.ResourceIDs, newQuery.Tags = nil, nil
	}
	multipleSpoken := strings.Contains(locatorText, "所有") || strings.Contains(locatorText, "全部") || strings.Contains(locatorText, "这些") || strings.Contains(locatorText, "这几台") || len(idsInText) > 1
	singularSpoken := strings.Contains(locatorText, "这台") || strings.Contains(locatorText, "这一台") || strings.Contains(locatorText, "单台") || strings.Contains(locatorText, "一个网关")
	if explicitTarget || singularSpoken {
		in.AllowMultiple = multipleSpoken
	}
	if multipleSpoken || explicitRegion && newQuery.ResourceType == "ecs" && newQuery.Name == "" && newQuery.ResourceID == "" && len(newQuery.ResourceIDs) == 0 {
		in.AllowMultiple = true
	}

	if explicitTarget {
		// A newly stated identity/name replaces the previous target filters. Only
		// tags explicitly supplied in this same turn may constrain that identity.
		newQuery.Tags = nil
	}
	if len(tagMatches) > 0 {
		tags := map[string]string{}
		for _, m := range tagMatches {
			tags[m[1]] = m[2]
		}
		newQuery.Tags = tags
		if !explicitTarget {
			newQuery.Name = ""
			newQuery.ResourceID = ""
			newQuery.ResourceIDs = nil
			newQuery.Group = ""
		}
		explicitTarget = true
	}
	asksBatch := strings.Contains(locatorText, "分批")
	if explicitTarget {
		in.BatchRequested = false
	}
	if asksBatch {
		in.BatchRequested = true
	}
	spokenMode := ""
	if rulesWords.MatchString(locatorText) {
		spokenMode = "rules_only"
	} else if modelWords.MatchString(locatorText) {
		spokenMode = "rules_and_agent"
	}
	if spokenMode != "" {
		if req.Mode != "" && req.Mode != spokenMode {
			in.Status = "needs_clarification"
			in.Query = newQuery
			reply = "本次选项与话语中的模型模式不一致，请核对只做规则还是使用模型补充，再继续。"
			in.Clarification = reply
			return save()
		}
		in.Mode = spokenMode
	}
	spokenPolicy := ""
	if refreshWords.MatchString(locatorText) {
		spokenPolicy = "force_refresh"
	} else if strings.Contains(locatorText, "复用") {
		spokenPolicy = "prefer_fresh"
	}
	if req.DataPolicy != nil && spokenPolicy != "" && req.DataPolicy.Mode != spokenPolicy {
		in.Status = "needs_clarification"
		in.Query = newQuery
		reply = "本次选项与话语中的资料策略不一致，请核对固定历史、合格复用或重新采集。不会静默改变云读取效果。"
		in.Clarification = reply
		return save()
	}
	if spokenPolicy != "" {
		in.DataPolicy.Mode = spokenPolicy
		if spokenPolicy != "pinned_snapshot" {
			in.DataPolicy.SnapshotID = ""
		}
	}
	if in.DataPolicy.Mode == "force_refresh" {
		in.Action = "recheck"
		if in.DataPolicy.FreshAfter == "" || refreshWords.MatchString(locatorText) {
			in.DataPolicy.FreshAfter = time.Now().UTC().Format(time.RFC3339Nano)
		}
	}
	if in.Action == "recheck" {
		var handled bool
		if reportRef == "" && old.Action == "recheck" && old.Status == "needs_clarification" && old.ContextReportID != "" && !explicitTarget && !explicitRegion && in.Boundary == old.Boundary && in.Direction == old.Direction {
			reportRef = old.ContextReportID
		}
		reply, plan, handled, err = s.reportRecheck(ctx, c, &in, text, reportRef, newQuery, explicitTarget, explicitRegion)
		if err != nil {
			return storage.IntentTurnResult{}, err
		}
		if handled {
			return save()
		}
	}
	in.Query = newQuery
	if regexp.MustCompile("客户|租户|业务组|业务群|资源组").MatchString(locatorText) {
		matches := regexp.MustCompile(`((?:客户|租户|业务组|业务群|资源组)\s*[^的下中，。；\s]{1,64})`).FindStringSubmatch(locatorText)
		names := []string{}
		if len(matches) > 1 {
			name := strings.Trim(matches[1], " \t“”\"")
			names = append(names, name)
			for _, prefix := range []string{"客户", "租户", "业务组", "业务群", "资源组"} {
				if strings.HasPrefix(name, prefix) {
					short := strings.Trim(strings.TrimPrefix(name, prefix), " \t“”\"")
					if short != "" && short != name {
						names = append(names, short)
					}
					break
				}
			}
		}
		source, owner := "", ""
		if cloud := s.Plans.Cloud; cloud != nil && cloud.Configured && in.DataPolicy.Mode != "pinned_snapshot" {
			source, owner = cloud.SourceRef, cloud.Grant.AccountRef
		}
		groups := []storage.ResourceGroupVersion{}
		for _, name := range names {
			group, e := s.Store.ResourceGroup(ctx, source, owner, name)
			if e == nil {
				groups = append(groups, group)
			} else if !errors.Is(e, storage.ErrNotFound) && !errors.Is(e, storage.ErrResourceGroupAmbiguous) {
				return storage.IntentTurnResult{}, e
			}
		}
		if len(groups) != 1 {
			in.Status = "needs_clarification"
			in.Selection = []domain.Resource{}
			reply = "客户／业务组须有来源且已经确认的成员映射；当前名称尚未唯一匹配。请核对组名／来源，或先按名称与ID选对象并预览确认归属。普通标签不作归属授权。"
			in.Clarification = reply
			return save()
		}
		if !explicitTarget || len(idsInText) == 0 && len(explicitResourceName.FindStringSubmatch(scopeText)) == 0 && hasString(names, newQuery.Name) {
			newQuery.Name = ""
			newQuery.ResourceID = ""
			newQuery.ResourceIDs = nil
			if len(tagMatches) == 0 {
				newQuery.Tags = nil
			}
		}
		newQuery.Group = groups[0].GroupName
		in.ResourceGroupVersionID = groups[0].ID
		in.AllowMultiple = !singularSpoken
		explicitTarget = true
		in.Query = newQuery
	} else if explicitTarget {
		in.ResourceGroupVersionID = ""
	}
	meaningful := regexp.MustCompile("审计|检查|核查|复审|入向|出向|规则|模型|刷新|复用|这台|这些|基线|风险|白名单|来源|网关|ECS|分批|预览|继续").MatchString(scopeText)
	if !explicitTarget && !explicitRegion && !meaningful {
		localOnly = true
		reply = "我负责云防火墙配置审计。可以说明资源名称／ID和边界，或查询当前任务进度。原目标与计划保持不变。"
		return save()
	}
	if newQuery.Group != "" && in.ResourceGroupVersionID != "" {
		previous, e := s.Store.ResourceGroupVersion(ctx, in.ResourceGroupVersionID)
		if e != nil {
			return storage.IntentTurnResult{}, e
		}
		active, e := s.Store.ResourceGroup(ctx, previous.SourceRef, previous.AccountRef, previous.GroupName)
		if e != nil {
			return storage.IntentTurnResult{}, e
		}
		if active.ID != previous.ID {
			in.ResourceGroupVersionID = active.ID
			explicitTarget = true
			in.Selection = []domain.Resource{}
			in.BatchRequested = asksBatch
		}
	}
	compatible := len(old.Selection) > 0 && (!singularSpoken || len(old.Selection) == 1)
	for _, r := range old.Selection {
		if explicitRegion && !queryRegionMatches(newQuery, r.Region) || newQuery.ResourceType != "" && r.ResourceType != "" && r.ResourceType != newQuery.ResourceType {
			compatible = false
		}
	}
	if !explicitTarget && compatible && !strings.Contains(scopeText, "所有") {
		in.Selection = old.Selection
		in.Candidates = []domain.Resource{}
		reply, plan, err = s.resolvePlan(ctx, c, &in)
		if err != nil {
			return storage.IntentTurnResult{}, err
		}
		return save()
	}
	if !explicitTarget && newQuery.Name == "" && newQuery.ResourceID == "" && len(newQuery.ResourceIDs) == 0 && len(newQuery.Tags) == 0 && newQuery.Group == "" && !(in.AllowMultiple && newQuery.ResourceType == "ecs" && len(queryRegions(newQuery)) > 0) {
		in.Status = "needs_clarification"
		reply = "请说明资源名称或ID，也可补充地域与资源类型。当前没有足够信息确定审计对象，不会默认选择第一台。"
		in.Clarification = reply
		return save()
	}
	filter := storage.ResourceFilter{Name: newQuery.Name, ResourceID: newQuery.ResourceID, Region: newQuery.Region, Regions: newQuery.Regions, ResourceType: newQuery.ResourceType, Group: newQuery.Group, Tags: newQuery.Tags}
	if cloud := s.Plans.Cloud; cloud != nil && cloud.Configured && in.DataPolicy.Mode != "pinned_snapshot" {
		filter.SourceRef = cloud.SourceRef
		filter.AccountRef = cloud.Grant.AccountRef
	}

	if newQuery.Group != "" && in.ResourceGroupVersionID != "" {
		version, e := s.Store.ResourceGroupVersion(ctx, in.ResourceGroupVersionID)
		if e != nil {
			return storage.IntentTurnResult{}, e
		}
		filter.SourceRef, filter.AccountRef = version.SourceRef, version.AccountRef
	}
	if len(newQuery.ResourceIDs) > 1 {
		matches := []domain.Resource{}
		ambiguous := false
		missing := false
		for _, id := range newQuery.ResourceIDs {
			f := filter
			f.ResourceID = id
			p, e := s.Store.SearchResources(ctx, f, "", 100)
			if e != nil {
				return storage.IntentTurnResult{}, e
			}
			if len(p.Items) != 1 || p.HasMore {
				ambiguous = true
			}
			if len(p.Items) == 0 {
				missing = true
			}
			matches = append(matches, p.Items...)
		}
		in.Selection = []domain.Resource{}
		in.Candidates = matches
		if ambiguous {
			in.Status = "needs_clarification"
			reply = "本次列出的多个ID尚未全部唯一定位，请核对缺失或同名地域候选；不会只审已找到的部分。"
			if missing {
				if s.Plans.Cloud != nil {
					in.Selection = []domain.Resource{}
					reply, plan, err = s.prepareProposal(ctx, c, &in)
					if err != nil {
						return storage.IntentTurnResult{}, err
					}
					return save()
				}
				in.Status = "needs_discovery"
			}
			in.Clarification = reply
			return save()
		}
		in.Selection = matches
		in.Candidates = []domain.Resource{}
		reply, plan, err = s.resolvePlan(ctx, c, &in)
		if err != nil {
			return storage.IntentTurnResult{}, err
		}
		return save()
	}
	var page storage.ResourcePage
	if newQuery.Group != "" && in.ResourceGroupVersionID != "" && in.AllowMultiple {
		page, err = s.allConfirmedGroupResources(ctx, filter, in.ResourceGroupVersionID)
	} else if in.AllowMultiple && newQuery.Name == "" && newQuery.ResourceID == "" {
		page, err = s.allFiniteResources(ctx, filter)
	} else {
		page, err = s.Store.SearchResources(ctx, filter, "", 100)
	}
	if err != nil {
		if errors.Is(err, storage.ErrCursor) || errors.Is(err, storage.ErrPlanStale) {
			in.Status = "needs_clarification"
			in.Selection = []domain.Resource{}
			reply = "完整读取期间目录或归属发生更新，请重新核对完整集合后再预览。不会使用旧页面与新成员拼成审计范围。"
			in.Clarification = reply
			return save()
		}
		return storage.IntentTurnResult{}, err
	}
	in.Catalogue = page.Coverage
	in.Selection = []domain.Resource{}
	in.Candidates = page.Items
	if len(page.Items) == 0 {
		if s.Plans.Cloud != nil {
			reply, plan, err = s.prepareProposal(ctx, c, &in)
			if err != nil {
				return storage.IntentTurnResult{}, err
			}
			return save()
		}
		in.Status = "needs_discovery"
		reply = "本地目录尚未取得匹配对象，不能据此证明资源不存在。请核对地域／名称／ID，或准备当前连接范围内的有限只读发现。"
		in.Clarification = reply
		return save()
	}
	if in.AllowMultiple && (newQuery.Group != "" || newQuery.Name == "" && newQuery.ResourceID == "") && page.Coverage.Complete && !page.HasMore && len(page.Items) > 0 {
		in.Selection = page.Items
		in.Candidates = []domain.Resource{}
		reply, plan, err = s.resolvePlan(ctx, c, &in)
		if err != nil {
			return storage.IntentTurnResult{}, err
		}
		return save()
	}
	if in.AllowMultiple && newQuery.Name == "" && newQuery.ResourceID == "" && !page.Coverage.Complete && !page.HasMore {
		if s.Plans.Cloud != nil {
			reply, plan, err = s.prepareProposal(ctx, c, &in)
			if err != nil {
				return storage.IntentTurnResult{}, err
			}
			return save()
		}
		in.Status = "needs_clarification"
		reply = fmt.Sprintf("已见%d个候选，但目录未证明完整集合。请明确选择这些身份，或补充完整有限目录后再预览全部对象；不会把当前已见对象当作全部。", len(page.Items))
		in.Clarification = reply
		return save()
	}
	if len(page.Items) > 1 || page.HasMore {
		in.Status = "needs_clarification"
		suffix := "仅代表已见目录"
		if page.HasMore {
			suffix = "未穷尽，可继续收窄"
		}
		reply = fmt.Sprintf("找到%d个候选，请核对名称、ID、地域、类型和来源后选择；当前结果%s。", len(page.Items), suffix)
		in.Clarification = reply
		return save()
	}
	if !page.Coverage.Complete && page.Items[0].Source == "live_readonly" && (newQuery.Name != "" || len(newQuery.Tags) > 0) && in.DataPolicy.Mode != "pinned_snapshot" {
		if s.Plans.Cloud != nil {
			reply, plan, err = s.prepareProposal(ctx, c, &in)
			if err != nil {
				return storage.IntentTurnResult{}, err
			}
			return save()
		}
		in.Status = "needs_clarification"
		reply = "当前目录只返回一个已见候选，但未穷尽，不能证明名称唯一。请明确选择该身份，或准备有限发现。"
		in.Clarification = reply
		return save()
	}
	in.Selection = page.Items
	in.Candidates = []domain.Resource{}
	reply, plan, err = s.resolvePlan(ctx, c, &in)
	if err != nil {
		return storage.IntentTurnResult{}, err
	}
	return save()
}

func (s *IntentService) resolvePlan(ctx context.Context, c storage.Conversation, in *domain.ConversationIntent) (string, *storage.ExecutionPlan, error) {
	if len(in.Selection) == 0 {
		return s.prepareProposal(ctx, c, in)
	}
	resolved, policyErr := s.Plans.ResolveWorkload("audit")
	if policyErr != nil {
		return "", nil, policyErr
	}
	if len(in.Selection) > resolved.Limits.AssetsPerBatch {
		if in.BatchRequested {
			return s.prepareBatchProposal(ctx, c, in, resolved)
		}
		in.Status = "needs_batch"
		in.Proposal = &domain.ActionProposal{Summary: fmt.Sprintf("本次完整选择%d个对象，超过当前单批%d个的保护值。", len(in.Selection), resolved.Limits.AssetsPerBatch), Gaps: []string{"BATCH_CONFIRMATION_REQUIRED"}, Warnings: []string{"可以说明按完整集合分批，或提供明确ID收窄；不会截取前几个。"}, Status: "blocked"}
		return "已保留完整集合。可以发送“按完整对象集分批审计”生成一张串行计划，或收窄对象；不会默认只审前几个。", nil, nil
	}
	var limitsErr error
	ctx, limitsErr = audit.WithInputObjectLimits(ctx, resolved.Limits.AssetsPerBatch, resolved.Limits.MaxPolicies, resolved.Limits.MaxQueryCells)
	if limitsErr != nil {
		return "", nil, limitsErr
	}

	refs := []string{}
	account := ""
	for _, r := range in.Selection {
		if account != "" && account != r.AccountRef {
			in.Status = "needs_clarification"
			return "候选来自不同账号，首版一次只分析一个显式账号，请收窄。", nil, nil
		}
		account = r.AccountRef
		refs = append(refs, r.ID)
	}
	bindings, err := s.Store.ResourceBindings(ctx, refs, in.Boundary, in.Direction)
	if err != nil {
		return "", nil, err
	}
	bySnapshot := map[string]map[string]string{}
	order := []string{}
	scope := map[string]string{}
	for _, b := range bindings {
		if bySnapshot[b.SnapshotID] == nil {
			bySnapshot[b.SnapshotID] = map[string]string{}
			order = append(order, b.SnapshotID)
			scope[b.SnapshotID] = b.ScopeRef
		}
		bySnapshot[b.SnapshotID][b.ResourceRef] = b.AssetID
	}
	chosen := ""
	for _, id := range order {
		if in.DataPolicy.Mode == "pinned_snapshot" && in.DataPolicy.SnapshotID != "" && id != in.DataPolicy.SnapshotID {
			continue
		}
		if len(bySnapshot[id]) == len(refs) {
			chosen = id
			break
		}
	}
	if chosen == "" {
		if s.Plans.Cloud != nil {
			return s.prepareProposal(ctx, c, in)
		}
		in.Status = "needs_data"
		in.Proposal = &domain.ActionProposal{Summary: "对象已明确，需准备所选分区的完整资料。", Gaps: []string{"SNAPSHOT_NOT_AVAILABLE"}, Warnings: []string{"模型工具不会在后台扩大范围或自由读取云接口。"}, RequiresDiscovery: true, Effects: domain.ProposalEffects{}, Status: "blocked"}
		return "对象已明确，但尚无覆盖全部对象及所选方向的固定资料。保留目标，下一步准备有限只读采集。", nil, nil
	}
	snap, err := s.Store.GetSnapshot(ctx, chosen)
	if err != nil {
		return "", nil, err
	}
	if in.DataPolicy.Mode == "force_refresh" {
		if s.Plans.Cloud != nil {
			return s.prepareProposal(ctx, c, in)
		}
		in.Status = "needs_data"
		return "已记录必须重新采集的要求。旧资料不能证明人工修改后的当前状态；请准备本次范围内的新资料。", nil, nil
	}
	// A live input cannot silently satisfy current-state intent without explicit
	// qualification; DataPreparation will attach eligible reuse/refresh here.
	if snap.SourceKind == "live_readonly" && in.DataPolicy.Mode != "pinned_snapshot" {
		if s.Plans.Cloud != nil {
			return s.prepareProposal(ctx, c, in)
		}
		in.Status = "needs_data"
		return "需要核对当前云资料资格或刷新后再审计；旧快照只可作为明确历史分析。", nil, nil
	}
	ids := []string{}
	for _, ref := range refs {
		if id := bySnapshot[chosen][ref]; id != "" {
			ids = append(ids, id)
		}
	}
	selection := domain.Selection{Kind: "assets", AssetIDs: ids}
	if in.Boundary == "nat" && len(ids) == 0 {
		selection.Kind = "gateway_policy"
	}
	prepared, heads, err := s.Store.FreezeSelection(ctx, chosen, selection)
	if err != nil {
		return "", nil, err
	}
	if err = audit.ValidateContext(ctx, prepared); err != nil {
		return "", nil, err
	}
	effects := storage.EffectCeiling{}
	gaps := []string{}
	if in.Mode == "rules_and_agent" {
		if s.Plans.ModelReady {
			n, e := s.Plans.modelAllowance(6)
			if e != nil {
				return "", nil, e
			}
			if n > 0 {
				effects.AllowModelRequests = true
				effects.ModelHTTPAttempts = n
			} else {
				gaps = append(gaps, "MODEL_ATTEMPTS_NOT_ALLOWED")
			}
		} else {
			gaps = append(gaps, "MODEL_NOT_CONFIGURED")
		}
	}
	request := PlanRequest{Kind: "audit", ConversationID: c.ID, DataSourceID: "local-offline", PolicyScopeRef: scope[chosen], Selection: PlanSelection{Kind: selection.Kind, AssetIDs: ids}, DataPolicy: DataPolicy{Mode: "pinned_snapshot", SnapshotID: chosen}, Mode: in.Mode, BaselineVersionID: audit.ProjectBaseline().Version}
	if selection.Kind == "gateway_policy" {
		request.Selection.GatewayRef = prepared.Scope.GatewayRef
	}
	encoded, _ := json.Marshal(request)
	plan := &storage.ExecutionPlan{ResourceGroupVersionID: in.ResourceGroupVersionID, Workload: resolved, Kind: "audit", RecheckOfJobID: in.RecheckOfJobID, ConversationID: c.ID, DataSourceID: "local-offline", PolicyScopeRef: scope[chosen], SnapshotID: chosen, Mode: in.Mode, Request: encoded, EffectCeiling: effects, PreparedInput: prepared, Heads: heads, ConfigRevision: s.Plans.ConfigRevision, BudgetRef: s.Plans.BudgetRef}
	in.Status = "ready_for_confirmation"
	warnings := []string{"仅分析固定资料时点的配置；允许不等于实际可达。"}
	if snap.SourceKind != "live_readonly" {
		warnings = append(warnings, "人工／导入资料不等于真实云观察，结果保留来源条件。")
	}
	in.Proposal = &domain.ActionProposal{Summary: fmt.Sprintf("对%d个明确对象执行%s/%s审计；资料时点%s。", len(refs), in.Boundary, in.Direction, prepared.CollectedAt), Gaps: gaps, Warnings: warnings, Effects: domain.ProposalEffects{AllowModelRequests: effects.AllowModelRequests}, Status: "ready"}
	return "已明确对象和所选分区，请核对资料时点、模型模式及缺项后开始。接受前不会调用云或模型。", plan, nil
}
func (s *IntentService) Choose(ctx context.Context, conversation, key string, req IntentChoiceRequest) (storage.IntentTurnResult, error) {
	digest := intentDigest(struct {
		Conversation string
		Request      IntentChoiceRequest
	}{conversation, req})
	if old, found, e := s.Store.IntentReplay(ctx, conversation, key, digest); e != nil || found {
		return old, e
	}
	c, err := s.Store.GetConversation(ctx, conversation)
	if err != nil {
		return storage.IntentTurnResult{}, err
	}
	in, err := s.Current(ctx, conversation)
	if err != nil {
		return storage.IntentTurnResult{}, err
	}
	if in.ID != req.IntentID || in.Revision != req.ExpectedRevision {
		return storage.IntentTurnResult{}, storage.ErrVersionConflict
	}
	if len(req.CandidateIDs) == 0 || len(req.CandidateIDs) > 100 {
		return storage.IntentTurnResult{}, storage.ErrInputInvalid
	}
	selected := []domain.Resource{}
	seen := map[string]bool{}
	for _, id := range req.CandidateIDs {
		if seen[id] {
			return storage.IntentTurnResult{}, storage.ErrInputInvalid
		}
		seen[id] = true
		found := false
		for _, r := range in.Candidates {
			if r.ID == id {
				selected = append(selected, r)
				found = true
				break
			}
		}
		if !found {
			return storage.IntentTurnResult{}, ErrScopeDenied
		}
	}
	in.Selection = selected
	in.AllowMultiple = len(selected) > 1
	in.Candidates = []domain.Resource{}
	in.Proposal = nil
	reply, p, err := s.resolvePlan(ctx, c, &in)
	if err != nil {
		return storage.IntentTurnResult{}, err
	}
	return s.Store.SaveIntentRevision(ctx, storage.SaveIntentTurn{RequestKey: key, RequestDigest: digest, ConversationID: conversation, ExpectedConversationRevision: c.Revision, ExpectedIntentRevision: req.ExpectedRevision, Intent: in, Text: "选择所列候选资源", Reply: reply, Plan: p})
}

func queryRegionMatches(q domain.ResourceQuery, region string) bool {
	if len(q.Regions) > 0 {
		for _, r := range q.Regions {
			if r == region {
				return true
			}
		}
		return false
	}
	return q.Region == region
}
