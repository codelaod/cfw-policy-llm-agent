import { computed, inject, ref, type Ref, type InjectionKey } from 'vue'
import { ApiError, ProtocolError, isTerminal } from './api.ts'
import { WorkspaceClient } from './workspace-api.ts'
import { LatestRead, mergeJobs } from './workspace-state.ts'
import { modelCapability } from './agent-view.ts'
import type { Capabilities, Conversation, ConversationMessage, Job, Page, ReportRecord, Snapshot, ConversationIntent, IntentTurnResponse, ResourceGroupResult } from './workspace-types.ts'

export function errorText(error: unknown): string {
  if (error instanceof ProtocolError) return error.message
  if (error instanceof ApiError) {
    const known: Record<string, string> = {
      VERSION_CONFLICT: '资料版本已变化，请重新读取并预览差异。', REVIEW_VERSION_CONFLICT: '其他审阅已先保存，请核对最新记录后再修改。',
      PLAN_STALE: '计划引用的资料已变化，请重新预览。', PLAN_CONSUMED: '此计划已被使用，请读取已有任务。',
      CONVERSATION_BUSY: '当前会话已有未结束任务，请查看该任务或明确取消。', TURN_IN_PROGRESS: '当前会话已有未结束任务，请查看该任务或明确取消。',
      IDEMPOTENCY_CONFLICT: '原请求标识与本次内容不一致，请先核对服务端记录。', MODEL_NOT_CONFIGURED: '模型尚未配置，草稿已保留；仍可手工确认资料并运行规则审计。',
      MODEL_UNAVAILABLE: '模型当前不可用，规则与已保存资料仍可使用。', INVALID_REQUEST: '输入未通过校验，请核对字段、范围及明确的访问要求。', SCOPE_DENIED: '所选对象或会话与固定资料范围不一致，请重新核对。', CONVERSATION_ARCHIVED: '当前会话已归档，请选择或新建可用会话。', QUEUE_FULL: '待执行任务已满，请等待现有任务结束。', INPUT_INVALID: '输入未通过校验，请核对字段、范围及明确的访问要求。',
      UNSUPPORTED_INPUT: '当前输入包含未支持的条件，请保留原资料并缩小可计算范围。', INVALID_EVENT_CURSOR: '事件游标与任务记录不一致，请从头只读恢复。',
      STORAGE_BUSY: '本地保存暂时繁忙，草稿未确认保存；请以原请求标识重试。', STORAGE_UNAVAILABLE: '本地保存未确认成功，草稿和原请求标识已保留。',
      SOURCE_NOT_CONFIGURED: '当前没有就绪的真实只读来源，可先使用人工样例或导入。', QUEUE_LIMIT: '待执行任务已满，请等待现有任务结束。',
      INTENT_STALE: '会话目标已变化，请重新读取并核对候选和计划。', PROPOSAL_STALE: '该计划已被后续目标替代，请核对当前计划。', CANDIDATE_STALE: '候选目录或目标版本已变化，请重新读取并选择。', CREDENTIAL_IN_PAYLOAD: '检测到可能凭据，内容未发送保存。请通过受控配置入口处理。',
      SERVER_STOPPING: '服务正在停止，请待服务恢复后读取已有任务。',
    }
    if (error.status === 401) return '访问口令已失效，请重新解锁；不会自动重发操作。'
    if (error.status === 403) return '此对象不在当前允许范围内，请重新选择资料。'
    if (error.status === 404) return '没有找到此记录，请核对所选对象或当前服务版本。'
    return known[error.code] ?? `请求未完成（${error.code}），请保留当前草稿并核对记录。`
  }
  return error instanceof Error && !/^fetch|network|failed to fetch/i.test(error.message) ? error.message : '连接未确认成功，当前草稿已保留；不会自动重发操作。'
}

export const sourceLabel = (value?: string) => ({ synthetic: '人工构造', imported: '导入声明', live_readonly: '只读观测' }[value ?? ''] ?? '来源待核对')
export const scopeLabel = (boundary?: string, direction?: string) => boundary === 'nat' ? 'NAT 出向' : boundary === 'internet' ? direction === 'in' ? '互联网入向' : '互联网出向' : '范围待选择'
export const verdictLabel = (value: string) => ({ pass: '通过', fail: '不符合', needs_confirmation: '待确认', insufficient_data: '信息不足', unsupported: '不支持', not_applicable: '不适用' }[value] ?? value)
export const severityLabel = (value: string) => ({ high: '高', medium: '中', low: '低', information: '信息', unassessed: '未评定' }[value] ?? value)
export const jobLabel = (value?: string) => ({ queued: '排队中', running: '执行中', cancelling: '取消中', completed: '已完成', partial: '部分完成', failed: '失败', cancelled: '已取消', interrupted: '已中断' }[value ?? ''] ?? '尚未启动')
export const planLabel = (value?: string) => value === 'todo' ? '采纳为待办' : value === 'deferred' ? '暂缓' : '未设置处理计划'
export function formatTime(value?: string) { return value && Number.isFinite(Date.parse(value)) ? new Date(value).toLocaleString('zh-CN', { hour12: false }) : '时间未知' }

