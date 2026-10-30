<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref, shallowReactive, watch } from 'vue'
import { onBeforeRouteLeave, useRouter } from 'vue-router'
import UButton from '@nuxt/ui/components/Button.vue'
import { ProtocolError } from './api'
import { cloudPlanRequest } from './cloud-view'
import { errorText, formatTime, scopeLabel, useWorkspace } from './workspace'
import { isDefiniteRejection, LatestRead, Mutation, PlanDraft } from './workspace-state'
import type { CollectionOptions, DataSource, Job } from './workspace-types'

const workspace = useWorkspace(), router = useRouter()
const sources = ref<DataSource[]>([]), sourceID = ref(''), scopeID = ref(''), conversationID = ref(''), selected = ref<string[]>([]), gatewayOnly = ref(false), kind = ref<'collect' | 'connection_check'>('collect')
const options = ref<CollectionOptions | null>(null), sourcesLoading = ref(false), optionsLoading = ref(false), loaded = ref(false), busy = ref(false), error = ref('')
const sourceReads = new LatestRead(), optionReads = new LatestRead(), previewReads = new LatestRead(), plan = reactive(new PlanDraft()), startMutation = shallowReactive(new Mutation<{ plan_id: string }, Job>())
const cachedStart = workspace.drafts.get('cloud-start') as { body: { plan_id: string }; key: string } | undefined
if (cachedStart) startMutation.restorePending(cachedStart.body, cachedStart.key)
const scope = computed(() => options.value?.scopes.find(item => item.scope.ref === scopeID.value))
const eligibleConversations = computed(() => workspace.conversations.value.filter(item => !item.archived && item.policy_scope_ref === scopeID.value))
const conversation = computed(() => eligibleConversations.value.find(item => item.id === conversationID.value) ?? null)
const disabled = computed(() => busy.value || !!startMutation.pendingBody)
const canPreview = computed(() => { try { cloudPlanRequest(options.value, sourceID.value, scopeID.value, selected.value, gatewayOnly.value, conversation.value, kind.value); return !disabled.value && !sourcesLoading.value && !optionsLoading.value } catch { return false } })
watch([sourceID, scopeID, selected, gatewayOnly, kind, conversationID, () => conversation.value?.revision], () => { previewReads.invalidate(); plan.update({ snapshot: sourceID.value, conversation: `${conversationID.value}:${conversation.value?.revision ?? ''}`, scope: scopeID.value, assets: kind.value === 'connection_check' ? [] : selected.value, kind: kind.value === 'connection_check' ? 'identity_only' : gatewayOnly.value ? 'gateway_policy' : 'assets', mode: kind.value }) }, { deep: true, flush: 'sync' })
watch(scopeID, () => { selected.value = []; gatewayOnly.value = false; conversationID.value = '' }, { flush: 'sync' })
watch(gatewayOnly, () => { selected.value = [] }, { flush: 'sync' })
watch(sourceID, () => { void loadOptions() })

