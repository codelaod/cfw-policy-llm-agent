<script setup lang="ts">
import { shallowReactive, computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { onBeforeRouteLeave, onBeforeRouteUpdate, useRoute, useRouter } from 'vue-router'
import UButton from '@nuxt/ui/components/Button.vue'
import { inventoryFocus } from '../agent-view'
import { errorText, formatTime, sourceLabel, useWorkspace } from '../workspace'
import { cloneData, formatPorts, isDefiniteRejection, LatestRead, missingDependencies, Mutation, parseAddresses, parsePorts } from '../workspace-state'
import type { Access, AssetChange, AssetView, ConfirmationPreview, ConfirmationResult, ProfileDraft, ServiceAccessChange } from '../workspace-types'

const workspace = useWorkspace(), route = useRoute(), router = useRouter()
const activeAsset = ref(''), selected = ref<string[]>([]), changes = ref<AssetChange[]>([]), draft = ref<ProfileDraft | null>(null), preview = ref<ConfirmationPreview | null>(null)
const dirty = ref(false), busy = ref(false), error = ref(''), message = ref(''), sections = ref<string[]>([])
const assetViews = ref<AssetView[]>([]), contextError = ref('')
const routeLoading = ref(false), focusError = ref(''), focusService = ref(''), missingService = ref(false)
const routeReads = new LatestRead(), assetViewReads = new LatestRead()
let routeSnapshot = '', assetViewsSource = ''
const routeScopeMatches = computed(() => typeof route.query.snapshot_id !== 'string' || route.query.snapshot_id === workspace.snapshotID.value)
const confirmMutation = shallowReactive(new Mutation<{ preview_id: string; revision: number; sections: string[] }, ConfirmationResult>())
const rejectMutation = shallowReactive(new Mutation<{ revision: number }, ConfirmationResult>())
const reads = new LatestRead()
const invalidFields = new Map<object, string>()
let restoringSnapshot = ''
const editable = computed(() => !!draft.value && ['draft', 'previewed'].includes(draft.value.status))
const unresolved = computed(() => confirmMutation.state === 'unsaved' || rejectMutation.state === 'unsaved')
const snapshot = computed(() => workspace.currentSnapshot.value)
const asset = computed(() => snapshot.value?.input.assets.find(item => item.id === activeAsset.value))
const change = computed(() => changes.value.find(item => item.asset_id === activeAsset.value))
const currentView = computed(() => assetViews.value.find(item => item.asset.id === activeAsset.value))
const missingSections = computed(() => missingDependencies(sections.value, preview.value?.section_dependencies))
const exceptionChecks = computed(() => snapshot.value?.input.scope.boundary === 'nat' ? [{ value: 'NAT-02:OUT-01', label: 'NAT 出向允许上界' }, { value: 'NAT-02:OUT-02', label: 'NAT 必要外联' }, { value: 'NAT-02:CHK-05', label: 'NAT 当前层覆盖' }] : snapshot.value?.input.scope.direction === 'out' ? [{ value: 'OUT-01', label: '出向允许上界' }, { value: 'OUT-02', label: '必要外联' }, { value: 'CHK-05', label: '当前层覆盖' }] : ['CHK-01','CHK-02','CHK-03','CHK-05','CHK-06','CHK-07','CHK-08'].map(value => ({ value, label: value })))
const roleLabels: Record<string, string> = { management: '管理入口', database: '数据库', web: 'Web', application: '应用服务', cache: '缓存', background: '后台任务', unknown: '未知' }
function markChanged() { dirty.value = true; preview.value = null; sections.value = []; message.value = ''; error.value = '' }
function hydrate(values: AssetChange[]) {
  const copied = cloneData(values)
  for (const item of copied) {
    if (!item.access) continue
    item.access.exceptions ??= []
    for (const role of item.roles) if (role.service_id && !item.access.services.some(service => service.service_id === role.service_id)) item.access.services.push({ service_id: role.service_id, public_access: 'unknown', allowed_complete: false, deny_all: false, allowed: [], required_confirmed: false, required: [] })
  }
  return copied
}
async function loadAssetViews() {
  const id = snapshot.value?.id, current = assetViewReads.begin()
  if (id !== assetViewsSource) { assetViews.value = []; contextError.value = ''; assetViewsSource = id ?? '' }
  if (!id) { assetViews.value = []; return }
  try { const value = await workspace.api.assets(id); if (current() && snapshot.value?.id === id) { assetViews.value = value.items.map(item => ({ ...item, local_confirmed_sections: item.local_confirmed_sections ?? [] })); contextError.value = '' } }
  catch (cause) { if (current() && snapshot.value?.id === id) contextError.value = errorText(cause) }
}
function applyRouteFocus() {
  const requested = typeof route.params.id === 'string' ? route.params.id : ''
  const known = assetViews.value.find(item => item.asset.id === requested)
  const focused = inventoryFocus(snapshot.value?.input.assets ?? [], requested, route.query.service, known?.asset.services ?? [])
  activeAsset.value = focused.assetID
  if (!draft.value) selected.value = focused.assetID ? [focused.assetID] : []
  focusService.value = focused.serviceID; missingService.value = focused.missingService
  focusError.value = focused.missingAsset ? '指定资产不在这份固定资料中，未自动切换到其他对象。' : ''
}
async function followFixedRoute() {
  if (typeof route.query.draft === 'string') return
  const id = typeof route.query.snapshot_id === 'string' ? route.query.snapshot_id : ''
  const current = routeReads.begin()
  if (!id) { routeSnapshot = ''; routeLoading.value = false; applyRouteFocus(); return }
  routeLoading.value = true; focusError.value = ''; routeSnapshot = id
  try {
    await workspace.selectSnapshot(id, false)
    if (!current() || workspace.snapshotID.value !== id || route.query.snapshot_id !== id) return
    reset(); await loadAssetViews()
    if (current()) applyRouteFocus()
  } catch (cause) { if (current()) focusError.value = errorText(cause) }
  finally { if (current()) { routeSnapshot = ''; routeLoading.value = false } }
}
function chooseAsset(id: string) { activeAsset.value = id; focusService.value = ''; missingService.value = false; focusError.value = '' }
function makeChanges(ids: string[]): AssetChange[] {
  return (snapshot.value?.input.assets ?? []).filter(item => ids.includes(item.id)).map(item => ({ asset_id: item.id,
    roles: item.services.map(service => ({ service_id: service.id, role: service.role, protocol: service.protocol, ports: cloneData(service.ports) })),
    access: { allowed_complete: item.allowed_complete, deny_all: item.deny_all, allowed: cloneData(item.allowed ?? []), services: item.services.map(service => ({ service_id: service.id, public_access: service.public_access, allowed_complete: service.allowed_complete, deny_all: service.deny_all, allowed: cloneData(service.allowed ?? []), required_confirmed: service.required_confirmed, required: cloneData(service.required ?? []) })) },
  }))
}
function reset() { activeAsset.value = snapshot.value?.input.assets[0]?.id ?? ''; selected.value = activeAsset.value ? [activeAsset.value] : []; draft.value = null; preview.value = null; changes.value = []; dirty.value = false; sections.value = []; error.value = ''; message.value = ''; confirmMutation.discard() }
async function restore() {
  if (!workspace.unlocked.value) return
  const id = typeof route.query.draft === 'string' ? route.query.draft : ''
  if (!id) return
  const current = reads.begin()
  busy.value = true; error.value = ''
  try {
    const saved = await workspace.api.draft(id)
    if (!current() || route.query.draft !== id) return
    restoringSnapshot = saved.snapshot_id
    await workspace.selectSnapshot(saved.snapshot_id, false)
    if (!current() || workspace.snapshotID.value !== saved.snapshot_id || route.query.draft !== id) return
    draft.value = saved; selected.value = saved.changes.map(c => c.asset_id); changes.value = hydrate(saved.changes); activeAsset.value = selected.value[0] ?? ''; dirty.value = false
    confirmMutation.discard(); rejectMutation.discard()
    const cached = workspace.drafts.get(id) as { changes?: AssetChange[]; pendingBody?: { preview_id: string; revision: number; sections: string[] }; key?: string; rejection?: { revision: number }; rejectionKey?: string } | undefined
    if (cached?.changes) { changes.value = hydrate(cached.changes); dirty.value = true }
    if (cached?.pendingBody && cached.key) { confirmMutation.restorePending(cached.pendingBody, cached.key); dirty.value = false }
    if (cached?.rejection && cached.rejectionKey) { rejectMutation.restorePending(cached.rejection, cached.rejectionKey); dirty.value = false }
    await loadAssetViews()
  } catch (cause) { if (current()) error.value = errorText(cause) }
  finally { if (current()) { restoringSnapshot = ''; busy.value = false } }
}
async function begin() {
  if (!snapshot.value || !selected.value.length || busy.value || unresolved.value) return
  if (dirty.value && !canLeave()) return
  busy.value = true; error.value = ''; message.value = ''
  try { const saved = await workspace.api.createDraft(snapshot.value.id, selected.value); draft.value = saved; changes.value = hydrate(saved.changes?.length ? saved.changes : makeChanges(selected.value)); preview.value = null; sections.value = []; dirty.value = false; await router.replace({ path: '/inventory', query: { draft: saved.id } }) }
  catch (cause) { error.value = errorText(cause) }
  finally { busy.value = false }
}
function setPorts(event: Event, target: { ports: { from: number; to: number }[] }) {
  markChanged(); const input = event.target as HTMLInputElement
  try { target.ports = parsePorts(input.value); invalidFields.delete(target); input.setCustomValidity('') }
  catch (cause) { error.value = errorText(cause); invalidFields.set(target, error.value); input.setCustomValidity(error.value) }
}
function setAddresses(event: Event, target: Access) {
  markChanged(); const input = event.target as HTMLTextAreaElement
  try { target.addresses = parseAddresses(input.value); invalidFields.delete(target); input.setCustomValidity('') }
  catch (cause) { error.value = errorText(cause); invalidFields.set(target, error.value); input.setCustomValidity(error.value) }
}
function editorError(): string {
  const targets = new Set<object>()
  function visit(value: unknown) { if (value && typeof value === 'object') { targets.add(value); for (const item of Object.values(value)) visit(item) } }
  visit(changes.value)
  for (const [target, message] of invalidFields) { if (targets.has(target)) return message; invalidFields.delete(target) }
  return ''
}
function addAccess(list: Access[]) { list.push({ scope_ref: snapshot.value!.input.scope.ref, protocol: 'tcp', addresses: [], ports: [], applicability: 'unknown' }); markChanged() }
function addService() { if (!change.value) return; change.value.roles.push({ service_id: '', role: 'unknown', protocol: 'unknown', ports: [] }); markChanged() }
function addException() { if (!change.value?.access || !snapshot.value) return; change.value.access.exceptions ??= []; change.value.access.exceptions.push({ check_id: '', subcheck: '', subject_ref: change.value.asset_id, service_ref: '', scope_ref: snapshot.value.input.scope.ref, expires_at: '', reason: '' }); markChanged() }
function exceptionCheck(event: Event, exception: NonNullable<NonNullable<AssetChange['access']>['exceptions']>[number]) { const [check = '', subcheck = ''] = (event.target as HTMLSelectElement).value.split(':'); exception.check_id = check; exception.subcheck = subcheck; markChanged() }
function serviceAccess(id: string): ServiceAccessChange | undefined { return change.value?.access?.services.find(item => item.service_id === id) }
async function save(asPreview: boolean) {
  if (!draft.value || !editable.value || busy.value || unresolved.value) return
  const validationError = editorError()
  if (validationError) { error.value = validationError; return }
  busy.value = true; message.value = ''; error.value = ''
  try {
    const saved = await workspace.api.updateDraft(draft.value.id, draft.value.revision, changes.value)
    draft.value = saved; changes.value = hydrate(saved.changes); dirty.value = false; workspace.drafts.delete(saved.id)
    if (asPreview) { preview.value = await workspace.api.previewDraft(saved.id); sections.value = []; message.value = '差异已生成，请明确选择确认角色或访问要求。' }
    else message.value = '草稿已保存，尚未确认或改变当前业务版本。'
  } catch (cause) { error.value = errorText(cause) }
  finally { busy.value = false }
}
async function confirm() {
  if (!draft.value || busy.value || !confirmMutation.pendingBody && (!preview.value || !sections.value.length || dirty.value || missingSections.value.length)) return
  busy.value = true; error.value = ''; message.value = ''
  try {
    const body = confirmMutation.pendingBody ?? { preview_id: preview.value!.preview_id, revision: draft.value.revision, sections: [...sections.value] }
    const saved = await confirmMutation.submit(body, (request, key) => workspace.api.confirmDraft(draft.value!.id, request.preview_id, request.revision, request.sections, key))
    message.value = `所选部分已保存，草稿修订 ${saved.revision}。未选择的部分没有被确认。`
    draft.value = await workspace.api.draft(saved.draft_id); preview.value = null; sections.value = []; dirty.value = false
    workspace.drafts.delete(saved.draft_id); await workspace.refresh(); await loadAssetViews()
  } catch (cause) { if (isDefiniteRejection(cause)) { confirmMutation.discard(); preview.value = null; sections.value = []; if (draft.value) workspace.drafts.delete(draft.value.id) }; error.value = errorText(cause) }
  finally { busy.value = false }
}
async function reject() {
  if (!draft.value || busy.value || confirmMutation.state === 'unsaved') return
  busy.value = true; error.value = ''
  try {
    const saved = await rejectMutation.submit(rejectMutation.pendingBody ?? { revision: draft.value.revision }, (request, key) => workspace.api.rejectDraft(draft.value!.id, request.revision, key))
    message.value = '候选已拒绝，原有确认版本保留。'; draft.value = await workspace.api.draft(saved.draft_id); preview.value = null; dirty.value = false; workspace.drafts.delete(saved.draft_id)
  } catch (cause) { if (isDefiniteRejection(cause)) rejectMutation.discard(); error.value = errorText(cause) }
  finally { busy.value = false }
}
function cacheEditor() {
  if (draft.value && (dirty.value || unresolved.value)) workspace.drafts.set(draft.value.id, { changes: cloneData(changes.value), pendingBody: confirmMutation.pendingBody, key: confirmMutation.key, rejection: rejectMutation.pendingBody, rejectionKey: rejectMutation.key })
}
function canLeave() {
  if (busy.value) return false
  if (unresolved.value) { const allow = window.confirm('上次动作结果尚未核对。离开后可重开原草稿，以原标识核对；不会自动发送新动作。'); if (allow) cacheEditor(); return allow }
  if (!dirty.value) return true
  const allow = window.confirm('当前编辑尚未保存，离开将丢弃这些编辑。已保存的草稿仍可重开。')
  if (allow) { if (draft.value) workspace.drafts.delete(draft.value.id); dirty.value = false }
  return allow
}
onBeforeRouteLeave(canLeave)
onBeforeRouteUpdate((to, from) => to.query.snapshot_id === from.query.snapshot_id && to.query.draft === from.query.draft || typeof to.query.draft === 'string' && to.query.draft === draft.value?.id || canLeave())
let lastSnapshot: string | undefined
watch(() => workspace.snapshotID.value, id => {
  if (id === lastSnapshot) return
  if (lastSnapshot === undefined) { lastSnapshot = id; void loadAssetViews(); return }
  lastSnapshot = id
  if (restoringSnapshot === id || routeSnapshot === id) { void loadAssetViews(); return }
  reads.invalidate(); routeReads.invalidate(); routeLoading.value = false; busy.value = false
  const old = draft.value; cacheEditor(); reset(); rejectMutation.discard()
  if (old && old.snapshot_id !== id && route.query.draft || typeof route.query.snapshot_id === 'string' && route.query.snapshot_id !== id) void router.replace('/inventory')
  void loadAssetViews()
}, { immediate: true })
watch(() => route.query.draft, id => { if (typeof id === 'string' && id !== draft.value?.id) void restore() })
watch(() => route.params.id, id => { if (typeof id === 'string' && snapshot.value?.input.assets.some(a => a.id === id)) activeAsset.value = id }, { immediate: true })
watch(() => [route.query.snapshot_id, route.query.service, route.params.id], () => { if (!route.query.draft) void followFixedRoute() })
onMounted(() => { workspace.setSourceGuard(() => !routeLoading.value && canLeave()); if (route.query.draft) void restore(); else void followFixedRoute() })
onBeforeUnmount(() => { assetViewReads.invalidate(); routeReads.invalidate(); reads.invalidate(); cacheEditor(); workspace.setSourceGuard(null) })
</script>

<template>
  <div class="workspace-page">
    <header class="page-title"><div><h1>资产与业务确认</h1><p>资料事实、候选角色和明确访问要求分开保留；角色不会自动成为授权。</p></div><RouterLink v-if="snapshot" to="/audits">预览审计计划</RouterLink></header>
    <div v-if="routeLoading || !routeScopeMatches" class="workspace-empty"><h2>{{ routeLoading ? '正在读取指定固定资料' : '指定固定资料尚未读取' }}</h2><p>{{ focusError || '按任务绑定定位资产，不使用其他资料版本替代。' }}</p></div>
    <div v-else-if="!snapshot" class="workspace-empty"><h2>先选择固定资料</h2><p>载入人工案例或导入离线输入，再确认所审资产。</p><RouterLink to="/overview">准备资料</RouterLink></div>
    <template v-else><p v-if="focusError" class="notice notice-warning">{{ focusError }}</p><p v-if="focusService" class="notice">定位服务：{{ focusService }}。{{ missingService ? '该服务尚未在当前人工版本中定位；请先核对版本，不会自动新增或确认。' : '角色候选仍需在现有表单中人工核对与预览确认。' }}</p><p class="notice">{{ sourceLabel(snapshot.source_kind) }}资料，构造／采集于 {{ formatTime(snapshot.input.collected_at) }}。标签、角色或端口声明不证明运行时监听或真实可达。</p><div v-if="!snapshot.input.assets.length" class="workspace-empty"><h2>此资料没有资产级对象</h2><p>网关策略集可以独立审计。资产角色和访问要求需有明确资产资料后再确认。</p><RouterLink to="/audits">预览网关策略审计</RouterLink></div><div v-else class="inventory-layout"><aside class="asset-list" aria-label="资产选择"><div class="section-title"><h2>本次资料中的资产</h2><span class="muted">{{ selected.length }} 个待编辑</span></div><label v-for="item in snapshot.input.assets" :key="item.id" :class="{ active: activeAsset === item.id }"><input v-model="selected" type="checkbox" :value="item.id" :disabled="editable" /><button type="button" @click="chooseAsset(item.id)"><strong>{{ item.id }}</strong><span>{{ item.address || '地址未知' }}</span></button></label><UButton color="neutral" variant="outline" :disabled="busy || !selected.length || unresolved" @click="begin">{{ draft ? '从当前版本新建草稿' : '编辑所选资产' }}</UButton><p class="field-hint">可在同一草稿分别编辑多个资产；确认时任一版本冲突都不会部分写入。</p></aside><div class="asset-detail"><section v-if="asset" class="workspace-section plain"><div class="section-title"><h2>{{ asset.id }}</h2><span class="tag tag-neutral">{{ sourceLabel(snapshot.source_kind) }}</span></div><dl class="fact-grid"><div><dt>配置地址</dt><dd>{{ asset.address || '未知' }}</dd></div><div><dt>端点关联</dt><dd>{{ asset.address_observed ? '资料有明确地址声明' : '尚未核实' }}</dd></div><div><dt>当前防火墙层</dt><dd>{{ snapshot.input.protection.state === 'on' ? '声明已开启' : snapshot.input.protection.state === 'off' ? '声明未开启' : '状态未知' }}</dd></div><div v-if="snapshot.input.scope.boundary === 'nat'"><dt>NAT 观察位置</dt><dd>{{ asset.association.observation_point || '未知' }} / {{ asset.association.status || '未知' }}</dd></div></dl><h3 class="subsection-title">原始资料中的服务声明</h3><div class="table-scroll"><table class="workspace-table compact-table"><thead><tr><th>服务</th><th>角色声明</th><th>协议／端口</th><th>状态</th></tr></thead><tbody><tr v-for="service in asset.services" :key="service.id"><td>{{ service.id }}</td><td>{{ roleLabels[service.role] ?? service.role }}</td><td>{{ service.protocol }} / {{ formatPorts(service.ports) || '未知' }}</td><td>{{ service.role_status === 'confirmed' ? '资料声明已确认' : service.role_status === 'candidate' ? '候选' : '待核实' }}</td></tr></tbody></table></div></section>
        <p v-if="contextError" class="notice notice-warning" role="status">当前本地确认版本暂未读到：{{ contextError }}。原始资料声明不自动作为新的本地确认。</p>
        <section v-if="currentView" class="workspace-section"><div class="section-title"><h2>本地已确认业务</h2><span class="muted">聚合修订 {{ currentView.head.revision }}</span></div><p v-if="!currentView.local_confirmed_sections.length" class="empty-inline">尚无本地人工确认版本。以上服务信息只按原始资料来源与声明解释。</p><template v-else><p class="field-hint">已确认部分：{{ currentView.local_confirmed_sections.map(value => value === 'roles' ? '角色' : '访问要求').join('、') }}。未确认部分保留其原来源保证。</p><div class="table-scroll"><table class="workspace-table compact-table"><thead><tr><th>服务</th><th>有效角色</th><th>协议／端口</th><th>访问要求</th></tr></thead><tbody><tr v-for="service in currentView.asset.services" :key="service.id"><td>{{ service.id }}</td><td>{{ roleLabels[service.role] ?? service.role }}</td><td>{{ service.protocol }} / {{ formatPorts(service.ports) || '未知' }}</td><td>{{ service.public_access === 'not_required' ? '明确无需公网' : service.public_access === 'required' ? '声明有公网需求' : '待确认' }}</td></tr></tbody></table></div></template></section>
        <section v-if="draft && change" class="draft-editor workspace-section" aria-labelledby="draft-title"><div class="section-title"><div><h2 id="draft-title">人工确认草稿</h2><p class="field-hint">修订 {{ draft.revision }} / {{ dirty ? '有未保存编辑' : editable ? '草稿已保存，尚未确认' : '此草稿已结束' }}</p></div><span class="tag tag-neutral">本地资料</span></div><fieldset :disabled="busy || !editable || unresolved"><legend>服务角色</legend><p class="field-hint">只确认角色不会同时确认来源、目标或必要访问。</p><div v-for="(role, index) in change.roles" :key="`${role.service_id}-${index}`" class="service-role-row"><label>服务<span class="field-value">{{ role.service_id || '保存后生成标识' }}</span></label><label>角色<select v-model="role.role" @change="markChanged"><option v-for="(label, value) in roleLabels" :key="value" :value="value">{{ label }}</option></select></label><label>协议<select v-model="role.protocol" @change="markChanged"><option value="unknown">未知</option><option value="tcp">TCP</option><option value="udp">UDP</option></select></label><label>业务服务端口<input :value="formatPorts(role.ports)" placeholder="未知可留空" @input="setPorts($event, role)" /></label></div><UButton color="neutral" variant="ghost" size="sm" @click="addService">添加待确认服务</UButton></fieldset>
          <fieldset v-if="change.access" :disabled="busy || !editable || unresolved"><legend>资产完整允许上界</legend><p class="field-hint">这与单个服务的允许来源分开；只填写部分服务不代表完整上界。</p><label class="check-line"><input v-model="change.access.allowed_complete" type="checkbox" @change="markChanged" />我确认这是本资产在当前分区的完整允许集合</label><label class="check-line"><input v-model="change.access.deny_all" type="checkbox" @change="markChanged" />我明确禁止本资产在当前分区的新发起访问</label><div v-for="(entry, i) in change.access.allowed" :key="i" class="access-entry"><label>{{ snapshot.input.scope.direction === 'in' ? '允许来源' : '允许外部目标' }}<textarea :value="entry.addresses.join('\n')" rows="2" placeholder="填写明确 IPv4/CIDR，不从现有策略复制授权" @input="setAddresses($event, entry)" /></label><label>协议<select v-model="entry.protocol" @change="markChanged"><option value="tcp">TCP</option><option value="udp">UDP</option><option value="any">ANY（仅支持子域）</option></select></label><label>目标端口<input :value="formatPorts(entry.ports)" @input="setPorts($event, entry)" /></label><label>边界归属<select v-model="entry.applicability" @change="markChanged"><option value="unknown">尚未核实</option><option value="applicable">属于所审边界</option><option value="not_applicable">不属于所审边界</option></select></label><button class="text-action danger" type="button" @click="change.access.allowed.splice(i, 1); markChanged()">移除此声明</button></div><UButton color="neutral" variant="ghost" size="sm" @click="addAccess(change.access.allowed)">增加允许声明</UButton></fieldset>
          <fieldset v-for="role in change.roles.filter(r => !!r.service_id)" :key="`access-${role.service_id}`" :disabled="busy || !editable || unresolved"><legend>{{ role.service_id }} 的访问要求</legend><template v-if="serviceAccess(role.service_id)"><label>公网访问要求<select v-model="serviceAccess(role.service_id)!.public_access" @change="markChanged"><option value="unknown">尚未声明</option><option value="required">有明确公网访问需求</option><option value="not_required">明确不需要公网访问</option></select></label><label class="check-line"><input v-model="serviceAccess(role.service_id)!.allowed_complete" type="checkbox" @change="markChanged" />该服务允许集合已声明完整</label><label class="check-line"><input v-model="serviceAccess(role.service_id)!.deny_all" type="checkbox" @change="markChanged" />明确禁止该服务在本分区的新发起访问</label><h3 class="subsection-title">{{ snapshot.input.scope.direction === 'in' ? '允许来源与服务端口' : '允许的外部依赖' }}</h3><div v-for="(entry, i) in serviceAccess(role.service_id)!.allowed" :key="`allowed-${i}`" class="access-entry"><label>明确地址／网段<textarea :value="entry.addresses.join('\n')" rows="2" @input="setAddresses($event, entry)" /></label><label>协议<select v-model="entry.protocol" @change="markChanged"><option value="tcp">TCP</option><option value="udp">UDP</option><option value="any">ANY</option></select></label><label>目标端口<input :value="formatPorts(entry.ports)" @input="setPorts($event, entry)" /></label><label>边界归属<select v-model="entry.applicability" @change="markChanged"><option value="unknown">尚未核实</option><option value="applicable">属于所审边界</option><option value="not_applicable">不属于所审边界</option></select></label><button type="button" class="text-action danger" @click="serviceAccess(role.service_id)!.allowed.splice(i, 1); markChanged()">移除此声明</button></div><UButton color="neutral" variant="ghost" size="sm" @click="addAccess(serviceAccess(role.service_id)!.allowed)">增加服务允许声明</UButton><h3 class="subsection-title">明确必要的访问</h3><label class="check-line"><input v-model="serviceAccess(role.service_id)!.required_confirmed" type="checkbox" @change="markChanged" />我确认下列必要访问声明</label><div v-for="(entry, i) in serviceAccess(role.service_id)!.required" :key="`required-${i}`" class="access-entry"><label>必要来源／目标<textarea :value="entry.addresses.join('\n')" rows="2" @input="setAddresses($event, entry)" /></label><label>协议<select v-model="entry.protocol" @change="markChanged"><option value="tcp">TCP</option><option value="udp">UDP</option><option value="any">ANY</option></select></label><label>目标端口<input :value="formatPorts(entry.ports)" @input="setPorts($event, entry)" /></label><label>边界归属<select v-model="entry.applicability" @change="markChanged"><option value="unknown">尚未核实</option><option value="applicable">属于所审边界</option><option value="not_applicable">不属于所审边界</option></select></label><button type="button" class="text-action danger" @click="serviceAccess(role.service_id)!.required.splice(i, 1); markChanged()">移除此声明</button></div><UButton color="neutral" variant="ghost" size="sm" @click="addAccess(serviceAccess(role.service_id)!.required)">增加必要访问</UButton></template></fieldset>
          <fieldset v-if="change.access" :disabled="busy || !editable || unresolved"><legend>本地业务例外</legend><p class="field-hint">例外需要明确范围、理由和到期时点，仅随访问要求确认生效。采纳待办／暂缓不属于例外，例外也不删除原始规则事实。</p><div v-for="(exception, index) in change.access.exceptions ?? []" :key="index" class="access-entry"><label>适用检查<select :value="exception.check_id + (exception.subcheck ? ':' + exception.subcheck : '')" @change="exceptionCheck($event, exception)"><option value="">明确选择检查</option><option v-for="check in exceptionChecks" :key="check.value" :value="check.value">{{ check.label }}</option></select></label><label>服务范围<select v-model="exception.service_ref" @change="markChanged"><option value="">当前资产整体（适用资产级判据）</option><option v-for="service in change.roles.filter(r => !!r.service_id)" :key="service.service_id" :value="service.service_id">{{ service.service_id }}</option></select></label><label>到期时点（含时区）<input v-model="exception.expires_at" placeholder="2026-11-01T00:00:00+08:00" @input="markChanged" /></label><label>明确理由<textarea v-model="exception.reason" rows="2" maxlength="4096" @input="markChanged" /></label><button type="button" class="text-action danger" @click="change.access!.exceptions!.splice(index, 1); markChanged()">移除此例外草稿</button></div><UButton color="neutral" variant="ghost" size="sm" @click="addException">增加业务例外草稿</UButton></fieldset>
          <div v-if="editable" class="form-actions"><UButton color="neutral" variant="outline" :disabled="busy || unresolved" @click="save(false)">保存草稿</UButton><UButton :disabled="busy || unresolved" @click="save(true)">预览确认差异</UButton><UButton color="neutral" variant="ghost" :disabled="busy || unresolved" @click="reject">拒绝候选</UButton></div></section>
        <p v-if="error" class="notice notice-error" role="alert">{{ error }} <button v-if="draft" type="button" class="text-action" @click="error = ''; preview = null">核对并继续编辑</button></p><p v-if="message" class="notice notice-success" role="status">{{ message }}</p>
        <section v-if="preview" class="workspace-section confirmation-preview"><div class="section-title"><h2>确认前的差异</h2><span class="muted">草稿修订 {{ preview.draft_revision }}</span></div><p class="section-description">只有明确选中的部分会创建新版本；此操作不会改云端策略。</p><article v-for="(diff, i) in preview.diff" :key="i" class="confirmation-diff"><h3>{{ diff.asset_id }} / {{ diff.section === 'roles' ? '角色' : '访问要求' }}</h3><div class="diff-columns"><div><h4>原版本</h4><pre>{{ JSON.stringify(diff.before, null, 2) }}</pre></div><div><h4>待确认</h4><pre>{{ JSON.stringify(diff.after, null, 2) }}</pre></div></div></article><p v-if="preview.section_dependencies?.access?.includes('roles')" class="notice notice-warning">本稿新增服务的访问要求依赖同时确认角色。需要你分别明确选择，不会自动勾选角色。</p><ul v-if="preview.validation_errors?.length" class="notice-list warning"><li v-for="(issue, index) in preview.validation_errors" :key="index">{{ issue.asset_id }} / {{ issue.section }}：{{ issue.code }}</li></ul><div class="confirm-sections"><label v-if="preview.confirmable_sections.includes('roles')" class="check-line"><input v-model="sections" :disabled="busy || unresolved" type="checkbox" value="roles" />确认角色部分</label><label v-if="preview.confirmable_sections.includes('access')" class="check-line"><input v-model="sections" :disabled="busy || unresolved" type="checkbox" value="access" />确认访问要求部分</label></div><UButton :disabled="busy || unresolved || dirty || !sections.length || !!missingSections.length" @click="confirm">{{ busy ? '确认中…' : '确认选中部分' }}</UButton></section>
      <section v-if="unresolved" class="notice notice-warning"><p>上次动作结果尚未核对。草稿与原请求标识保留，不会自动改为新的动作。</p><UButton v-if="confirmMutation.state === 'unsaved'" color="neutral" variant="outline" :disabled="busy" @click="confirm">以原标识核对确认</UButton><UButton v-if="rejectMutation.state === 'unsaved'" color="neutral" variant="outline" :disabled="busy" @click="reject">以原标识核对拒绝</UButton></section></div></div></template>
  </div>
</template>
