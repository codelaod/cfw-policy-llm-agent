import test from 'node:test'
import assert from 'node:assert/strict'
import { ref } from 'vue'
import { readFile } from 'node:fs/promises'
import { parse, compileScript } from '@vue/compiler-sfc'
import ts from 'typescript'
import * as Vue from 'vue'
import { renderToString } from 'vue/server-renderer'
import { WorkspaceClient } from './workspace-api.ts'
import * as businessView from './business-view.ts'
import { businessAllowedLabel, businessConfirmRequest, businessRequiredLabel, createBusinessController, decodeBusinessContext, decodeBusinessResult, markBusinessDeclaration } from './business-view.ts'
import * as workspaceState from './workspace-state.ts'
import { cloneData } from './workspace-state.ts'
import { intentFixture, intentResponseFixture } from './test-fixtures/conversation.ts'
import type { ConversationBusinessContext, ConversationBusinessRequest, ConversationBusinessResult, IntentTurnRequest, IntentTurnResponse } from './workspace-types.ts'

function contextFixture(conversation = 'conversation-one'): ConversationBusinessContext {
  const scope = { ref: 'scope-one', provider: 'aliyun', account_ref: 'account-local', control_plane_ref: 'control-local', ip_family: 'ipv4', boundary: 'internet', direction: 'in', policy_set_ref: 'policy-one' }
  const access = { scope_ref: scope.ref, protocol: 'tcp', addresses: [], ports: [{ from: 443, to: 443 }], applicability: 'unknown' }
  return { target: { ref: 'business_target_one', conversation_id: conversation, intent_revision: 1, job_id: 'job-one', snapshot_id: 'snapshot-one', scope, asset_ids: ['asset-one'] }, draft: { id: 'draft-one', snapshot_id: 'snapshot-one', policy_scope_ref: scope.ref, revision: 3, status: 'previewed', preview_id: 'preview-one', base_revisions: [{ asset_id: 'asset-one', revision: 0, profile_version_id: '', intent_version_id: '' }], created_at: '2026-10-29T01:00:00Z', updated_at: '2026-10-29T01:01:00Z', changes: [{ asset_id: 'asset-one', roles: [{ service_id: 'service-one', declared: true, role: 'web', protocol: 'tcp', ports: [{ from: 443, to: 443 }] }, { service_id: 'service-two', declared: false, role: 'management', protocol: 'tcp', ports: [{ from: 22, to: 22 }] }], access: { allowed_declared: false, exceptions_declared: false, allowed_complete: false, deny_all: false, allowed: [], services: [{ service_id: 'service-one', allowed_declared: false, required_declared: true, public_access: 'unknown', allowed_complete: false, deny_all: false, allowed: [], required_confirmed: true, required: [access] }, { service_id: 'service-two', allowed_declared: false, required_declared: false, public_access: 'unknown', allowed_complete: false, deny_all: false, allowed: [], required_confirmed: false, required: [] }], exceptions: [] } }] }, preview: { preview_id: 'preview-one', draft_id: 'draft-one', draft_revision: 3, base_revisions: [], confirmable_sections: ['roles', 'access'], section_dependencies: { access: ['roles'] }, validation_errors: [], diff: [{ asset_id: 'asset-one', section: 'roles', before: { services: [] }, after: { services: [{ id: 'service-one', role: 'web', protocol: 'tcp', ports: [{ from: 443, to: 443 }] }] } }], created_at: '2026-10-29T01:01:00Z' }, candidates: [{ ref: 'candidate-one', asset_id: 'asset-one', service_id: 'service-one', role: 'database', protocol: 'tcp', ports: [{ from: 443, to: 443 }], evidence: [{ document: 'input', field: '/assets/0' }], contradicting_evidence: [], unknowns: ['端口不证明用途'], questions: ['请确认服务用途'], decision: 'pending', conflicts_with_confirmed: true }] }
}
function resultFixture(context = contextFixture()): ConversationBusinessResult { return { state: 'preview_required', reply: '草稿已保存，请核对差异后明确分项确认。', ...cloneData(context), unknown_fields: ['management_sources'], missing_inputs: ['required_sources'], replayed: false } }
function setup() {
  const conversationID = ref('conversation-one'), unlocked = ref(true), context = { value: contextFixture() }, calls: { kind: string; id: string; body?: unknown; key?: string }[] = []
  const handlers = { read: async (_id: string): Promise<ConversationBusinessContext | null> => cloneData(context.value), turn: async (id: string, _request: IntentTurnRequest, _key: string): Promise<IntentTurnResponse> => ({ ...intentResponseFixture(id), business: resultFixture(context.value) }) }
  const workspace = { unlocked, conversationID, currentIntent: ref(intentFixture()), captureContext: () => { const owner = conversationID.value; return () => unlocked.value && conversationID.value === owner }, api: { businessContext: async (id: string) => { calls.push({ kind: 'read', id }); return handlers.read(id) }, intentTurn: async (id: string, request: IntentTurnRequest, key: string) => { calls.push({ kind: 'turn', id, body: cloneData(request), key }); return handlers.turn(id, request, key) } }, applyIntentResponse: (response: IntentTurnResponse) => calls.push({ kind: 'apply', id: response.conversation.id }), loadMessages: async () => { calls.push({ kind: 'messages', id: conversationID.value }) } }
  const controller = createBusinessController(workspace as unknown as Parameters<typeof createBusinessController>[0], () => conversationID.value, value => value instanceof Error ? value.message : '受控错误')
  return { controller, workspace, context, handlers, calls }
}

