import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { compileScript, parse } from '@vue/compiler-sfc'
import ts from 'typescript'
import * as Vue from 'vue'
import * as state from './workspace-state.ts'
import * as conversationView from './conversation-view.ts'
import type { ExecutionPlan, Job, ReportRecord } from './workspace-types.ts'

const report=(id:string):ReportRecord=>({id,audit_id:'audit-'+id,job_id:'job-'+id,format:'markdown',created_at:'2026-10-29T01:00:00Z',reviews:[]})
const job=(id:string):Job=>({id,conversation_id:'conversation-one',status:'queued',view_seq:1,created_at:'2026-10-29T01:00:00Z'})
async function setup(){
 const filename=new URL('./pages/ReportsPage.vue',import.meta.url),{descriptor}=parse(await readFile(filename,'utf8'),{filename:filename.pathname})
 const compiled=compileScript(descriptor,{id:'report-actions'}),js=ts.transpileModule(compiled.content,{compilerOptions:{module:ts.ModuleKind.CommonJS,target:ts.ScriptTarget.ES2022}}).outputText
 const routes:unknown[]=[],cachedJobs:Job[]=[],handlers={start:async()=>job('recheck-one'),generate:async()=>report('generated-one')}
 const workspace={drafts:new Map(),jobs:Vue.ref([]),api:{report:async(id:string)=>report(id),reportContent:async()=>new Blob(['public artificial report']),startJob:async()=>handlers.start(),createReport:async()=>handlers.generate()},updateJob:(value:Job)=>cachedJobs.push(value),refresh:async()=>{}}
 const modules:Record<string,unknown>={vue:{...Vue,watch:()=>{},onBeforeUnmount:()=>{}},'vue-router':{useRoute:()=>({params:{id:'report-one'},query:{}}),useRouter:()=>({push:async(route:unknown)=>{routes.push(route)}})},'../GuidancePanel.vue':{},'@nuxt/ui/components/Button.vue':{},'../workspace':{useWorkspace:()=>workspace,errorText:(value:unknown)=>value instanceof Error?value.message:String(value)},'../workspace-state':state,'../conversation-view':conversationView}
 const module={exports:{} as {default?:{setup:(props:unknown,context:unknown)=>unknown}}};new Function('require','exports','module',js)((name:string)=>{if(!(name in modules))throw Error('unexpected report dependency '+name);return modules[name]},module.exports,module)
 const view=module.exports.default!.setup({}, {expose(){}}) as {selected:Vue.Ref<ReportRecord|null>;recheckPlan:Vue.Ref<ExecutionPlan|null>;auditID:Vue.Ref<string>;error:Vue.Ref<string>;busy:Vue.Ref<boolean>;load:(id:string)=>Promise<void>;startRecheck:()=>Promise<void>;generate:()=>Promise<void>}
 return {routes,cachedJobs,handlers,view}
}
test('a late recheck acceptance stays cached without navigating away from a newly selected report',async()=>{
 const subject=await setup();await subject.view.load('report-one');subject.view.recheckPlan.value={id:'plan-one',expires_at:'2026-11-01T01:00:00Z'}
 let release!:(value:Job)=>void;subject.handlers.start=async()=>new Promise(resolve=>{release=resolve})
 const pending=subject.view.startRecheck();await subject.view.load('report-two');release(job('recheck-one'));await pending
 assert.equal(subject.view.selected.value?.id,'report-two');assert.deepEqual(subject.routes,[]);assert.equal(subject.cachedJobs[0]?.id,'recheck-one')
})
test('a late generated report cannot replace a different report selected while it was saving',async()=>{
 const subject=await setup();await subject.view.load('report-one');subject.view.auditID.value='audit-one'
 let release!:(value:ReportRecord)=>void;subject.handlers.generate=async()=>new Promise(resolve=>{release=resolve})
 const pending=subject.view.generate();await subject.view.load('report-two');release(report('generated-one'));await pending
 assert.equal(subject.view.selected.value?.id,'report-two');assert.deepEqual(subject.routes,[])
})
test('an earlier report mutation failure is not displayed under a newly selected report',async()=>{
 const subject=await setup();await subject.view.load('report-one');subject.view.recheckPlan.value={id:'plan-one',expires_at:'2026-11-01T01:00:00Z'}
 let reject!:(cause:Error)=>void;subject.handlers.start=async()=>new Promise((_resolve,fail)=>{reject=fail})
 const pending=subject.view.startRecheck();await subject.view.load('report-two');reject(new TypeError('artificial old response lost'));await pending
 assert.equal(subject.view.selected.value?.id,'report-two');assert.equal(subject.view.error.value,'')
})
