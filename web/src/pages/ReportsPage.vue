<script setup lang="ts">
import { shallowReactive, computed, onBeforeUnmount, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import UButton from '@nuxt/ui/components/Button.vue'
import { errorText, formatTime, jobLabel, useWorkspace, verdictLabel } from '../workspace'
import { isDefiniteRejection, LatestRead, Mutation } from '../workspace-state'
import type { ExecutionPlan, Job, ReportRecord } from '../workspace-types'

const workspace = useWorkspace(), route = useRoute(), router = useRouter()
const selected = ref<ReportRecord | null>(null), content = ref(''), error = ref(''), busy = ref(false), format = ref<'json' | 'markdown'>('markdown'), auditID = ref(''), recheckSnapshot = ref(''), recheckPlan = ref<ExecutionPlan | null>(null)
const reportMutation = shallowReactive(new Mutation<{ audit_id: string; format: 'json' | 'markdown' }, ReportRecord>())
const recheckMutation = shallowReactive(new Mutation<{ plan_id: string }, Job>())
const reads = new LatestRead()
const cachedReport = workspace.drafts.get('report-create') as { body: { audit_id: string; format: 'json' | 'markdown' }; key: string } | undefined
const cachedRecheck = workspace.drafts.get('recheck-start') as { body: { plan_id: string }; key: string } | undefined
if (cachedReport) { reportMutation.restorePending(cachedReport.body, cachedReport.key); auditID.value = cachedReport.body.audit_id; format.value = cachedReport.body.format }
if (cachedRecheck) recheckMutation.restorePending(cachedRecheck.body, cachedRecheck.key)
const completed = computed(() => workspace.jobs.value.filter(job => !!job.result_ref))
const baselineJob = computed(() => selected.value?.job_id || workspace.jobs.value.find(job => job.result_ref === selected.value?.audit_id)?.id || '')
const markdown = computed(() => content.value.split('\n').map((line, index) => ({ index, text: line.replace(/^#{1,3}\s+/, '').replace(/^-\s+/, ''), kind: line.startsWith('# ') ? 'h2' : line.startsWith('## ') ? 'h3' : line.startsWith('- ') ? 'li' : 'p' })))
const publicJSON = computed(() => { if (selected.value?.format !== 'json') return null; try { return JSON.parse(content.value) as { qualification?: string; collected_at?: string; evaluation_at?: string; findings?: { alias: string; rule_id: string; verdict: string; explanation: string; severity: string; remediation_priority: string }[]; coverage?: { complete: boolean } } } catch { return null } })
async function load(id: string) {
  const current = reads.begin()
  error.value = ''; selected.value = null; content.value = ''; recheckPlan.value = null; busy.value = false
  if (!id) return
  busy.value = true
  try {
    const report = await workspace.api.report(id)
    if (!current()) return
    const body = await workspace.api.reportContent(id), text = await body.text()
    if (current()) { selected.value = report; content.value = text }
  } catch (cause) { if (current()) error.value = errorText(cause) }
  finally { if (current()) busy.value = false }
}
async function generate() {
  if (!auditID.value || busy.value) return
  busy.value = true; error.value = ''
  try { const report = await reportMutation.submit(reportMutation.pendingBody ?? { audit_id: auditID.value, format: format.value }, (request, key) => workspace.api.createReport(request.audit_id, request.format, key)); workspace.drafts.delete('report-create'); await workspace.refresh(); await router.push(`/reports/${report.id}`) }
  catch (cause) { if (isDefiniteRejection(cause)) { reportMutation.discard(); workspace.drafts.delete('report-create') }; error.value = errorText(cause) }
  finally { busy.value = false }
}
async function download() {
  if (!selected.value) return
  const report = selected.value
  busy.value = true; error.value = ''
  try { const blob = await workspace.api.reportContent(report.id); const url = URL.createObjectURL(blob); const link = document.createElement('a'); link.href = url; link.download = `audit-report-${report.id}.${report.format === 'json' ? 'json' : 'md'}`; link.click(); URL.revokeObjectURL(url) }
  catch (cause) { error.value = errorText(cause) }
  finally { busy.value = false }
}
async function previewRecheck() {
  if (!baselineJob.value || !recheckSnapshot.value || busy.value || recheckMutation.pendingBody) return
  const baseline = baselineJob.value, snapshotID = recheckSnapshot.value, reportID = selected.value?.id
  busy.value = true; error.value = ''
  try {
    const source = workspace.snapshots.value.find(item => item.id === snapshotID) ?? await workspace.api.snapshot(snapshotID)
    const conversation = await workspace.ensureConversation(source)
    const plan = await workspace.api.recheck(baseline, snapshotID, conversation.id)
    if (selected.value?.id === reportID && recheckSnapshot.value === snapshotID) recheckPlan.value = plan
  } catch (cause) { error.value = errorText(cause) }
  finally { busy.value = false }
}
async function startRecheck() {
  if (busy.value || !recheckMutation.pendingBody && !recheckPlan.value) return
  busy.value = true; error.value = ''
  try { const job = await recheckMutation.submit(recheckMutation.pendingBody ?? { plan_id: recheckPlan.value!.id }, (request, key) => workspace.api.startJob(request.plan_id, key)); workspace.drafts.delete('recheck-start'); workspace.updateJob(job); await router.push(`/audits/${job.id}`) }
  catch (cause) { if (isDefiniteRejection(cause)) { recheckMutation.discard(); recheckPlan.value = null; workspace.drafts.delete('recheck-start') }; error.value = errorText(cause) }
  finally { busy.value = false }
}
onBeforeUnmount(() => {
  reads.invalidate()
  if (reportMutation.pendingBody && reportMutation.key) workspace.drafts.set('report-create', { body: reportMutation.pendingBody, key: reportMutation.key })
  if (recheckMutation.pendingBody && recheckMutation.key) workspace.drafts.set('recheck-start', { body: recheckMutation.pendingBody, key: recheckMutation.key })
})
watch(() => route.params.id, value => { void load(typeof value === 'string' ? value : '') }, { immediate: true })
watch(() => route.query.audit_id, value => { if (typeof value === 'string' && !reportMutation.pendingBody) auditID.value = value }, { immediate: true })
watch(recheckSnapshot, () => { recheckPlan.value = null })
</script>

<template>
  <div class="workspace-page"><header class="page-title"><div><h1>报告与复审</h1><p>报告固定输入、结果和生成时的审阅修订。新意见或新资料不会原地改写旧报告。</p></div><UButton v-if="selected" color="neutral" variant="outline" :disabled="busy" @click="download">下载固定{{ selected.format === 'json' ? ' JSON' : ' Markdown' }}</UButton></header><section v-if="recheckMutation.pendingBody" class="notice notice-warning"><p>原复审计划 {{ recheckMutation.pendingBody.plan_id }} 的启动结果尚未核对。</p><UButton :disabled="busy" @click="startRecheck">以原计划标识核对复审</UButton></section><div class="reports-layout"><aside class="report-list workspace-section"><h2>已保存报告</h2><RouterLink v-for="report in workspace.reports.value" :key="report.id" :to="`/reports/${report.id}`" :class="{ active: selected?.id === report.id }"><strong>{{ report.format === 'json' ? 'JSON 数据报告' : 'Markdown 报告' }}</strong><span>{{ formatTime(report.created_at) }}</span><small>固定 {{ report.reviews?.length ?? 0 }} 项审阅修订</small></RouterLink><p v-if="!workspace.reports.value.length" class="empty-inline">尚未生成报告。先运行审计并审阅已得结果。</p><UButton v-if="workspace.listHasMore.value.reports" color="neutral" variant="outline" :disabled="workspace.listsLoading.value" @click="workspace.loadMore('reports')">读取更多报告</UButton><form class="generate-report" @submit.prevent="generate"><h3>生成新的固定版本</h3><label>已保存结果<select v-model="auditID" :disabled="busy || reportMutation.state === 'unsaved'"><option value="">选择审计结果</option><option v-for="job in completed" :key="job.id" :value="job.result_ref">{{ formatTime(job.created_at) }} / {{ jobLabel(job.status) }}</option><option v-if="auditID && !completed.some(job => job.result_ref === auditID)" :value="auditID">当前发现所属的固定结果</option></select></label><label>格式<select v-model="format" :disabled="busy || reportMutation.state === 'unsaved'"><option value="markdown">Markdown 可读报告</option><option value="json">JSON 结构化报告</option></select></label><p class="field-hint">公开投影使用报告内别名，默认不导出意见自由文本、原始地址或证据路径。</p><UButton type="submit" :disabled="busy || !auditID">{{ busy ? '生成中…' : reportMutation.state === 'unsaved' ? '以原标识重试' : '生成固定报告' }}</UButton></form></aside><div class="report-detail"><template v-if="selected"><div class="report-meta"><span class="tag tag-neutral">固定版本</span><span>{{ formatTime(selected.created_at) }}</span><span>{{ selected.format === 'json' ? 'JSON' : 'Markdown' }}</span></div><article class="report-paper"><template v-if="publicJSON"><h2>云防火墙策略审计报告</h2><p class="notice">{{ publicJSON.qualification }}</p><dl class="fact-grid"><div><dt>评估时点</dt><dd>{{ formatTime(publicJSON.evaluation_at) }}</dd></div><div><dt>构造／采集时点</dt><dd>{{ formatTime(publicJSON.collected_at) }}</dd></div></dl><p class="section-description">{{ publicJSON.coverage?.complete ? '支持的查询范围已完整计算。' : '存在未穷尽或信息不足范围，不能把未发现当通过。' }}</p><div class="table-scroll"><table class="workspace-table"><thead><tr><th>检查</th><th>判定</th><th>公开解释</th><th>整改顺序</th></tr></thead><tbody><tr v-for="finding in publicJSON.findings ?? []" :key="finding.alias"><td>{{ finding.rule_id }}</td><td>{{ verdictLabel(finding.verdict) }}</td><td class="wrap-cell">{{ finding.explanation }}</td><td>{{ finding.remediation_priority }}</td></tr></tbody></table></div><details class="json-detail"><summary>查看完整固定 JSON</summary><pre>{{ content }}</pre></details></template><template v-else><template v-for="line in markdown" :key="line.index"><h2 v-if="line.kind === 'h2'">{{ line.text }}</h2><h3 v-else-if="line.kind === 'h3'">{{ line.text }}</h3><p v-else-if="line.kind === 'li'" class="report-bullet">• {{ line.text }}</p><p v-else-if="line.text">{{ line.text }}</p></template></template></article><section class="workspace-section recheck-section"><h2>用新资料预览复审</h2><p class="section-description">沿用原评估条件，新建独立审计；缺对象、范围变化或覆盖不足时会明确不可比较，不记作已修复。</p><label>新的固定资料<select v-model="recheckSnapshot" :disabled="busy || !!recheckMutation.pendingBody"><option value="">选择重新采集或补充后的资料</option><option v-for="item in workspace.snapshots.value" :key="item.id" :value="item.id">{{ formatTime(item.input.collected_at) }} / {{ item.source_kind }} / {{ item.id }}</option></select></label><p v-if="!baselineJob" class="field-hint">此报告尚未关联可访问任务，不能编造复审引用。可从审计历史选择对应任务。</p><UButton color="neutral" variant="outline" :disabled="busy || !!recheckMutation.pendingBody || !baselineJob || !recheckSnapshot" @click="previewRecheck">预览复审计划</UButton><div v-if="recheckPlan && !recheckMutation.pendingBody" class="notice"><p>已生成独立计划，有效至 {{ formatTime(recheckPlan.expires_at) }}；旧报告保持不变。</p><UButton :disabled="busy || Date.parse(recheckPlan.expires_at) <= Date.now()" @click="startRecheck">接受计划并启动复审</UButton></div></section></template><div v-else class="workspace-empty"><h2>选择一份固定报告</h2><p>也可以从已保存结果生成新版本。生成与下载不调用模型或云端。</p></div></div></div><p v-if="error" class="notice notice-error" role="alert">{{ error }}</p></div>
</template>
