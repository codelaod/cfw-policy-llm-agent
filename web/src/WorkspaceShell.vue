<script setup lang="ts">
import { shallowReactive, computed, nextTick, onBeforeUnmount, provide, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import UButton from '@nuxt/ui/components/Button.vue'
import { createWorkspace, errorText, formatTime, jobLabel, scopeLabel, sourceLabel, workspaceKey } from './workspace'
import { isDefiniteRejection, Mutation } from './workspace-state'
import type { MessageAcceptance, MessageRequest } from './workspace-types'

const workspace = createWorkspace()
provide(workspaceKey, workspace)
const route = useRoute(), router = useRouter()
const candidate = ref(''), accessError = ref(''), drawer = ref(false), menu = ref(false), messageDraft = ref(''), messageError = ref(''), sending = ref(false), title = ref('')
type MessageEnvelope = { conversation_id: string; request: MessageRequest }
const messageMutation = shallowReactive(new Mutation<MessageEnvelope, MessageAcceptance>())
const drawerElement = ref<HTMLElement | null>(null)
let previousFocus: HTMLElement | null = null
const nav = [{ path: '/overview', title: '数据与任务', short: '数据' }, { path: '/inventory', title: '资产与业务确认', short: '资产' }, { path: '/audits', title: '审计过程', short: '审计' }, { path: '/findings', title: '发现与审阅', short: '发现' }, { path: '/reports', title: '报告与复审', short: '报告' }]
const activeNav = computed(() => nav.find(item => route.path.startsWith(item.path)) ?? nav[0]!)
const scope = computed(() => workspace.currentSnapshot.value?.input.scope)
async function unlock() { accessError.value = ''; try { await workspace.unlock(candidate.value); candidate.value = '' } catch (error) { accessError.value = errorText(error) } }
async function selectSnapshot(event: Event) { const select = event.target as HTMLSelectElement; try { await workspace.selectSnapshot(select.value) } catch (error) { workspace.error.value = errorText(error) } finally { select.value = workspace.snapshotID.value } }
async function selectConversation(event: Event) { messageError.value = ''; if (messageMutation.state === 'unsaved') { messageError.value = '先核对原会话待确认的发送，再切换会话。'; return } try { await workspace.selectConversation((event.target as HTMLSelectElement).value) } catch (error) { messageError.value = errorText(error) } }
async function createConversation() { messageError.value = ''; try { await workspace.newConversation(title.value); title.value = '' } catch (error) { messageError.value = errorText(error) } }
async function send() {
  if (!messageDraft.value.trim() || sending.value) return
  sending.value = true; messageError.value = ''
  try {
    let body = messageMutation.pendingBody
    if (!body) {
      const snapshot = workspace.currentSnapshot.value
      if (!snapshot) throw new Error('先选择一份固定资料。')
      const conversation = await workspace.ensureConversation(snapshot)
      if (workspace.snapshotID.value !== snapshot.id) throw new Error('资料范围已变化，请核对后重新发送。')
      body = { conversation_id: conversation.id, request: { client_message_id: crypto.randomUUID(), policy_scope_ref: snapshot.input.scope.ref, snapshot_id: snapshot.id, text: messageDraft.value.trim(), model_use_accepted: true } }
    }
    const accepted = await messageMutation.submit(body, (envelope, key) => workspace.api.sendMessage(envelope.conversation_id, envelope.request, key))
    workspace.updateJob(accepted.job); messageDraft.value = ''; drawer.value = false
    await router.push(`/audits/${accepted.job.id}`)
    void workspace.loadMessages().catch(cause => { workspace.error.value = errorText(cause) })
    void workspace.refresh()
  } catch (error) { if (isDefiniteRejection(error)) messageMutation.discard(); messageError.value = errorText(error) }
  finally { sending.value = false; await nextTick(); if (drawer.value) drawerElement.value?.querySelector<HTMLTextAreaElement>('textarea')?.focus() }
}
async function changeConversationScope() {
  const current = workspace.currentConversation.value, snapshot = workspace.currentSnapshot.value
  if (!current || !snapshot || sending.value || messageMutation.state === 'unsaved') return
  try { const changed = await workspace.api.patchConversation(current.id, { expected_revision: current.revision, policy_scope_ref: snapshot.input.scope.ref }); const index = workspace.conversations.value.findIndex(item => item.id === changed.id); if (index >= 0) workspace.conversations.value[index] = changed; await workspace.loadMessages() }
  catch (error) { messageError.value = errorText(error) }
}
function trapTab(event: KeyboardEvent) {
  const focusable = [...(drawerElement.value?.querySelectorAll<HTMLElement>('button:not([disabled]),a[href],input:not([disabled]),select:not([disabled]),textarea:not([disabled])') ?? [])]
  const first = focusable[0], last = focusable.at(-1)
  if (!drawerElement.value?.contains(document.activeElement)) { event.preventDefault(); (event.shiftKey ? last : first)?.focus() }
  else if (event.shiftKey && document.activeElement === first) { event.preventDefault(); last?.focus() }
  else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first?.focus() }
}
function drawerKey(event: KeyboardEvent) {
  if (!drawer.value) return
  if (event.key === 'Escape') { event.preventDefault(); drawer.value = false }
  else if (event.key === 'Tab') trapTab(event)
}
watch(() => route.path, () => { menu.value = false })
watch(drawer, async open => {
  if (open) { document.addEventListener('keydown', drawerKey); previousFocus = document.activeElement as HTMLElement; await nextTick(); drawerElement.value?.querySelector<HTMLElement>('button')?.focus(); if (workspace.conversationID.value) { try { await workspace.loadMessages() } catch (error) { messageError.value = errorText(error) } } }
  else { document.removeEventListener('keydown', drawerKey); previousFocus?.focus() }
})
onBeforeUnmount(() => { document.removeEventListener('keydown', drawerKey); workspace.lock() })
</script>

