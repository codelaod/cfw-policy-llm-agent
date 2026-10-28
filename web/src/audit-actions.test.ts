import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { compileScript, parse } from '@vue/compiler-sfc'
import ts from 'typescript'
import * as Vue from 'vue'
import * as api from './api.ts'
import * as agentView from './agent-view.ts'
import * as state from './workspace-state.ts'
import type { Job } from './api.ts'

const job = (id: string, status: Job['status'] = 'running', seq = 1): Job => ({ id, conversation_id: 'conversation-one', status, view_seq: seq, created_at: '2026-10-28T00:00:00Z' })
async function setup(cancelJob: () => Promise<Job>) {
  const filename = new URL('./pages/AuditsPage.vue', import.meta.url)
  const { descriptor } = parse(await readFile(filename, 'utf8'), { filename: filename.pathname })
  const compiled = compileScript(descriptor, { id: 'audit-actions-test' })
  const js = ts.transpileModule(compiled.content, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } }).outputText
  const route = Vue.reactive({ params: { id: '' }, query: {} })
  const workspace = { snapshotID: Vue.ref(''), conversationID: Vue.ref(''), currentSnapshot: Vue.ref(null), drafts: new Map(), api: { cancelJob, job: async (id: string) => job(id), jobResult: async () => ({ audit: null, view_seq: 1, is_final: false }) }, updateJob: () => {} }
  class Observer { stop() {} async start() {} }
  const modules: Record<string, unknown> = { vue: { ...Vue, onBeforeUnmount: () => {} }, 'vue-router': { useRoute: () => route, useRouter: () => ({ push: async () => {} }), onBeforeRouteLeave: () => {} }, '@nuxt/ui/components/Button.vue': {}, '../AgentResults.vue': {}, '../events': { EventObserver: Observer }, '../api': api, '../agent-view': agentView, '../workspace-state': state, '../workspace': { useWorkspace: () => workspace, errorText: (value: unknown) => value instanceof Error ? value.message : 'controlled failure' } }
  const module = { exports: {} as { default?: { setup: (...args: unknown[]) => Record<string, unknown> } } }
  new Function('require', 'exports', 'module', js)((name: string) => { if (!(name in modules)) throw new Error(`unexpected page dependency ${name}`); return modules[name] }, module.exports, module)
  const scope = Vue.effectScope()
  const view = scope.run(() => module.exports.default!.setup({}, { expose() {} })) as unknown as { job: Vue.Ref<Job | null>; error: Vue.Ref<string>; busy: Vue.Ref<boolean>; cancel: () => Promise<void>; pause: () => void; load: (id: string) => Promise<void> }
  return { view, dispose: () => scope.stop() }
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
