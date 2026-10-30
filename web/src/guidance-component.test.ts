import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { parse, compileScript } from '@vue/compiler-sfc'
import ts from 'typescript'
import * as Vue from 'vue'
import * as guidance from './guidance.ts'
import * as conversation from './conversation-state.ts'
import { guidanceCatalog, guidanceItem } from './test-fixtures/guidance.ts'
import { intentFixture } from './test-fixtures/conversation.ts'
import type { GuidanceCatalog } from './guidance.ts'

async function setup() {
 const filename=new URL('./GuidancePanel.vue',import.meta.url),{descriptor}=parse(await readFile(filename,'utf8'),{filename:filename.pathname})
 const compiled=compileScript(descriptor,{id:'guidance-test'}),js=ts.transpileModule(compiled.content,{compilerOptions:{module:ts.ModuleKind.CommonJS,target:ts.ScriptTarget.ES2022}}).outputText
 const props=Vue.reactive({surface:'conversation',conversationId:'conversation-one'}),routes:unknown[]=[],requests:{conversationID?:string}[]=[]
 let unlocked=true
 const handlers={guidance:async()=>guidanceCatalog(),intent:async()=>intentFixture()}
 const workspace={unlocked:Vue.ref(true),currentIntent:Vue.ref(null),jobs:Vue.ref([]),conversationID:Vue.ref('conversation-one'),drafts:new Map(),captureContext:()=>()=>unlocked,api:{guidance:async(value:{conversationID?:string})=>{requests.push({...value});return handlers.guidance()},currentIntent:async()=>handlers.intent()}}
 const modules:Record<string,unknown>={vue:{...Vue,watch:()=>{},onBeforeUnmount:()=>{}},'vue-router':{useRouter:()=>({push:async(value:unknown)=>{routes.push(value)}})},'./guidance':guidance,'./conversation-state':conversation,'./workspace':{useWorkspace:()=>workspace,errorText:(value:unknown)=>value instanceof Error?value.message:String(value)}}
 const module={exports:{} as {default?:{setup:(...args:unknown[])=>unknown}}}
 new Function('require','exports','module',js)((name:string)=>{if(!(name in modules))throw new Error(`unexpected guidance dependency ${name}`);return modules[name]},module.exports,module)
 const view=module.exports.default!.setup(props,{expose(){}}) as {catalog:Vue.Ref<GuidanceCatalog|null>;load:()=>Promise<void>;follow:(item:guidance.GuidanceItem)=>Promise<void>}
 return{props,routes,requests,handlers,workspace,view,lock:()=>{unlocked=false;workspace.unlocked.value=false}}
}
test('late guidance response from another selected conversation never replaces the current catalog',async()=>{
 const s=await setup();let release!:(value:GuidanceCatalog)=>void
 s.handlers.guidance=async()=>new Promise(resolve=>{release=resolve})
 const old=s.view.load();s.props.conversationId='conversation-two';s.handlers.guidance=async()=>({...guidanceCatalog(),state:'conversation-two'})
 await s.view.load();release(guidanceCatalog());await old
 assert.equal(s.view.catalog.value?.state,'conversation-two');assert.deepEqual(s.requests.map(value=>value.conversationID),['conversation-one','conversation-two'])
})
test('locking during a catalog read discards its response',async()=>{
 const s=await setup();let release!:(value:GuidanceCatalog)=>void
 s.handlers.guidance=async()=>new Promise(resolve=>{release=resolve})
 const pending=s.view.load();s.lock();release(guidanceCatalog());await pending
 assert.equal(s.view.catalog.value,null);assert.deepEqual(s.routes,[])
})
test('late local plan lookup cannot navigate after switching conversations',async()=>{
 const s=await setup(),item=guidanceItem('navigate_local','plan_preview','plan-one');item.action.target.intent_revision=1
 s.view.catalog.value=guidanceCatalog(item)
 let release!:(value:ReturnType<typeof intentFixture>)=>void
 s.handlers.intent=async()=>new Promise(resolve=>{release=resolve})
 const pending=s.view.follow(s.view.catalog.value.items[0]!)
 s.props.conversationId='conversation-two';release(intentFixture());await pending
 assert.deepEqual(s.routes,[])
})
