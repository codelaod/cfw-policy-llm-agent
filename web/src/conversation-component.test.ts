import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { parse, compileScript } from '@vue/compiler-sfc'
import ts from 'typescript'
import * as Vue from 'vue'
import { renderToString } from 'vue/server-renderer'
import * as controller from './conversation-state.ts'
import * as view from './conversation-view.ts'
import { conversationFixture, intentFixture } from './test-fixtures/conversation.ts'
import type { ConversationIntent, ConversationMessage } from './workspace-types.ts'

async function render(intent: ConversationIntent | null = null, messages: ConversationMessage[] = []) {
  const filename = new URL('./ConversationWorkspace.vue', import.meta.url)
  const { descriptor } = parse(await readFile(filename, 'utf8'), { filename: filename.pathname })
  const compiled = compileScript(descriptor, { id: 'conversation-component-test', inlineTemplate: true })
  const js = ts.transpileModule(compiled.content, { compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.CommonJS } }).outputText
  const workspace = { drafts: new Map(), unlocked: Vue.ref(true), conversationID: Vue.ref(intent ? 'conversation-one' : ''), currentConversation: Vue.ref(intent ? conversationFixture() : null), currentIntent: Vue.ref(intent), snapshotID: Vue.ref(''), currentSnapshot: Vue.ref(null), jobs: Vue.ref([]), messages: Vue.ref(messages), messagesHasMore: Vue.ref(false), conversations: Vue.ref(intent ? [conversationFixture()] : []), listHasMore: Vue.ref({ conversations: false }), listsLoading: Vue.ref(false), modelState: Vue.ref({ label: '模型未配置', mode: '未知' }), captureContext: () => () => true, api: {}, containsAppCredential: () => false }
  const button = { default: Vue.defineComponent({ setup: (_props, context) => () => Vue.h('button', context.attrs, context.slots.default?.()) }) }
  const modules: Record<string, unknown> = { './ConversationGroupPanel.vue': { default: Vue.defineComponent({ render: () => Vue.h('div', { class: 'group-panel' }) }) }, './GuidancePanel.vue': { default: Vue.defineComponent({ render: () => Vue.h('div', { class: 'shared-guidance' }) }) }, vue: Vue, './conversation-state': controller, './conversation-view': view, './ConversationTaskStatus.vue': { default: Vue.defineComponent({ render: () => Vue.h('div', 'existing task') }) }, '@nuxt/ui/components/Button.vue': button, './workspace': { useWorkspace: () => workspace, errorText: () => '受控错误', formatTime: (value: string) => value || '时间未知', scopeLabel: (boundary: string, direction: string) => boundary === 'internet' ? direction === 'in' ? '互联网入向' : '互联网出向' : '范围待明确', jobLabel: (value: string) => value, sourceLabel: (value: string) => value } }
  modules['./ConversationBusinessPanel.vue'] = { default: Vue.defineComponent({ render: () => Vue.h('div', { class: 'business-panel' }) }) }
  const module = { exports: {} as { default?: Vue.Component } }
  new Function('require', 'exports', 'module', js)((name: string) => { if (!(name in modules)) throw new Error(`unexpected conversation dependency ${name}`); return modules[name] }, module.exports, module)
  const app = Vue.createSSRApp(module.exports.default!)
  app.component('RouterLink', Vue.defineComponent({ setup: (_props, context) => () => Vue.h('a', context.attrs, context.slots.default?.()) }))
  return renderToString(app)
}
test('real empty conversation entry offers target input without a snapshot prerequisite or fake audit result', async () => {
  const html = await render()
  assert.match(html, /没有快照也能先保存目标/); assert.match(html, /目标、补充说明或追问/); assert.match(html, /模型与资料策略|本次模式与资料策略/)
  assert.ok(!html.includes('先选择一份固定资料')); assert.ok(!html.includes('审计已完成')); assert.ok(!html.includes('0风险'))
})
test('ambiguous server candidates show canonical identity without selecting the first candidate', async () => {
  const intent = intentFixture(); intent.status = 'needs_clarification'; intent.selection = []; intent.proposal = null; intent.candidates = [intentFixture().selection[0]!, { ...intentFixture().selection[0]!, id: 'resource-two', region: 'cn-beijing', resource_id: 'i-public-two' }]; intent.catalogue.complete = false
  const html = await render(intent)
  for (const value of ['cn-hangzhou', 'cn-beijing', 'i-public-one', 'i-public-two', '目录尚未穷尽', '确认所选对象']) assert.ok(html.includes(value), value)
  assert.ok(!/<input[^>]+checked/.test(html)); assert.ok(!html.includes('接受计划并开始'))
})
test('server proposal displays read/model effects and independent business gaps before explicit acceptance', async () => {
  const html = await render(intentFixture())
  for (const value of ['本次执行计划', '仅运行确定性规则', '本计划不允许新增云读取', '本计划不使用模型请求', '管理来源尚未知', '接受计划并开始']) assert.ok(html.includes(value), value)
  assert.ok(!html.includes('JSON')); assert.ok(!html.includes('HTTP 上限'))
})
test('external text in messages and names remains escaped data', async () => {
  const intent = intentFixture(); intent.selection[0]!.name = '<img src=x onerror=alert(1)>'
  const html = await render(intent, [{ id: 'message-one', conversation_id: 'conversation-one', seq: 1, role: 'assistant', origin: 'application', public_text: '<script>attempt()</script>', policy_scope_ref: '', created_at: '2026-10-29T01:00:00Z' }])
  assert.ok(html.includes('&lt;script&gt;')); assert.ok(html.includes('&lt;img')); assert.ok(!html.includes('<script>attempt'))
})

