package report

import (
	"bytes"
	"context"
	"fmt"
	"strings"
)

func agentMarkdown(ctx context.Context, out *bytes.Buffer, a *AgentLayer) error {
	fmt.Fprintf(out, "## Agent 补充（独立于原规则）\n\n%s\n\n模型阶段：%s；逻辑模型调用=%d；工具调用=%d；来源=%s/%s；范围=%s；规则版本=%s。\n\n", a.Qualification, a.ModelStage, a.ModelCalls, a.ToolCalls, a.SourceKind, a.AssessmentBasis, a.ScopeAlias, a.RuleVersionAlias)
	if a.Error != nil {
		fmt.Fprintf(out, "- 阶段缺口 %s：%s\n", a.Error.Code, a.Error.Text)
	}
	if a.Usage.PhysicalAttempts == nil {
		fmt.Fprintln(out, "- 物理模型HTTP尝试与usage：未知，不记为零。")
	} else {
		fmt.Fprintf(out, "- 物理模型HTTP尝试预记=%d；已报告usage响应=%d；usage未知尝试=%d；usage状态=%s。\n", *a.Usage.PhysicalAttempts, *a.Usage.KnownUsageResponses, *a.Usage.UnknownUsageAttempts, a.Usage.Status)
	}
	if a.Usage.ReportedTokens == nil {
		if a.Usage.UsageTotalsOverflow {
			fmt.Fprintln(out, "- 已报告用量合计超出安全整数边界，聚合总量未知，不记为零。")
		} else {
			fmt.Fprintln(out, "- token用量未知。")
		}
	} else {
		u := a.Usage.ReportedTokens
		fmt.Fprintf(out, "- 供应商已报告计数：prompt=%d；completion=%d；total=%d。\n", u.PromptTokens, u.CompletionTokens, u.TotalTokens)
	}
	fmt.Fprintf(out, "%s\n\n", a.Usage.Qualification)
	for _, role := range a.Roles {
		if e := ctx.Err(); e != nil {
			return e
		}
		fmt.Fprintf(out, "### %s · 角色候选\n\n对象=%s；服务=%s；候选角色=%s；状态=%s。\n\n支持证据：%s。反向证据：%s。\n\n", role.Alias, role.Target.AssetAlias, role.Target.ServiceAlias, role.Role, role.Status, strings.Join(role.SupportingEvidence, ", "), strings.Join(role.ContradictingEvidence, ", "))
		for _, n := range role.Conditions {
			fmt.Fprintf(out, "- 条件 %s：%s\n", n.Code, n.Text)
		}
		fmt.Fprintf(out, "影响：%s\n\n人工下一步：%s\n\n", role.Impact, role.ManualNextStep)
	}
	for _, rec := range a.Recommendations {
		if e := ctx.Err(); e != nil {
			return e
		}
		fmt.Fprintf(out, "### %s · %s\n\n%s\n\n目标=%s/%s；对象=%s；服务=%s；依据=%s；状态=%s。\n\n原发现=%s；业务=%s；访问集合=%s；action=%s；field=%s；组件主题=%s。\n\n证据：%s。\n\n", rec.Alias, rec.Kind, rec.Explanation, rec.Target.Kind, rec.Target.Alias, rec.Target.AssetAlias, rec.Target.ServiceAlias, rec.BasisKind, rec.Status, rec.FindingAlias, rec.BusinessAlias, rec.AccessAlias, rec.Action, rec.Field, rec.ComponentTopic, strings.Join(rec.EvidenceAliases, ", "))
		for _, n := range rec.Conditions {
			fmt.Fprintf(out, "- 条件 %s：%s\n", n.Code, n.Text)
		}
		fmt.Fprintf(out, "影响：%s\n\n人工下一步：%s\n\n", rec.Impact, rec.ManualNextStep)
		if out.Len() > MaxBytes {
			return ErrTooLarge
		}
	}
	fmt.Fprintf(out, "待确认问句数=%d；问题正文保留在受保护任务记录。\n\n", a.PendingQuestions)
	for _, n := range a.Limitations {
		fmt.Fprintf(out, "- %s：%s\n", n.Code, n.Text)
	}
	fmt.Fprint(out, "\n### Agent证据索引\n\n")
	for _, e := range a.Evidence {
		fmt.Fprintf(out, "- %s：目标=%s/%s；对象=%s；服务=%s。%s\n", e.Alias, e.Target.Kind, e.Target.Alias, e.Target.AssetAlias, e.Target.ServiceAlias, e.Explanation)
		if out.Len() > MaxBytes {
			return ErrTooLarge
		}
	}
	return nil
}
