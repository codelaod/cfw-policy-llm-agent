import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { compileScript, compileTemplate, parse } from '@vue/compiler-sfc'
import ts from 'typescript'
import * as Vue from 'vue'
import { renderToString } from 'vue/server-renderer'
import * as state from './workspace-state.ts'
import * as conversationView from './conversation-view.ts'
import { formatTime, jobLabel, scopeLabel, verdictLabel } from './workspace.ts'
import type { ExecutionPlan, ReportRecord, Snapshot } from './workspace-types.ts'

function plan(mode: 'rules_only' | 'rules_and_agent'): ExecutionPlan {
  return {
    id: 'recheck-plan', expires_at: '2099-01-01T00:00:00Z', mode, policy_scope_ref: 'scope-original',
    request: { kind: 'audit', conversation_id: 'conversation-original', data_source_id: 'local-offline', policy_scope_ref: 'scope-original', selection: { kind: 'assets', asset_ids: ['asset-original-a', 'asset-original-b'] }, data_policy: { mode: 'pinned_snapshot', snapshot_id: 'snapshot-plan' }, baseline_version_id: 'baseline-one', mode },
    effect_ceiling: { allow_cloud_reads: false, allow_model_requests: mode === 'rules_and_agent', cloud_http_attempts: 0, model_http_attempts: mode === 'rules_and_agent' ? 2 : 0 },
    gaps: ['管理来源尚未知'], warnings: ['仅分析固定输入时点的配置，不证明实际监听或端到端可达。'],
  }
}
async function render(preview: ExecutionPlan) {
  const filename = new URL('./pages/ReportsPage.vue', import.meta.url)
  const { descriptor } = parse(await readFile(filename, 'utf8'), { filename: filename.pathname })
  const script = compileScript(descriptor, { id: 'report-plan-review' })
  const template = compileTemplate({ source: descriptor.template!.content, filename: filename.pathname, id: 'report-plan-review', compilerOptions: { bindingMetadata: script.bindings } })
  assert.deepEqual(template.errors, [])
  const transpile = (source: string) => ts.transpileModule(source, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } }).outputText
  const source = { id: 'snapshot-plan', input: { scope: { ref: 'scope-original', boundary: 'internet', direction: 'in' }, collected_at: '2026-10-29T02:00:00Z' } } as Snapshot
  const workspace = { drafts: new Map(), jobs: Vue.ref([]), reports: Vue.ref([]), snapshots: Vue.ref([source]), listHasMore: Vue.ref({ reports: false }), listsLoading: Vue.ref(false) }
  const stub = { default: Vue.defineComponent({ setup: (_props, context) => () => Vue.h('button', context.attrs, context.slots.default?.()) }) }
  const modules: Record<string, unknown> = { vue: Vue, 'vue-router': { useRoute: () => ({ params: {}, query: {} }), useRouter: () => ({ push: async () => {} }) }, '../GuidancePanel.vue': { default: Vue.defineComponent({ render: () => Vue.h('div') }) }, '@nuxt/ui/components/Button.vue': stub, '../conversation-view': conversationView, '../workspace-state': state, '../workspace': { useWorkspace: () => workspace, formatTime, scopeLabel, jobLabel, verdictLabel, errorText: String } }
  function evaluate(source: string) { const module = { exports: {} as Record<string, unknown> }; new Function('require', 'exports', 'module', transpile(source))((name: string) => { if (!(name in modules)) throw new Error('unexpected report dependency ' + name); return modules[name] }, module.exports, module); return module.exports }
  const component = evaluate(script.content).default as Vue.Component & { setup: (props: unknown, context: unknown) => Record<string, Vue.Ref<unknown>>; render: unknown }
  component.render = evaluate(template.code).render
  const setup = component.setup
  component.setup = (props, context) => {
    const result = setup(props, context)
    result.selected!.value = { id: 'report-original', audit_id: 'audit-original', job_id: 'job-original', format: 'markdown', created_at: '2026-10-29T01:00:00Z', reviews: [] } as ReportRecord
    result.recheckPlan!.value = preview
    return result
  }
  const app = Vue.createSSRApp(component)
  app.component('RouterLink', Vue.defineComponent({ setup: (_props, context) => () => Vue.h('a', context.attrs, context.slots.default?.()) }))
  return renderToString(app)
}
test('report recheck acceptance shows its exact fixed objects, rules mode and zero external effects', async () => {
  const html = await render(plan('rules_only'))
  for (const text of ['仅运行确定性规则', '固定使用所选历史资料', 'snapshot-plan', 'asset-original-a', 'asset-original-b', 'scope-original', '本计划不允许新增云读取', '本计划不使用模型请求', '管理来源尚未知', '不证明实际监听或端到端可达']) assert.ok(html.includes(text), text)
})
test('report recheck acceptance reveals inherited model effects and respects an unavailable model gap', async () => {
  const agent = await render(plan('rules_and_agent'))
  assert.ok(agent.includes('规则后由受限模型补充')); assert.ok(agent.includes('允许本次有界模型补充')); assert.ok(agent.includes('本计划不允许新增云读取'))
  const blocked = plan('rules_and_agent'); blocked.effect_ceiling = { allow_cloud_reads: false, allow_model_requests: false, cloud_http_attempts: 0, model_http_attempts: 0 }; blocked.gaps = ['MODEL_NOT_CONFIGURED']
  const missing = await render(blocked)
  assert.ok(missing.includes('模型尚未配置')); assert.ok(missing.includes('本计划不使用模型请求')); assert.ok(!missing.includes('允许本次有界模型补充'))
})
