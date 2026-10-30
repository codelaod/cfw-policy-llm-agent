import { computed, reactive, shallowReactive } from 'vue'
import { ApiError, isTerminal } from './api.ts'
import { Mutation, isDefiniteRejection } from './workspace-state.ts'
import { canAcceptProposal } from './conversation-view.ts'
import type { Workspace } from './workspace.ts'
import type { Conversation, ConversationDataPolicy, ConversationMode, IntentChoiceRequest, IntentTurnRequest, IntentTurnResponse, ProposalAcceptance } from './workspace-types.ts'

export type PlanningWorkspace = Pick<Workspace, 'api' | 'unlocked' | 'conversationID' | 'currentConversation' | 'currentIntent' | 'snapshotID' | 'jobs' | 'newPlanningConversation' | 'applyIntentResponse' | 'loadIntent' | 'loadMessages' | 'refresh' | 'updateJob' | 'captureContext' | 'containsAppCredential'>
type TurnEnvelope = { conversation_id: string; request: IntentTurnRequest }
type ChoiceEnvelope = { conversation_id: string; request: IntentChoiceRequest }
type AcceptEnvelope = { conversation_id: string; proposal_id: string; expected_intent_revision: number }
export type ConversationEditor = { text: string; mode: ConversationMode; dataPolicy: ConversationDataPolicy['mode']; candidateIDs: string[]; optionsInitialized: boolean; modeEdited: boolean; dataPolicyEdited: boolean; error: string; busy: boolean; turn: Mutation<TurnEnvelope, IntentTurnResponse>; choice: Mutation<ChoiceEnvelope, IntentTurnResponse>; acceptance: Mutation<AcceptEnvelope, ProposalAcceptance> }
const credentialPattern = /(?:LTAI[A-Za-z0-9]{8,}|sk-[A-Za-z0-9_-]{12,}|(?:access[_ -]?key|secret|密码|密钥)\s*[:=：]\s*\S{4,})/ig
/** A narrow secret guard, never an intent parser or a promise to identify arbitrary secrets. */
export function redactKnownCredentials(text: string) { return text.replace(credentialPattern, '[敏感内容已移除]') }

