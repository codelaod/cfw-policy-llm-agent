import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { compileScript, parse } from '@vue/compiler-sfc'
import ts from 'typescript'
import * as Vue from 'vue'
import * as api from './api.ts'
import * as agentView from './agent-view.ts'
import * as cloudView from './cloud-view.ts'
import * as state from './workspace-state.ts'
import type { Job } from './api.ts'
import type { Conversation, ExecutionPlan, JobResult, Snapshot } from './workspace-types.ts'

const job = (id: string, status: Job['status'] = 'running', seq = 1): Job => ({ id, conversation_id: 'conversation-one', status, view_seq: seq, created_at: '2026-10-28T00:00:00Z' })
async function setup(startJob: () => Promise<Job>) {
  const filename = new URL('./pages/AuditsPage.vue', import.meta.url)
  const { descriptor } = parse(await readFile(filename, 'utf8'), { filename: filename.pathname })
  const compiled = compileScript(descriptor, { id: 'audit-actions-test' })
  const js = ts.transpileModule(compiled.content, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } }).outputText
  const route = Vue.reactive({ params: { id: '' }, query: {} })
  const selections: string[] = [], routes: unknown[] = [], cachedJobs: Job[] = []
  const handlers = { preview: async () => ({id:'plan-preview',expires_at:'2099-01-01T00:00:00Z'} as ExecutionPlan), ensure: async () => ({id:'conversation-one'} as Conversation) }
  const workspace = { snapshotID: Vue.ref(''), conversationID: Vue.ref('conversation-one'), currentSnapshot: Vue.ref<Snapshot | null>(null), ensureConversation: async () => handlers.ensure(), drafts: new Map(), api: { startJob, previewPlan: async () => handlers.preview(), job: async (id: string) => job(id), jobResult: async () => ({ audit: null, view_seq: 1, is_final: false }) }, updateJob: (value: Job) => { cachedJobs.push(value) }, selectSnapshot: async (id: string) => { selections.push(id); workspace.snapshotID.value = id } }
  class Observer { stop() {} async start() {} }
  const modules: Record<string, unknown> = { '../BatchResults.vue': {}, '../PreparationResults.vue': {}, '../GuidancePanel.vue': { default: Vue.defineComponent({ render: () => Vue.h('div', { class: 'shared-guidance' }) }) }, vue: { ...Vue, onBeforeUnmount: () => {} }, 'vue-router': { useRoute: () => route, useRouter: () => ({ push: async (target: unknown) => { routes.push(target) } }), onBeforeRouteLeave: () => {} }, '@nuxt/ui/components/Button.vue': {}, '../AgentResults.vue': {}, '../CloudResults.vue': {}, '../events': { EventObserver: Observer }, '../api': api, '../agent-view': agentView, '../cloud-view': cloudView, '../workspace-state': state, '../workspace': { useWorkspace: () => workspace, errorText: (value: unknown) => value instanceof Error ? value.message : 'controlled failure' } }
  const module = { exports: {} as { default?: { setup: (...args: unknown[]) => Record<string, unknown> } } }
  new Function('require', 'exports', 'module', js)((name: string) => { if (!(name in modules)) throw new Error(`unexpected page dependency ${name}`); return modules[name] }, module.exports, module)
  const scope = Vue.effectScope()
  const view = scope.run(() => module.exports.default!.setup({}, { expose() {} })) as unknown as { plan: state.PlanDraft; startMutation: state.Mutation<{plan_id:string}, Job>; preview: () => Promise<void>; start: () => Promise<void>; job: Vue.Ref<Job | null>; result: Vue.Ref<JobResult | null>; error: Vue.Ref<string>; busy: Vue.Ref<boolean>; cancel: () => Promise<void>; pause: () => void; load: (id: string) => Promise<void>; selectCloudSnapshot: (id: string, destination: 'inventory' | 'audits') => Promise<void> }
  return { view, selections, routes, cachedJobs, handlers, workspace, dispose: () => scope.stop() }
}

test('late audit start success preserves the newly selected historical task', async () => {
  let release!: (value: Job) => void
  const subject = await setup(() => new Promise(resolve => { release = resolve }))
  try {
    subject.view.plan.acceptPreview({id:'plan-old',expires_at:'2099-01-01T00:00:00Z'} as ExecutionPlan)
    const pending = subject.view.start()
    await subject.view.load('job-new')
    release(job('job-started'))
    await pending
    assert.equal(subject.view.job.value?.id, 'job-new')
    assert.equal(subject.cachedJobs.some(value => value.id === 'job-started'), true)
    assert.deepEqual(subject.routes, [])
  } finally { subject.dispose() }
})

