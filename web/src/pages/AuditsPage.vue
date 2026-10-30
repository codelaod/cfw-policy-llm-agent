<script setup lang="ts">
import { shallowReactive, computed, markRaw, onBeforeUnmount, reactive, ref, watch } from 'vue'
import { onBeforeRouteLeave, useRoute, useRouter } from 'vue-router'
import UButton from '@nuxt/ui/components/Button.vue'
import PreparationResults from '../PreparationResults.vue'
import BatchResults from '../BatchResults.vue'
import GuidancePanel from '../GuidancePanel.vue'
import AgentResults from '../AgentResults.vue'
import CloudResults from '../CloudResults.vue'
import { needsResultRefresh } from '../agent-view'
import { cloudSnapshotID } from '../cloud-view'
import { EventObserver } from '../events'
import type { Mode, ObservationState } from '../events'
import { isTerminal } from '../api'
import { errorText, formatTime, jobLabel, scopeLabel, sourceLabel, useWorkspace, verdictLabel } from '../workspace'
import { isDefiniteRejection, mergeJobs, Mutation, PlanDraft } from '../workspace-state'
import type { Job, JobEvent, JobResult, PlanRequest } from '../workspace-types'

const workspace = useWorkspace(), route = useRoute(), router = useRouter()
const selected = ref<string[]>([]), gateway = ref(false), mode = ref<'rules_only' | 'rules_and_agent'>('rules_only')
const plan = reactive(new PlanDraft()), startMutation = shallowReactive(new Mutation<{ plan_id: string }, Job>())
const busy = ref(false), error = ref(''), job = ref<Job | null>(null), result = ref<JobResult | null>(null), events = ref<JobEvent[]>([]), observation = ref<ObservationState>('paused')
let observer: EventObserver | undefined, generation = 0, taskContext = 0
const resultRequests = new Map<string, { promise: Promise<void>; generation: number }>()
const resultRefreshes = new Set<string>()
const cachedStart = workspace.drafts.get('audit-start') as { body: { plan_id: string }; key: string } | undefined
if (cachedStart) startMutation.restorePending(cachedStart.body, cachedStart.key)
const snapshot = computed(() => workspace.currentSnapshot.value)
const isCloud = computed(() => job.value?.kind === 'collect' || job.value?.kind === 'connection_check')
const taskTitle = computed(() => job.value?.kind === 'connection_check' ? '身份核对过程' : job.value?.kind === 'collect' ? '只读采集过程' : job.value?.kind === 'assistant_turn' ? '助手任务过程' : '审计过程')
const observationLabels: Record<ObservationState, string> = { connecting: '连接中', live: '实时更新', reconnecting: '连接中断，正在只读重连', polling: '轮询更新', paused: '观察已暂停', ended: '已读完终态', auth_required: '需要重新解锁', forbidden: '记录不可访问', protocol_error: '事件协议错误' }
const phases: Record<string, string> = { preparing_data: '等待并准备本次范围资料', prepared_data_saved: '本次资料准备记录已保存', qualified_data_reused: '已复用合格的固定资料', batch_segment_started: '分批审计：开始本段', batch_segment_finished: '分批审计：本段结果已保存', batch_request_stopped: '分批审计：主任务已结束', local_mock: '本机人工流程', input_preparation: '准备固定输入', prepare: '准备固定输入', input_sealed: '固定输入已保存', rules_started: '执行确定性规则', report_building: '生成固定报告', model_not_configured: '模型未配置，保留规则结果', agent_result_saved: '模型结构结果已保存', agent_started: '受限模型阶段开始', rules: '执行确定性规则', rule_audit: '执行确定性规则', agent: '受限 Agent 补查', finalize: '保存结果', completed: '流程结束', L0: '计划与排队', L1: '固定事实', L2: '规则结果', L3: 'Agent 补充', L4: '固定报告' }
watch(() => workspace.snapshotID.value, () => { selected.value = snapshot.value?.input.assets.map(a => a.id) ?? []; gateway.value = snapshot.value?.input.selection.kind === 'gateway_policy'; plan.invalidate() }, { immediate: true })
watch([selected, gateway, mode, () => workspace.snapshotID.value, () => workspace.conversationID.value], () => { plan.update({ snapshot: workspace.snapshotID.value, conversation: workspace.conversationID.value, scope: snapshot.value?.input.scope.ref ?? '', assets: selected.value, kind: gateway.value ? 'gateway_policy' : 'assets', mode: mode.value }) }, { deep: true, flush: 'sync' })
async function preview() {
  if (!snapshot.value || busy.value || startMutation.pendingBody) return
  const source = snapshot.value, assetIDs = [...selected.value], gatewayOnly = gateway.value, selectedMode = mode.value, activeContext = taskContext
  let owner = workspace.conversationID.value
  const matches = () => taskContext === activeContext && snapshot.value?.id === source.id && gateway.value === gatewayOnly && mode.value === selectedMode && [...selected.value].sort().join('\0') === [...assetIDs].sort().join('\0')
  const current = () => matches() && workspace.conversationID.value === owner
  busy.value = true; error.value = ''
  try {
    const conversation = await workspace.ensureConversation(source)
    if (!matches() || workspace.conversationID.value !== conversation.id) return
    owner = conversation.id
    const request: PlanRequest = { kind: 'audit', conversation_id: conversation.id, data_source_id: 'local-offline', policy_scope_ref: source.input.scope.ref, selection: { kind: gatewayOnly ? 'gateway_policy' : 'assets', asset_ids: gatewayOnly ? [] : assetIDs }, data_policy: { mode: 'pinned_snapshot', snapshot_id: source.id }, baseline_version_id: source.input.baseline.version, mode: selectedMode }
    plan.update({ snapshot: source.id, conversation: conversation.id, scope: request.policy_scope_ref, assets: assetIDs, kind: request.selection.kind, mode: selectedMode })
    const accept = plan.capturePreview(), response = await workspace.api.previewPlan(request)
    if (current()) accept(response)
  } catch (cause) { if (current()) error.value = errorText(cause) }
  finally { if (taskContext === activeContext) busy.value = false }
}
async function start() {
  if (busy.value || !startMutation.pendingBody && (!plan.preview || !plan.startable)) return
  const body = startMutation.pendingBody ?? { plan_id: plan.preview!.id }, activeContext = taskContext, owner = workspace.conversationID.value, sourceID = workspace.snapshotID.value
  const current = () => taskContext === activeContext && workspace.conversationID.value === owner && workspace.snapshotID.value === sourceID
  busy.value = true; error.value = ''
  try {
    const accepted = await startMutation.submit(body, (request, key) => workspace.api.startJob(request.plan_id, key))
    workspace.drafts.delete('audit-start'); workspace.updateJob(accepted)
    if (current()) { plan.invalidate(); await router.push(`/audits/${accepted.id}`) }
  } catch (cause) { if (isDefiniteRejection(cause)) { startMutation.discard(); workspace.drafts.delete('audit-start'); if (plan.preview?.id === body.plan_id && ['PLAN_STALE', 'PLAN_CONSUMED'].includes((cause as { code: string }).code)) plan.invalidate() }; if (current()) error.value = errorText(cause) }
  finally { if (taskContext === activeContext) busy.value = false }
}
function pause() { generation++; observer?.stop(); observer = undefined; observation.value = 'paused' }
async function readResult(id: string, runGeneration = generation): Promise<void> {
  const existing = resultRequests.get(id)
  if (existing) { if (existing.generation === runGeneration) { resultRefreshes.add(id); return existing.promise }; await existing.promise; if (runGeneration === generation && job.value?.id === id) return readResult(id, runGeneration); return }
  const request = (async () => {
    do {
      resultRefreshes.delete(id)
      try { const value = await workspace.api.jobResult(id); if (runGeneration === generation && job.value?.id === id && (!result.value || value.view_seq >= result.value.view_seq)) result.value = value }
      catch (cause) { if (runGeneration === generation) error.value = errorText(cause) }
    } while (resultRefreshes.has(id) && runGeneration === generation && job.value?.id === id)
  })()
  resultRequests.set(id, { promise: request, generation: runGeneration })
  try { await request } finally { if (resultRequests.get(id)?.promise === request) resultRequests.delete(id) }
}
function observe(mode: Mode) {
  if (!job.value) return
  pause(); error.value = ''
  const id = job.value.id, activeGeneration = generation
  const current = () => generation === activeGeneration && job.value?.id === id
  observer = markRaw(new EventObserver(workspace.api.transport, id, events.value.at(-1)?.seq ?? 0, {
    event: event => { if (!current()) return; events.value.push(event); if (needsResultRefresh(event)) void readResult(id, activeGeneration); if (event.type === 'message_available' && workspace.conversationID.value === job.value?.conversation_id) void workspace.loadMessages().catch(cause => { if (current()) error.value = errorText(cause) }) },
    job: value => { if (current() && (!job.value || value.view_seq >= job.value.view_seq)) { job.value = value; workspace.updateJob(value) } },
    state: value => { if (current()) { observation.value = value; if (value === 'auth_required') workspace.lock() } },
    error: cause => { if (current()) error.value = errorText(cause) },
  }))
  void observer.start(mode)
}
async function load(id: string) {
  taskContext++; pause(); const activeGeneration = generation; busy.value = false; job.value = null; result.value = null; events.value = []; error.value = ''
  if (!id) return
  try { const value = await workspace.api.job(id); if (generation !== activeGeneration) return; job.value = value; workspace.updateJob(value); await readResult(id, activeGeneration); if (generation === activeGeneration) observe('sse') }
  catch (cause) { if (generation === activeGeneration) error.value = errorText(cause) }
}
async function cancel() {
  if (!job.value || isTerminal(job.value.status) || busy.value) return
  busy.value = true; error.value = ''
  const id = job.value.id, activeContext = taskContext
  const current = () => taskContext === activeContext && job.value?.id === id
  try { const value = await workspace.api.cancelJob(job.value.parent_job_id || id); workspace.updateJob(value); if (current() && job.value?.id === value.id) job.value = mergeJobs([job.value], [value])[0]! }
  catch (cause) { if (current()) error.value = errorText(cause) }
  finally { if (current()) busy.value = false }
}
async function selectCloudSnapshot(id: string, destination: 'inventory' | 'audits') {
  if (!job.value || id !== cloudSnapshotID(result.value?.cloud_result, job.value.id, result.value?.snapshot_id)) return
  const activeContext = taskContext, taskID = job.value.id
  const current = () => taskContext === activeContext && job.value?.id === taskID
  try { await workspace.selectSnapshot(id); if (!current() || workspace.snapshotID.value !== id) return; await router.push(destination === 'inventory' ? { path: '/inventory', query: { snapshot_id: id } } : '/audits') }
  catch (cause) { if (current()) error.value = errorText(cause) }
}
function eventTitle(event: JobEvent) {
  if (event.type === 'stage_changed' && event.payload.stage === 'cloud_result_saved') return '云读取结果已保存'
  if (event.type === 'job_status' || event.type === 'job_terminal') return jobLabel(event.payload.status)
  if (event.type === 'stage_changed') return phases[event.payload.stage ?? ''] ?? event.payload.stage ?? '阶段更新'
  return ({ findings_available: '规则结果已保存', message_available: '公开消息已保存', report_available: '固定报告已保存', tool_started: '只读工具开始', tool_finished: '只读工具结束' } as Record<string, string>)[event.type] ?? event.type
}
const comparisonStates: Record<string, string> = { improved: '可比配置中的违规范围缩小，问题仍存在', persistent: '仍然存在', new: '本次新增', not_detected: '可比配置中未再检出' }
const comparisonReasons: Record<string, string> = { object_identity_changed: '对象或网关的原始身份变化', scope_changed: '审计范围变化', profile_version_changed: '角色版本变化', intent_version_changed: '访问要求版本变化', baseline_changed: '基线或例外变化', kernel_version_changed: '规则内核版本变化', normalizer_version_changed: '归一化版本变化', semantics_changed: '云策略语义变化', source_kind_changed: '来源保证变化', evaluation_time_changed: '评估时点变化', incomplete_coverage: '资料或计算覆盖不完整', selection_changed: '所选对象变化', check_missing: '原检查缺失', rule_version_changed: '判据版本变化', check_inventory_changed: '检查清单变化', issue_not_examined: '原问题本次未得到充分检查', object_identity_ambiguous: '对象标识有歧义', object_missing: '原对象缺失', object_scope_mismatch: '对象范围不一致', object_endpoint_changed: '对象端点变化', endpoint_unverified: '端点关联未核实', observation_point_changed: 'NAT 观察位置变化', association_unverified: 'NAT 关联未核实', object_business_changed: '对象业务声明变化' }
function comparisonTitle(ids: string[]) { const record = result.value?.audit?.findings.find(item => ids.includes(item.id) || ids.includes(item.finding.id)); return record ? `${record.finding.rule_id} / ${record.finding.subject_ref}${record.finding.service_ref ? ' / ' + record.finding.service_ref : ''}` : '原审计问题' }
const verdictCounts = computed(() => { const counts: Record<string, number> = {}; for (const item of result.value?.audit?.findings ?? []) counts[item.finding.verdict] = (counts[item.finding.verdict] ?? 0) + 1; return counts })
watch(() => route.params.id, value => { void load(typeof value === 'string' ? value : '') }, { immediate: true })
onBeforeRouteLeave(() => !busy.value || startMutation.state === 'saved')
onBeforeUnmount(() => { taskContext++; pause(); if (startMutation.pendingBody && startMutation.key) workspace.drafts.set('audit-start', { body: startMutation.pendingBody, key: startMutation.key }) })
</script>

