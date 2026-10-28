import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { parse, compileScript } from '@vue/compiler-sfc'
import ts from 'typescript'
import * as Vue from 'vue'
import { renderToString } from 'vue/server-renderer'
import * as cloudView from './cloud-view.ts'
import * as agentView from './agent-view.ts'
import { cloudRecordFixture } from './test-fixtures/cloud.ts'

async function render(props: Record<string, unknown>) {
  const filename = new URL('./CloudResults.vue', import.meta.url)
  const { descriptor } = parse(await readFile(filename, 'utf8'), { filename: filename.pathname })
  const compiled = compileScript(descriptor, { id: 'cloud-results-test', inlineTemplate: true })
  const js = ts.transpileModule(compiled.content, { compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.CommonJS } }).outputText
  const modules: Record<string, unknown> = { vue: Vue, './cloud-view': cloudView, './agent-view': agentView, './workspace': { formatTime: (value: string) => value }, '@nuxt/ui/components/Button.vue': { default: Vue.defineComponent({ setup: (_props, context) => () => Vue.h('button', context.attrs, context.slots.default?.()) }) } }
  const module = { exports: {} as { default?: Vue.Component } }
  new Function('require', 'exports', 'module', js)((name: string) => { if (!(name in modules)) throw new Error(`unexpected result dependency ${name}`); return modules[name] }, module.exports, module)
  return renderToString(Vue.createSSRApp(module.exports.default!, props))
}

test('identity-only cloud results disclose partial identity scope without product or snapshot success', async () => {
  const record = cloudRecordFixture()
  record.content = { ...record.content, kind: 'connection_check', product_ready: false, snapshot_id: undefined, observations: [], limitations: ['IDENTITY_ONLY'] }
  const html = await render({ jobId: 'job-cloud', record })
  assert.match(html, /本次身份已核对/)
  assert.match(html, /仅核对调用身份/)
  assert.match(html, /产品权限、字段与完整性尚未验证/)
  assert.ok(!html.includes('选择这份资料'))
  assert.ok(!html.includes('已保存产品观测'))
})

test('collection results show immutable observation provenance and only explicit next-step actions', async () => {
  const record = cloudRecordFixture(), before = JSON.stringify(record)
  const html = await render({ jobId: 'job-cloud', record, snapshotId: 'snapshot-cloud', usage: { cloud_attempts: 9, model_attempts: 0, unknown_delivery: 0 } })
  for (const value of ['已保存产品观测', 'snapshot-cloud', '防火墙资产目录', '2', 'a'.repeat(64), '选择这份资料并确认业务', '选择这份资料并预览审计', '产品默认行为或策略语义尚未核实']) assert.ok(html.includes(value), value)
  assert.equal(JSON.stringify(record), before)
  assert.ok(!html.includes('自动审计完成'))
})

test('a foreign cloud record or conflicting snapshot cannot expose a selection action', async () => {
  const foreign = await render({ jobId: 'job-other', record: cloudRecordFixture(), snapshotId: 'snapshot-cloud' })
  assert.ok(!foreign.includes('选择这份资料'))
  assert.ok(!foreign.includes('本次身份已核对'))
  const mismatch = await render({ jobId: 'job-cloud', record: cloudRecordFixture(), snapshotId: 'snapshot-other' })
  assert.ok(!mismatch.includes('选择这份资料'))
})
