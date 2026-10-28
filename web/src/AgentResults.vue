<script setup lang="ts">
import { computed, shallowReactive, watch } from 'vue'
import { AgentEvidenceReader, actionLabels, assetRoute, basisLabels, bindingFor, countText, localEvidenceError, recommendationLabels, recordFor, roleLabels, toolLabels, toolRuns, toolStatusLabels, usageView } from './agent-view'
import type { AgentRecord, AttemptUsage, AuditRecord, JobEvent } from './workspace-types'

const props = withDefaults(defineProps<{
  jobId: string
  record?: AgentRecord | null
  usage?: AttemptUsage | null
  audit?: AuditRecord | null
  snapshotId?: string
  events?: JobEvent[]
  loadInput?: (jobId: string) => Promise<unknown>
}>(), { record: null, audit: null, snapshotId: '', events: () => [] })
const current = computed(() => recordFor(props.record, props.jobId))
const content = computed(() => current.value?.content)
const output = computed(() => content.value?.output)
const usage = computed(() => usageView(props.usage))
const runs = computed(() => toolRuns(props.events, props.jobId))
const reader = shallowReactive(new AgentEvidenceReader())
watch(() => [props.jobId, current.value?.id ?? ''], () => reader.reset(props.jobId, current.value?.id ?? ''), { immediate: true, flush: 'sync' })
function inspect(alias: string) { const id = props.jobId, record = current.value; if (record) void reader.inspect(id, record, alias, props.audit, props.loadInput ? () => props.loadInput!(id) : undefined) }
function refLabel(alias: string) { const binding = bindingFor(current.value, props.jobId, alias, false); return binding?.service_ref ? `${binding.asset_ref} / ${binding.service_ref}` : binding?.asset_ref ?? alias }
function routeFor(alias: string, service?: string) { return assetRoute(current.value, props.jobId, alias, props.snapshotId, service) }
const valueText = computed(() => { if (!reader.value.found) return ''; return JSON.stringify(reader.value.value, null, 2) ?? '未提供可展示值' })
const preview = computed(() => valueText.value.slice(0, 16384))
const sourceText = computed(() => ({ synthetic: '人工构造', imported: '导入声明', live_readonly: '只读观测' }[output.value?.source_kind ?? content.value?.input_manifest.source_kind ?? ''] ?? '来源待核对'))
const basisText = computed(() => ({ synthetic_assumption: '仅人工构造案例', imported_assumption: '导入声明条件下', live_observed: '固定只读观测时点' }[output.value?.assessment_basis ?? ''] ?? '结论依据待核对'))
</script>

