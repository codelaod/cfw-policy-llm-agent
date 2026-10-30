import test from 'node:test'
import assert from 'node:assert/strict'
import { createWorkspace } from './workspace.ts'
import { conversationFixture, intentFixture, intentResponseFixture } from './test-fixtures/conversation.ts'

function subject() { const workspace = createWorkspace(); workspace.unlocked.value = true; workspace.conversationID.value = 'conversation-one'; workspace.conversations.value = [conversationFixture(), conversationFixture('conversation-two')]; return workspace }
test('a late null planning read cannot erase a newer saved goal', async t => {
  const workspace = subject(); let release!: (value: Response) => void
  t.mock.method(globalThis, 'fetch', async () => new Promise(resolve => { release = resolve }))
  const pending = workspace.loadIntent(), saved = intentResponseFixture(); saved.intent!.revision = 2; saved.intent!.proposal!.intent_revision = 2; workspace.applyIntentResponse(saved)
  release(Response.json(null)); await pending
  assert.equal(workspace.currentIntent.value?.revision, 2)
})
test('late messages from an earlier read never remove subsequently loaded persisted messages', async t => {
  const workspace = subject(); let release!: (value: Response) => void; let calls = 0
  const value = intentResponseFixture()
  t.mock.method(globalThis, 'fetch', async () => { if (!calls++) return new Promise(resolve => { release = resolve }); return Response.json({ items: [value.message, value.reply], next_seq: 2, last_seq: 2, has_more: false }) })
  const old = workspace.loadMessages(); await workspace.loadMessages(); release(Response.json({ items: [], next_seq: 0, last_seq: 0, has_more: false })); await old
  assert.equal(workspace.messages.value.length, 2)
})
test('planning caches stay conversation-bound and an older response cannot roll back a newer revision', () => {
  const workspace = subject(); workspace.intents.value['conversation-one'] = { ...intentFixture(), revision: 4 }
  workspace.conversationID.value = 'conversation-two'; workspace.applyIntentResponse(intentResponseFixture())
  assert.equal(workspace.currentIntent.value, null); assert.equal(workspace.intents.value['conversation-one']?.revision, 4)
})
test('locking clears planning records and invalidates in-flight reads', async t => {
  const workspace = subject(); workspace.intents.value['conversation-one'] = intentFixture(); let release!: (value: Response) => void
  t.mock.method(globalThis, 'fetch', async () => new Promise(resolve => { release = resolve }))
  const pending = workspace.loadIntent(); workspace.lock(); release(Response.json(intentFixture())); await pending
  assert.deepEqual(workspace.intents.value, {}); assert.equal(workspace.unlocked.value, false)
})
test('new planning conversation stores the server response with empty scope and the original creation key', async t => {
  const workspace = subject(); let sent: unknown, key: string | null = null
  t.mock.method(globalThis, 'fetch', async (_url: string, init: RequestInit) => { sent = JSON.parse(String(init.body)); key = new Headers(init.headers).get('Idempotency-Key'); return Response.json(conversationFixture('conversation-new')) })
  await workspace.newPlanningConversation('新的审计对话', 'create-original')
  assert.deepEqual(sent, { title: '新的审计对话', policy_scope_ref: '' }); assert.equal(key, 'create-original'); assert.equal(workspace.conversationID.value, 'conversation-new'); assert.equal(workspace.currentIntent.value, null); assert.equal(workspace.snapshotID.value, '')
})

test('a late created conversation stays cached but never steals a changed selection or submits its old goal', async t => {
  const workspace = createWorkspace(); workspace.unlocked.value = true; workspace.conversations.value = [conversationFixture('conversation-two')]
  const { createConversationController } = await import('./conversation-state.ts')
  const controller = createConversationController(workspace, String), original = controller.editor.value
  original.text = '原空白目标'
  let release!: (value: Response) => void
  const requests: { url: string; key: string | null }[] = []
  t.mock.method(globalThis, 'fetch', async (url: string, init: RequestInit) => { requests.push({ url, key: new Headers(init.headers).get('Idempotency-Key') }); if (url === '/api/v1/conversations') return new Promise(resolve => { release = resolve }); throw new Error('unexpected later request') })
  const pending = controller.send(); workspace.conversationID.value = 'conversation-two'; controller.editor.value.text = '另一会话草稿'
  release(Response.json(conversationFixture('conversation-new'))); await pending
  assert.equal(workspace.conversationID.value, 'conversation-two'); assert.equal(controller.editor.value.text, '另一会话草稿')
  assert.equal(requests.length, 1); assert.ok(workspace.conversations.value.some(item => item.id === 'conversation-new'))
  assert.equal(controller.editors.get('conversation-new')?.text, '原空白目标'); assert.equal(controller.editors.has('new'), false)
  assert.equal(controller.creationReceipts.get('conversation-new')?.key, requests[0]!.key)
})
test('archived source and newly created conversation never share editor or unresolved intent ownership', async t => {
  const workspace = createWorkspace(); workspace.unlocked.value = true; const archived = conversationFixture('conversation-archived'); archived.archived = true; workspace.conversations.value = [archived]; workspace.conversationID.value = archived.id
  const { createConversationController } = await import('./conversation-state.ts')
  const controller = createConversationController(workspace, String), original = controller.editor.value; original.text = '从归档记录准备的新目标'
  t.mock.method(globalThis, 'fetch', async (url: string) => { if (url === '/api/v1/conversations') return Response.json(conversationFixture('conversation-new')); if (url.endsWith('/intent-turns')) throw new TypeError('public fixture unknown response'); throw new Error('unexpected request') })
  await controller.send()
  const created = controller.editor.value
  assert.notEqual(created, original); assert.equal(created.turn.pendingBody?.conversation_id, 'conversation-new'); assert.ok(created.turn.key)
  workspace.conversationID.value = archived.id
  assert.equal(controller.editor.value, original); assert.equal(original.turn.pendingBody, null); assert.equal(original.turn.key, null)
  original.text = '归档会话独立草稿'; assert.notEqual(created.text, original.text); assert.equal(created.turn.pendingBody?.request.text, '从归档记录准备的新目标')
})
