import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { parse, compileScript } from '@vue/compiler-sfc'
import ts from 'typescript'
import * as Vue from 'vue'
import { renderToString } from 'vue/server-renderer'
import { createMemoryHistory, createRouter } from 'vue-router'
import * as agentView from './agent-view.ts'
import { agentFixture } from './test-fixtures/agent.ts'

async function render(props: Record<string, unknown>): Promise<string> {
  const filename = new URL('./AgentResults.vue', import.meta.url)
  const source = await readFile(filename, 'utf8')
  const { descriptor } = parse(source, { filename: filename.pathname })
  const compiled = compileScript(descriptor, { id: 'agent-results-test', inlineTemplate: true })
  const js = ts.transpileModule(compiled.content, { compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.CommonJS } }).outputText
  const module = { exports: {} as { default?: Vue.Component } }
  const imports: Record<string, unknown> = { vue: Vue, './agent-view': agentView }
  new Function('require', 'exports', 'module', js)((name: string) => { if (!(name in imports)) throw new Error(`unexpected component import ${name}`); return imports[name] }, module.exports, module)
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/inventory/:id?', name: 'inventory', component: {} }] })
  await router.push('/inventory')
  const app = Vue.createSSRApp(module.exports.default!, props)
  app.use(router)
  return renderToString(app)
}

test('component renders distinct explanation, candidates and all four conditional recommendation kinds', async () => {
  const record = agentFixture()
  record.content.output!.recommendations = ['policy_change', 'coverage_followup', 'component_consideration', 'confirm_information'].map(kind => ({ ...record.content.output!.recommendations[0]!, kind: kind as never }))
  const before = JSON.stringify(record)
  const html = await render({ jobId: 'job-one', record, snapshotId: 'snapshot-real' })
  for (const value of ['模型补充解释，保留原始规则。', '待确认', '策略调整建议', '保护覆盖核实', '组件适用性评估', '补充／确认资料', '先核对用途', '业务要求会影响审计依据', '人工打开资产表单核对', '实际监听未核实', '是否有已确认的允许来源？']) assert.ok(html.includes(value), value)
  assert.match(html, /snapshot_id=snapshot-real/)
  assert.match(html, /service=service-real/)
  assert.equal(JSON.stringify(record), before)
  assert.ok(!html.includes('自动下发成功'))
})

test('old or partial results keep missing usage unknown and do not manufacture a model completion', async () => {
  const html = await render({ jobId: 'job-one' })
  assert.match(html, /尚无已保存的模型结构结果/)
  assert.match(html, /未知/)
  assert.ok(!html.includes('已保存模型补充'))
  const failed = agentFixture()
  failed.content.output = null
  failed.content.error_code = 'MODEL_OUTPUT_TRUNCATED'
  const failure = await render({ jobId: 'job-one', record: failed, usage: { cloud_attempts: 0, model_attempts: 2, unknown_delivery: 1, known_usage_responses: 0, unknown_usage_attempts: 2, reported_tokens: null, usage_totals_overflow: false } })
  assert.match(failure, /MODEL_OUTPUT_TRUNCATED/)
  assert.match(failure, /2 次模型尝试未报告用量/)
  assert.match(failure, /本任务没有经校验的模型补充输出/)
  assert.ok(!failure.includes('已保存的规则结果仍可审阅'))
  assert.ok(!failure.includes('0 token'))
})

test('foreign job bindings and model markup cannot create asset actions or HTML', async () => {
  const record = agentFixture('other-job')
  const foreign = await render({ jobId: 'job-one', record, snapshotId: 'snapshot-real' })
  assert.ok(!foreign.includes('/inventory/asset-real'))
  assert.ok(!foreign.includes('模型补充解释，保留原始规则。'))
  const current = agentFixture()
  current.content.output!.explanation = '<script>untrusted()</script>'
  const html = await render({ jobId: 'job-one', record: current })
  assert.ok(!html.includes('<script>'))
  assert.ok(html.includes('&lt;script&gt;'))
})