export function createConversationController(workspace: PlanningWorkspace, formatError: (cause: unknown) => string) {
  const editors = shallowReactive(new Map<string, ConversationEditor>())
  const creation = shallowReactive(new Mutation<{ title: string }, Conversation>())
  const creationReceipts = new Map<string, { key: string; title: string }>()
  async function createStoredConversation() {
    return creation.submit(creation.pendingBody ?? { title: '新的审计对话' }, async (body, key) => {
      const conversation = await workspace.newPlanningConversation(body.title, key)
      creationReceipts.set(conversation.id, { key, title: body.title })
      return conversation
    })
  }
  function editorFor(id: string) {
    let editor = editors.get(id)
    if (!editor) {
      editor = reactive({ text: '', mode: 'rules_only' as ConversationMode, dataPolicy: 'prefer_fresh' as ConversationDataPolicy['mode'], candidateIDs: [] as string[], optionsInitialized: false, modeEdited: false, dataPolicyEdited: false, error: '', busy: false, turn: shallowReactive(new Mutation<TurnEnvelope, IntentTurnResponse>()), choice: shallowReactive(new Mutation<ChoiceEnvelope, IntentTurnResponse>()), acceptance: shallowReactive(new Mutation<AcceptEnvelope, ProposalAcceptance>()) }) as ConversationEditor
      editors.set(id, editor)
    }
    return editor
  }
  const editor = computed(() => editorFor(workspace.conversationID.value || 'new'))
  const currentJob = computed(() => workspace.jobs.value.find(job => job.conversation_id === workspace.conversationID.value && !isTerminal(job.status)) ?? null)
  const proposal = computed(() => workspace.currentIntent.value?.proposal ?? null)
  const optionsChanged = computed(() => { const intent = workspace.currentIntent.value; return !!intent && (editor.value.mode !== intent.mode || editor.value.dataPolicy !== intent.data_policy.mode || editor.value.dataPolicy === 'pinned_snapshot' && intent.data_policy.snapshot_id !== workspace.snapshotID.value) })
  const canAccept = computed(() => !editor.value.busy && !currentJob.value && !optionsChanged.value && canAcceptProposal(workspace.currentIntent.value) && !editor.value.turn.pendingBody && !editor.value.choice.pendingBody)
  function failure(current: ConversationEditor, cause: unknown, mutation?: Mutation<unknown, unknown>) {
    if (cause instanceof ApiError && cause.code === 'CREDENTIAL_IN_PAYLOAD') { current.text = ''; mutation?.discard(); current.error = '检测到可能凭据，原内容已清除；旧目标仍保留。请通过受控配置入口提供凭据。'; return }
    if (isDefiniteRejection(cause)) mutation?.discard()
    current.error = formatError(cause)
  }
  function synchronizeOptions(current: ConversationEditor, value: IntentTurnResponse) { if (value.intent) { current.mode = value.intent.mode; current.dataPolicy = value.intent.data_policy.mode; current.candidateIDs = []; current.optionsInitialized = true; current.modeEdited = false; current.dataPolicyEdited = false } }
  async function createConversation() {
    const current = editor.value
    if (current.busy) return null
    current.busy = true; current.error = ''
    const valid = workspace.captureContext()
    try {
      const conversation = await createStoredConversation()
      if (!valid()) return null
      if (workspace.conversationID.value !== conversation.id) { current.error = '新会话已保存。当前选择已变化，请明确打开新会话后继续。'; return conversation }
      editors.set(conversation.id, editorFor(conversation.id)); return conversation
    } catch (cause) { failure(current, cause, creation as Mutation<unknown, unknown>); return null }
    finally { current.busy = false }
  }
  async function send(text?: string) {
    const originalID = workspace.conversationID.value, originalEditor = editor.value
    let current = originalEditor
    if (current.busy) return
    const raw = text ?? current.text
    if (!current.turn.pendingBody && !raw.trim()) return
    const redacted = redactKnownCredentials(raw)
    if (redacted !== raw || workspace.containsAppCredential(raw)) {
      current.text = redacted !== raw ? redacted : ''; current.error = '检测到可能凭据，内容未发送。已移除可识别内容，请核对剩余目标后再次发送；不能保证识别所有秘密。'; return
    }
    if (current.choice.pendingBody || current.acceptance.pendingBody) { current.error = '先核对本会话尚未确认结果的选择或计划提交。'; return }
    current.busy = true; current.error = ''
    const valid = workspace.captureContext()
    try {
      let body = current.turn.pendingBody
      if (!body) {
        let conversation = workspace.currentConversation.value
        if (!conversation || conversation.archived) {
          conversation = await createStoredConversation()
          if (!valid()) return
          if (!originalID) {
            editors.delete('new')
            editors.set(conversation.id, current)
          } else {
            const destination = editorFor(conversation.id)
            destination.text = raw
            destination.mode = current.mode; destination.dataPolicy = current.dataPolicy
            destination.optionsInitialized = current.optionsInitialized; destination.modeEdited = current.modeEdited; destination.dataPolicyEdited = current.dataPolicyEdited
            current = destination
          }
          if (workspace.conversationID.value !== conversation.id) { current.error = '新会话已保存，原目标尚未发送。请明确打开此会话并核对后再发送。'; return }
          current.busy = true
        }
        let dataPolicy: ConversationDataPolicy | undefined
        if (current.dataPolicyEdited || current.dataPolicy === 'pinned_snapshot') {
          dataPolicy = { mode: current.dataPolicy }
          if (current.dataPolicy === 'pinned_snapshot') { if (!workspace.snapshotID.value) throw new Error('固定历史资料需要先在资料视图选择已保存的输入。'); dataPolicy.snapshot_id = workspace.snapshotID.value }
        }
        body = { conversation_id: conversation.id, request: { client_turn_id: crypto.randomUUID(), text: raw.trim(), ...(current.modeEdited ? { mode: current.mode } : {}), ...(dataPolicy ? { data_policy: dataPolicy } : {}), ...(workspace.currentIntent.value ? { expected_revision: workspace.currentIntent.value.revision } : {}) } }

      }
      const response = await current.turn.submit(body, (value, key) => workspace.api.intentTurn(value.conversation_id, value.request, key))
      if (!valid()) return
      workspace.applyIntentResponse(response); current.text = ''; synchronizeOptions(current, response)
      if (workspace.conversationID.value === body.conversation_id) await workspace.loadMessages()
    } catch (cause) { failure(current, cause, current.turn as Mutation<unknown, unknown>) }
    finally { current.busy = false; originalEditor.busy = false }
  }
  async function choose() {
    const current = editor.value, intent = workspace.currentIntent.value
    if (current.busy || !intent || !current.candidateIDs.length && !current.choice.pendingBody) return
    if (current.turn.pendingBody || current.acceptance.pendingBody) { current.error = '先核对本会话尚未确认结果的提交。'; return }
    current.busy = true; current.error = ''; const valid = workspace.captureContext()
    try {
      const body = current.choice.pendingBody ?? { conversation_id: workspace.conversationID.value, request: { intent_id: intent.id, expected_revision: intent.revision, candidate_ids: [...current.candidateIDs] } }
      const response = await current.choice.submit(body, (value, key) => workspace.api.chooseIntent(value.conversation_id, value.request, key))
      if (!valid()) return
      workspace.applyIntentResponse(response); synchronizeOptions(current, response)
      if (workspace.conversationID.value === body.conversation_id) await workspace.loadMessages()
    } catch (cause) { failure(current, cause, current.choice as Mutation<unknown, unknown>) }
    finally { current.busy = false }
  }
  async function accept() {
    const current = editor.value, intent = workspace.currentIntent.value
    if (current.busy || !current.acceptance.pendingBody && (!canAccept.value || !intent?.proposal)) return
    current.busy = true; current.error = ''; const valid = workspace.captureContext()
    try {
      const body = current.acceptance.pendingBody ?? { conversation_id: workspace.conversationID.value, proposal_id: intent!.proposal!.id, expected_intent_revision: intent!.revision }
      const response = await current.acceptance.submit(body, (value, key) => workspace.api.acceptProposal(value.conversation_id, value.proposal_id, value.expected_intent_revision, key))
      if (!valid()) return
      workspace.updateJob(response.job)
      if (workspace.conversationID.value === body.conversation_id) await Promise.all([workspace.loadIntent(), workspace.loadMessages()])
    } catch (cause) { failure(current, cause, current.acceptance as Mutation<unknown, unknown>) }
    finally { current.busy = false }
  }
  async function readCurrent() { const current = editor.value; current.error = ''; try { await Promise.all([workspace.refresh(), workspace.loadIntent(), workspace.loadMessages()]) } catch (cause) { current.error = formatError(cause) } }
  function resetOptions() { const intent = workspace.currentIntent.value; if (intent) { editor.value.mode = intent.mode; editor.value.dataPolicy = intent.data_policy.mode; editor.value.optionsInitialized = true; editor.value.modeEdited = false; editor.value.dataPolicyEdited = false } }
  function discardUnknown() { const current = editor.value; current.turn.discard(); current.choice.discard(); current.error = '' }
  return { editor, editors, creation, creationReceipts, currentJob, proposal, optionsChanged, canAccept, createConversation, send, choose, accept, readCurrent, resetOptions, discardUnknown }
}
