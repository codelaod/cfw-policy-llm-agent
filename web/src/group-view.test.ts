import test from 'node:test'
import assert from 'node:assert/strict'
import { createWorkspace } from './workspace.ts'
import { WorkspaceClient } from './workspace-api.ts'
import { createGroupController, decodeGroupPreview, decodeGroupResult, groupConfirmRequest } from './group-view.ts'
import { groupIntentFixture, groupPreviewFixture, groupResultFixture } from './test-fixtures/group.ts'
import { conversationFixture, intentResponseFixture } from './test-fixtures/conversation.ts'
import type { IntentTurnRequest, ResourceGroupResult } from './workspace-types.ts'

function subject() {
 const workspace=createWorkspace();workspace.unlocked.value=true;workspace.conversationID.value='conversation-one';workspace.conversations.value=[conversationFixture(),conversationFixture('conversation-two')];workspace.intents.value['conversation-one']=groupIntentFixture();workspace.intents.value['conversation-two']=groupIntentFixture('conversation-two')
 const controller=createGroupController(workspace,()=>workspace.conversationID.value,cause=>cause instanceof Error?cause.message:String(cause));controller.editor.value.name='客户甲';controller.editor.value.authority='业务负责人确认工单AUTH-42'
 return {workspace,controller}
}
function response(result:ResourceGroupResult,conversation='conversation-one') {const value=intentResponseFixture(conversation);value.intent=groupIntentFixture(conversation);value.group=result;return value}
test('group preview reads are protected local GETs and validate same-conversation source-qualified members',async t=>{
 const calls:{url:string;init:RequestInit}[]=[]
 t.mock.method(globalThis,'fetch',async(url:string,init:RequestInit)=>{calls.push({url,init});return Response.json(groupPreviewFixture())})
 const api=new WorkspaceClient(()=> 'public-group-access')
 assert.equal((await api.resourceGroupPreview('conversation-one'))?.members.length,2)
 assert.equal(calls[0]!.url,'/api/v1/conversations/conversation-one/resource-group-preview');assert.equal(calls[0]!.init.body,undefined);assert.equal(new Headers(calls[0]!.init.headers).get('X-App-Token'),'public-group-access')
 const foreign=groupPreviewFixture();foreign.members[0]!.source_ref='another-source'
 assert.throws(()=>decodeGroupPreview(foreign,'conversation-one'),/其他来源/)
 assert.throws(()=>decodeGroupPreview(groupPreviewFixture('conversation-two'),'conversation-one'),/当前会话/)
})
test('valid imported local identities and account aliases remain opaque data in a trusted group receipt',()=>{
 const value=groupPreviewFixture('conversation-one',1);value.account_ref='人工账号 A';const member=value.members[0]!
 member.account_ref=value.account_ref;member.resource_id='';member.local_asset_id='vm.web/业务 01';member.identity_context='人工范围 A'
 assert.equal(decodeGroupPreview(value,'conversation-one')?.members[0]!.local_asset_id,'vm.web/业务 01')
})
test('typed group preview carries only current selected references and never accepts an audit or role effect',async t=>{
 const s=subject(),calls:{url:string;body:IntentTurnRequest}[]=[]
 t.mock.method(globalThis,'fetch',async(url:string,init:RequestInit)=>{if(url.endsWith('/messages?after_seq=0&limit=100'))return Response.json({items:[],next_seq:0,last_seq:0,has_more:false});calls.push({url,body:JSON.parse(String(init.body))});return Response.json(response(groupResultFixture()))})
 await s.controller.preview()
 assert.equal(calls.length,1);assert.match(calls[0]!.url,/intent-turns$/);assert.deepEqual(calls[0]!.body.group?.resource_refs,['resource-0','resource-1']);assert.equal(calls[0]!.body.group?.expected_revision,1);assert.equal(calls[0]!.body.mode,undefined);assert.equal(calls[0]!.body.business,undefined);assert.equal(s.workspace.jobs.value.length,0);assert.equal(s.controller.editor.value.preview?.id,'group-preview-one')
})
test('unknown group submission keeps its original body/key despite edits or changed intent revision',async t=>{
 const s=subject(),calls:{body:unknown;key:string|null}[]=[];let attempt=0
 t.mock.method(globalThis,'fetch',async(url:string,init:RequestInit)=>{if(url.includes('/messages?'))return Response.json({items:[],next_seq:0,last_seq:0,has_more:false});calls.push({body:JSON.parse(String(init.body)),key:new Headers(init.headers).get('Idempotency-Key')});if(!attempt++)throw new TypeError('public fixture unknown delivery');return Response.json(response(groupResultFixture()))})
 await s.controller.preview();assert.ok(s.controller.editor.value.mutation.pendingBody)
 s.controller.editor.value.name='后改内容';s.workspace.intents.value['conversation-one']!.revision=3
 await s.controller.submit()
 assert.deepEqual(calls[1],calls[0]);assert.equal(s.controller.editor.value.mutation.pendingBody,null)
})
test('a success response missing its group receipt remains an unknown submission with the same request key',async t=>{
 const s=subject();t.mock.method(globalThis,'fetch',async()=>Response.json(intentResponseFixture()))
 await s.controller.preview();assert.ok(s.controller.editor.value.mutation.pendingBody);assert.ok(s.controller.editor.value.mutation.key);assert.match(s.controller.editor.value.error,/未能核实/)
})
test('group confirm is bound to the exact preview, current selected identities, revision and explicit review',()=>{
 const preview=groupPreviewFixture(),intent=groupIntentFixture()
 assert.throws(()=>groupConfirmRequest(preview,intent,false,false),/核对/)
 assert.throws(()=>groupConfirmRequest(preview,intent,true,true),/重新预览/)
 assert.deepEqual(groupConfirmRequest(preview,intent,true,false),{action:'confirm',preview_id:preview.id,expected_revision:1})
 intent.revision=2;assert.throws(()=>groupConfirmRequest(preview,intent,true,false),/目标/)
 intent.revision=1;intent.selection=[intent.selection[0]!];assert.throws(()=>groupConfirmRequest(preview,intent,true,false),/当前明确选择/)
 assert.throws(()=>groupConfirmRequest(preview,groupIntentFixture(),true,false,Date.parse(preview.expires_at)+1),/过期/)
})
test('a late group preview cannot replace another conversation or transfer a mutation',async t=>{
 const s=subject();let release!:(value:Response)=>void
 t.mock.method(globalThis,'fetch',async(url:string)=>url.includes('/conversation-one/')?new Promise(resolve=>{release=resolve}):Response.json(groupPreviewFixture('conversation-two')))
 const old=s.controller.load();s.workspace.conversationID.value='conversation-two';await s.controller.load();release(Response.json(groupPreviewFixture()));await old
 assert.equal(s.controller.editor.value.preview?.conversation_id,'conversation-two');assert.equal(s.controller.editors.get('conversation-one')?.preview,null)
})
test('late group write receipts remain on their original editor and do not change the other target',async t=>{
 const s=subject();let release!:(value:Response)=>void
 t.mock.method(globalThis,'fetch',async()=>new Promise(resolve=>{release=resolve}))
 const pending=s.controller.preview();s.workspace.conversationID.value='conversation-two';s.controller.editor.value.name='客户乙';release(Response.json(response(groupResultFixture())));await pending
 assert.equal(s.controller.editor.value.name,'客户乙');assert.equal(s.controller.editor.value.preview,null);assert.equal(s.workspace.groupResults.value['conversation-two'],undefined);assert.equal(s.controller.editors.get('conversation-one')?.mutation.result?.group?.preview?.conversation_id,'conversation-one')
})
test('unknown authority outcomes remain clarification and never become a confirmed version',()=>{
 const result=decodeGroupResult(groupResultFixture('missing'),'conversation-one')!
 assert.equal(result.state,'needs_clarification');assert.equal(result.preview,null);assert.equal(result.version,null);assert.deepEqual(result.missing,['authorization_source'])
 const bad=groupResultFixture('confirmed');bad.version!.conversation_id='conversation-two'
 assert.throws(()=>decodeGroupResult(bad,'conversation-one'),/当前会话/)
})
test('unselected contexts issue no group POST and recognized credentials are removed before transport',async t=>{
 const s=subject();let calls=0;t.mock.method(globalThis,'fetch',async()=>{calls++;throw new Error('unexpected transport')})
 s.workspace.intents.value['conversation-one']!.selection=[];await s.controller.preview();assert.equal(calls,0)
 s.workspace.intents.value['conversation-one']=groupIntentFixture();s.controller.editor.value.authority='密钥=sk-PUBLIC_ARTIFICIAL_ONLY_123456';await s.controller.preview();assert.equal(calls,0);assert.equal(s.controller.editor.value.authority,'');assert.equal(s.controller.editor.value.mutation.pendingBody,null)
})