test('late audit start failure is not displayed under a newly selected historical task', async () => {
  let reject!: (value: Error) => void
  const subject = await setup(() => new Promise((_resolve, fail) => { reject = fail }))
  try {
    subject.view.plan.acceptPreview({id:'plan-old',expires_at:'2099-01-01T00:00:00Z'} as ExecutionPlan)
    const pending = subject.view.start(), originalKey = subject.view.startMutation.key
    await subject.view.load('job-new')
    reject(new TypeError('artificial previous start response lost'))
    await pending
    assert.equal(subject.view.job.value?.id, 'job-new')
    assert.equal(subject.view.error.value, '')
    assert.equal(subject.view.startMutation.key, originalKey)
    assert.deepEqual(subject.view.startMutation.pendingBody, {plan_id:'plan-old'})
  } finally { subject.dispose() }
})

const snapshot = () => ({id:'snapshot-original', input:{assets:[{id:'asset-one'}], selection:{kind:'assets'}, scope:{ref:'scope-one',boundary:'internet',direction:'in'},baseline:{version:'baseline-one'}}} as Snapshot)
async function preparePreview(subject: Awaited<ReturnType<typeof setup>>) {subject.workspace.currentSnapshot.value=snapshot();subject.workspace.snapshotID.value='snapshot-original';await Vue.nextTick()}

test('late audit preview success cannot install its plan under a selected historical task',async()=>{
  const subject=await setup(async()=>job('unused'));let release!:(value:ExecutionPlan)=>void
  try{await preparePreview(subject);subject.handlers.preview=async()=>new Promise(resolve=>{release=resolve});const pending=subject.view.preview();await new Promise<void>(resolve=>setImmediate(resolve));await subject.view.load('job-new');release({id:'plan-old',expires_at:'2099-01-01T00:00:00Z'} as ExecutionPlan);await pending;assert.equal(subject.view.job.value?.id,'job-new');assert.equal(subject.view.plan.preview,null);assert.equal(subject.view.error.value,'')}
  finally{subject.dispose()}
})
test('late audit preview failure cannot display errors under another historical task',async()=>{
  const subject=await setup(async()=>job('unused'));let reject!:(cause:Error)=>void
  try{await preparePreview(subject);subject.handlers.preview=async()=>new Promise((_resolve,fail)=>{reject=fail});const pending=subject.view.preview();await new Promise<void>(resolve=>setImmediate(resolve));await subject.view.load('job-new');reject(new TypeError('artificial old preview failed'));await pending;assert.equal(subject.view.job.value?.id,'job-new');assert.equal(subject.view.error.value,'')}
  finally{subject.dispose()}
})
test('a changed selected source discards late preview and releases the original busy state',async()=>{
  const subject=await setup(async()=>job('unused'));let release!:(value:ExecutionPlan)=>void
  try{await preparePreview(subject);subject.handlers.preview=async()=>new Promise(resolve=>{release=resolve});const pending=subject.view.preview();await new Promise<void>(resolve=>setImmediate(resolve));subject.workspace.currentSnapshot.value={...snapshot(),id:'snapshot-other'};subject.workspace.snapshotID.value='snapshot-other';release({id:'plan-old',expires_at:'2099-01-01T00:00:00Z'} as ExecutionPlan);await pending;assert.equal(subject.view.plan.preview,null);assert.equal(subject.view.error.value,'');assert.equal(subject.view.busy.value,false)}
  finally{subject.dispose()}
})
test('an unrelated selected conversation prevents an in-flight local preview request',async()=>{
  const subject=await setup(async()=>job('unused'));let release!:(value:Conversation)=>void,calls=0
  try{await preparePreview(subject);subject.handlers.ensure=async()=>new Promise(resolve=>{release=resolve});subject.handlers.preview=async()=>{calls++;return{id:'plan-old',expires_at:'2099-01-01T00:00:00Z'} as ExecutionPlan};const pending=subject.view.preview();subject.workspace.conversationID.value='conversation-other';release({id:'conversation-one'} as Conversation);await pending;assert.equal(calls,0);assert.equal(subject.view.plan.preview,null);assert.equal(subject.view.busy.value,false)}
  finally{subject.dispose()}
})
