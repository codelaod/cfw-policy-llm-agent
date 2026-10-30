import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { compileScript, parse } from '@vue/compiler-sfc'
import ts from 'typescript'
import * as Vue from 'vue'
import * as api from './api.ts'
import * as business from './business-view.ts'
import * as guidance from './guidance.ts'
import * as agentView from './agent-view.ts'
import * as state from './workspace-state.ts'
import * as conflict from './inventory-conflict.ts'
import { inventoryDraftFixture } from './test-fixtures/inventory-conflict.ts'
import type { AssetChange, DraftRebaseRequest, ProfileDraft, ProfileDraftCurrentBase, Snapshot } from './workspace-types.ts'

async function setup(){
 const filename=new URL('./pages/InventoryPage.vue',import.meta.url),{descriptor}=parse(await readFile(filename,'utf8'),{filename:filename.pathname})
 const compiled=compileScript(descriptor,{id:'inventory-conflict-component'}),js=ts.transpileModule(compiled.content,{compilerOptions:{module:ts.ModuleKind.CommonJS,target:ts.ScriptTarget.ES2022}}).outputText
 const original=inventoryDraftFixture(),latest={value:state.cloneData(original)},base={value:{draft_id:original.id,draft_revision:original.revision,snapshot_id:original.snapshot_id,policy_scope_ref:original.policy_scope_ref,changes:state.cloneData(original.changes),base_revisions:state.cloneData(original.base_revisions)} as ProfileDraftCurrentBase}
 const updates:{id:string;revision:number;changes:AssetChange[]}[]=[],rebases:{id:string;request:DraftRebaseRequest;key:string}[]=[],previews:string[]=[],reads:string[]=[]
 const handlers={update:async(id:string,revision:number,changes:AssetChange[])=>({...state.cloneData(latest.value),id,revision:revision+1,changes:state.cloneData(changes)}),rebase:async(id:string,request:DraftRebaseRequest)=>({...state.cloneData(latest.value),id,revision:request.expected_draft_revision+1,changes:state.cloneData(request.changes),base_revisions:state.cloneData(request.expected_base_revisions)})}
 const snapshot=Vue.ref({id:original.snapshot_id,input:{scope:{ref:original.policy_scope_ref,boundary:'internet',direction:'in'},assets:[{id:'asset-one',services:[]}]}} as unknown as Snapshot)
 const workspace={unlocked:Vue.ref(true),snapshotID:Vue.ref(original.snapshot_id),currentSnapshot:snapshot,currentConversation:Vue.ref(null),currentIntent:Vue.ref(null),drafts:new Map(),setSourceGuard:()=>{},api:{draft:async(id:string)=>{reads.push('draft:'+id);return state.cloneData(latest.value)},draftCurrentBase:async(id:string)=>{reads.push('base:'+id);return state.cloneData(base.value)},updateDraft:async(id:string,revision:number,changes:AssetChange[])=>{updates.push({id,revision,changes:state.cloneData(changes)});const value=await handlers.update(id,revision,changes);latest.value=value;return value},rebaseDraft:async(id:string,request:DraftRebaseRequest,key:string)=>{rebases.push({id,request:state.cloneData(request),key});return handlers.rebase(id,request)},previewDraft:async(id:string)=>{previews.push(id);return{preview_id:'preview-current',draft_id:id,draft_revision:latest.value.revision,base_revisions:latest.value.base_revisions,diff:[],confirmable_sections:['roles','access'],created_at:'2026-10-29T01:00:00Z'}},assets:async()=>({items:[],offset:0,limit:100})}}
 const modules:Record<string,unknown>={vue:{...Vue,watch:()=>{},onMounted:()=>{},onBeforeUnmount:()=>{}},'vue-router':{useRoute:()=>({params:{},query:{draft:original.id}}),useRouter:()=>({replace:async()=>{}}),onBeforeRouteLeave:()=>{},onBeforeRouteUpdate:()=>{}},'../GuidancePanel.vue':{},'@nuxt/ui/components/Button.vue':{},'../api':api,'../inventory-conflict':conflict,'../guidance':guidance,'../agent-view':agentView,'../business-view':business,'../workspace-state':state,'../workspace':{useWorkspace:()=>workspace,errorText:(cause:unknown)=>cause instanceof Error?cause.message:String(cause)}}
 const module={exports:{} as {default?:{setup:(props:unknown,context:unknown)=>unknown}}};new Function('require','exports','module',js)((name:string)=>{if(!(name in modules))throw Error('unexpected inventory dependency '+name);return modules[name]},module.exports,module)
 const view=module.exports.default!.setup({}, {expose(){}}) as {draft:Vue.Ref<ProfileDraft|null>;changes:Vue.Ref<AssetChange[]>;dirty:Vue.Ref<boolean>;preview:Vue.Ref<unknown>;sections:Vue.Ref<string[]>;error:Vue.Ref<string>;conflictBase:Vue.Ref<ProfileDraft|null>;conflictReview:Vue.Ref<conflict.InventoryConflictReview|null>;conflictSelections:Vue.Ref<string[]>;rebaseReceipt:Vue.Ref<ProfileDraft|null>;rebaseMutation:state.Mutation<{draft_id:string;request:DraftRebaseRequest},ProfileDraft>;save:(preview:boolean)=>Promise<void>;readConflict:()=>Promise<void>;applyConflict:(keep?:boolean)=>Promise<void>;retryRebase:()=>Promise<void>;noteConflict:(cause:unknown)=>void}
 view.draft.value=state.cloneData(original);view.changes.value=state.cloneData(original.changes);view.changes.value[0]!.roles[0]!.declared=true;view.changes.value[0]!.roles[0]!.role='application';view.dirty.value=true
 return{view,workspace,original,latest,base,handlers,updates,rebases,previews,reads}
}
test('a rejected save retains original input, reads latest without writes, and only selected fields re-enter the same draft preview',async()=>{
 const s=await setup();s.handlers.update=async()=>{throw new api.ApiError(409,'VERSION_CONFLICT')};await s.view.save(false);await s.view.save(false)
 assert.equal(s.updates.length,1);assert.equal(s.view.conflictBase.value?.revision,1);assert.equal(s.view.changes.value[0]!.roles[0]!.role,'application')
 s.latest.value.revision=2;s.base.value.draft_revision=2;s.latest.value.changes[0]!.roles[1]!.role='database';s.latest.value.changes[0]!.roles.push({service_id:'new-cache',declared:true,role:'cache',protocol:'tcp',ports:[{from:6379,to:6379}]})
 await s.view.readConflict();assert.deepEqual(s.reads,['draft:draft-one','base:draft-one']);assert.equal(s.updates.length,1);assert.deepEqual(s.view.conflictSelections.value,[])
 await s.view.applyConflict();assert.equal(s.updates.length,1)
 s.view.conflictSelections.value=[s.view.conflictReview.value!.choices.find(value=>value.kind==='role')!.key];s.handlers.update=async(id,revision,changes)=>({...state.cloneData(s.latest.value),id,revision:revision+1,changes})
 await s.view.applyConflict();assert.equal(s.updates[1]!.id,'draft-one');assert.equal(s.updates[1]!.revision,2);assert.equal(s.view.draft.value?.revision,3);assert.equal(s.view.changes.value[0]!.roles[1]!.role,'database');assert.equal(s.view.changes.value[0]!.roles[2]!.service_id,'new-cache');assert.deepEqual(s.view.sections.value,[]);assert.deepEqual(s.previews,['draft-one'])
})
test('unknown rebase retry preserves original key/body and separates its old receipt from a newer current revision',async()=>{
 const s=await setup();s.original.revision=4;s.view.draft.value!.revision=4;s.latest.value.revision=4;s.base.value.draft_revision=4;s.base.value.base_revisions[0]!.revision=1;s.view.noteConflict(new api.ApiError(409,'VERSION_CONFLICT'));await s.view.readConflict();s.view.conflictSelections.value=[s.view.conflictReview.value!.choices[0]!.key]
 let receipt!:ProfileDraft;s.handlers.rebase=async(id,request)=>{receipt={...state.cloneData(s.latest.value),id,revision:5,changes:state.cloneData(request.changes),base_revisions:state.cloneData(request.expected_base_revisions)};s.latest.value=state.cloneData(receipt);throw new TypeError('artificial lost acknowledgement')}
 await s.view.applyConflict();const request=state.cloneData(s.rebases[0]);assert.equal(s.view.rebaseMutation.state,'unsaved');assert.ok(request?.key)
 s.latest.value.revision=6;s.latest.value.changes[0]!.roles[1]!.role='cache';s.base.value.draft_revision=6;s.base.value.base_revisions=state.cloneData(s.latest.value.base_revisions);await s.view.readConflict();assert.deepEqual(s.view.rebaseMutation.pendingBody,{draft_id:request!.id,request:request!.request});assert.equal(s.view.rebaseMutation.key,request!.key);assert.equal(s.rebases.length,1)
 s.handlers.rebase=async()=>receipt;await s.view.retryRebase();assert.deepEqual(s.rebases[1],request);assert.equal(s.view.rebaseReceipt.value?.revision,5);assert.equal(s.view.draft.value?.revision,6);assert.equal(s.view.conflictBase.value?.revision,5);assert.equal(s.view.changes.value[0]!.roles[0]!.role,'application');assert.equal(s.view.preview.value,null);assert.deepEqual(s.previews,[]);assert.match(s.view.error.value,/修订 5.*修订 6/)
})
test('a late conflict read cannot attach original draft fields to a changed fixed snapshot',async()=>{
 const s=await setup();s.view.noteConflict(new api.ApiError(409,'VERSION_CONFLICT'));let release!:(value:ProfileDraft)=>void
 s.workspace.api.draft=async()=>new Promise(resolve=>{release=resolve});const pending=s.view.readConflict();s.workspace.currentSnapshot.value={...s.workspace.currentSnapshot.value,id:'snapshot-other'};release(s.latest.value);await pending
 assert.equal(s.view.conflictReview.value,null);assert.deepEqual(s.updates,[]);assert.deepEqual(s.rebases,[])
})
