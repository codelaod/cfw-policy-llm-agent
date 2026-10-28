<script setup lang="ts">
import { computed, markRaw, onBeforeUnmount, reactive, ref } from 'vue'
import UButton from '@nuxt/ui/components/Button.vue'
import UBadge from '@nuxt/ui/components/Badge.vue'
import { ApiClient, ApiError, ProtocolError, isTerminal } from './api'
import type { Job, JobEvent } from './api'
import { EventObserver } from './events'
import type { Mode, ObservationState } from './events'

type Slot = {
  conversationId: string; job: Job | null; events: JobEvent[]; state: ObservationState;
  error: string; busy: boolean; requestId: string | null; observer?: Pick<EventObserver, 'stop'>; generation: number;
}
const pageId = crypto.randomUUID()
const slots = reactive<Slot[]>(Array.from({ length: 3 }, (_, i) => ({
  conversationId: `e0-web-${pageId}-${i + 1}`, job: null, events: [], state: 'paused',
  error: '', busy: false, requestId: null, generation: 0,
})))
const selected = ref(0)
const active = computed(() => slots[selected.value]!)
const token = ref('')
const candidate = ref('')
const unlocking = ref(false)
const unlocked = ref(false)
const accessError = ref('')
const recoverId = ref('')
const api = new ApiClient(() => token.value)
const runningCount = computed(() => slots.filter(slot => slot.job?.status === 'running').length)
const statusLabels: Record<string, string> = { queued: '排队中', running: '执行中', cancelling: '取消中', completed: '已完成', failed: '失败', cancelled: '已取消', interrupted: '已中断' }
const stateLabels: Record<ObservationState, string> = {
  connecting: '连接中', live: 'SSE 实时观察', reconnecting: '连接中断，正在重连', polling: 'JSON 轮询兜底',
  paused: '观察已暂停', ended: '已读完终态', auth_required: '需要重新解锁', forbidden: '记录不可访问', protocol_error: '事件协议错误',
}
const eventLabels: Record<string, string> = { job_status: '任务状态', stage_changed: '人工阶段', job_terminal: '任务终态' }
function describe(error: unknown): string {
  if (error instanceof ProtocolError) return error.message
  if (error instanceof ApiError) {
    const labels: Record<string, string> = {
      CONVERSATION_BUSY: '该会话已有未结束任务，请读取原任务。', IDEMPOTENCY_CONFLICT: '请求标识与已有内容冲突。',
      INVALID_EVENT_CURSOR: '事件游标无效，请重新读取记录。',
    }
    if (error.status === 401) return '访问口令无效或已失效，请重新解锁。'
    if ([403, 404].includes(error.status)) return '记录不存在或不可访问，请核对任务 ID。'
    return labels[error.code] ?? `请求失败（${error.code}）。`
  }
  return '请求未确认成功。请检查本机服务；重试沿用同一请求标识。'
}
function pause(slot: Slot) {
  slot.generation++
  slot.observer?.stop()
  slot.observer = undefined
  slot.state = 'paused'
}
function lock() {
  slots.forEach(pause)
  token.value = ''; candidate.value = ''; unlocked.value = false
}
async function unlock() {
  accessError.value = ''; unlocking.value = true
  try {
    const value = candidate.value
    await new ApiClient(() => value).health()
    token.value = value; candidate.value = ''; unlocked.value = true
  } catch (error) { accessError.value = describe(error) }
  finally { unlocking.value = false }
}
function observe(slot: Slot, mode: Mode) {
  if (!slot.job || !unlocked.value) return
  pause(slot)
  slot.error = ''
  const generation = slot.generation
  const jobId = slot.job.id
  const current = () => generation === slot.generation && slot.job?.id === jobId
  const observer = new EventObserver(api, jobId, slot.events.at(-1)?.seq ?? 0, {
    event: event => { if (current()) slot.events.push(event) },
    job: job => { if (current() && (!slot.job || job.view_seq >= slot.job.view_seq)) slot.job = job },
    state: state => {
      if (!current()) return
      slot.state = state
      if (state === 'auth_required') { lock(); accessError.value = '访问口令已失效，请重新解锁后恢复观察。' }
    },
    error: error => { if (current()) slot.error = describe(error) },
  })
  slot.observer = markRaw(observer)
  void observer.start(mode)
}
async function start(slot: Slot) {
  if (!unlocked.value || slot.busy || slot.job && !isTerminal(slot.job.status)) return
  slot.busy = true; slot.error = ''
  // A request with an unknown outcome keeps its original key for an explicit retry.
  slot.requestId ??= crypto.randomUUID()
  try {
    const job = await api.createJob(slot.conversationId, slot.requestId)
    pause(slot); slot.job = job; slot.events = []; slot.requestId = null
    observe(slot, 'sse')
  } catch (error) {
    slot.error = describe(error)
    if (error instanceof ApiError && error.status === 401) lock()
  } finally { slot.busy = false }
}
async function startAll() { await Promise.allSettled(slots.map(slot => start(slot))) }
async function recover() {
  if (!recoverId.value.trim() || !unlocked.value || active.value.busy) return
  const slot = active.value
  slot.busy = true; slot.error = ''
  try {
    const job = await api.getJob(recoverId.value.trim())
    pause(slot); slot.job = job; slot.events = []; slot.conversationId = job.conversation_id; slot.requestId = null
    recoverId.value = ''; observe(slot, 'sse')
  } catch (error) {
    slot.error = describe(error)
    if (error instanceof ApiError && error.status === 401) lock()
  }
  finally { slot.busy = false }
}
function time(value: string) { return new Date(value).toLocaleTimeString('zh-CN', { hour12: false }) }
function eventSummary(event: JobEvent) {
  return event.type === 'stage_changed' ? (event.payload.stage === 'local_mock' ? '本机人工任务' : event.payload.stage)
    : statusLabels[event.payload.status ?? ''] ?? event.payload.status
}
function onVisibility() { if (document.hidden) slots.forEach(pause) }
document.addEventListener('visibilitychange', onVisibility)
onBeforeUnmount(() => { slots.forEach(pause); token.value = ''; document.removeEventListener('visibilitychange', onVisibility) })
</script>

