import test from 'node:test'
import assert from 'node:assert/strict'
import { WorkspaceClient } from './workspace-api.ts'
import type { DraftRebaseRequest, ProfileDraft, ProfileDraftCurrentBase } from './workspace-types.ts'
function current(): ProfileDraftCurrentBase { return {draft_id:'draft-one',draft_revision:4,snapshot_id:'snapshot-one',policy_scope_ref:'scope-one',changes:[{asset_id:'vm.web/业务 01',roles:[{service_id:'service-one',declared:false,role:'web',protocol:'tcp',ports:[{from:443,to:443}]}]}],base_revisions:[{asset_id:'vm.web/业务 01',revision:2,profile_version_id:'profile-two',intent_version_id:'intent-two'}]} }
function rebased(): ProfileDraft { const base=current();return {id:base.draft_id,snapshot_id:base.snapshot_id,policy_scope_ref:base.policy_scope_ref,revision:5,status:'draft',changes:base.changes,base_revisions:base.base_revisions,created_at:'2026-10-29T01:00:00Z',updated_at:'2026-10-29T01:01:00Z'} }
const request=():DraftRebaseRequest=>({expected_draft_revision:4,expected_base_revisions:current().base_revisions,changes:current().changes})
test('current business base is a protected GET; explicit rebase retains the draft ID and user-reviewed heads',async t=>{
 const calls:{url:string;method:string;body:unknown;key:string|null}[]=[]
 t.mock.method(globalThis,'fetch',async(url:string,init:RequestInit)=>{calls.push({url,method:init.method??'GET',body:init.body?JSON.parse(String(init.body)):undefined,key:new Headers(init.headers).get('Idempotency-Key')});assert.equal(new Headers(init.headers).get('X-App-Token'),'public-local');return Response.json(url.endsWith('/current-base')?current():rebased())})
 const api=new WorkspaceClient(()=> 'public-local'),base=await api.draftCurrentBase('draft-one')
 assert.equal(base.changes[0]!.roles[0]!.declared,false);assert.equal(base.base_revisions[0]!.revision,2);assert.deepEqual(calls.map(call=>call.method),['GET'])
 const body=request(),saved=await api.rebaseDraft('draft-one',body,'original-rebase-key')
 assert.equal(saved.id,'draft-one');assert.equal(saved.status,'draft');assert.equal(saved.revision,5);assert.deepEqual(calls[1],{url:'/api/v1/profile-drafts/draft-one/rebase',method:'POST',body,key:'original-rebase-key'})
})
test('foreign bases, duplicate object heads or a confirmation-like rebase response are rejected',async t=>{
 let value:unknown={...current(),draft_id:'foreign'};t.mock.method(globalThis,'fetch',async()=>Response.json(value));const api=new WorkspaceClient(()=> 'public')
 await assert.rejects(api.draftCurrentBase('draft-one'),/原草稿/)
 value={...current(),base_revisions:[{...current().base_revisions[0],asset_id:'foreign'}]};await assert.rejects(api.draftCurrentBase('draft-one'),/修订引用/)
 value={...rebased(),status:'confirmed'};await assert.rejects(api.rebaseDraft('draft-one',request(),'same-key'),/未能核实/)
 value={...rebased(),base_revisions:[{...current().base_revisions[0],revision:3}]};await assert.rejects(api.rebaseDraft('draft-one',request(),'same-key'),/未能核实/)
})