test('business context is read-only and scoped; typed business actions use the existing persistent intent turn', async t => {
  const calls: { url: string; method: string; body?: unknown; key: string | null }[] = []
  t.mock.method(globalThis, 'fetch', async (url: string, init: RequestInit) => { calls.push({ url, method: init.method ?? 'GET', body: init.body ? JSON.parse(String(init.body)) : undefined, key: new Headers(init.headers).get('Idempotency-Key') }); return Response.json(url.endsWith('/business-context') ? contextFixture() : { ...intentResponseFixture(), business: resultFixture() }) })
  const api = new WorkspaceClient(() => 'public-local-token')
  assert.equal((await api.businessContext('conversation-one'))?.draft?.revision, 3)
  const saved = await api.intentTurn('conversation-one', { client_turn_id: 'business-turn', text: '', business: { action: 'preview', expected_target_ref: 'business_target_one' } }, 'original-business-key')
  assert.equal(saved.business?.preview?.preview_id, 'preview-one'); assert.equal(calls[0]?.method, 'GET'); assert.equal(calls[0]?.body, undefined)
  assert.equal(calls[1]?.url, '/api/v1/conversations/conversation-one/intent-turns'); assert.equal(calls[1]?.key, 'original-business-key')
  assert.equal((calls[1]?.body as IntentTurnRequest).business?.action, 'preview')
})

test('business decoder refuses foreign targets, stale previews and cross-scope access declarations', () => {
  const original = contextFixture()
  assert.equal(decodeBusinessContext(original, 'conversation-one')?.draft?.id, 'draft-one')
  assert.throws(() => decodeBusinessContext(original, 'conversation-two'), /会话/)
  const stale = contextFixture(); stale.preview!.draft_revision = 2; assert.throws(() => decodeBusinessContext(stale, 'conversation-one'), /草稿修订/)
  const foreign = contextFixture(); foreign.draft!.changes[0]!.asset_id = 'foreign-asset'; assert.throws(() => decodeBusinessContext(foreign, 'conversation-one'), /对象/)
  const wrongScope = contextFixture(); wrongScope.draft!.changes[0]!.access!.services[0]!.required[0]!.scope_ref = 'scope-other'; assert.throws(() => decodeBusinessContext(wrongScope, 'conversation-one'), /其他范围/)
  const badCandidate = contextFixture(); badCandidate.candidates[0]!.asset_id = 'foreign-asset'; assert.throws(() => decodeBusinessContext(badCandidate, 'conversation-one'), /当前对象/)
  assert.equal(decodeBusinessContext(null, 'conversation-one'), null)
  assert.throws(() => decodeBusinessResult({ ...resultFixture(), target: null }, 'conversation-one'), /缺少对象/)
})

