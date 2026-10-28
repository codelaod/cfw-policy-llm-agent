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
import type { JobResult } from './workspace-types.ts'
import { cloudRecordFixture } from './test-fixtures/cloud.ts'

const job = (id: string, status: Job['status'] = 'running', seq = 1): Job => ({ id, conversation_id: 'conversation-one', status, view_seq: seq, created_at: '2026-10-28T00:00:00Z' })
async function setup(cancelJob: () => Promise<Job>, selectSnapshot?: (id: string) => Promise<void>) {
  const filename = new URL('./pages/AuditsPage.vue', import.meta.url)
  const { descriptor } = parse(await readFile(filename, 'utf8'), { filename: filename.pathname })
  const compiled = compileScript(descriptor, { id: 'audit-actions-test' })
  const js = ts.transpileModule(compiled.content, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } }).outputText
  const route = Vue.reactive({ params: { id: '' }, query: {} })
  const selections: string[] = [], routes: unknown[] = []
  const workspace = { snapshotID: Vue.ref(''), conversationID: Vue.ref(''), currentSnapshot: Vue.ref(null), drafts: new Map(), api: { cancelJob, job: async (id: string) => job(id), jobResult: async () => ({ audit: null, view_seq: 1, is_final: false }) }, updateJob: () => {}, selectSnapshot: async (id: string) => { selections.push(id); await selectSnapshot?.(id); workspace.snapshotID.value = id } }
  class Observer { stop() {} async start() {} }
  const modules: Record<string, unknown> = { vue: { ...Vue, onBeforeUnmount: () => {} }, 'vue-router': { useRoute: () => route, useRouter: () => ({ push: async (target: unknown) => { routes.push(target) } }), onBeforeRouteLeave: () => {} }, '@nuxt/ui/components/Button.vue': {}, '../AgentResults.vue': {}, '../CloudResults.vue': {}, '../events': { EventObserver: Observer }, '../api': api, '../agent-view': agentView, '../cloud-view': cloudView, '../workspace-state': state, '../workspace': { useWorkspace: () => workspace, errorText: (value: unknown) => value instanceof Error ? value.message : 'controlled failure' } }
  const module = { exports: {} as { default?: { setup: (...args: unknown[]) => Record<string, unknown> } } }
  new Function('require', 'exports', 'module', js)((name: string) => { if (!(name in modules)) throw new Error(`unexpected page dependency ${name}`); return modules[name] }, module.exports, module)
  const scope = Vue.effectScope()
  const view = scope.run(() => module.exports.default!.setup({}, { expose() {} })) as unknown as { job: Vue.Ref<Job | null>; result: Vue.Ref<JobResult | null>; error: Vue.Ref<string>; busy: Vue.Ref<boolean>; cancel: () => Promise<void>; pause: () => void; load: (id: string) => Promise<void>; selectCloudSnapshot: (id: string, destination: 'inventory' | 'audits') => Promise<void> }
  return { view, selections, routes, dispose: () => scope.stop() }
}

test('a late cancel snapshot cannot roll a committed terminal task back to cancelling', async () => {
  let release!: (value: Job) => void
  const { view, dispose } = await setup(() => new Promise(resolve => { release = resolve }))
  try {
    view.job.value = job('job-old', 'running', 2)
    const request = view.cancel()
    view.job.value = job('job-old', 'cancelled', 5)
    release(job('job-old', 'cancelling', 3))
    await request
    assert.equal(view.job.value.status, 'cancelled')
    assert.equal(view.job.value.view_seq, 5)
  } finally { dispose() }
})

test('a late cancel error is not displayed under a different task', async () => {
  let reject!: (reason: Error) => void
  const { view, dispose } = await setup(() => new Promise((_resolve, fail) => { reject = fail }))
  try {
    view.job.value = job('job-old')
    const request = view.cancel()
    await view.load('job-new')
    reject(new api.ApiError(503, 'SERVER_STOPPING'))
    await request
    assert.equal(view.job.value?.id, 'job-new')
    assert.equal(view.error.value, '')
  } finally { dispose() }
})

test('pausing observation does not discard the acknowledged cancel of that same task', async () => {
  let release!: (value: Job) => void
  const { view, dispose } = await setup(() => new Promise(resolve => { release = resolve }))
  try {
    view.job.value = job('job-old', 'running', 1)
    const request = view.cancel()
    view.pause()
    release(job('job-old', 'cancelling', 2))
    await request
    assert.equal(view.job.value.status, 'cancelling')
    assert.equal(view.job.value.view_seq, 2)
  } finally { dispose() }
})

test('a cloud result only selects its own returned snapshot and opens a separate audit preview', async () => {
  const subject = await setup(async () => job('unused'))
  try { subject.view.job.value = { ...job('job-cloud', 'completed'), kind: 'collect' }; subject.view.result.value = { is_final: true, view_seq: 5, snapshot_id: 'snapshot-cloud', cloud_result: cloudRecordFixture() }; await subject.view.selectCloudSnapshot('snapshot-other', 'audits'); assert.deepEqual(subject.selections, []); await subject.view.selectCloudSnapshot('snapshot-cloud', 'audits'); assert.deepEqual(subject.selections, ['snapshot-cloud']); assert.deepEqual(subject.routes, ['/audits']); assert.equal(subject.view.error.value, '') }
  finally { subject.dispose() }
})

test('a late cloud snapshot selection cannot navigate away from a newly selected task', async () => {
  let release!: () => void
  const subject = await setup(async () => job('unused'), () => new Promise(resolve => { release = resolve }))
  try { subject.view.job.value = { ...job('job-cloud', 'completed'), kind: 'collect' }; subject.view.result.value = { is_final: true, view_seq: 5, snapshot_id: 'snapshot-cloud', cloud_result: cloudRecordFixture() }; const pending = subject.view.selectCloudSnapshot('snapshot-cloud', 'audits'); await subject.view.load('job-new'); release(); await pending; assert.equal(subject.view.job.value?.id, 'job-new'); assert.deepEqual(subject.routes, []) }
  finally { subject.dispose() }
})