<template>
  <div class="workspace-shell">
    <aside class="workspace-sidebar" :class="{ 'is-open': menu }"><button v-if="menu" type="button" class="mobile-nav-close" aria-label="关闭导航" @click="menu = false">关闭</button>
      <RouterLink to="/overview" class="workspace-brand" aria-label="云防火墙策略审计首页"><svg viewBox="0 0 28 32" aria-hidden="true"><path d="M14 2 25 6v10c0 6-5 10-11 14C8 26 3 22 3 16V6Z" fill="none" stroke="currentColor" stroke-width="2"/><path d="M9 12h10M9 17h10" stroke="currentColor" stroke-width="2"/></svg><span>云防火墙策略审计<small>固定证据，人工决策</small></span></RouterLink>
      <nav aria-label="业务工作区"><RouterLink v-for="item in nav" :key="item.path" :to="item.path" :class="{ active: route.path.startsWith(item.path) }" @click="menu = false"><span>{{ item.title }}</span><span v-if="item.path === '/audits' && workspace.activeJobs.value.length" class="nav-count">{{ workspace.activeJobs.value.length }}</span></RouterLink></nav>
      <div class="sidebar-bottom"><span class="status-dot" /><span>云端只读 · 人工应用策略</span></div>
    </aside>
    <div class="workspace-body">
      <header class="workspace-toolbar">
        <button class="mobile-menu" type="button" :aria-expanded="menu" aria-label="打开导航" @click="menu = !menu">☰</button>
        <span class="toolbar-section">{{ activeNav.title }}</span>
        <div class="toolbar-actions"><UButton v-if="workspace.unlocked.value" color="neutral" variant="outline" size="sm" @click="drawer = true">会话与历史</UButton><UButton v-if="workspace.unlocked.value" color="neutral" variant="ghost" size="sm" @click="workspace.lock()">锁定</UButton><span v-else class="muted">本机工作区未解锁</span></div>
      </header>
      <main class="workspace-main">
        <section v-if="!workspace.unlocked.value" class="workspace-unlock" aria-labelledby="unlock-title"><div class="unlock-rule" /><h1 id="unlock-title">打开审计工作区</h1><p>输入本机服务的应用访问口令。资料与历史保存在本机服务，刷新后需要重新解锁。</p><form @submit.prevent="unlock"><label for="workspace-token">应用访问口令</label><div class="input-action"><input id="workspace-token" v-model="candidate" type="password" autocomplete="off" required :disabled="workspace.loading.value" /><UButton type="submit" :disabled="workspace.loading.value || !candidate">{{ workspace.loading.value ? '验证中…' : '解锁工作区' }}</UButton></div></form><p v-if="accessError || workspace.error.value" role="alert" class="error-text">{{ accessError || workspace.error.value }}</p></section>
        <template v-else>
          <section class="source-strip" aria-label="新任务的资料范围"><div class="source-select"><label for="active-snapshot">当前资料</label><select id="active-snapshot" :value="workspace.snapshotID.value" @change="selectSnapshot"><option value="">选择固定资料</option><option v-for="item in workspace.snapshots.value" :key="item.id" :value="item.id">{{ sourceLabel(item.source_kind) }} / {{ scopeLabel(item.input.scope.boundary, item.input.scope.direction) }} / {{ formatTime(item.input.collected_at) }}</option></select></div><div class="source-meta"><span class="source-kind">{{ sourceLabel(workspace.currentSnapshot.value?.source_kind) }}</span><span>{{ scopeLabel(scope?.boundary, scope?.direction) }}</span><span v-if="workspace.currentSnapshot.value">构造／采集于 {{ formatTime(workspace.currentSnapshot.value.input.collected_at) }}</span><span v-else>尚无审计结论</span></div></section>
          <p v-if="workspace.error.value" class="notice notice-error" role="alert">{{ workspace.error.value }} <button type="button" class="text-action" @click="workspace.refresh">重新读取</button></p>
          <RouterView />
        </template>
      </main>
    </div>
    <div v-if="drawer && workspace.unlocked.value" class="drawer-backdrop" @click.self="drawer = false"><aside ref="drawerElement" class="conversation-drawer" role="dialog" aria-modal="true" aria-labelledby="conversation-heading"><header><div><h2 id="conversation-heading">会话与历史</h2><p>公开消息与任务引用从本机持久记录读取。</p></div><UButton color="neutral" variant="ghost" size="sm" aria-label="关闭会话与历史" @click="drawer = false">关闭</UButton></header><label for="conversation-select">重开已有会话</label><select id="conversation-select" :disabled="sending || messageMutation.state === 'unsaved'" :value="workspace.conversationID.value" @change="selectConversation"><option value="">选择会话</option><option v-for="conversation in workspace.conversations.value" :key="conversation.id" :value="conversation.id">{{ conversation.title }}{{ conversation.archived ? '（已归档）' : '' }}</option></select><button v-if="workspace.listHasMore.value.conversations" type="button" class="text-action" :disabled="workspace.listsLoading.value" @click="workspace.loadMore('conversations')">读取更早会话</button><form class="new-conversation" @submit.prevent="createConversation"><input v-model="title" :disabled="sending || messageMutation.state === 'unsaved'" aria-label="新会话标题" placeholder="新会话标题" maxlength="120" /><UButton color="neutral" variant="outline" type="submit" :disabled="sending || messageMutation.state === 'unsaved'">新建会话</UButton></form><div v-if="workspace.currentConversation.value && workspace.currentSnapshot.value && workspace.currentConversation.value.policy_scope_ref !== workspace.currentSnapshot.value.input.scope.ref" class="notice"><p>此会话当前绑定其他范围。切换保留本地历史，新任务仍只使用明确选择的范围。</p><button type="button" class="text-action" :disabled="sending || messageMutation.state === 'unsaved'" @click="changeConversationScope">将此会话切换到当前范围</button></div><div class="conversation-messages" aria-live="polite"><article v-for="message in workspace.messages.value" :key="message.id" :class="{ 'message-user': message.role === 'user' }"><div class="message-meta"><strong>{{ message.role === 'user' ? '你' : message.origin === 'model' ? '助手' : '应用记录' }}</strong><time>{{ formatTime(message.created_at) }}</time></div><p>{{ message.public_text }}</p><RouterLink v-if="message.job_id" :to="`/audits/${message.job_id}`" @click="drawer = false">查看关联任务</RouterLink></article><button v-if="workspace.messagesHasMore.value" type="button" class="text-action" @click="workspace.loadMessages(true).catch(cause => messageError = errorText(cause))">继续读取会话历史</button><div v-if="!workspace.messages.value.length" class="empty-inline">选择会话可重开历史。新会话从明确的操作开始，打开面板不会调用模型。</div></div><p v-if="!workspace.modelAvailable.value" class="notice">{{ workspace.modelState.value.label }}。你仍可确认业务资料、运行规则审计和审阅结果。</p><form class="conversation-compose" @submit.prevent="send"><label for="message-text">连续提问</label><textarea id="message-text" v-model="messageDraft" rows="3" maxlength="4000" :disabled="sending || messageMutation.state === 'unsaved'" placeholder="针对当前范围或已保存的结果提问" /><p class="field-hint">发送将按当前范围创建本轮助手任务，随后展示执行结果；公开回复保存到当前会话。</p><p v-if="messageError" class="error-text" role="alert">{{ messageError }}</p><button v-if="messageMutation.state === 'unsaved'" type="button" class="text-action" @click="messageMutation.discard(); messageError = ''">核对历史后重新编辑</button><UButton type="submit" :disabled="sending || !messageDraft.trim()">{{ sending ? '发送中…' : messageMutation.state === 'unsaved' ? '以原内容重试' : '发送提问' }}</UButton></form><section v-if="workspace.currentConversation.value" class="conversation-task-links"><h3>此会话的任务</h3><RouterLink v-for="job in workspace.jobs.value.filter(j => j.conversation_id === workspace.conversationID.value)" :key="job.id" :to="`/audits/${job.id}`" @click="drawer = false"><span>{{ jobLabel(job.status) }}</span><span>{{ formatTime(job.created_at) }}</span></RouterLink></section></aside></div>
  </div>
</template>
