<script setup lang="ts">
import { shallowReactive, computed, onBeforeUnmount, ref, watch } from 'vue'
import { onBeforeRouteLeave, onBeforeRouteUpdate, useRoute } from 'vue-router'
import GuidancePanel from '../GuidancePanel.vue'
import UButton from '@nuxt/ui/components/Button.vue'
import { errorText, formatTime, planLabel, severityLabel, useWorkspace, verdictLabel } from '../workspace'
import { isDefiniteRejection, LatestRead, Mutation } from '../workspace-state'
import type { FindingRecord, ReviewAction, ReviewRequest } from '../workspace-types'

const workspace = useWorkspace(), route = useRoute()
const hasMore = ref(false), historyHasMore = ref(false)
const items = ref<FindingRecord[]>([]), active = ref<FindingRecord | null>(null), history = ref<ReviewAction[]>([]), loading = ref(false), busy = ref(false), error = ref(''), saved = ref('')
const verdict = ref(''), severity = ref(''), priority = ref(''), subject = ref(''), comment = ref(''), disposition = ref(''), reason = ref('')
const mutation = shallowReactive(new Mutation<{ finding_id: string; request: ReviewRequest }, ReviewAction>())
const filtered = computed(() => items.value.filter(item => (!verdict.value || item.finding.verdict === verdict.value) && (!severity.value || item.finding.severity === severity.value) && (!priority.value || item.finding.remediation_priority === priority.value) && (!subject.value || item.finding.subject_ref === subject.value)))
const subjects = computed(() => [...new Set(items.value.map(item => item.finding.subject_ref))])
const revision = computed(() => Math.max(history.value.at(-1)?.review_revision ?? 0, active.value?.review_head?.revision ?? active.value?.review_head?.review_revision ?? active.value?.review_revision ?? 0))
const currentPlan = computed(() => history.value.at(-1)?.review_revision === revision.value ? history.value.at(-1)?.plan ?? '' : active.value?.review_head?.plan ?? active.value?.plan ?? '')
const dirty = computed(() => !!comment.value.trim() || !!reason.value.trim() || !!disposition.value || mutation.state === 'unsaved')
let generation = 0, listOffset = 0, approvedRoute = ''
const routeSelectionKey = (query: Record<string, unknown>) => JSON.stringify([query.audit_id ?? '', query.finding_id ?? '', query.finding_key ?? ''])
const detailReads = new LatestRead()
type ReviewDraft = { comment: string; reason: string; disposition: string; pendingBody: { finding_id: string; request: ReviewRequest } | null; key: string | null }
function cacheReview() { if (active.value && dirty.value) workspace.drafts.set(`review:${active.value.id}`, { comment: comment.value, reason: reason.value, disposition: disposition.value, pendingBody: mutation.pendingBody, key: mutation.key }) }
async function load(append = false) {
  if (busy.value) { error.value = '先核对当前审阅操作，原对象与请求已保留。'; return }
  loading.value = true; error.value = ''; const current = ++generation
  const audit = typeof route.query.audit_id === 'string' ? route.query.audit_id : ''
  const requested = typeof route.query.finding_id === 'string' ? route.query.finding_id : ''
  const findingKey = typeof route.query.finding_key === 'string' ? route.query.finding_key : ''
  const selection = routeSelectionKey(route.query), routeApproved = approvedRoute === selection
  approvedRoute = ''
  try {
    const response = await workspace.api.findings(audit, append ? listOffset : 0)
    if (current !== generation || selection !== routeSelectionKey(route.query)) return
    const old = append ? items.value : [], existing = new Set(old.map(item => item.id))
    items.value = [...old, ...response.items.filter(item => !existing.has(item.id))]
    listOffset = (append ? listOffset : 0) + response.items.length
    hasMore.value = response.items.length === 100
    if (append) return
    let target: FindingRecord | undefined
    if (requested) {
      target = await workspace.api.finding(requested)
      if (target.id !== requested || audit && target.audit_id !== audit || findingKey && target.finding_key !== findingKey && target.finding.id !== findingKey) throw new Error('指定发现与当前审计不一致，未自动改选其他发现。')
    } else if (findingKey) {
      if (!audit) throw new Error('按原发现键定位需要明确审计引用，未扫描其他结果。')
      let page = response.items, offset = 0
      for (let count = 0; count < 100; count++) {
        target = page.find(item => item.finding_key === findingKey || item.finding.id === findingKey)
        if (target || page.length < 100) break
        offset += page.length
        page = (await workspace.api.findings(audit, offset)).items
        if (current !== generation || selection !== routeSelectionKey(route.query)) return
      }
      if (!target) throw new Error('指定发现键不在已读取的有限审计结果中，未自动改选第一条。')
      target = await workspace.api.finding(target.id)
      if (target.audit_id !== audit || target.finding_key !== findingKey && target.finding.id !== findingKey) throw new Error('原发现键与当前审计不一致。')
    } else {
      target = items.value.find(item => item.id === active.value?.id) ?? items.value[0]
    }
    if (current !== generation || selection !== routeSelectionKey(route.query)) return
    if (!target) { cacheReview(); active.value = null; return }
    if (!items.value.some(item => item.id === target!.id)) items.value.push(target)
    if (active.value?.id !== target.id || !dirty.value) await select(target, !routeApproved, !!requested || !!findingKey)
  } catch (cause) {
    if (current === generation && selection === routeSelectionKey(route.query)) {
      if (requested || findingKey) { cacheReview(); detailReads.invalidate(); active.value = null; history.value = [] }
      error.value = errorText(cause)
    }
  } finally { if (current === generation) loading.value = false }
}
async function select(item: FindingRecord, ask = true, loaded = false) {
  if (busy.value) return
  if (ask && dirty.value) {
    if (mutation.pendingBody) { if (!window.confirm('上次审阅结果尚未核对，切换后保留原对象草稿与原请求标识。确认切换？')) return; cacheReview() }
    else { if (!window.confirm('当前审阅草稿尚未保存，切换将丢弃这些编辑。已保存意见不受影响。')) return; if (active.value) workspace.drafts.delete(`review:${active.value.id}`) }
  }
  active.value = item; history.value = []; historyHasMore.value = false; comment.value = ''; reason.value = ''; disposition.value = ''; error.value = ''; saved.value = ''; mutation.discard()
  const id = item.id, current = detailReads.begin(), cached = workspace.drafts.get(`review:${id}`) as ReviewDraft | undefined
  if (cached) { comment.value = cached.comment; reason.value = cached.reason; disposition.value = cached.disposition; if (cached.pendingBody && cached.key) mutation.restorePending(cached.pendingBody, cached.key) }
  try { const [detail, actions] = await Promise.all([loaded ? Promise.resolve(item) : workspace.api.finding(id), workspace.api.reviewHistory(id)]); if (!current() || active.value?.id !== id) return; if (detail.id !== id || detail.audit_id !== item.audit_id) throw new Error('发现详情与原对象不一致。'); active.value = detail; history.value = actions; historyHasMore.value = actions.length === 100 }
  catch (cause) { if (current() && active.value?.id === id) error.value = errorText(cause) }
}
async function save() {
  if (!active.value || busy.value) return
  const id = active.value.id
  busy.value = true; error.value = ''; saved.value = ''
  try {
    if (!mutation.pendingBody && !disposition.value && !comment.value.trim()) throw new Error('填写审阅意见，或选择采纳待办／暂缓。')
    if (!mutation.pendingBody && disposition.value === 'deferred' && !reason.value.trim()) throw new Error('暂缓需要说明理由。')
    const body = mutation.pendingBody ?? { finding_id: id, request: { expected_review_revision: revision.value, kind: disposition.value ? 'plan_change' as const : 'comment' as const, ...(disposition.value ? { plan: disposition.value as 'todo' | 'deferred' } : {}), ...(comment.value.trim() ? { comment: comment.value.trim() } : {}), ...(disposition.value === 'deferred' ? { reason: reason.value.trim() } : {}) } }
    const action = await mutation.submit(body, (data, key) => workspace.api.review(data.finding_id, data.request, key))
    workspace.drafts.delete(`review:${body.finding_id}`)
    if (active.value?.id !== id) return
    saved.value = `已保存审阅修订 ${action.review_revision}，时间 ${formatTime(action.saved_at)}。原始规则判定保持不变。`
    comment.value = ''; reason.value = ''; disposition.value = ''; await reloadHistory(false)
  } catch (cause) { if (isDefiniteRejection(cause)) { mutation.discard(); workspace.drafts.delete(`review:${id}`) }; if (active.value?.id === id) error.value = errorText(cause) }
  finally { busy.value = false }
}
async function reloadHistory(append = false) {
  if (!active.value) return
  const id = active.value.id
  try { const actions = await workspace.api.reviewHistory(id, append ? history.value.at(-1)?.review_revision ?? 0 : 0); if (active.value?.id === id) { history.value = append ? [...history.value, ...actions] : actions; historyHasMore.value = actions.length === 100; error.value = '' } }
  catch (cause) { if (active.value?.id === id) error.value = errorText(cause) }
}
function resumeEditing() { if (active.value) workspace.drafts.delete(`review:${active.value.id}`); mutation.discard(); saved.value = ''; error.value = '' }
onBeforeRouteLeave(() => !busy.value && (!dirty.value || window.confirm('当前审阅尚未保存，离开后在本页面会话中保留草稿和原标识。确认离开？')))
onBeforeRouteUpdate(to => {
  if (routeSelectionKey(to.query) === routeSelectionKey(route.query)) return true
  if (busy.value || dirty.value && !window.confirm('当前审阅尚未保存。切换前保留原对象草稿与原请求标识，确认切换？')) return false
  cacheReview(); approvedRoute = routeSelectionKey(to.query); return true
})
onBeforeUnmount(() => { generation++; detailReads.invalidate(); cacheReview() })
watch(() => [route.query.audit_id, route.query.finding_id, route.query.finding_key], () => load(), { immediate: true })
</script>

