<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { batchEvidenceLabel, batchFor, batchResourceLabel, batchStatusLabel, resolveBatchSegment, type BatchDestination } from './batch-view'
import { errorText, formatTime, scopeLabel, sourceLabel, useWorkspace } from './workspace'
import type { BatchRequest, BatchSegment } from './workspace-types'

const props = defineProps<{ jobId: string; conversationId: string; record?: BatchRequest | null }>()
const workspace = useWorkspace(), router = useRouter(), error = ref(''), busy = ref('')
const current = computed(() => batchFor(props.record,props.jobId))
let generation = 0
watch(() => [props.jobId,props.conversationId], () => { generation++; error.value = ''; busy.value = '' })
onBeforeUnmount(() => { generation++ })
async function open(segment: BatchSegment, destination: BatchDestination, assetID = '') {
 const batch = current.value, version = generation, parent = props.jobId, owner = props.conversationId
 if (!batch || busy.value) return
 const key = `${segment.ordinal}-${destination}-${assetID}`
 busy.value = key; error.value = ''
 try { const route = await resolveBatchSegment(batch,segment.ordinal,owner,destination,workspace.api,assetID); if (version === generation && props.jobId === parent && props.conversationId === owner) await router.push(route) }
 catch (cause) { if (version === generation) error.value = errorText(cause) }
 finally { if (version === generation) busy.value = '' }
}
const elapsed = (value: number) => `${(value / 1000).toLocaleString('zh-CN',{maximumFractionDigits:3})} 秒`
</script>

<template>
 <section v-if="current" class="workspace-section batch-results" aria-label="完整对象集的分批审计记录" :data-parent-job="current.parent_job_id">
  <header class="section-title"><h2>完整对象集 · 分批审计</h2><span class="tag tag-neutral">{{ batchStatusLabel(current.status) }}</span></header>
  <p class="section-description">本次已接受 {{ current.spec.resources.length }} 个明确对象，共 {{ current.segments.length }} 段。每段分别保留输入、规则与报告；这里没有代表全组的合并审计或合并报告。</p>
  <p class="field-hint">{{ scopeLabel(current.scope.boundary,current.scope.direction) }} · 来源 {{ current.source_ref }} · {{ formatTime(current.accepted_at) }} 接受。当前目标或归属变化不会改写本次集合。</p>
  <div class="batch-summary" aria-label="完整集合覆盖情况"><span><strong>{{ current.covered.length }}</strong> 已有审计证据</span><span><strong>{{ current.failed.length }}</strong> 执行未取得证据</span><span><strong>{{ current.unprocessed.length }}</strong> 尚未取得证据</span></div>
  <p class="notice">已有审计证据只表示该段保存了审计输入与结果，不等于规则通过、模型完整完成或真实可达性已验证。尚未取得证据包括等待、执行中及未处理对象。</p>
  <p v-if="current.error_code" class="notice notice-warning">分批任务状态：{{ current.error_code }}。保留已取得的段结果与尚未处理集合。</p>
  <details class="batch-accepted"><summary>核对本次完整的 {{ current.spec.resources.length }} 个对象</summary><ol><li v-for="resource in current.spec.resources" :key="`${resource.asset_id}-${resource.region}-${resource.resource_id}`">{{ batchResourceLabel(resource) }}</li></ol></details>
  <article v-for="segment in current.segments" :key="segment.ordinal" class="batch-segment" :data-batch-ordinal="segment.ordinal">
   <header><h3>第 {{ segment.ordinal }} 段 · {{ segment.resources.length }} 个对象</h3><span class="tag tag-neutral">{{ batchStatusLabel(segment.status) }}</span></header>
   <p class="batch-evidence">{{ batchEvidenceLabel(segment) }}</p><p v-if="segment.error_code" class="field-hint">{{ segment.error_code }}</p>
   <p v-if="segment.started_at || segment.finished_at" class="field-hint">开始 {{ formatTime(segment.started_at) }}；结束 {{ formatTime(segment.finished_at) }}</p>
   <template v-if="segment.input_manifest"><p class="batch-manifest">{{ sourceLabel(segment.input_manifest.source_kind) }}；资料时点 {{ formatTime(segment.input_manifest.collected_at) }}；评估时点 {{ formatTime(segment.input_manifest.evaluation_at) }}</p><details><summary>本段固定输入版本</summary><dl><dt>快照／业务资料</dt><dd>{{ segment.input_manifest.snapshot_version }} / {{ segment.input_manifest.profile_version }} / {{ segment.input_manifest.intent_version }}</dd><dt>基线／归一化／规则内核</dt><dd>{{ segment.input_manifest.baseline_version }} / {{ segment.input_manifest.normalizer_version }} / {{ segment.input_manifest.kernel_version }}</dd><dt>审计／报告引用</dt><dd>{{ segment.audit_ref || '尚未保存' }} / {{ segment.report_ref || '尚未保存' }}</dd></dl></details></template>
   <p v-else class="field-hint">本段尚无封存审计清单，不能借用其他段的来源时点。</p>
   <ul class="batch-members"><li v-for="resource in segment.resources" :key="`${resource.asset_id}-${resource.region}-${resource.resource_id}`"><span>{{ batchResourceLabel(resource) }}</span><button v-if="segment.audit_ref && resource.asset_id" type="button" class="text-action" :disabled="!!busy" @click="open(segment,'object',resource.asset_id)">查看此对象资料</button></li></ul>
   <div class="batch-actions"><button v-if="segment.child_job_id" type="button" class="text-action" :disabled="!!busy" @click="open(segment,'process')">查看本段过程</button><button v-if="segment.audit_ref" type="button" class="text-action" :disabled="!!busy" @click="open(segment,'findings')">审阅本段规则</button><button v-if="segment.report_ref" type="button" class="text-action" :disabled="!!busy" @click="open(segment,'report')">打开本段固定报告</button><button v-if="segment.report_ref" type="button" class="text-action" :disabled="!!busy" @click="open(segment,'recheck')">从本段报告准备复审</button></div>
   <p class="field-hint">{{ segment.phase_source === 'unknown' ? '本段阶段用时未知，不能把记录中的零当作无耗时。' : segment.phase_source === 'measured' ? `本段执行用时 ${elapsed(segment.execution_ms)}，来自实际阶段计时。` : segment.phase_source === 'persisted_stage_windows' ? `本段执行窗口 ${elapsed(segment.execution_ms)}，由持久事件推导。` : '本段尚无阶段计时记录。' }}</p>
  </article>
  <section class="batch-usage" aria-label="完整分批任务的用量"><h3>本次分批任务的已记录用量</h3><p>物理 HTTP 尝试：云 {{ current.usage.cloud_attempts }} 次；模型 {{ current.usage.model_attempts }} 次。</p><p>已保存逻辑调用：模型 {{ current.usage.recorded_model_calls }} 次；工具 {{ current.usage.recorded_tool_calls }} 次。逻辑调用与物理请求分别计数。</p><p v-if="current.usage.usage_unknown" class="notice notice-warning">部分逻辑调用或阶段用时未知；已记录的零不能解释为全部实际用量为零。{{ current.usage.unknown_reasons.join('、') }}</p><p v-else class="field-hint">本次已保存记录中没有声明逻辑调用或阶段用时缺口；不据此推导供应商账单。</p><p class="field-hint">各段已记录执行用时合计 {{ elapsed(current.usage.execution_ms) }}{{ current.usage.usage_unknown ? '，包含缺口，仅作已记录部分' : '' }}。准备 {{ elapsed(current.usage.phases.collection_ms) }}，规则 {{ elapsed(current.usage.phases.rules_ms) }}，模型 {{ elapsed(current.usage.phases.model_ms) }}，保存 {{ elapsed(current.usage.phases.finalize_ms) }}。</p></section>
  <p class="field-hint">复审请明确选择原段，再核对新的资料与计划；尚不提供全组合并比较。取消操作作用于所属分批主任务。</p><p v-if="busy" class="field-hint" role="status">正在只读核对父任务、子任务与固定证据…</p><p v-if="error" class="error-text" role="alert">{{ error }}</p>
 </section>