test('confirmation requires explicit sections and dependencies never auto-select roles or access', () => {
  const context = contextFixture()
  assert.throws(() => businessConfirmRequest(context, []), /明确选择/)
  const selected = ['access']; assert.throws(() => businessConfirmRequest(context, selected), /不会自动勾选/); assert.deepEqual(selected, ['access'])
  const body = businessConfirmRequest(context, ['roles'])
  assert.deepEqual(body.sections, ['roles']); assert.equal(body.preview_id, 'preview-one'); assert.equal(body.expected_draft_revision, 3); assert.equal(body.expected_target_ref, 'business_target_one')
  assert.equal(body.patches, undefined)
})

test('editing one form field declares only its role, allowed, required or exception collection', () => {
  const context = contextFixture(), changes = context.draft!.changes, change = changes[0]!
  change.roles[0]!.declared = false
  change.access!.services[0]!.required_declared = false
  markBusinessDeclaration(changes, change.roles[0]!)
  assert.equal(change.roles[0]!.declared, true); assert.equal(change.roles[1]!.declared, false); assert.equal(change.access!.allowed_declared, false)
  markBusinessDeclaration(changes, change.access!.services[0]!.required[0]!)
  assert.equal(change.access!.services[0]!.required_declared, true); assert.equal(change.access!.services[0]!.allowed_declared, false); assert.equal(change.access!.services[1]!.allowed_declared, false)
  markBusinessDeclaration(changes, change.access!.services[1]!, 'allowed')
  assert.equal(change.access!.services[1]!.allowed_declared, true); assert.equal(change.access!.services[1]!.required_declared, false)
  change.access!.exceptions = [{ check_id: 'CHK-07', subject_ref: 'asset-one', scope_ref: 'scope-one', service_ref: 'service-one', expires_at: '', authorization_source: '', reason: '' }]
  markBusinessDeclaration(changes, change.access!.exceptions[0]!)
  assert.equal(change.access!.exceptions_declared, true); assert.equal(change.access!.allowed_declared, false)
})

test('unknown allowed and required sources remain visible and differ from an explicit complete empty set', () => {
  const change = contextFixture().draft!.changes[0]!
  assert.match(businessAllowedLabel(change.access), /未知/); assert.ok(!businessAllowedLabel(change.access).includes('不允许所审访问'))
  assert.match(businessRequiredLabel(change.access!.services[0]), /443/); assert.match(businessRequiredLabel(change.access!.services[0]), /来源／目标未知/)
  assert.match(businessAllowedLabel({ ...change.access!, deny_all: true, allowed_complete: true }), /明确空集/)
})

test('unknown business acknowledgement retains exact target, body, sections and key for one explicit retry', async () => {
  const subject = setup(); await subject.controller.load(); let attempt = 0
  subject.handlers.turn = async id => { if (!attempt++) throw new TypeError('人工丢失受理答复'); return { ...intentResponseFixture(id), business: resultFixture(subject.context.value) } }
  await subject.controller.submit(businessConfirmRequest(subject.context.value, ['roles']))
  const original = cloneData(subject.calls.find(call => call.kind === 'turn'))
  assert.equal(subject.controller.editor.value.mutation.state, 'unsaved')
  subject.context.value.draft!.revision = 7; subject.context.value.preview = null
  await subject.controller.load()
  assert.equal(subject.calls.filter(call => call.kind === 'turn').length, 1)
  await subject.controller.submit()
  assert.deepEqual(subject.calls.filter(call => call.kind === 'turn')[1], original)
  assert.equal(subject.controller.editor.value.mutation.pendingBody, null)
})

test('late business reads and mutation replies cannot change another conversation or its messages', async () => {
  const subject = setup(); let releaseRead!: (value: ConversationBusinessContext) => void
  subject.handlers.read = async () => new Promise(resolve => { releaseRead = resolve })
  const read = subject.controller.load(); subject.workspace.conversationID.value = 'conversation-two'; releaseRead(contextFixture()); await read
  assert.equal(subject.controller.editor.value.context, null)
  subject.workspace.conversationID.value = 'conversation-one'; subject.handlers.read = async () => contextFixture(); await subject.controller.load()
  let releaseTurn!: (value: IntentTurnResponse) => void
  subject.handlers.turn = async () => new Promise(resolve => { releaseTurn = resolve })
  const pending = subject.controller.submit({ action: 'accept_candidate', candidate_ref: 'candidate-one' })
  subject.workspace.conversationID.value = 'conversation-two'
  releaseTurn({ ...intentResponseFixture(), business: resultFixture() }); await pending
  assert.equal(subject.controller.editor.value.context, null); assert.ok(!subject.calls.some(call => call.kind === 'apply' || call.kind === 'messages'))
  assert.equal(subject.controller.editors.get('conversation-one')!.mutation.state, 'saved')
})

