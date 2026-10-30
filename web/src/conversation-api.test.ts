import test from 'node:test'
import assert from 'node:assert/strict'
import { WorkspaceClient } from './workspace-api.ts'
import { canAcceptProposal, decodeConversationIntent } from './conversation-view.ts'
import { conversationFixture, intentFixture, intentResponseFixture } from './test-fixtures/conversation.ts'

const body = () => ({ client_turn_id: 'turn-one', text: '审计杭州 web-prod', mode: 'rules_only' as const, data_policy: { mode: 'prefer_fresh' as const } })
test('empty-scope conversation and planning turn save goals without requiring or inventing a snapshot', async t => {
  const calls: { url: string; body: unknown; key: string | null }[] = []
  t.mock.method(globalThis, 'fetch', async (url: string, init: RequestInit) => { const headers = new Headers(init.headers); assert.equal(headers.get('X-App-Token'), 'public-access-fixture'); calls.push({ url, body: JSON.parse(String(init.body)), key: headers.get('Idempotency-Key') }); return Response.json(calls.length === 1 ? conversationFixture() : intentResponseFixture()) })
  const api = new WorkspaceClient(() => 'public-access-fixture')
  await api.createConversation('审计目标', '', 'create-original')
  const value = await api.intentTurn('conversation-one', body(), 'turn-original')
  assert.deepEqual(calls[0], { url: '/api/v1/conversations', body: { title: '审计目标', policy_scope_ref: '' }, key: 'create-original' })
  assert.equal(calls[1]!.url, '/api/v1/conversations/conversation-one/intent-turns')
  assert.equal(calls[1]!.key, 'turn-original'); assert.ok(!('snapshot_id' in (calls[1]!.body as object)))
  assert.equal(value.job, null); assert.equal(value.message?.id, 'message-conversation-one-1')
})
test('candidate choice carries canonical IDs and the exact saved target revision', async t => {
  let sent: unknown
  t.mock.method(globalThis, 'fetch', async (_url: string, init: RequestInit) => { sent = JSON.parse(String(init.body)); return Response.json(intentResponseFixture()) })
  await new WorkspaceClient(() => 'public-access-fixture').chooseIntent('conversation-one', { intent_id: 'intent-conversation-one', expected_revision: 3, candidate_ids: ['canonical-resource-two'] }, 'choice-original')
  assert.deepEqual(sent, { intent_id: 'intent-conversation-one', expected_revision: 3, candidate_ids: ['canonical-resource-two'] })
})
test('planning response rejects a foreign message, foreign intent or stale proposal instead of showing saved success', async t => {
  let value = intentResponseFixture()
  t.mock.method(globalThis, 'fetch', async () => Response.json(value))
  const api = new WorkspaceClient(() => 'public-access-fixture')
  value.reply!.conversation_id = 'another-session'; await assert.rejects(api.intentTurn('conversation-one', body(), 'key'), /会话/)
  value = intentResponseFixture(); value.intent!.conversation_id = 'another-session'; await assert.rejects(api.intentTurn('conversation-one', body(), 'key'), /会话/)
  value = intentResponseFixture(); value.intent!.proposal!.intent_revision = 2; await assert.rejects(api.intentTurn('conversation-one', body(), 'key'), /版本/)
})
test('planning accepts a persisted controlled reply without retaining sensitive original text', async t => {
  const value = intentResponseFixture(); value.message = null; value.intent = null; value.reply!.public_text = '已识别内容不进入消息，请使用受控配置入口。'
  t.mock.method(globalThis, 'fetch', async () => Response.json(value))
  const saved = await new WorkspaceClient(() => 'public-access-fixture').intentTurn('conversation-one', body(), 'key')
  assert.equal(saved.message, null); assert.equal(saved.intent, null); assert.equal(saved.reply?.seq, 2)
})
test('only an explicit proposal acceptance creates a job and its response must belong to the same conversation', async t => {
  const calls: { url: string; body: unknown; key: string | null }[] = []; let conversationID = 'conversation-one'
  t.mock.method(globalThis, 'fetch', async (url: string, init: RequestInit) => { calls.push({ url, body: JSON.parse(String(init.body)), key: new Headers(init.headers).get('Idempotency-Key') }); return Response.json({ job: { id: 'job-one', conversation_id: conversationID, status: 'queued', view_seq: 1, created_at: '2026-10-29T01:00:00Z' } }) })
  const api = new WorkspaceClient(() => 'public-access-fixture')
  assert.equal((await api.acceptProposal('conversation-one', 'proposal-one', 3, 'accept-original')).job.id, 'job-one')
  assert.deepEqual(calls[0], { url: '/api/v1/conversations/conversation-one/proposals/proposal-one/accept', body: { expected_intent_revision: 3 }, key: 'accept-original' })
  conversationID = 'another-session'; await assert.rejects(api.acceptProposal('conversation-one', 'proposal-one', 3, 'accept-original'), /会话/)
})
test('unknown planning statuses cannot enable acceptance and rules-only cannot acquire model effects', () => {
  const intent = intentFixture(); intent.proposal!.status = 'future-unknown'
  assert.equal(canAcceptProposal(intent), false)
  intent.proposal!.effects.allow_model_requests = true
  assert.throws(() => decodeConversationIntent(intent, 'conversation-one'), /模型/)
})
test('current intent and history are read-only and validate continuous persisted sequence', async t => {
  const methods: string[] = []
  t.mock.method(globalThis, 'fetch', async (url: string, init: RequestInit) => { methods.push(init.method ?? 'GET'); return Response.json(url.includes('/intents/current') ? null : { items: [intentResponseFixture().message, intentResponseFixture().reply], next_seq: 2, has_more: false, last_seq: 2 }) })
  const api = new WorkspaceClient(() => 'public-access-fixture')
  assert.equal(await api.currentIntent('conversation-one'), null); assert.equal((await api.messages('conversation-one')).items.length, 2)
  assert.deepEqual(methods, ['GET', 'GET'])
})

