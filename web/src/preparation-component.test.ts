import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { compileScript, parse } from '@vue/compiler-sfc'
import ts from 'typescript'
import * as Vue from 'vue'
import { renderToString } from 'vue/server-renderer'
import * as conversation from './conversation-state.ts'
import * as guidance from './guidance.ts'
import * as preparation from './preparation-view.ts'
import * as conversationView from './conversation-view.ts'
import { createWorkspace } from './workspace.ts'
import { preparationFixture } from './test-fixtures/guidance.ts'
import { conversationFixture, intentFixture } from './test-fixtures/conversation.ts'
import type { ResourceCandidate } from './workspace-types.ts'

async function component(inlineTemplate: boolean, workspace: unknown, routes: unknown[]) {
 const filename=new URL('./PreparationResults.vue',import.meta.url),{descriptor}=parse(await readFile(filename,'utf8'),{filename:filename.pathname})
 const compiled=compileScript(descriptor,{id:'preparation-test',inlineTemplate}),js=ts.transpileModule(compiled.content,{compilerOptions:{module:ts.ModuleKind.CommonJS,target:ts.ScriptTarget.ES2022}}).outputText
 const modules:Record<string,unknown>={vue:Vue,'vue-router':{useRouter:()=>({push:async(value:unknown)=>{routes.push(value)}})},'./conversation-state':conversation,'./guidance':guidance,'./preparation-view':preparation,'./conversation-view':conversationView,'./workspace':{useWorkspace:()=>workspace,errorText:(value:unknown)=>value instanceof Error?value.message:String(value),formatTime:(value:string)=>value}}
 const module={exports:{} as {default?:Vue.Component&{setup:(...args:unknown[])=>unknown}}}
 new Function('require','exports','module',js)((name:string)=>{if(!(name in modules))throw new Error(`unexpected preparation dependency ${name}`);return modules[name]},module.exports,module)
 return module.exports.default!
}
function workspace() {const value=createWorkspace();value.unlocked.value=true;value.conversationID.value='conversation-one';value.conversations.value=[conversationFixture()];const intent=intentFixture();intent.status='accepted';intent.proposal!.status='accepted';value.intents.value['conversation-one']=intent;return value}
test('saved preparation displays source/time/partial candidates as escaped data without reviving accepted plans',async()=>{
 const current=workspace(),record=preparationFixture();record.complete=false;record.code='DISCOVERY_NEEDS_CLARIFICATION';record.resources[0]!.name='<img src=x onerror=external()>';record.unverified_resource_refs=[record.resources[0]!.id];record.coverages[0]!.complete=false;record.coverages[0]!.limitations=['RESOURCE_FILTER_UNVERIFIED']
 const view=await component(true,current,[]),html=await renderToString(Vue.createSSRApp(view,{jobId:'job-one',conversationId:'conversation-one',record}))
 for(const text of ['多个候选','目录准备记录','未穷尽','受控只读观测','2026-10-29T01:00:00Z','RESOURCE_FILTER_UNVERIFIED','筛选依据未核实','用此 ID 准备新目标','旧计划与已接受范围保持原版本'])assert.ok(html.includes(text),text)
 assert.ok(html.includes('&lt;img'));assert.ok(!html.includes('<img src=x'));assert.ok(!html.includes('接受计划并开始'))
})
test('candidate button only prefills the same conversation and preserves its accepted intent and pending submissions',async t=>{
 let externalCalls=0;t.mock.method(globalThis,'fetch',async()=>{externalCalls++;throw new Error('unexpected request')})
 const current=workspace(),record=preparationFixture(),routes:unknown[]=[],viewComponent=await component(false,current,routes)
 const props=Vue.reactive({jobId:'job-one',conversationId:'conversation-one',record}),view=viewComponent.setup(props,{expose(){}}) as {prepare:(resource:ResourceCandidate)=>Promise<void>;error:Vue.Ref<string>}
 const saved=JSON.stringify(current.currentIntent.value)
 await view.prepare(record.resources[0]!)
 const controller=current.drafts.get('conversation-controller') as ReturnType<typeof conversation.createConversationController>
 assert.match(controller.editor.value.text,/i-public-one/);assert.equal(JSON.stringify(current.currentIntent.value),saved);assert.equal(externalCalls,0);assert.equal(routes.length,1)
 const body={conversation_id:'conversation-one',request:{client_turn_id:'old-turn',text:'原请求'}};controller.editor.value.turn.restorePending(body,'old-key')
 await view.prepare(record.resources[0]!);assert.deepEqual(controller.editor.value.turn.pendingBody,body);assert.equal(controller.editor.value.turn.key,'old-key');assert.equal(routes.length,1)
 current.conversationID.value='conversation-two';await view.prepare(record.resources[0]!);assert.match(view.error.value,/任务所属会话/);assert.equal(routes.length,1);assert.equal(externalCalls,0)
})
test('incomplete empty preparation never renders no-match as resource absence or a passing audit',async()=>{
 const current=workspace(),record=preparationFixture();record.resources=[];record.complete=false;record.code='DISCOVERY_FAILED';record.coverages[0]!.complete=false
 const view=await component(true,current,[]),html=await renderToString(Vue.createSSRApp(view,{jobId:'job-one',conversationId:'conversation-one',record}))
 assert.ok(html.includes('未知、缺页或失败不能当作资源不存在'));assert.ok(!html.includes('全部通过'));assert.ok(!html.includes('用此 ID 准备新目标'))
})