test('a changed form revision invalidates all selected sections before a new preview', async () => {
  const subject = setup(); await subject.controller.load(); subject.controller.editor.value.selected = ['roles', 'access']
  subject.context.value.draft!.revision = 4; subject.context.value.draft!.status = 'draft'; subject.context.value.draft!.preview_id = ''; subject.context.value.preview = null
  await subject.controller.load(); assert.deepEqual(subject.controller.editor.value.selected, []); assert.equal(subject.controller.editor.value.context?.preview, null)
})

test('business candidate reject and unknown carry server-issued references without proposing access changes', async () => {
  const subject = setup(); await subject.controller.load()
  for (const action of ['reject_candidate', 'keep_unknown'] as const) await subject.controller.submit({ action, candidate_ref: 'candidate-one', ...(action === 'keep_unknown' ? { unknown_fields: ['role'] } : {}) })
  for (const call of subject.calls.filter(item => item.kind === 'turn')) { const request = (call.body as IntentTurnRequest).business!; assert.equal(request.candidate_ref, 'candidate-one'); assert.equal(request.expected_target_ref, 'business_target_one'); assert.equal(request.patches, undefined); assert.equal(request.sections, undefined) }
})

test('actual business card renders readable fixed evidence and unchecked sections without raw JSON', async () => {
  const filename = new URL('./ConversationBusinessPanel.vue', import.meta.url), { descriptor } = parse(await readFile(filename, 'utf8'), { filename: filename.pathname })
  const compiled = compileScript(descriptor, { id: 'business-card-test', inlineTemplate: true })
  const js = ts.transpileModule(compiled.content, { compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.CommonJS } }).outputText
  const saved = contextFixture(), editor = Vue.ref({ context: saved, selected: [], loading: false, busy: false, error: '', message: '', mutation: new workspaceState.Mutation() })
  let reads = 0
  const workspace = { unlocked: Vue.ref(true), conversationID: Vue.ref('conversation-one'), currentIntent: Vue.ref(intentFixture()), drafts: new Map(), messages: Vue.ref([]), jobs: Vue.ref([]) }
  const controller = { editor, editors: new Map(), load: async () => { reads++ }, submit: async (_body?: ConversationBusinessRequest) => {}, confirm: async () => {}, dispose: () => {} }
  const modules: Record<string, unknown> = { vue: Vue, './business-view': { ...businessView, createBusinessController: () => controller }, './workspace-state': workspaceState, './workspace': { useWorkspace: () => workspace, errorText: (cause: unknown) => String(cause), formatTime: (value: string) => value || '时间未知', scopeLabel: () => '互联网入向' } }
  const module = { exports: {} as { default?: Vue.Component } }
  new Function('require', 'exports', 'module', js)((name: string) => { if (!(name in modules)) throw new Error(name); return modules[name] }, module.exports, module)
  const app = Vue.createSSRApp(module.exports.default!, { conversationId: 'conversation-one' }); app.component('RouterLink', Vue.defineComponent({ setup: (_props, context) => () => Vue.h('a', context.attrs, context.slots.default?.()) }))
  const html = await renderToString(app)
  for (const text of ['业务资料', '草稿修订 3', '同一表单中编辑', '用途', '加入草稿并预览', '拒绝候选', '保留未知', '确认勾选的部分', '来源／目标未知', '原版本', '待确认']) assert.ok(html.includes(text), text)
  assert.equal(reads, 1); assert.ok(!/<input[^>]+checked/.test(html)); assert.ok(!html.includes('<pre')); assert.ok(!html.includes('JSON.stringify')); assert.ok(!html.includes('&quot;services&quot;'))
})
