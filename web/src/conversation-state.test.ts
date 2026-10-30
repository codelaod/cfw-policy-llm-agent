import test from 'node:test'
import assert from 'node:assert/strict'
import { computed, ref } from 'vue'
import { ApiError } from './api.ts'
import { createConversationController, type PlanningWorkspace } from './conversation-state.ts'
import { cloneData, mergeJobs } from './workspace-state.ts'
import { conversationFixture, intentFixture, intentResponseFixture } from './test-fixtures/conversation.ts'
import type { Conversation, ConversationIntent, IntentChoiceRequest, IntentTurnRequest, IntentTurnResponse, Job } from './workspace-types.ts'

function setup(empty = false) {
  const unlocked = ref(true), conversationID = ref(empty ? '' : 'conversation-one'), conversations = ref<Conversation[]>([conversationFixture(), conversationFixture('conversation-two')]), intents = ref<Record<string, ConversationIntent | null>>({}), jobs = ref<Job[]>([])
  const calls: { kind: string; id?: string; body?: unknown; key?: string }[] = []
  let version = 0
  const handlers = {
    turn: async (id: string, _body: IntentTurnRequest, _key: string) => intentResponseFixture(id),
    choice: async (id: string, _body: IntentChoiceRequest, _key: string) => intentResponseFixture(id),
    accept: async (id: string, _proposal: string, _revision: number, _key: string) => ({ job: { id: 'job-one', conversation_id: id, status: 'queued' as const, view_seq: 1, created_at: '2026-10-29T01:00:00Z' } }),
  }
  const workspace = {
    unlocked, conversationID, jobs, snapshotID: ref(''), currentConversation: computed(() => conversations.value.find(item => item.id === conversationID.value) ?? null), currentIntent: computed(() => intents.value[conversationID.value] ?? null),
    api: {
      intentTurn: async (id: string, body: IntentTurnRequest, key: string) => { calls.push({ kind: 'turn', id, body: cloneData(body), key }); return handlers.turn(id, body, key) },
      chooseIntent: async (id: string, body: IntentChoiceRequest, key: string) => { calls.push({ kind: 'choice', id, body: cloneData(body), key }); return handlers.choice(id, body, key) },
      acceptProposal: async (id: string, proposal: string, revision: number, key: string) => { calls.push({ kind: 'accept', id, body: { proposal_id: proposal, expected_intent_revision: revision }, key }); return handlers.accept(id, proposal, revision, key) },
    },
    newPlanningConversation: async (_title: string, key: string) => { calls.push({ kind: 'create-empty-conversation', key }); const value = conversationFixture('conversation-new'); conversations.value.push(value); conversationID.value = value.id; return value },
    applyIntentResponse: (value: IntentTurnResponse) => { calls.push({ kind: 'saved-response', id: value.conversation.id }); intents.value[value.conversation.id] = value.intent; if (value.job) workspace.updateJob(value.job) },
    loadIntent: async () => { calls.push({ kind: 'read-intent', id: conversationID.value }) }, loadMessages: async () => { calls.push({ kind: 'read-messages', id: conversationID.value }) }, refresh: async () => { calls.push({ kind: 'read-lists' }) },
    updateJob: (value: Job) => { jobs.value = mergeJobs(jobs.value, [value]) }, captureContext: () => { const captured = version; return () => unlocked.value && captured === version }, containsAppCredential: (text: string) => text.includes('public-app-token-fixture'),
  }
  const controller = createConversationController(workspace as unknown as PlanningWorkspace, value => value instanceof Error ? value.message : 'controlled failure')
  return { controller, workspace, calls, handlers, intents, lock: () => { version++; unlocked.value = false } }
}

