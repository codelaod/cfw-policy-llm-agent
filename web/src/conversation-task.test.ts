import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { compileScript, parse } from '@vue/compiler-sfc'
import ts from 'typescript'
import * as Vue from 'vue'
import * as api from './api.ts'
import * as agentView from './agent-view.ts'
import type { Job, JobEvent, JobResult } from './workspace-types.ts'

const job = (id: string): Job => ({ id, conversation_id: 'conversation-one', status: 'running', view_seq: 2, created_at: '2026-10-29T01:00:00Z' })
const result = (seq: number): JobResult => ({ audit: null, is_final: false, view_seq: seq })
async function setup() {
  const filename = new URL('./ConversationTaskStatus.vue', import.meta.url)
  const { descriptor } = parse(await readFile(filename, 'utf8'), { filename: filename.pathname })
  const compiled = compileScript(descriptor, { id: 'conversation-task-test' })
  const js = ts.transpileModule(compiled.content, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } }).outputText
  const props = Vue.reactive({ jobId: 'job-one', conversationId: 'conversation-one' }), reads: string[] = [], cancellations: string[] = [], states: { mode: string; stopped: boolean; callbacks: { event: (event: JobEvent) => void; job: (job: Job) => void; state: (state: string) => void } }[] = []
  const handlers = { result: async (_id: string) => result(2), cancel: async (id: string): Promise<Job> => ({ ...job(id), status: 'cancelled' as const, view_seq: 3 }) }
  const workspace = { api: { transport: {}, job: async (id: string) => job(id), jobResult: async (id: string) => { reads.push(id); return handlers.result(id) }, cancelJob: async (id: string) => { cancellations.push(id); return handlers.cancel(id) } }, updateJob: () => {}, loadMessages: async () => {}, loadIntent: async () => {}, lock: () => {} }
  class Observer {
    state: typeof states[number]
    constructor(_transport: unknown, _id: string, _seq: number, callbacks: typeof states[number]['callbacks']) { this.state = { mode: '', stopped: false, callbacks }; states.push(this.state) }
    stop() { this.state.stopped = true }
    async start(mode: string) { this.state.mode = mode }
  }
  const modules: Record<string, unknown> = { './CloudResults.vue': {}, './BatchResults.vue': {}, './PreparationResults.vue': {}, vue: { ...Vue, watch: () => {}, onBeforeUnmount: () => {} }, './events': { EventObserver: Observer }, './api': api, './agent-view': agentView, './workspace': { useWorkspace: () => workspace, errorText: (value: unknown) => value instanceof Error ? value.message : '受控错误', formatTime: (value: string) => value, jobLabel: (value: string) => value, scopeLabel: () => '固定范围', sourceLabel: (value: string) => value, verdictLabel: (value: string) => value }, '@nuxt/ui/components/Button.vue': {} }
  const module = { exports: {} as { default?: { setup: (...args: unknown[]) => unknown } } }
  new Function('require', 'exports', 'module', js)((name: string) => { if (!(name in modules)) throw new Error(`unexpected conversation task dependency ${name}`); return modules[name] }, module.exports, module)
  const view = module.exports.default!.setup(props, { expose() {} }) as { job: Vue.Ref<Job | null>; result: Vue.Ref<JobResult | null>; events: Vue.Ref<JobEvent[]>; cancelling: Vue.Ref<boolean>; currentStage: Vue.Ref<string>; load: () => Promise<void>; cancel: () => Promise<void>; observe: (mode?: string) => void; pause: () => void }
  return { props, reads, cancellations, states, handlers, view }
}
test('conversation task observation and polling are read-only; pause never cancels', async () => {
  const subject = await setup(); await subject.view.load(); subject.view.pause(); subject.view.observe('polling')
  assert.deepEqual(subject.reads, ['job-one']); assert.deepEqual(subject.cancellations, []); assert.equal(subject.states[0]!.stopped, true); assert.equal(subject.states[1]!.mode, 'polling')
})
test('switching task waits for and discards an older pending result then reads the new task', async () => {
  const subject = await setup(); let release!: (value: JobResult) => void
  subject.handlers.result = async id => id === 'job-one' ? new Promise(resolve => { release = resolve }) : result(5)
  const old = subject.view.load(); await Promise.resolve(); subject.props.jobId = 'job-two'; const next = subject.view.load(); await Promise.resolve(); release(result(99)); await Promise.all([old, next])
  assert.deepEqual(subject.reads, ['job-one', 'job-two']); assert.equal(subject.view.job.value?.id, 'job-two'); assert.equal(subject.view.result.value?.view_seq, 5)
})
test('late cancellation response stays bound to its original task and cannot disable the new task controls', async () => {
  const subject = await setup(); let release!: (value: Job) => void
  await subject.view.load(); subject.handlers.cancel = async () => new Promise(resolve => { release = resolve })
  const pending = subject.view.cancel(); subject.props.jobId = 'job-two'; await subject.view.load(); release({ ...job('job-one'), status: 'cancelled', view_seq: 8 }); await pending
  assert.deepEqual(subject.cancellations, ['job-one']); assert.equal(subject.view.job.value?.id, 'job-two'); assert.equal(subject.view.job.value?.status, 'running'); assert.equal(subject.view.cancelling.value, false)
  subject.states[0]!.callbacks.job({ ...job('job-one'), status: 'completed', view_seq: 99 }); assert.equal(subject.view.job.value?.status, 'running')
})
test('a batch child cancels its parent while retaining the displayed child record', async () => {
  const subject = await setup(); await subject.view.load(); subject.view.job.value!.parent_job_id = 'parent-one'; await subject.view.cancel()
  assert.deepEqual(subject.cancellations,['parent-one']); assert.equal(subject.view.job.value?.id,'job-one'); assert.equal(subject.view.job.value?.status,'running')
})