test('local offline artifacts explicitly disclose the missing cloud identity', async () => {
  const intent = intentFixture(); intent.selection[0]!.resource_id = ''; intent.selection[0]!.identity_kind = 'artifact_local'; intent.selection[0]!.local_asset_id = 'asset-local-one'; intent.selection[0]!.region = ''
  const html = await render(intent)
  assert.ok(html.includes('asset-local-one')); assert.ok(html.includes('本地对象；未取得云 ID')); assert.ok(html.includes('地域未知'))
})

test('known server gap codes render as customer conditions rather than debug identifiers', async () => {
  const intent = intentFixture(); intent.proposal!.gaps = ['MODEL_NOT_CONFIGURED', 'SNAPSHOT_NOT_AVAILABLE']
  const html = await render(intent)
  assert.ok(html.includes('模型尚未配置')); assert.ok(html.includes('覆盖所选对象与方向')); assert.ok(!html.includes('MODEL_NOT_CONFIGURED')); assert.ok(!html.includes('SNAPSHOT_NOT_AVAILABLE'))
})

test('a natural accepted job replaces an explicit old history choice and foreign receipts stay isolated',async()=>{
 const filename=new URL('./ConversationWorkspace.vue',import.meta.url),{descriptor}=parse(await readFile(filename,'utf8'),{filename:filename.pathname})
 const compiled=compileScript(descriptor,{id:'conversation-natural-task'}),js=ts.transpileModule(compiled.content,{compilerOptions:{module:ts.ModuleKind.CommonJS,target:ts.ScriptTarget.ES2022}}).outputText
 const old={id:'job-old',conversation_id:'conversation-one',status:'completed',view_seq:9,created_at:'2026-10-29T01:00:00Z'},next={...old,id:'job-next'}
 const editor=Vue.ref({acceptance:{result:null as {job:typeof old}|null},turn:{result:null as {job:typeof old}|null},choice:{},optionsInitialized:true,candidateIDs:[]})
 const workspace={drafts:new Map(),conversationID:Vue.ref('conversation-one'),currentIntent:Vue.ref(null),jobs:Vue.ref([old,next]),messages:Vue.ref([])}
 const controller={editor,currentJob:Vue.ref(null)},modules:Record<string,unknown>={vue:{...Vue,onMounted:()=>{}},'@nuxt/ui/components/Button.vue':{},'./GuidancePanel.vue':{},'./ConversationTaskStatus.vue':{},'./ConversationGroupPanel.vue':{},'./ConversationBusinessPanel.vue':{},'./conversation-state':{createConversationController:()=>controller},'./conversation-view':view,'./workspace':{useWorkspace:()=>workspace,errorText:String}}
 const module={exports:{} as {default?:{setup:(props:unknown,context:unknown)=>unknown}}};new Function('require','exports','module',js)((name:string)=>{if(!(name in modules))throw Error('unexpected natural-task dependency '+name);return modules[name]},module.exports,module)
 const effect=Vue.effectScope();try{
  const state=effect.run(()=>module.exports.default!.setup({}, {expose(){}})) as {selectedJobID:Vue.Ref<string>;selectedJob:Vue.Ref<typeof old|null>;contextOpen:Vue.Ref<boolean>}
  state.selectedJobID.value=old.id;state.contextOpen.value=false;editor.value.turn.result={job:next};await Vue.nextTick()
  assert.equal(state.selectedJob.value?.id,next.id);assert.equal(state.contextOpen.value,true)
  state.selectedJobID.value=old.id;state.contextOpen.value=false;editor.value.turn.result={job:{...next,conversation_id:'conversation-two'}};await Vue.nextTick()
  assert.equal(state.selectedJob.value?.id,old.id);assert.equal(state.contextOpen.value,false)
 }finally{effect.stop()}
})