test('blank goal creates an empty-scope conversation and persists a turn without selecting a snapshot or starting a job', async () => {
  const subject = setup(true), editor = subject.controller.editor.value
  editor.text = '审计杭州 web-prod'
  await subject.controller.send()
  assert.equal(subject.workspace.snapshotID.value, '')
  assert.equal(subject.calls[0]!.kind, 'create-empty-conversation')
  assert.equal(subject.controller.editor.value, editor)
  const sent = subject.calls.find(item => item.kind === 'turn')!.body as IntentTurnRequest
  assert.equal(editor.mode, 'rules_only'); assert.equal(editor.dataPolicy, 'prefer_fresh'); assert.equal(sent.mode, undefined); assert.equal(sent.data_policy, undefined); assert.ok(!('snapshot_id' in sent))
  assert.equal(subject.calls.some(item => item.kind === 'accept'), false)
  assert.equal(subject.workspace.jobs.value.length, 0)
  assert.equal(editor.text, ''); assert.equal(subject.workspace.currentIntent.value?.selection[0]?.resource_id, 'i-public-one')
})
test('unknown turn acceptance keeps the original text, revision and key for one explicit retry', async () => {
  const subject = setup(); let attempts = 0
  subject.intents.value['conversation-one'] = intentFixture()
  subject.handlers.turn = async id => { if (!attempts++) throw new TypeError('synthetic lost acknowledgement'); return intentResponseFixture(id) }
  const editor = subject.controller.editor.value; editor.text = '审计杭州 web-prod'
  await subject.controller.send()
  assert.equal(editor.turn.state, 'unsaved'); assert.equal(subject.workspace.currentIntent.value?.revision, 1)
  const original = cloneData(subject.calls.find(item => item.kind === 'turn'))
  editor.text = 'later edit cannot replace unresolved request'
  await subject.controller.send()
  assert.deepEqual(subject.calls.filter(item => item.kind === 'turn')[1], original)
  assert.equal(editor.turn.state, 'saved')
})
test('failed candidate choice retains its original canonical set and target version', async () => {
  const subject = setup(); let attempts = 0
  subject.intents.value['conversation-one'] = intentFixture()
  subject.handlers.choice = async id => { if (!attempts++) throw new Error('synthetic storage busy'); return intentResponseFixture(id) }
  const editor = subject.controller.editor.value; editor.candidateIDs = ['canonical-second']
  await subject.controller.choose(); editor.candidateIDs = ['canonical-other']; subject.intents.value['conversation-one']!.revision = 4
  await subject.controller.choose()
  const sent = subject.calls.filter(item => item.kind === 'choice')
  assert.deepEqual(sent[1], sent[0]); assert.equal((sent[0]!.body as IntentChoiceRequest).expected_revision, 1)
})
test('proposal acceptance is explicit and an unknown result retries the same proposal and key', async () => {
  const subject = setup(); let attempts = 0
  subject.intents.value['conversation-one'] = intentFixture()
  subject.handlers.accept = async id => { if (!attempts++) throw new Error('synthetic lost start response'); return { job: { id: 'job-one', conversation_id: id, status: 'queued', view_seq: 1, created_at: '2026-10-29T01:00:00Z' } } }
  assert.equal(subject.controller.canAccept.value, true)
  assert.equal(subject.calls.length, 0)
  await subject.controller.accept(); assert.equal(subject.workspace.jobs.value.length, 0)
  subject.intents.value['conversation-one']!.proposal!.id = 'a-newer-proposal'
  await subject.controller.accept()
  const accepts = subject.calls.filter(item => item.kind === 'accept'); assert.deepEqual(accepts[1], accepts[0]); assert.equal(subject.workspace.jobs.value.length, 1)
})
test('changing options or selecting a different pinned input prevents accepting the old proposal', async () => {
  const subject = setup(); subject.intents.value['conversation-one'] = intentFixture()
  subject.controller.editor.value.mode = 'rules_and_agent'
  assert.equal(subject.controller.optionsChanged.value, true); await subject.controller.accept(); assert.equal(subject.calls.length, 0)
  subject.controller.resetOptions(); assert.equal(subject.controller.canAccept.value, true)
  subject.intents.value['conversation-one']!.data_policy = { mode: 'pinned_snapshot', snapshot_id: 'original-input' }; subject.controller.resetOptions(); subject.workspace.snapshotID.value = 'different-input'
  assert.equal(subject.controller.canAccept.value, false)
})
test('each conversation keeps its own unsaved editor and a late reply cannot replace the other editor or messages', async () => {
  const subject = setup(); let release!: (value: IntentTurnResponse) => void
  subject.handlers.turn = async () => new Promise(resolve => { release = resolve })
  const oldEditor = subject.controller.editor.value; oldEditor.text = '原会话目标'
  const pending = subject.controller.send()
  subject.workspace.conversationID.value = 'conversation-two'; subject.controller.editor.value.text = '另一个会话草稿'
  release(intentResponseFixture('conversation-one')); await pending
  assert.equal(subject.controller.editor.value.text, '另一个会话草稿')
  assert.equal(subject.workspace.currentIntent.value, null)
  assert.ok(subject.intents.value['conversation-one'])
  assert.ok(!subject.calls.some(item => item.kind === 'read-messages'))
})
test('locking invalidates in-flight planning reads without showing a saved public result', async () => {
  const subject = setup(); let release!: (value: IntentTurnResponse) => void
  subject.handlers.turn = async () => new Promise(resolve => { release = resolve })
  subject.controller.editor.value.text = '审计杭州 web-prod'; const pending = subject.controller.send(); subject.lock(); release(intentResponseFixture()); await pending
  assert.equal(subject.calls.some(item => item.kind === 'saved-response'), false)
  assert.equal(subject.workspace.currentIntent.value, null)
})
test('known credential patterns and the app credential never enter a mutation or transport', async () => {
  const subject = setup(), editor = subject.controller.editor.value, fake = 'sk-ARTIFICIAL_ONLY_123456789'
  editor.text = '检查杭州 web-prod 密钥='+fake; await subject.controller.send()
  assert.equal(subject.calls.length, 0); assert.equal(editor.turn.pendingBody, null); assert.ok(!editor.text.includes(fake)); assert.match(editor.text, /检查杭州/)
  editor.text = 'public-app-token-fixture'; await subject.controller.send(); assert.equal(editor.text, ''); assert.equal(subject.calls.length, 0)
})
test('a server credential rejection clears the raw body while normal storage failures keep the original request', async () => {
  const subject = setup(), editor = subject.controller.editor.value
  subject.handlers.turn = async () => { throw new ApiError(422, 'CREDENTIAL_IN_PAYLOAD') }
  editor.text = 'opaque private fixture'; await subject.controller.send()
  assert.equal(editor.text, ''); assert.equal(editor.turn.pendingBody, null); assert.equal(editor.turn.key, null)
})
test('one known active task blocks a second acceptance but still allows status and cancel text to reach the server', async () => {
  const subject = setup(); subject.intents.value['conversation-one'] = intentFixture(); subject.workspace.jobs.value = [{ id: 'job-active', conversation_id: 'conversation-one', status: 'running', view_seq: 2, created_at: '2026-10-29T01:00:00Z' }]
  assert.equal(subject.controller.canAccept.value, false); await subject.controller.accept(); assert.equal(subject.calls.length, 0)
  subject.controller.editor.value.text = '取消当前任务'; await subject.controller.send()
  assert.equal(subject.calls.filter(item => item.kind === 'turn').length, 1)
})
test('reading saved state never retries a POST or discards unknown acceptance', async () => {
  const subject = setup(); subject.intents.value['conversation-one'] = intentFixture(); subject.handlers.accept = async () => { throw new Error('synthetic unknown response') }
  await subject.controller.accept(); const key = subject.controller.editor.value.acceptance.key
  await subject.controller.readCurrent()
  assert.equal(subject.calls.filter(item => item.kind === 'accept').length, 1)
  assert.equal(subject.controller.editor.value.acceptance.key, key)
  assert.equal(subject.controller.editor.value.acceptance.state, 'unsaved')
})

test('untouched defaults leave natural-language mode and freshness decisions to the server while explicit controls are sent', async () => {
  const subject = setup(), editor = subject.controller.editor.value
  editor.text = '这次使用模型补充，已人工修改后重采'; await subject.controller.send()
  let request = subject.calls.find(item => item.kind === 'turn')!.body as IntentTurnRequest
  assert.equal(request.mode, undefined); assert.equal(request.data_policy, undefined)
  editor.mode = 'rules_and_agent'; editor.modeEdited = true; editor.dataPolicy = 'force_refresh'; editor.dataPolicyEdited = true; editor.text = '核对已选对象的计划'; await subject.controller.send()
  request = subject.calls.filter(item => item.kind === 'turn')[1]!.body as IntentTurnRequest
  assert.equal(request.mode, 'rules_and_agent'); assert.deepEqual(request.data_policy, { mode: 'force_refresh' })
})