<template>
  <div class="workspace-page">
    <header class="page-title"><div><h1>{{ job ? taskTitle : '预览审计计划' }}</h1><p>{{ job ? '任务与输入版本固定。连接恢复只读取历史，不重新执行。' : '先核对范围、资料与模型效果，再明确启动。预览不会外呼。' }}</p></div><RouterLink v-if="job" :to="isCloud ? '/overview' : '/audits'">{{ isCloud ? '返回数据与任务' : '新建审计计划' }}</RouterLink></header>
    <GuidancePanel surface="audit" :conversation-id="job?.conversation_id || workspace.conversationID.value" :job-id="job?.id" :snapshot-id="result?.snapshot_id || result?.audit?.snapshot_id" :disabled="busy" />
    <template v-if="!route.params.id"><div v-if="!snapshot" class="workspace-empty"><h2>先选择一份固定资料</h2><p>无模型或云连接时，人工输入与规则审计仍可使用。</p><RouterLink to="/overview">准备资料</RouterLink></div><section v-else class="workspace-section plan-form"><div class="section-title"><h2>{{ scopeLabel(snapshot.input.scope.boundary, snapshot.input.scope.direction) }}</h2><span class="tag tag-neutral">{{ sourceLabel(snapshot.source_kind) }}</span></div><dl class="fact-grid"><div><dt>资料时点</dt><dd>{{ formatTime(snapshot.input.collected_at) }}</dd></div><div><dt>数据策略</dt><dd>固定所选资料，不刷新云端</dd></div><div><dt>当前会话</dt><dd>{{ workspace.currentConversation.value?.title ?? '预览时选择或创建本范围会话' }}</dd></div><div><dt>完整性</dt><dd>{{ snapshot.input.completeness.enumeration_complete ? '资料声明完整，保证级别仍按来源' : '存在枚举缺口，受影响检查不通过' }}</dd></div></dl><fieldset :disabled="busy || !!startMutation.pendingBody"><legend>本次比较对象</legend><label v-if="snapshot.input.scope.boundary === 'nat'" class="check-line"><input v-model="gateway" type="checkbox" />只审查所选网关的策略集，不推定全部资产路径</label><div v-if="!gateway" class="asset-choices"><label v-for="asset in snapshot.input.assets" :key="asset.id" class="check-line"><input v-model="selected" type="checkbox" :value="asset.id" />{{ asset.id }} <span class="muted">{{ asset.address || '地址未知' }}</span></label></div><p v-else class="field-hint">网关：{{ snapshot.input.scope.gateway_ref }}；资产级外联比较不会被自动补为全部网段。</p></fieldset><fieldset :disabled="busy || !!startMutation.pendingBody"><legend>执行模式</legend><p class="field-hint">当前服务：{{ workspace.modelState.value.label }}；模式 {{ workspace.modelState.value.mode }}。</p><label class="check-line"><input v-model="mode" type="radio" value="rules_only" />仅运行确定性规则</label><label class="check-line"><input v-model="mode" type="radio" value="rules_and_agent" />规则后由受限 Agent 补查与解释</label><p v-if="mode === 'rules_and_agent' && !workspace.modelAvailable.value" class="field-hint">模型尚未就绪。服务端会在预览中说明缺项；不能把模型未执行说成 Agent 已完成。</p></fieldset><UButton :disabled="busy || !!startMutation.pendingBody || !gateway && !selected.length" @click="preview">{{ busy ? '预览中…' : '预览范围与效果' }}</UButton></section>
      <section v-if="plan.preview && !startMutation.pendingBody" class="workspace-section accepted-plan"><div class="section-title"><h2>启动前核对</h2><span class="muted">有效至 {{ formatTime(plan.preview.expires_at) }}</span></div><dl class="fact-grid"><div><dt>固定资料</dt><dd>{{ sourceLabel(snapshot?.source_kind) }} / {{ formatTime(snapshot?.input.collected_at) }}</dd></div><div><dt>云读取</dt><dd>{{ plan.preview.effect_ceiling?.allow_cloud_reads === false ? '不允许' : plan.preview.effect_ceiling?.allow_cloud_reads === true ? '按计划受限只读' : '以服务端计划为准' }}</dd></div><div><dt>模型请求</dt><dd>{{ plan.preview.effect_ceiling?.allow_model_requests === false ? '不使用模型' : plan.preview.effect_ceiling?.allow_model_requests === true ? '允许有界模型阶段' : mode === 'rules_only' ? '请求模式为仅规则' : '以服务端计划为准' }}</dd></div><div><dt>结果边界</dt><dd>配置逻辑分析，不证明实际可达</dd></div></dl><ul v-if="plan.preview.warnings?.length" class="notice-list"><li v-for="warning in plan.preview.warnings" :key="warning">{{ warning }}</li></ul><ul v-if="plan.preview.gaps?.length" class="notice-list"><li v-for="gap in plan.preview.gaps" :key="gap">{{ gap }}</li></ul><p v-if="!plan.startable" class="notice notice-warning">计划已过期，请重新预览；不会自动创建替代任务。</p><UButton :disabled="busy || !plan.startable" @click="start">{{ busy ? '受理中…' : startMutation.state === 'unsaved' ? '以原计划标识核对并重试' : '接受计划并启动' }}</UButton></section>
      <section v-if="startMutation.pendingBody" class="notice notice-warning"><p>原计划 {{ startMutation.pendingBody.plan_id }} 的启动结果尚未核对。以原标识重试只核对同一次启动。</p><UButton :disabled="busy" @click="start">以原计划标识核对并重试</UButton></section>
      <section v-if="workspace.jobs.value.length" class="workspace-section"><div class="section-title"><h2>已保存的任务</h2></div><div class="table-scroll"><table class="workspace-table"><thead><tr><th>创建时间</th><th>会话</th><th>状态</th><th>任务引用</th></tr></thead><tbody><tr v-for="item in workspace.jobs.value" :key="item.id"><td>{{ formatTime(item.created_at) }}</td><td>{{ workspace.conversations.value.find(c => c.id === item.conversation_id)?.title ?? '已有会话' }}</td><td>{{ jobLabel(item.status) }}</td><td><RouterLink :to="`/audits/${item.id}`">查看过程</RouterLink></td></tr></tbody></table><UButton v-if="workspace.listHasMore.value.jobs" color="neutral" variant="outline" :disabled="workspace.listsLoading.value" @click="workspace.loadMore('jobs')">读取更多任务</UButton></div></section></template>
    <template v-else-if="job"><section class="task-banner"><div><span :class="['tag', job.status === 'partial' || job.status === 'failed' ? 'tag-warning' : 'tag-neutral']">{{ jobLabel(job.status) }}</span><span class="task-reference">{{ job.id }}</span><p>发起于 {{ formatTime(job.created_at) }}；{{ observationLabels[observation] }}</p></div><UButton v-if="!isTerminal(job.status)" color="neutral" variant="outline" :disabled="busy || job.status === 'cancelling'" @click="cancel">{{ job.status === 'cancelling' ? '取消已受理' : job.parent_job_id ? '取消所属分批主任务' : '取消当前任务' }}</UButton></section><p v-if="job.parent_job_id" class="field-hint">这是分批任务的一个独立段。<RouterLink :to="`/audits/${job.parent_job_id}`">查看所属主任务与完整对象集</RouterLink></p><div class="audit-process-layout"><section class="workspace-section event-timeline"><div class="section-title"><h2>已保存的执行事件</h2><div class="small-actions"><button type="button" @click="pause">暂停观察</button><button type="button" @click="observe('sse')">恢复实时</button><button type="button" @click="observe('polling')">改用轮询</button></div></div><ol v-if="events.length"><li v-for="event in events" :key="event.seq"><span class="timeline-seq">{{ event.seq }}</span><div><strong>{{ eventTitle(event) }}</strong><p v-if="event.payload.summary">{{ event.payload.summary }}</p><p v-if="event.payload.tool_name" class="muted">{{ event.payload.tool_name }} / {{ event.payload.call_id }}</p><time>{{ formatTime(event.occurred_at) }}</time></div></li></ol><p v-else class="empty-inline">正在只读补发已保存事件。断开连接不会取消后台任务。</p></section><aside class="workspace-section rule-progress"><h2>{{ isCloud ? '本轮只读任务' : job.kind === 'assistant_turn' ? '本轮公开回复' : '已得规则结果' }}</h2><p v-if="isCloud" class="empty-inline">{{ job.kind === 'connection_check' ? '本轮仅核对调用身份，不生成产品资料或审计结论。' : '本轮只读采集独立保存新资料。后续业务确认与审计仍由你明确选择。' }}已保存结果与物理请求次数见下方。</p><p v-else-if="result?.batch" class="empty-inline">完整对象集的覆盖与证据按下方逐段记录展示，不以第一段结果代表整组。</p><template v-else-if="result?.audit"><p class="section-description">先展示服务端已保存事实，不等待模型阶段。</p><div class="verdict-summary"><span v-for="(count, verdict) in verdictCounts" :key="verdict" :class="['tag', `verdict-${verdict}`]">{{ verdictLabel(verdict) }} {{ count }}</span></div><p class="field-hint">{{ result.audit.result.coverage.complete ? '本次所支持查询已完整计算。' : '存在未穷尽或信息不足范围；未发现问题不能当作通过。' }}</p><p class="field-hint">评估时点 {{ formatTime(result.audit.result.input_manifest.evaluation_at) }}<br />资料时点 {{ formatTime(result.audit.result.input_manifest.collected_at) }}</p><RouterLink :to="{ path: '/findings', query: { audit_id: result.audit.id } }">审阅发现与证据</RouterLink><RouterLink class="block-link" :to="{ path: '/reports', query: { audit_id: result.audit.id } }">生成固定报告</RouterLink></template><p v-else-if="job.kind === 'assistant_turn'" class="empty-inline">{{ result?.agent_result?.content.output ? '公开模型回复已保存到当前会话；候选与条件建议见下方模型补充。' : '本轮尚无经校验的模型结构结果，失败不会产生伪助手回复。' }}可从“会话与历史”重开公开记录。</p><p v-else class="empty-inline">尚无已保存规则结果。排队、失败或缺资料不显示为风险零。</p><p v-if="job.error_code" class="notice notice-warning">阶段状态：{{ job.error_code }}。已有结果仍保留。</p></aside></div></template>
    <PreparationResults v-if="job && result?.preparation_discovery" :job-id="job.id" :conversation-id="job.conversation_id" :record="result.preparation_discovery" />
    <BatchResults v-if="job && result?.batch" :job-id="job.id" :conversation-id="job.conversation_id" :record="result.batch" />
    <CloudResults v-if="job && (isCloud || result?.cloud_result)" :job-id="job.id" :record="result?.cloud_result" :usage="result?.attempt_usage" :snapshot-id="result?.snapshot_id || ''" @open-snapshot="selectCloudSnapshot" />
    <AgentResults v-if="job && !isCloud && !result?.batch" :job-id="job.id" :record="result?.agent_result" :usage="result?.attempt_usage" :audit="result?.audit" :snapshot-id="result?.snapshot_id || result?.audit?.snapshot_id || ''" :events="events" :load-input="id => workspace.api.jobInput(id)" />
    <section v-if="result?.recheck" class="workspace-section recheck-comparison"><div class="section-title"><h2>复审比较结果</h2><span :class="['tag', result.recheck.comparison.comparable ? 'tag-neutral' : 'tag-warning']">{{ result.recheck.comparison.comparable ? '可比较' : '不可比较' }}</span></div><p class="section-description">沿用原评估条件。未再检出仅表示本次可比配置证据中的变化，不证明真实整改或运行时恢复。</p><ul v-if="result.recheck.comparison.reasons.length" class="notice-list warning"><li v-for="reason in result.recheck.comparison.reasons" :key="reason">{{ comparisonReasons[reason] ?? reason }}</li></ul><div v-if="result.recheck.comparison.comparable" class="table-scroll"><table class="workspace-table"><thead><tr><th>问题／对象</th><th>本次变化</th><th>对应发现</th></tr></thead><tbody><tr v-for="issue in result.recheck.comparison.issues" :key="issue.issue_key"><td>{{ comparisonTitle(issue.new_finding_ids) }}</td><td>{{ comparisonStates[issue.state] ?? issue.state }}</td><td><RouterLink v-if="issue.old_finding_ids.length" :to="{path:'/findings',query:{audit_id:result.recheck.old_audit_id,finding_key:issue.old_finding_ids[0]}}">原审计</RouterLink><span v-else>原审计无对应发现</span><span> / </span><RouterLink v-if="issue.new_finding_ids.length" :to="{path:'/findings',query:{audit_id:result.recheck.new_audit_id,finding_key:issue.new_finding_ids[0]}}">本次复审</RouterLink><span v-else>本次无对应发现</span></td></tr></tbody></table><p v-if="!result.recheck.comparison.issues.length" class="empty-inline">可比范围内没有需要列示的失败变化，仍按本次覆盖范围解释。</p></div></section>
    <p v-if="error" class="notice notice-error" role="alert">{{ error }}</p>
  </div>
</template>