export function createWorkspace() {
  const access = ref(''), unlocked = ref(false), loading = ref(false), error = ref('')
  let generation = 0
  let pointerLoaded = false
  const refreshReads = new LatestRead(), snapshotReads = new LatestRead(), conversationReads = new LatestRead(), messageReads = new LatestRead(), intentReads = new LatestRead()
  const api = new WorkspaceClient(() => access.value, () => lock('访问口令已失效，请重新解锁；待核对操作不会自动重发。'))
  const snapshots = ref<Snapshot[]>([]), conversations = ref<Conversation[]>([]), jobs = ref<Job[]>([]), reports = ref<ReportRecord[]>([])
  const messagesHasMore = ref(false), intents = ref<Record<string, ConversationIntent | null>>({})
  const capabilities = ref<Capabilities>({}), snapshotID = ref(''), conversationID = ref(''), messages = ref<ConversationMessage[]>([])
  const currentSnapshot = computed(() => snapshots.value.find(item => item.id === snapshotID.value) ?? null)
  const currentConversation = computed(() => conversations.value.find(item => item.id === conversationID.value) ?? null)
  const currentIntent = computed(() => intents.value[conversationID.value] ?? null)
  const activeJobs = computed(() => jobs.value.filter(job => !isTerminal(job.status)))
  const modelState = computed(() => modelCapability(capabilities.value))
  const modelAvailable = computed(() => modelState.value.ready)
  const drafts = new Map<string, unknown>()
  const groupResults = ref<Record<string,{sequence:number;result:ResourceGroupResult}>>({})
  type ListKind = 'snapshots' | 'conversations' | 'jobs' | 'reports'
  const listHasMore = ref<Record<ListKind, boolean>>({snapshots:false,conversations:false,jobs:false,reports:false}), listsLoading = ref(false)
  async function appendPage<T extends {id:string}>(kind: ListKind, target: Ref<T[]>, read: (offset:number)=>Promise<Page<T>>) {
    if (listsLoading.value) return
    const requestGeneration = generation
    listsLoading.value = true
    try { const result = await read(target.value.length); if (generation !== requestGeneration) return; const ids = new Set(target.value.map(item=>item.id)); target.value.push(...result.items.filter(item=>!ids.has(item.id))); listHasMore.value[kind] = result.items.length === 100 }
    catch (cause) { if (generation === requestGeneration) error.value = errorText(cause) }
    finally { listsLoading.value = false }
  }
  function loadMore(kind: ListKind) {
    if (kind === 'snapshots') return appendPage(kind, snapshots, offset=>api.snapshots(offset))
    if (kind === 'conversations') return appendPage(kind, conversations, offset=>api.conversations(offset))
    if (kind === 'jobs') return appendPage(kind, jobs, offset=>api.jobs(offset))
    return appendPage(kind, reports, offset=>api.reports(offset))
  }
  let sourceGuard: (() => boolean) | null = null
  async function refresh() {
    if (!unlocked.value) return
    const requestGeneration = generation, current = refreshReads.begin()
    let messageError = ''
    const results = await Promise.allSettled([api.snapshots(), api.conversations(), api.jobs(), api.reports(), api.capabilities(), api.workspace()])
    if (requestGeneration !== generation || !current()) return
    const [sourceResult, conversationResult, jobResult, reportResult, capabilityResult, pointerResult] = results
    if (sourceResult.status === 'fulfilled') {
      snapshots.value = [...sourceResult.value.items, ...snapshots.value.filter(item => !sourceResult.value.items.some(saved => saved.id === item.id))]; listHasMore.value.snapshots = sourceResult.value.items.length === 100
      if (!snapshotID.value && snapshots.value.length) snapshotID.value = snapshots.value[0]!.id
    }
    if (conversationResult.status === 'fulfilled') { conversations.value = [...conversationResult.value.items, ...conversations.value.filter(item => !conversationResult.value.items.some(saved => saved.id === item.id))]; listHasMore.value.conversations = conversationResult.value.items.length === 100 }
    if (jobResult.status === 'fulfilled') { jobs.value = mergeJobs(jobs.value, jobResult.value.items); listHasMore.value.jobs = jobResult.value.items.length === 100 }
    if (reportResult.status === 'fulfilled') { reports.value = [...reportResult.value.items, ...reports.value.filter(item => !reportResult.value.items.some(saved => saved.id === item.id))]; listHasMore.value.reports = reportResult.value.items.length === 100 }
    if (capabilityResult.status === 'fulfilled') capabilities.value = capabilityResult.value
    if (pointerResult.status === 'fulfilled' && !pointerLoaded) {
      conversationID.value = pointerResult.value.current_conversation_id
      pointerLoaded = true
      if (conversationID.value && !conversations.value.some(item => item.id === conversationID.value)) {
        try { const conversation = await api.conversation(conversationID.value); if (!current() || requestGeneration !== generation) return; conversations.value.unshift(conversation) } catch { /* The main page can still show available records. */ }
      }
      if (conversationID.value) { try { await Promise.all([loadMessages(), loadIntent()]) } catch (cause) { messageError = errorText(cause) } }
    }
    if (!current() || requestGeneration !== generation) return
    const rejected = results.find(result => result.status === 'rejected')
    error.value = rejected?.status === 'rejected' ? errorText(rejected.reason) : messageError
  }
  async function unlock(value: string) {
    loading.value = true; error.value = ''
    try { await new WorkspaceClient(() => value).health(); access.value = value; unlocked.value = true; generation++; await refresh() }
    finally { loading.value = false }
  }
  function lock(reason = '') { generation++; pointerLoaded = false; access.value = ''; unlocked.value = false; messages.value = []; messagesHasMore.value = false; capabilities.value = {}; error.value = reason; refreshReads.invalidate(); snapshotReads.invalidate(); conversationReads.invalidate(); messageReads.invalidate(); intentReads.invalidate(); intents.value = {}; groupResults.value={} }
  async function selectSnapshot(id: string, checkGuard = true) {
    if (checkGuard && id !== snapshotID.value && sourceGuard && !sourceGuard()) return
    if (!id) { snapshotID.value = ''; return }
    const current = snapshotReads.begin(), requestGeneration = generation
    const existing = snapshots.value.find(item => item.id === id)
    const snapshot = existing ?? await api.snapshot(id)
    if (!current() || requestGeneration !== generation) return
    if (!existing) snapshots.value.unshift(snapshot)
    snapshotID.value = snapshot.id
  }
  async function loadMessages(append = false) {
    const id = conversationID.value
    if (!id || !unlocked.value) { messages.value = []; messagesHasMore.value = false; return }
    const requestGeneration = generation, current = messageReads.begin()
    const collected: ConversationMessage[] = append ? [...messages.value] : []
    let after = collected.at(-1)?.seq ?? 0, hasMore = false
    for (let i = 0; i < 10; i++) {
      const page = await api.messages(id, after)
      if (!Array.isArray(page.items) || !Number.isSafeInteger(page.next_seq) || page.next_seq < after) throw new ProtocolError('会话历史响应不连续。')
      for (const message of page.items) { if (message.conversation_id !== id || message.seq !== after + 1) throw new ProtocolError('会话消息不连续。'); collected.push(message); after = message.seq }
      hasMore = page.has_more
      if (!hasMore) break
    }
    if (current() && requestGeneration === generation && conversationID.value === id) { messages.value = collected; messagesHasMore.value = hasMore }
  }
  async function selectConversation(id: string) {
    if (!id) return
    const selected = conversationReads.begin(), requestGeneration = generation
    const current = await api.workspace()
    await api.setCurrentConversation(id, current.revision)
    if (!selected() || requestGeneration !== generation) return
    conversationID.value = id; messages.value = []; messagesHasMore.value = false; pointerLoaded = true; await Promise.all([loadMessages(), loadIntent()])
  }
  async function newConversation(title = '', source: Snapshot | null = currentSnapshot.value) {
    const scope = source?.input.scope
    return newConversationForScope(scope?.ref ?? '', title.trim() || (scope ? `${scopeLabel(scope.boundary, scope.direction)}审计` : '新的审计对话'))
  }
  async function newConversationForScope(scopeRef: string, title: string) {
    const requestGeneration = generation, originalConversation = conversationID.value, selectionCurrent = conversationReads.begin()
    const conversation = await api.createConversation(title, scopeRef)
    if (requestGeneration !== generation || !unlocked.value) throw new Error('工作区已锁定，请重新核对会话。')
    conversations.value = [conversation, ...conversations.value.filter(item => item.id !== conversation.id)]
    if (!selectionCurrent() || conversationID.value !== originalConversation) return conversation
    conversationID.value = conversation.id; messages.value = []; messagesHasMore.value = false; intents.value[conversation.id] = null
    pointerLoaded = true
    return conversation
  }
  async function newPlanningConversation(title = '新的审计对话', key?: string) {
    const requestGeneration = generation, originalConversation = conversationID.value, selectionCurrent = conversationReads.begin()
    const conversation = await api.createConversation(title, '', key)
    if (requestGeneration !== generation || !unlocked.value) throw new Error('工作区已锁定，请重新核对会话。')
    conversations.value = [conversation, ...conversations.value.filter(item => item.id !== conversation.id)]
    if (!selectionCurrent() || conversationID.value !== originalConversation) return conversation
    conversationID.value = conversation.id; messages.value = []; messagesHasMore.value = false; intents.value[conversation.id] = null; pointerLoaded = true
    return conversation
  }
  async function loadIntent() {
    const id = conversationID.value, requestGeneration = generation, current = intentReads.begin()
    if (!id || !unlocked.value) return
    const value = await api.currentIntent(id)
    if (current() && requestGeneration === generation && conversationID.value === id) {
      const previous = intents.value[id]
      if (value && (!previous || value.revision >= previous.revision)) intents.value[id] = value
      else if (!value && !previous) intents.value[id] = null
    }
  }
  function applyIntentResponse(response: IntentTurnResponse) {
    const id = response.conversation.id
    const previous = conversations.value.find(item => item.id === id)
    if (!previous || response.conversation.revision >= previous.revision) conversations.value = [response.conversation, ...conversations.value.filter(item => item.id !== id)]
    const previousIntent = intents.value[id]
    if (id === conversationID.value) intentReads.invalidate()
    if (response.intent && (!previousIntent || response.intent.revision >= previousIntent.revision)) intents.value[id] = response.intent
    else if (!previousIntent) intents.value[id] = null
    if (response.group) {
      const sequence=response.reply?.seq ?? response.message?.seq ?? 0, previous=groupResults.value[id]
      if(!previous || sequence>=previous.sequence)groupResults.value[id]={sequence,result:response.group}
    }
    if (response.job) updateJob(response.job)
  }
  function captureContext() { const version = generation; return () => unlocked.value && version === generation }
  function containsAppCredential(text: string) { return access.value.length >= 8 && text.includes(access.value) }
  async function ensureConversation(source: Snapshot | null = currentSnapshot.value) {
    const scope = source?.input.scope.ref ?? ''
    if (currentConversation.value && !currentConversation.value.archived && currentConversation.value.policy_scope_ref === scope) return currentConversation.value
    const existing = conversations.value.find(item => !item.archived && item.policy_scope_ref === scope)
    if (existing) { await selectConversation(existing.id); return existing }
    return newConversation('', source)
  }
  async function addExample(name: string) { const item = await api.example(name); snapshots.value.unshift(item); snapshotID.value = item.id; return item }
  async function importInput(value: unknown) { const item = await api.importSnapshot(value); snapshots.value.unshift(item); snapshotID.value = item.id; return item }
  function updateJob(job: Job) { jobs.value = mergeJobs(jobs.value, [job]) }
  function setSourceGuard(guard: (() => boolean) | null) { sourceGuard = guard }
  return { api, unlocked, loading, error, snapshots, conversations, jobs, reports, capabilities, snapshotID, conversationID, messages, messagesHasMore, currentSnapshot, currentConversation, activeJobs, modelAvailable, modelState, currentIntent, intents, groupResults, loadIntent, applyIntentResponse, captureContext, containsAppCredential, newPlanningConversation, drafts, listHasMore, listsLoading, loadMore, unlock, lock, refresh, selectSnapshot, selectConversation, newConversation, newConversationForScope, ensureConversation, loadMessages, addExample, importInput, updateJob, setSourceGuard }
}

export type Workspace = ReturnType<typeof createWorkspace>
export const workspaceKey: InjectionKey<Workspace> = Symbol('workspace')
export function useWorkspace(): Workspace { const value = inject(workspaceKey); if (!value) throw new Error('工作区尚未初始化。'); return value }