</template>

<style scoped>
.batch-results{padding:20px;margin:16px 0;overflow-wrap:anywhere}.batch-results h2{font-size:16px}.batch-results h3{font-size:13px}.batch-summary{display:flex;flex-wrap:wrap;gap:12px;margin:16px 0}.batch-summary span{padding:9px 12px;background:#f1f5f9;border-radius:6px;font-size:12px}.batch-summary strong{font-size:18px;margin-right:5px}.batch-accepted{font-size:12px;margin:14px 0}.batch-accepted ol{padding-left:22px}.batch-segment{padding:16px 0;border-top:1px solid #e2e8f0}.batch-segment>header{display:flex;align-items:center;justify-content:space-between;gap:10px}.batch-segment p,.batch-segment details,.batch-members{font-size:12px}.batch-evidence{font-weight:600;color:#334155}.batch-manifest{color:#475569}.batch-segment dl{display:grid;gap:4px}.batch-segment dt{color:#64748b}.batch-segment dd{margin:0 0 7px}.batch-members{padding-left:18px}.batch-members li{margin:7px 0}.batch-members button{margin-left:10px}.batch-actions{display:flex;flex-wrap:wrap;gap:12px;margin:12px 0}.batch-usage{border-top:1px solid #e2e8f0;padding-top:14px}.batch-usage p{font-size:12px}.batch-results button:disabled{opacity:.5}@media(max-width:600px){.batch-results{padding:14px}.batch-summary{gap:6px}.batch-summary span{flex:1 1 130px}.batch-members button{display:block;margin:5px 0}}
</style>