async function loadSources() {
  const current = sourceReads.begin(); sourcesLoading.value = true; error.value = ''
  try { const result = await workspace.api.sources(); if (current()) { sources.value = result.items.filter(item => item.cloud_reads); loaded.value = true; if (sourceID.value && !sources.value.some(item => item.id === sourceID.value)) sourceID.value = '' } }
  catch (cause) { if (current()) error.value = errorText(cause) }
  finally { if (current()) sourcesLoading.value = false }
}
async function loadOptions() {
  const current = optionReads.begin(), id = sourceID.value; options.value = null; scopeID.value = ''; plan.invalidate(); error.value = ''; optionsLoading.value = !!id
  if (!id) return
  try { const value = await workspace.api.collectionOptions(id); if (current() && sourceID.value === id) options.value = value }
  catch (cause) { if (current()) error.value = errorText(cause) }
  finally { if (current()) optionsLoading.value = false }
}
async function chooseConversation(event: Event) {
  const id = (event.target as HTMLSelectElement).value
  conversationID.value = ''; error.value = ''
  if (!eligibleConversations.value.some(item => item.id === id)) return
  busy.value = true
  try { await workspace.selectConversation(id); if (eligibleConversations.value.some(item => item.id === id)) conversationID.value = id }
  catch (cause) { error.value = errorText(cause) }
  finally { busy.value = false }
}
async function createConversation() {
  if (!scope.value || disabled.value) return
  const ref = scopeID.value, label = scopeLabel(scope.value.scope.boundary, scope.value.scope.direction)
  busy.value = true; error.value = ''
  try { const value = await workspace.newConversationForScope(ref, `${label}资料读取`); if (scopeID.value === ref) conversationID.value = value.id }
  catch (cause) { error.value = errorText(cause) }
  finally { busy.value = false }
}
async function preview() {
  if (!canPreview.value) return
  const current = previewReads.begin()
  busy.value = true; error.value = ''
  try {
    const request = cloudPlanRequest(options.value, sourceID.value, scopeID.value, selected.value, gatewayOnly.value, conversation.value, kind.value), accept = plan.capturePreview()
    const max = options.value!.max_http_attempts
    const response = await workspace.api.previewPlan(request), effects = response.effect_ceiling
    if (!current()) return
    if (effects?.allow_cloud_reads !== true || effects.allow_model_requests !== false || effects.model_http_attempts !== 0 || !Number.isSafeInteger(effects.cloud_http_attempts) || Number(effects.cloud_http_attempts) < 1 || Number(effects.cloud_http_attempts) > max) throw new ProtocolError('云计划调用上限与本次只读选择不一致，请重新核对。')
    accept(response)
  } catch (cause) { if (current()) error.value = errorText(cause) }
  finally { busy.value = false }
}
async function start() {
  if (busy.value || !startMutation.pendingBody && !plan.startable) return
  busy.value = true; error.value = ''
  try {
    const accepted = await startMutation.submit(startMutation.pendingBody ?? { plan_id: plan.preview!.id }, (body, key) => workspace.api.startJob(body.plan_id, key))
    workspace.drafts.delete('cloud-start'); workspace.updateJob(accepted); plan.invalidate(); await router.push(`/audits/${accepted.id}`)
  } catch (cause) { if (isDefiniteRejection(cause)) { startMutation.discard(); workspace.drafts.delete('cloud-start'); plan.invalidate() }; error.value = errorText(cause) }
  finally { busy.value = false }
}
onMounted(() => { void loadSources() })
onBeforeRouteLeave(() => !busy.value || startMutation.state === 'saved')
onBeforeUnmount(() => { sourceReads.invalidate(); optionReads.invalidate(); previewReads.invalidate(); plan.invalidate(); if (startMutation.pendingBody && startMutation.key) workspace.drafts.set('cloud-start', { body: startMutation.pendingBody, key: startMutation.key }) })
</script>