<template>
  <section class="workspace-section agent-results" aria-labelledby="agent-results-title">
    <div class="section-title">
      <div><h2 id="agent-results-title">模型补充</h2><p class="section-description">解释、候选与条件建议独立保存；原始规则判定保持不变。</p></div>
      <span class="tag tag-neutral">{{ output ? '已保存模型补充' : content?.error_code ? '模型阶段未完成' : '尚无结构结果' }}</span>
    </div>
    <template v-if="current">
      <dl class="fact-grid agent-context">
        <div><dt>资料来源／结论依据</dt><dd>{{ sourceText }} / {{ basisText }}</dd></div>
        <div><dt>固定输入版本</dt><dd>{{ content?.input_manifest.snapshot_version || '未知' }}</dd></div>
        <div><dt>角色／访问要求版本</dt><dd>{{ content?.input_manifest.profile_version || '未知' }} / {{ content?.input_manifest.intent_version || '未知' }}</dd></div>
        <div><dt>判据版本</dt><dd>{{ output?.rule_version || content?.input_manifest.baseline_version || '未知' }}</dd></div>
        <div><dt>逻辑模型轮次／工具调用</dt><dd>{{ countText(content?.model_calls) }} / {{ countText(content?.tool_calls) }}</dd></div>
        <div><dt>历史披露</dt><dd>{{ content?.context?.disclosure_version || '版本未知' }}；纳入 {{ countText(content?.context?.included_messages) }} 条，排除 {{ countText(content?.context?.excluded_messages) }} 条；{{ content?.context?.truncated === true ? '历史已截断' : content?.context?.truncated === false ? '未截断' : '截断状态未知' }}</dd></div>
      </dl>
      <p v-if="content?.error_code" class="notice notice-warning" role="status">本轮模型补充未完成。{{ audit ? '本任务已保存的规则结果仍可审阅。' : '本任务没有经校验的模型补充输出。' }}受控状态：<code>{{ content.error_code }}</code>。</p>
      <template v-if="output">
        <section class="agent-explanation"><h3>模型解释</h3><p>{{ output.explanation }}</p></section>
        <section v-if="output.roles?.length" class="agent-candidates" aria-labelledby="agent-roles-title">
          <h3 id="agent-roles-title">角色候选</h3>
          <article v-for="(role, index) in output.roles" :key="index" class="agent-item">
            <div class="section-title"><strong>{{ refLabel(role.asset_ref) }} / {{ roleLabels[role.role] ?? role.role }}</strong><span class="tag tag-warning">待确认</span></div>
            <p v-if="role.service_ref" class="field-hint">服务：{{ refLabel(role.service_ref) }}</p>
            <div class="agent-evidence-group"><h4>支持证据</h4><div class="agent-aliases"><template v-for="ref in role.supporting_evidence ?? []" :key="ref"><button v-if="bindingFor(current, jobId, ref)" type="button" class="text-action" @click="inspect(ref)">{{ ref }}</button><span v-else class="muted">{{ ref }}（本轮未绑定）</span></template></div></div>
            <div v-if="role.contradicting_evidence?.length" class="agent-evidence-group"><h4>反对证据</h4><div class="agent-aliases"><template v-for="ref in role.contradicting_evidence" :key="ref"><button v-if="bindingFor(current, jobId, ref)" type="button" class="text-action" @click="inspect(ref)">{{ ref }}</button><span v-else class="muted">{{ ref }}（本轮未绑定）</span></template></div></div>
            <div v-if="role.unknowns?.length" class="agent-open-items"><h4>尚未核实</h4><ul><li v-for="item in role.unknowns" :key="item">{{ item }}</li></ul></div>
            <div v-if="role.questions?.length" class="agent-open-items"><h4>需要确认</h4><ul><li v-for="item in role.questions" :key="item">{{ item }}</li></ul></div>
            <RouterLink v-if="routeFor(role.asset_ref, role.service_ref)" class="agent-form-link" :to="routeFor(role.asset_ref, role.service_ref)!">打开该资料的人工确认表单</RouterLink>
            <p v-else class="field-hint">尚无可核实的固定资料／资产绑定，不生成确认入口。</p>
          </article>
        </section>
        <section v-if="output.recommendations?.length" class="agent-recommendations" aria-labelledby="agent-recommendations-title">
          <h3 id="agent-recommendations-title">条件建议</h3>
          <article v-for="(item, index) in output.recommendations" :key="index" class="agent-item">
            <div class="section-title"><h4 class="agent-kind">{{ recommendationLabels[item.kind] ?? '建议类型待核对' }}</h4><span class="tag tag-neutral">{{ basisLabels[item.basis_kind] ?? '依据待核对' }}</span></div>
            <dl class="agent-recommendation-meta"><dt>对象</dt><dd>{{ refLabel(item.target_ref) }}</dd><template v-if="item.action"><dt>人工意图</dt><dd>{{ actionLabels[item.action] ?? item.action }}</dd></template><template v-if="item.component_topic"><dt>组件主题</dt><dd>{{ item.component_topic }}</dd></template></dl>
            <div class="agent-open-items"><h4>适用条件</h4><ul v-if="item.conditions?.length"><li v-for="condition in item.conditions" :key="condition">{{ condition }}</li></ul><p v-else>未提供明确条件，请先人工核对。</p></div>
            <div class="agent-open-items"><h4>可能影响</h4><p>{{ item.impact }}</p></div>
            <div class="agent-open-items"><h4>人工下一步</h4><p>{{ item.manual_next_step }}</p></div>
            <div class="agent-evidence-group"><h4>证据与关联依据</h4><div class="agent-aliases"><template v-for="ref in [...(item.evidence_refs ?? []), item.business_ref, item.finding_ref, item.access_ref].filter((ref): ref is string => !!ref)" :key="ref"><button v-if="bindingFor(current, jobId, ref)" type="button" class="text-action" @click="inspect(ref)">{{ ref }}</button><span v-else class="muted">{{ ref }}（本轮未绑定）</span></template></div></div>
            <RouterLink v-if="item.kind === 'confirm_information' && routeFor(item.target_ref)" class="agent-form-link" :to="routeFor(item.target_ref)!">打开人工资料表单</RouterLink>
          </article>
        </section>
        <section v-if="output.questions?.length" class="agent-open-items agent-questions"><h3>仍需人工确认</h3><ul><li v-for="question in output.questions" :key="question">{{ question }}</li></ul></section>
        <p class="field-hint">角色候选仍为待确认，不证明实际监听、可达性或业务授权。任何建议都需人工核对，不自动确认业务资料或下发策略。</p>
      </template>
    </template>
    <p v-else class="empty-inline">尚无已保存的模型结构结果。已有规则事实仍按原结果保留，空白不表示审计通过。</p>
    <section v-if="runs.length" class="agent-tools"><h3>受控工具记录</h3><div class="table-scroll"><table class="workspace-table compact-table"><thead><tr><th>查询</th><th>调用标识</th><th>状态／耗时</th><th>已保存证据别名</th></tr></thead><tbody><tr v-for="run in runs" :key="run.callID"><td>{{ toolLabels[run.name] ?? run.name }}</td><td><code>{{ run.callID }}</code></td><td>{{ toolStatusLabels[run.status] ?? '状态未知' }} / {{ run.elapsedMS === undefined ? '耗时未提供' : `${countText(run.elapsedMS)} ms` }}</td><td><div class="agent-aliases"><template v-for="ref in run.evidenceRefs" :key="ref"><button v-if="bindingFor(current, jobId, ref)" type="button" class="text-action" @click="inspect(ref)">{{ ref }}</button><span v-else>{{ ref }}</span></template></div><span v-if="!run.evidenceRefs.length" class="muted">未提供引用</span></td></tr></tbody></table></div></section>
    <section v-if="reader.selection" class="agent-local-evidence" aria-labelledby="agent-evidence-title">
      <div class="section-title"><h3 id="agent-evidence-title">本次固定证据字段</h3><button type="button" class="text-action" @click="reader.close()">收起字段</button></div>
      <dl class="agent-recommendation-meta"><dt>证据别名</dt><dd>{{ reader.selection.alias }}</dd><dt>固定文档</dt><dd>{{ reader.selection.binding.document === 'input' ? '任务封存输入' : '任务规则结果' }}</dd><dt>字段位置</dt><dd>{{ reader.selection.binding.field || '根文档' }}</dd></dl>
      <p v-if="reader.busy" class="field-hint" role="status">正在只读获取本次封存输入，不触发云或模型调用。</p>
      <p v-else-if="reader.failure" class="notice notice-warning" role="status">{{ localEvidenceError(reader.failure) }}</p>
      <template v-else-if="reader.value.found"><pre>{{ preview }}</pre><p v-if="valueText.length > preview.length" class="field-hint">此字段较大，当前仅展示开头；完整字段仍保存在本机固定任务输入中。</p></template>
      <p v-else class="field-hint">本次固定文档尚未提供该字段，无法据此补造值。</p>
    </section>
  </section>
  <section class="workspace-section agent-usage" aria-labelledby="agent-usage-title">
    <div class="section-title"><h2 id="agent-usage-title">本次调用与已报告用量</h2><span class="tag tag-neutral">{{ usage.tokenState === 'reported' ? '用量已报告' : usage.tokenState === 'partial' ? '部分未报告' : '用量未知' }}</span></div>
    <dl class="fact-grid"><div><dt>模型 HTTP 实际尝试</dt><dd>{{ usage.modelAttempts }}</dd></div><div><dt>云／凭据 HTTP 实际尝试</dt><dd>{{ usage.cloudAttempts }}</dd></div><div><dt>送达结果未知的尝试</dt><dd>{{ usage.unknownDelivery }}</dd></div><div><dt>已报告用量的响应</dt><dd>{{ usage.knownResponses }}</dd></div></dl>
    <dl v-if="usage.tokens" class="agent-token-values"><div><dt>已报告输入 token</dt><dd>{{ usage.tokens.prompt }}</dd></div><div><dt>已报告输出 token</dt><dd>{{ usage.tokens.completion }}</dd></div><div><dt>已报告 token 总量</dt><dd>{{ usage.tokens.total }}</dd></div></dl>
    <p v-else class="agent-token-unknown">Token：未知</p>
    <p class="field-hint">{{ usage.coverage }}HTTP 尝试包含失败或送达未知；token 仅按服务返回记录，不据此估算费用。</p>
  </section>
</template>
