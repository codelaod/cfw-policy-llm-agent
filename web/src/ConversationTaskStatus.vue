<script setup lang="ts">
import { computed, markRaw, onBeforeUnmount, ref, watch } from 'vue'
import PreparationResults from './PreparationResults.vue'
import BatchResults from './BatchResults.vue'
import CloudResults from './CloudResults.vue'
import UButton from '@nuxt/ui/components/Button.vue'
import { EventObserver, type Mode, type ObservationState } from './events'
import { ProtocolError, isTerminal } from './api'
import { needsResultRefresh } from './agent-view'
import { errorText, formatTime, jobLabel, scopeLabel, sourceLabel, useWorkspace, verdictLabel } from './workspace'
import type { Job, JobEvent, JobResult } from './workspace-types'

const props = defineProps<{ jobId: string; conversationId: string }>()
const workspace = useWorkspace(), job = ref<Job | null>(null), result = ref<JobResult | null>(null), events = ref<JobEvent[]>([]), error = ref(''), observation = ref<ObservationState>('paused'), cancelling = ref(false)
let generation = 0, observer: EventObserver | null = null
let request: { version: number; id: string; promise: Promise<void> } | null = null
const requestedAgain = new Set<string>()
const stateLabels: Record<ObservationState, string> = { connecting: '正在恢复观察', live: '实时观察', reconnecting: '正在只读重连', polling: '轮询观察', paused: '观察已暂停', ended: '已收到终态', auth_required: '需要重新解锁', forbidden: '记录不可访问', protocol_error: '事件格式需要核对' }
const stages: Record<string, string> = { preparing_data: '等待并准备本次范围资料', prepared_data_saved: '本次资料准备记录已保存', qualified_data_reused: '已复用合格的固定资料', batch_segment_started: '分批审计：开始本段', batch_segment_finished: '分批审计：本段结果已保存', batch_request_stopped: '分批审计：主任务已结束', L0: '核对计划与排队', L1: '准备资料', L2: '规则检查', L3: '模型补充', L4: '保存汇总', input_preparation: '准备资料', prepare: '准备资料', input_sealed: '资料已封存', rules_started: '规则检查开始', rules: '规则检查', rule_audit: '规则检查', agent_started: '模型补充开始', agent: '受限补查', agent_result_saved: '模型结果已保存', model_not_configured: '模型未配置，保留规则结果', cloud_result_saved: '只读资料已保存', discovery_saved: '目录准备记录已保存', report_building: '保存固定报告', finalize: '保存结果', completed: '任务结束' }
const currentStage = computed(() => { if (job.value && isTerminal(job.value.status)) return jobLabel(job.value.status); const event = [...events.value].reverse().find(item => item.type === 'stage_changed'); return event ? stages[String(event.payload.stage)] ?? '执行阶段已更新' : jobLabel(job.value?.status) })
const counts = computed(() => { const value: Record<string, number> = {}; for (const finding of result.value?.audit?.findings ?? []) value[finding.finding.verdict] = (value[finding.finding.verdict] ?? 0) + 1; return value })
function pause() { observer?.stop(); observer = null; observation.value = 'paused' }
async function readResult(version: number): Promise<void> {
  const id = props.jobId, previous = request
  if (previous) {
    if (previous.version === version && previous.id === id) { requestedAgain.add(id); return previous.promise }
    await previous.promise
    if (version === generation && props.jobId === id) return readResult(version)
    return
  }
  const promise = (async () => {
    do {
      requestedAgain.delete(id)
      try { const value = await workspace.api.jobResult(id); if (version === generation && props.jobId === id && (!result.value || value.view_seq >= result.value.view_seq)) result.value = value }
      catch (cause) { if (version === generation && props.jobId === id) error.value = errorText(cause) }
    } while (requestedAgain.has(id) && version === generation && props.jobId === id)
  })()
  request = { id, version, promise }
  try { await promise } finally { if (request?.promise === promise) request = null }
}
function observe(mode: Mode = 'sse') {
  if (!job.value) return
  pause(); error.value = ''
  const version = generation, id = job.value.id
  observer = markRaw(new EventObserver(workspace.api.transport, id, events.value.at(-1)?.seq ?? 0, {
    event: event => {
      if (version !== generation || props.jobId !== id) return
      events.value.push(event)
      if (needsResultRefresh(event)) void readResult(version)
      if (event.type === 'message_available' || event.type === 'job_terminal') void Promise.all([workspace.loadMessages(), workspace.loadIntent()]).catch(cause => { if (version === generation) error.value = errorText(cause) })
    },
    job: value => { if (version !== generation || value.id !== id || value.conversation_id !== props.conversationId) return; if (!job.value || value.view_seq >= job.value.view_seq) { job.value = value; workspace.updateJob(value) } },
    state: value => { if (version === generation) { observation.value = value; if (value === 'auth_required') workspace.lock() } },
    error: cause => { if (version === generation) error.value = errorText(cause) },
  }))
  void observer.start(mode)
}
async function load() {
  generation++; pause(); const version = generation
  job.value = null; result.value = null; events.value = []; error.value = ''; requestedAgain.clear(); cancelling.value = false
  if (!props.jobId) return
  try {
    const value = await workspace.api.job(props.jobId)
    if (version !== generation) return
    if (value.conversation_id !== props.conversationId) throw new ProtocolError('所选任务不属于此会话。')
    job.value = value; workspace.updateJob(value); await readResult(version)
    if (version === generation) observe()
  } catch (cause) { if (version === generation) error.value = errorText(cause) }
}
async function cancel() {
  if (!job.value || isTerminal(job.value.status) || cancelling.value) return
  const version = generation, id = job.value.id
  cancelling.value = true; error.value = ''
  try { const value = await workspace.api.cancelJob(job.value.parent_job_id || id); workspace.updateJob(value); if (version === generation && job.value?.id === value.id && value.view_seq >= job.value.view_seq) job.value = value }
  catch (cause) { if (version === generation) error.value = errorText(cause) }
  finally { if (version === generation) cancelling.value = false }
}
watch(() => [props.jobId, props.conversationId], () => { void load() }, { immediate: true })
onBeforeUnmount(() => { generation++; pause() })
</script>