<template>
  <section class="workspace-section cloud-collection" aria-labelledby="cloud-collection-title">
    <div class="section-title"><div><h2 id="cloud-collection-title">从显式云连接读取资料</h2><p class="section-description">先选择有限范围与对象，再预览并接受本轮只读计划。目录读取和预览不访问云或模型。</p></div><button type="button" class="text-action" :disabled="sourcesLoading || disabled" @click="loadSources">重新读取本地目录</button></div>
    <p v-if="sourcesLoading" class="field-hint" role="status">正在读取本地连接目录。</p>
    <p v-else-if="loaded && !sources.length" class="empty-inline">服务端尚无显式云只读连接。仍可载入人工案例或导入离线资料；连接凭据仅由服务端私有配置提供。</p>
    <fieldset v-if="sources.length" :disabled="disabled" class="cloud-selection">
      <legend>本轮读取范围</legend>
      <label for="cloud-source">服务端只读连接</label><select id="cloud-source" v-model="sourceID"><option value="">明确选择一个连接</option><option v-for="source in sources" :key="source.id" :value="source.id">{{ source.label }}{{ source.available ? '（配置可用，身份未核对）' : '（当前未就绪）' }}</option></select>
      <p v-if="optionsLoading" class="field-hint" role="status">正在读取该连接的本地有限范围。</p>
      <template v-if="options"><p class="notice">{{ options.configured ? '服务端配置已提供' : '服务端配置未就绪' }}；{{ options.available ? '本地条件可用' : '本地条件未就绪' }}。尚未核对调用身份，真实云能力仍待本轮受控执行取证。每任务云／凭据 HTTP 上限 {{ options.max_http_attempts }} 次。</p><label for="cloud-scope">明确业务范围</label><select id="cloud-scope" v-model="scopeID"><option value="">明确选择一个范围</option><option v-for="item in options.scopes" :key="item.scope.ref" :value="item.scope.ref">{{ scopeLabel(item.scope.boundary, item.scope.direction) }} / {{ item.scope.ref }}</option></select></template>
      <template v-if="scope"><p v-if="kind === 'connection_check'" class="field-hint">本次仅核对显式连接的调用身份，无需选择资产或网关；不读取产品资料，也不保存审计快照。</p><label v-if="kind === 'collect' && scope.scope.boundary === 'nat'" class="check-line"><input v-model="gatewayOnly" type="checkbox" />只读取所选 NAT 网关策略集</label><p v-if="kind === 'collect' && gatewayOnly" class="field-hint">网关绑定：{{ scope.scope.gateway_ref }}。不据此推定资产路径或完整地址转换关系。</p><div v-else-if="kind === 'collect'" class="asset-choices"><label v-for="asset in scope.assets" :key="asset.id" class="check-line"><input v-model="selected" :value="asset.id" type="checkbox" />{{ asset.id }} <span class="muted">{{ asset.region }}</span></label><p v-if="!scope.assets.length" class="field-hint">该范围没有可明确选择的资产，不会把空列表当作扫描全账号。</p></div><label for="cloud-conversation">同范围会话</label><div class="input-action"><select id="cloud-conversation" :value="conversationID" @change="chooseConversation"><option value="">明确选择同范围会话</option><option v-for="item in eligibleConversations" :key="item.id" :value="item.id">{{ item.title }}</option></select><UButton color="neutral" variant="outline" @click="createConversation">为该范围新建会话</UButton></div><button v-if="workspace.listHasMore.value.conversations" type="button" class="text-action" @click="workspace.loadMore('conversations')">读取更早会话</button></template>
      <div class="cloud-operation"><label class="check-line"><input v-model="kind" type="radio" value="collect" />只读采集并保存新资料</label><label class="check-line"><input v-model="kind" type="radio" value="connection_check" />仅核对本轮调用身份</label></div><p class="field-hint">身份核对不验证产品权限、字段或完整性。采集不自动确认业务资料、不运行审计或模型；云端策略由人工修改。</p><UButton :disabled="!canPreview" @click="preview">{{ busy ? '处理中…' : '预览本轮只读计划' }}</UButton>
    </fieldset>
    <section v-if="plan.preview && !startMutation.pendingBody" class="cloud-accepted-plan" aria-labelledby="cloud-preview-title"><div class="section-title"><h3 id="cloud-preview-title">云读取启动前核对</h3><span class="muted">有效至 {{ formatTime(plan.preview.expires_at) }}</span></div><dl class="fact-grid"><div><dt>本轮操作</dt><dd>{{ kind === 'connection_check' ? '仅核对调用身份' : '只读采集并保存新资料' }}</dd></div><div><dt>固定范围／对象</dt><dd>{{ scopeID }} / {{ kind === 'connection_check' ? '仅身份，不读取资产或网关' : gatewayOnly ? '所选 NAT 网关策略集' : `${selected.length} 个明确资产` }}</dd></div><div><dt>云／凭据 HTTP 物理上限</dt><dd>{{ plan.preview.effect_ceiling?.cloud_http_attempts }} 次，共享额度内取严</dd></div><div><dt>模型调用</dt><dd>不允许，物理上限 0 次</dd></div><div><dt>数据时效</dt><dd>强制重新读取，不使用旧资料兜底</dd></div><div><dt>当前会话</dt><dd>{{ conversation?.title }}</dd></div></dl><ul v-if="plan.preview.warnings?.length" class="notice-list"><li v-for="warning in plan.preview.warnings" :key="warning">{{ warning }}</li></ul><ul v-if="plan.preview.gaps?.length" class="notice-list"><li v-for="gap in plan.preview.gaps" :key="gap">{{ gap }}</li></ul><p v-if="!plan.startable" class="notice notice-warning">计划已过期，请重新预览。</p><UButton :disabled="busy || !plan.startable" @click="start">接受只读计划并启动</UButton></section>
    <section v-if="startMutation.pendingBody" class="notice notice-warning"><p>原计划 {{ startMutation.pendingBody.plan_id }} 的启动结果尚未核对。以原标识重试只核对同一次启动，不创建替代计划。</p><UButton :disabled="busy" @click="start">以原计划标识核对并重试</UButton></section>
    <p v-if="error" class="notice notice-error" role="alert">{{ error }}</p>
  </section>
</template>