test('artifact-local resources retain local identity and never invent a cloud resource ID', () => {
  const value = intentFixture(); value.selection[0]!.identity_kind = 'artifact_local'; value.selection[0]!.resource_id = ''; value.selection[0]!.local_asset_id = 'asset-local-one'
  const decoded = decodeConversationIntent(value, 'conversation-one')
  assert.equal(decoded?.selection[0]?.resource_id, ''); assert.equal(decoded?.selection[0]?.local_asset_id, 'asset-local-one')
  value.selection[0]!.local_asset_id = ''; assert.throws(() => decodeConversationIntent(value, 'conversation-one'), /规范身份/)
})
test('unknown catalogue source and time remain unknown, and a blocked proposal without a plan cannot start', () => {
  const value = intentFixture() as unknown as Record<string, unknown>
  value.catalogue = { complete: false, limitations: null }
  const decoded = decodeConversationIntent(value, 'conversation-one')!
  assert.equal(decoded.catalogue.source, ''); assert.equal(decoded.catalogue.observed_at, '')
  decoded.proposal!.plan_id = ''; assert.equal(canAcceptProposal(decoded), false)
})

test('persisted application event messages restore in order while invalid role/source pairs stay rejected', async t => {
  const event = { ...intentResponseFixture().reply!, id: 'event-message', role: 'event', origin: 'application', seq: 2 }
  t.mock.method(globalThis, 'fetch', async () => Response.json({ items: [intentResponseFixture().message, event], next_seq: 2, last_seq: 2, has_more: false }))
  const api = new WorkspaceClient(() => 'public-access-fixture')
  assert.equal((await api.messages('conversation-one')).items[1]?.role, 'event')
  event.origin = 'model'; await assert.rejects(api.messages('conversation-one'), /公开消息/)
  event.origin = 'application'; event.role = 'system'; await assert.rejects(api.messages('conversation-one'), /公开消息/)
})
