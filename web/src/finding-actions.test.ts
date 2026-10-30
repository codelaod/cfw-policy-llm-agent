import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { compileScript, parse } from '@vue/compiler-sfc'
import ts from 'typescript'
import * as Vue from 'vue'
import * as state from './workspace-state.ts'
import type { FindingRecord } from './workspace-types.ts'
const finding = (id: string, key = id): FindingRecord => ({id,audit_id:'audit-one',finding_key:key,finding:{id:key,verdict:'fail'}} as FindingRecord)
async function setup() {
 const filename=new URL('./pages/FindingsPage.vue',import.meta.url),{descriptor}=parse(await readFile(filename,'utf8'),{filename:filename.pathname})
 const compiled=compileScript(descriptor,{id:'finding-actions'}),js=ts.transpileModule(compiled.content,{compilerOptions:{module:ts.ModuleKind.CommonJS,target:ts.ScriptTarget.ES2022}}).outputText
 const route=Vue.reactive({query:{audit_id:'audit-one',finding_id:'',finding_key:''}}),drafts=new Map<string,unknown>(),calls:string[]=[]
 const handlers={page:async(_offset:number)=>[finding('finding-one'),finding('finding-two')],detail:async(id:string)=>finding(id)}
 const workspace={drafts,jobs:Vue.ref([]),api:{findings:async(_audit:string,offset=0)=>({items:await handlers.page(offset)}),finding:async(id:string)=>{calls.push(id);return handlers.detail(id)},reviewHistory:async()=>[]}}
 const modules:Record<string,unknown>={vue:{...Vue,watch:()=>{},onBeforeUnmount:()=>{}},'vue-router':{useRoute:()=>route,onBeforeRouteLeave:()=>{},onBeforeRouteUpdate:()=>{}},'../GuidancePanel.vue':{},'@nuxt/ui/components/Button.vue':{},'../workspace':{useWorkspace:()=>workspace,errorText:(value:unknown)=>value instanceof Error?value.message:String(value)},'../workspace-state':state}
 const module={exports:{} as {default?:{setup:(...args:unknown[])=>unknown}}}
 new Function('require','exports','module',js)((name:string)=>{if(!(name in modules))throw new Error(`unexpected finding dependency ${name}`);return modules[name]},module.exports,module)
 const view=module.exports.default!.setup({}, {expose(){}}) as {load:(append?:boolean)=>Promise<void>;active:Vue.Ref<FindingRecord|null>;error:Vue.Ref<string>;comment:Vue.Ref<string>;mutation:state.Mutation<{finding_id:string;request:{expected_review_revision:number;kind:'comment';comment:string}},unknown>}
 return{route,drafts,calls,handlers,view}
}
test('explicit finding IDs outside the first page are read exactly and same-audit route changes follow the new object',async()=>{
 const s=await setup();s.route.query.finding_id='finding-101'
 await s.view.load();assert.equal(s.view.active.value?.id,'finding-101');assert.deepEqual(s.calls,['finding-101'])
 s.route.query.finding_id='finding-two';await s.view.load();assert.equal(s.view.active.value?.id,'finding-two')
})
test('missing or foreign explicit finding never defaults to the first saved finding',async()=>{
 const s=await setup();s.route.query.finding_id='missing';s.handlers.detail=async()=>{throw new Error('missing finding')}
 await s.view.load();assert.equal(s.view.active.value,null);assert.match(s.view.error.value,/missing/)
 s.handlers.detail=async id=>({...finding(id),audit_id:'audit-other'});await s.view.load();assert.equal(s.view.active.value,null);assert.match(s.view.error.value,/不一致/)
})
test('a missing destination preserves unresolved review body and key under the original finding',async()=>{
 const s=await setup();s.route.query.finding_id='finding-one';await s.view.load();s.view.comment.value='原对象未确认意见'
 const body={finding_id:'finding-one',request:{expected_review_revision:0,kind:'comment' as const,comment:'原对象意见'}};s.view.mutation.restorePending(body,'original-review-key')
 s.route.query.finding_id='missing';s.handlers.detail=async()=>{throw new Error('missing')};await s.view.load()
 assert.equal(s.view.active.value,null)
 const cached=s.drafts.get('review:finding-one') as {pendingBody:unknown;key:string}
 assert.deepEqual(cached.pendingBody,body);assert.equal(cached.key,'original-review-key')
})
test('comparison finding keys are resolved within their explicit audit before exact record reads',async()=>{
 const s=await setup();s.route.query.finding_key='rule-key-101';s.handlers.page=async offset=>offset===0?Array.from({length:100},(_,i)=>finding('finding-'+i,'rule-key-'+i)):[finding('finding-101','rule-key-101')];s.handlers.detail=async id=>finding(id,'rule-key-101')
 await s.view.load();assert.equal(s.view.active.value?.id,'finding-101');assert.deepEqual(s.calls,['finding-101'])
})