<template>
  <div class="workbench">
    <header class="masthead">
      <RouterLink to="/e0" class="brand" aria-label="E0 本机联调首页">
        <svg viewBox="0 0 28 32" width="24" height="28" aria-hidden="true"><path d="M14 2 25 6v10c0 6-5 10-11 14C8 26 3 22 3 16V6Z" fill="none" stroke="currentColor" stroke-width="2"/><path d="M9 12h10M9 17h10" stroke="currentColor" stroke-width="2"/></svg>
        <span>云防火墙策略审计<span class="brand-subtitle">工程验证工作区</span></span>
      </RouterLink>
      <UBadge color="neutral" variant="subtle">E0 · 人工数据</UBadge>
    </header>

    <main>
      <div class="page-heading">
        <div><h1>本机联调</h1><p>观察三个独立会话的任务与事件，检查断线后的只读恢复。</p></div>
        <span class="boundary-note">本页任务仅运行本机模拟流程</span>
      </div>

      <section v-if="!unlocked" class="access-panel" aria-labelledby="access-title">
        <div><h2 id="access-title">解锁本机服务</h2><p>输入启动后端时设置的应用访问口令。刷新页面后需要重新输入。</p></div>
        <form class="access-form" @submit.prevent="unlock">
          <label for="app-token">应用访问口令</label>
          <div class="input-action"><input id="app-token" v-model="candidate" type="password" autocomplete="off" required :disabled="unlocking" /><UButton type="submit" :disabled="unlocking || !candidate">{{ unlocking ? '验证中…' : '解锁' }}</UButton></div>
        </form>
        <p v-if="accessError" role="alert" class="error-text">{{ accessError }}</p>
      </section>

      <template v-else>
        <section class="connection-bar" aria-label="服务访问状态">
          <span class="access-state"><span class="status-dot" />本机服务已解锁</span>
          <span>口令仅保留在当前页面内存</span>
          <UButton color="neutral" variant="ghost" size="sm" @click="lock">锁定</UButton>
        </section>

        <section class="sessions-section" aria-labelledby="sessions-title">
          <div class="section-heading"><div><h2 id="sessions-title">会话并行</h2><p>正在执行 {{ runningCount }} / 3；每个会话的事件序号独立递增。</p></div><UButton :disabled="slots.every(s => s.busy || s.job && !isTerminal(s.job.status))" @click="startAll">启动三会话联调</UButton></div>
          <div class="session-grid">
            <article v-for="(slot, index) in slots" :key="index" class="session" :class="{ selected: selected === index }">
              <button class="session-select" :aria-pressed="selected === index" @click="selected = index"><span>人工会话 {{ index + 1 }}</span><span class="session-seq">seq {{ slot.events.at(-1)?.seq ?? 0 }}</span></button>
              <div class="session-body"><strong>{{ slot.job ? statusLabels[slot.job.status] : '尚未启动' }}</strong><span>{{ slot.job ? stateLabels[slot.state] : '等待人工启动' }}</span><p class="job-id">{{ slot.job?.id ?? '启动后显示持久任务 ID' }}</p><UButton color="neutral" variant="outline" size="sm" :disabled="slot.busy || !!slot.job && !isTerminal(slot.job.status)" @click="start(slot)">{{ slot.busy ? '提交中…' : slot.requestId ? '以原标识重试' : slot.job ? '新建人工任务' : '启动此会话' }}</UButton></div>
            </article>
          </div>
        </section>

        <section class="event-section" aria-labelledby="events-title">
          <div class="section-heading"><div><h2 id="events-title">人工会话 {{ selected + 1 }} 的事件记录</h2><p>{{ stateLabels[active.state] }}<template v-if="active.job"> · 当前任务状态：{{ statusLabels[active.job.status] }}</template></p></div><div v-if="active.job" class="observer-actions"><UButton size="sm" color="neutral" variant="outline" @click="pause(active)">暂停观察</UButton><UButton size="sm" color="neutral" variant="outline" @click="observe(active, 'sse')">恢复 SSE</UButton><UButton size="sm" color="neutral" variant="outline" @click="observe(active, 'polling')">使用轮询</UButton></div></div>
          <p class="observation-note">暂停、切换页面或关闭连接只停止观察，后台任务继续执行。</p>
          <p v-if="active.error" class="error-text event-error" role="alert">{{ active.error }}</p>
          <div v-if="active.events.length" class="event-table-wrap"><table><thead><tr><th scope="col">序号</th><th scope="col">保存时间</th><th scope="col">事件</th><th scope="col">公开摘要</th></tr></thead><tbody><tr v-for="event in active.events" :key="event.seq"><td class="seq-cell">{{ event.seq }}</td><td>{{ time(event.occurred_at) }}</td><td>{{ eventLabels[event.type] }}</td><td>{{ eventSummary(event) }}</td></tr></tbody></table></div>
          <div v-else class="empty-events"><span class="empty-symbol" aria-hidden="true">∅</span><p>{{ active.job ? '尚未读取到已保存的事件。恢复观察后将从 seq 0 补读。' : '启动人工任务后，这里显示服务端保存的事件。' }}</p></div>
        </section>

        <section class="recover-panel" aria-labelledby="recover-title"><div><h2 id="recover-title">读取已有任务</h2><p>刷新后可粘贴任务 ID 恢复记录。此操作只读取数据。</p></div><form @submit.prevent="recover"><label class="sr-only" for="recover-id">已有任务 ID</label><input id="recover-id" v-model="recoverId" placeholder="任务 ID" autocomplete="off" /><UButton color="neutral" variant="outline" type="submit" :disabled="!recoverId.trim() || active.busy">读取到当前会话</UButton></form></section>
      </template>

      <footer class="scope-footer"><span>当前验证：工程依赖、三会话隔离、持久事件与观察恢复</span><span>审计规则、云采集与模型能力尚未在本阶段验收</span></footer>
    </main>
  </div>
</template>