<template>
  <div class="workspace-page findings-page"><header class="page-title"><div><h1>发现与审阅</h1><p>原始判定、证据与本地处理计划分开。采纳为待办不表示已经修复。</p></div><UButton color="neutral" variant="outline" :disabled="loading || busy" @click="load()">重新读取</UButton></header><GuidancePanel surface="finding" :job-id="workspace.jobs.value.find(job => job.result_ref === active?.audit_id)?.id" :snapshot-id="active?.snapshot_id" :disabled="busy" /><div class="filter-bar"><label>判定<select v-model="verdict"><option value="">全部判定</option><option v-for="value in ['fail','needs_confirmation','insufficient_data','unsupported','pass','not_applicable']" :key="value" :value="value">{{ verdictLabel(value) }}</option></select></label><label>严重程度<select v-model="severity"><option value="">全部等级</option><option v-for="value in ['high','medium','low','information','unassessed']" :key="value" :value="value">{{ severityLabel(value) }}</option></select></label><label>整改顺序<select v-model="priority"><option value="">全部顺序</option><option v-for="value in ['R1','R2','R3','V0']" :key="value" :value="value">{{ value === 'V0' ? 'V0 先核实缺口' : value }}</option></select></label><label>对象<select v-model="subject"><option value="">全部对象</option><option v-for="value in subjects" :key="value" :value="value">{{ value }}</option></select></label></div>
    <div v-if="loading && !items.length" class="workspace-empty"><h2>正在读取已保存发现</h2><p>不会触发新审计或模型请求。</p></div><div v-else-if="!items.length" class="workspace-empty"><h2>尚无已保存发现</h2><p>先预览并运行审计。这里为空不代表当前配置安全。</p><RouterLink to="/audits">预览审计计划</RouterLink></div><div v-else class="findings-layout"><aside class="finding-list" aria-label="发现列表"><button v-for="item in filtered" :key="item.id" type="button" :disabled="busy" :class="{ active: active?.id === item.id }" @click="select(item)"><div><span :class="['tag', `verdict-${item.finding.verdict}`]">{{ verdictLabel(item.finding.verdict) }}</span><span class="finding-rule">{{ item.finding.rule_id }}{{ item.finding.subcheck ? ` / ${item.finding.subcheck}` : '' }}</span></div><strong>{{ item.finding.summary }}</strong><span>{{ item.finding.subject_ref }}{{ item.finding.service_ref ? ` / ${item.finding.service_ref}` : '' }}</span><span class="finding-level">{{ severityLabel(item.finding.severity) }} / {{ item.finding.remediation_priority }} / {{ item.finding.origin === 'rule' ? '确定性规则' : '模型疑点' }}</span></button><UButton v-if="hasMore" color="neutral" variant="outline" :disabled="loading || busy" @click="load(true)">读取更多发现</UButton><p v-if="!filtered.length" class="empty-inline">没有匹配筛选的记录，请调整筛选条件。</p></aside><article v-if="active" class="finding-detail"><section class="workspace-section plain"><div class="section-title"><span :class="['tag', `verdict-${active.finding.verdict}`]">{{ verdictLabel(active.finding.verdict) }}</span><span class="muted">{{ active.finding.origin === 'rule' ? '原始规则结论' : '模型疑点，需人工核实' }}</span></div><h2>{{ active.finding.summary }}</h2><dl class="fact-grid"><div><dt>对象／服务</dt><dd>{{ active.finding.subject_ref }} / {{ active.finding.service_ref || '资产级' }}</dd></div><div><dt>严重程度／整改顺序</dt><dd>{{ severityLabel(active.finding.severity) }} / {{ active.finding.remediation_priority }}</dd></div><div><dt>结论依据</dt><dd>{{ active.finding.assessment_basis === 'imported_assumption' ? '导入声明条件下' : active.finding.assessment_basis === 'synthetic_assumption' ? '仅人工构造案例' : '固定只读观测时点' }}</dd></div><div><dt>当前本地计划</dt><dd>{{ planLabel(currentPlan) }}，修订 {{ revision }}</dd></div></dl><ul v-if="active.finding.assumptions.length" class="notice-list"><li v-for="assumption in active.finding.assumptions" :key="assumption">{{ assumption }}</li></ul><ul v-if="active.finding.limitations.length" class="notice-list warning"><li v-for="limitation in active.finding.limitations" :key="limitation">{{ limitation }}</li></ul></section><section class="workspace-section"><h2>可定位证据</h2><article v-for="evidence in active.evidence ?? []" :key="evidence.id" class="evidence-item"><div><strong>{{ evidence.summary }}</strong><time>{{ formatTime(evidence.observed_at) }}</time></div><dl><dt>固定输入版本</dt><dd>{{ evidence.snapshot_version }}</dd><dt>字段位置</dt><dd>{{ evidence.field }}</dd><dt>对象／策略</dt><dd>{{ evidence.subject_ref }} / {{ evidence.policy_ref || '默认行为或固定资料' }}</dd></dl><pre v-if="'value' in evidence">{{ JSON.stringify((evidence as { value?: unknown }).value, null, 2) }}</pre></article><p v-if="!active.evidence?.length" class="empty-inline">证据引用：{{ active.finding.evidence_refs.join('、') || '尚未取得' }}。未读取到字段值时不补造事实。</p><div v-if="active.finding.differences.length" class="table-scroll"><h3 class="subsection-title">配置差异／维护候选</h3><table class="workspace-table compact-table"><thead><tr><th>类型</th><th>地址范围</th><th>端口</th><th>策略</th></tr></thead><tbody><tr v-for="(difference, i) in active.finding.differences" :key="i"><td>{{ difference.action === 'candidate' ? '精确重复候选' : difference.action }}</td><td>{{ difference.action === 'candidate' ? '不推导流量范围' : `${difference.address_from} ～ ${difference.address_to}` }}</td><td>{{ difference.action === 'candidate' ? '不适用' : `${difference.port_from}–${difference.port_to}` }}</td><td>{{ difference.policy_ref || '默认行为' }}</td></tr></tbody></table></div></section><section class="workspace-section review-section"><div class="section-title"><h2>本地审阅</h2><span class="muted">不会修改原始规则或云端策略</span></div><form @submit.prevent="save"><label>审阅意见<textarea v-model="comment" rows="3" maxlength="4096" :disabled="busy || mutation.state === 'unsaved'" placeholder="记录证据核对、业务影响或需进一步确认的事项" /></label><label>处理计划<select v-model="disposition" :disabled="busy || mutation.state === 'unsaved'"><option value="">只保存意见</option><option value="todo">采纳为待办</option><option value="deferred">暂缓</option></select></label><label v-if="disposition === 'deferred'">暂缓理由<textarea v-model="reason" rows="2" maxlength="4096" :disabled="busy || mutation.state === 'unsaved'" /></label><div class="form-actions"><UButton type="submit" :disabled="busy">{{ busy ? '保存中…' : mutation.state === 'unsaved' ? '以原标识重试' : '保存审阅' }}</UButton><UButton color="neutral" variant="outline" :disabled="busy" @click="reloadHistory(false)">核对已保存记录</UButton><button v-if="mutation.state === 'unsaved'" type="button" class="text-action" @click="resumeEditing">核对后重新编辑</button></div></form><p v-if="saved" class="notice notice-success" role="status">{{ saved }}</p><p v-if="error" class="notice notice-error" role="alert">{{ error }}</p><div class="review-history"><h3>已保存的审阅历史</h3><article v-for="action in history" :key="action.action_id"><div><strong>修订 {{ action.review_revision }} / {{ planLabel(action.plan) }}</strong><time>{{ formatTime(action.saved_at) }}</time></div><p v-if="action.comment">{{ action.comment }}</p><p v-if="action.reason">暂缓理由：{{ action.reason }}</p></article><UButton v-if="historyHasMore" color="neutral" variant="outline" :disabled="busy" @click="reloadHistory(true)">继续读取审阅历史</UButton><p v-if="!history.length" class="empty-inline">尚无已保存意见。编辑中的草稿不计入处理计划。</p></div></section><RouterLink :to="{ path: '/reports', query: { audit_id: active.audit_id } }">从固定结果生成报告</RouterLink></article></div><p v-if="error && !active" class="notice notice-error" role="alert">{{ error }}</p>
  </div>
</template>
