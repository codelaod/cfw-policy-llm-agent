import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { compileScript, parse } from '@vue/compiler-sfc'
import ts from 'typescript'
import * as Vue from 'vue'
import * as api from './api.ts'
import * as cloudView from './cloud-view.ts'
import * as state from './workspace-state.ts'
import { cloudConversationFixture, cloudOptionsFixture } from './test-fixtures/cloud.ts'
import type { ExecutionPlan, PlanRequest } from './workspace-types.ts'

const plan = (): ExecutionPlan => ({ id: 'plan-cloud', expires_at: '2099-01-01T00:00:00Z', effect_ceiling: { allow_cloud_reads: true, cloud_http_attempts: 40, allow_model_requests: false, model_http_attempts: 0 }, warnings: [], gaps: [] })
async function setup(previewPlan: (request: PlanRequest) => Promise<ExecutionPlan> = async () => plan(), optionsValue = cloudOptionsFixture()) {
  const filename = new URL('./CloudCollection.vue', import.meta.url)
  const { descriptor } = parse(await readFile(filename, 'utf8'), { filename: filename.pathname })
  const compiled = compileScript(descriptor, { id: 'cloud-actions-test' })
  const js = ts.transpileModule(compiled.content, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } }).outputText
  const reads: string[] = [], starts: { id: string; key: string }[] = [], routes: string[] = []
  const conversations = Vue.ref([cloudConversationFixture()])
  const workspace = { drafts: new Map(), conversations, listHasMore: Vue.ref({ conversations: false }), selectConversation: async () => {}, updateJob: () => {}, api: { sources: async () => { reads.push('sources'); return { items: [{ id: 'cloud-local', label: '只读连接', kind: 'aliyun', available: true, cloud_reads: true }] } }, collectionOptions: async () => { reads.push('options'); return optionsValue }, previewPlan, startJob: async (id: string, key: string) => { starts.push({ id, key }); if (starts.length === 1) throw new TypeError('synthetic lost acknowledgement'); return { id: 'job-cloud', conversation_id: 'conversation-cloud', status: 'queued', view_seq: 1, created_at: '2026-10-28T00:00:00Z' } } } }
  const modules: Record<string, unknown> = { vue: { ...Vue, onMounted: () => {}, onBeforeUnmount: () => {} }, 'vue-router': { useRouter: () => ({ push: async (path: string) => { routes.push(path) } }), onBeforeRouteLeave: () => {} }, '@nuxt/ui/components/Button.vue': {}, './api': api, './cloud-view': cloudView, './workspace-state': state, './workspace': { useWorkspace: () => workspace, errorText: (value: unknown) => value instanceof Error ? value.message : 'controlled error', scopeLabel: () => '所选范围', formatTime: (value: string) => value } }
  const module = { exports: {} as { default?: { setup: (...args: unknown[]) => unknown } } }
  new Function('require', 'exports', 'module', js)((name: string) => { if (!(name in modules)) throw new Error(`unexpected collection dependency ${name}`); return modules[name] }, module.exports, module)
  const effect = Vue.effectScope()
  const view = effect.run(() => module.exports.default!.setup({}, { expose() {} })) as { sourceID: Vue.Ref<string>; scopeID: Vue.Ref<string>; conversationID: Vue.Ref<string>; selected: Vue.Ref<string[]>; kind: Vue.Ref<'collect'|'connection_check'>; canPreview: Vue.Ref<boolean>; error: Vue.Ref<string>; plan: state.PlanDraft; loadSources: () => Promise<void>; preview: () => Promise<void>; start: () => Promise<void> }
  async function select() { await view.loadSources(); view.sourceID.value = 'cloud-local'; await Vue.nextTick(); await Vue.nextTick(); view.scopeID.value = 'scope-in'; view.selected.value = ['asset-one']; view.conversationID.value = 'conversation-cloud' }
  return { view, select, reads, starts, routes, conversations, dispose: () => effect.stop() }
}

test('local catalog and preview never start a cloud job, and a lost acceptance retries only its original plan/key', async () => {
  const subject = await setup()
  try {
    assert.equal(subject.view.canPreview.value, false)
    await subject.select()
    assert.deepEqual(subject.reads, ['sources', 'options'])
    await subject.view.preview()
    assert.equal(subject.view.plan.preview?.id, 'plan-cloud')
    assert.deepEqual(subject.starts, [])
    await subject.view.start()
    assert.equal(subject.routes.length, 0)
    await subject.view.start()
    assert.equal(subject.starts.length, 2)
    assert.deepEqual(subject.starts[0], subject.starts[1])
    assert.deepEqual(subject.routes, ['/audits/job-cloud'])
  } finally { subject.dispose() }
})

test('a conversation revision change invalidates an in-flight cloud preview', async () => {
  let release!: (value: ExecutionPlan) => void
  const subject = await setup(() => new Promise(resolve => { release = resolve }))
  try { await subject.select(); const pending = subject.view.preview(); subject.conversations.value[0]!.revision++; release(plan()); await pending; assert.equal(subject.view.plan.preview, null); assert.equal(subject.starts.length, 0) }
  finally { subject.dispose() }
})

test('a preview that broadens model effects cannot become an accepted cloud plan', async () => {
  const subject = await setup(async () => ({ ...plan(), effect_ceiling: { allow_cloud_reads: true, cloud_http_attempts: 40, allow_model_requests: true, model_http_attempts: 1 } }))
  try { await subject.select(); await subject.view.preview(); assert.equal(subject.view.plan.preview, null); assert.match(subject.view.error.value, /调用上限/); await subject.view.start(); assert.deepEqual(subject.starts, []) }
  finally { subject.dispose() }
})

test('switching the local source discards a late preview without leaking an old-source error', async () => {
  let release!: (value: ExecutionPlan) => void
  const subject = await setup(() => new Promise(resolve => { release = resolve }))
  try { await subject.select(); const pending = subject.view.preview(); subject.view.sourceID.value = ''; await Vue.nextTick(); release(plan()); await pending; assert.equal(subject.view.plan.preview, null); assert.equal(subject.view.error.value, '') }
  finally { subject.dispose() }
})
test('an identity-only preview accepts its explicit server HTTP ceiling above legacy six without starting work',async()=>{
 const options=cloudOptionsFixture();options.max_http_attempts=250
 const subject=await setup(async()=>({...plan(),effect_ceiling:{allow_cloud_reads:true,allow_model_requests:false,cloud_http_attempts:200,model_http_attempts:0}}),options)
 try{await subject.select();subject.view.kind.value='connection_check';await subject.view.preview();assert.equal(subject.view.plan.preview?.id,'plan-cloud');assert.deepEqual(subject.starts,[])}finally{subject.dispose()}
})
test('an empty configured asset directory can still preview the explicit identity-only selection',async()=>{
 const options=cloudOptionsFixture();options.scopes[0]!.assets=[];let captured:PlanRequest|undefined
 const subject=await setup(async request=>{captured=request;return {...plan(),effect_ceiling:{allow_cloud_reads:true,allow_model_requests:false,cloud_http_attempts:1,model_http_attempts:0}}},options)
 try{await subject.select();subject.view.selected.value=[];subject.view.kind.value='connection_check';assert.equal(subject.view.canPreview.value,true);await subject.view.preview();assert.deepEqual(captured?.selection,{kind:'identity_only',asset_ids:[]});assert.equal(subject.view.plan.preview?.id,'plan-cloud');assert.deepEqual(subject.starts,[])}finally{subject.dispose()}
})