<template>
  <section class="conversation-task" aria-labelledby="conversation-task-title">
    <header><div><span class="conversation-kicker">此会话的固定任务</span><h2 id="conversation-task-title">{{ currentStage }}</h2></div><span v-if="job" class="tag tag-neutral">{{ jobLabel(job.status) }}</span></header>
    <p class="conversation-task-observation">{{ stateLabels[observation] }}<span v-if="job"> · {{ formatTime(job.created_at) }} 发起</span></p>
    <p v-if="result?.audit" class="conversation-task-fixed-scope">本任务固定范围：{{ scopeLabel(result.audit.result.input_manifest.policy_scope.boundary, result.audit.result.input_manifest.policy_scope.direction) }}<br />{{ sourceLabel(result.audit.result.input_manifest.source_kind) }}；资料时点 {{ formatTime(result.audit.result.input_manifest.collected_at) }}。当前对话目标的变化不会改写这份输入。</p><p v-else-if="result?.cloud_result?.content.limitations.includes('NO_SEALED_INPUT')" class="field-hint">本任务尚未形成可审计的封存输入，以下仅为已保存读取记录；不能借用旧资料补成本次完整结果。</p><p v-else class="field-hint">本任务的范围与资料以其保存记录为准，当前目标的修改不会改写旧任务。</p><p v-if="result?.audit" class="conversation-result-summary">已保存 {{ result.audit.findings.length }} 条规则结果，无需等待模型汇总。</p>
    <div v-if="result?.audit" class="verdict-summary"><span v-for="(count, verdict) in counts" :key="verdict" class="tag tag-neutral">{{ verdictLabel(verdict) }} {{ count }}</span></div>
    <p v-if="result?.audit && !result.audit.result.coverage.complete" class="field-hint">本次存在未覆盖或信息不足范围，未检出不能当作全部通过。</p>
    <PreparationResults v-if="result?.preparation_discovery" :job-id="jobId" :conversation-id="conversationId" :record="result.preparation_discovery" />
    <CloudResults v-if="result?.cloud_result" :job-id="jobId" :record="result.cloud_result" :usage="result.attempt_usage" :snapshot-id="result.snapshot_id || ''" :show-snapshot-actions="false" />
    <BatchResults v-if="result?.batch" :job-id="jobId" :conversation-id="conversationId" :record="result.batch" />
    <p v-if="job?.error_code" class="notice notice-warning">任务未完整完成。已保存结果仍保留，具体缺口请查看过程与证据。</p>
    <div class="conversation-task-actions"><RouterLink v-if="job?.parent_job_id" :to="`/audits/${job.parent_job_id}`">查看所属分批主任务</RouterLink><RouterLink v-if="job" :to="`/audits/${job.id}`">查看完整过程</RouterLink><RouterLink v-if="result?.audit" :to="{ path: '/findings', query: { audit_id: result.audit.id } }">审阅规则与证据</RouterLink><RouterLink v-if="result?.audit?.snapshot_id" :to="{ path: '/inventory', query: { snapshot_id: result.audit.snapshot_id } }">本任务资料与业务确认</RouterLink><RouterLink v-if="result?.audit" :to="{ path: '/reports', query: { audit_id: result.audit.id } }">固定报告与复审</RouterLink><UButton v-if="job && !isTerminal(job.status)" size="sm" color="neutral" variant="outline" :disabled="cancelling || job.status === 'cancelling'" @click="cancel">{{ cancelling ? '取消受理中…' : job.status === 'cancelling' ? '取消已受理' : job.parent_job_id ? '取消所属分批主任务' : '取消这个任务' }}</UButton></div>
    <details class="conversation-observation-controls"><summary>观察与恢复</summary><p>暂停或重连只读取记录，不取消任务，也不重新执行。</p><div><button type="button" @click="pause">暂停观察</button><button type="button" @click="observe('sse')">恢复观察</button><button type="button" @click="observe('polling')">改用轮询</button></div><ol v-if="events.length"><li v-for="event in events" :key="event.seq"><span>{{ event.type === 'stage_changed' ? stages[String(event.payload.stage)] ?? '执行阶段更新' : event.type === 'findings_available' ? '规则结果已保存' : event.type === 'message_available' ? '公开答复已保存' : event.type === 'job_terminal' ? jobLabel(String(event.payload.status)) : event.type.startsWith('tool_') ? '只读资料补查' : '任务状态更新' }}</span><p v-if="event.payload.summary">{{ event.payload.summary }}</p><time>{{ formatTime(event.occurred_at) }}</time></li></ol></details>
    <p v-if="error" class="error-text" role="alert">{{ error }}</p>
  </section>
</template>
