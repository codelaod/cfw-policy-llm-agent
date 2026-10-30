import test from 'node:test'
import assert from 'node:assert/strict'
import type { Snapshot } from './workspace-types.ts'
import { WorkspaceClient } from './workspace-api.ts'
import { createWorkspace } from './workspace.ts'
import { createConversationController } from './conversation-state.ts'
import { applyGuidanceFill, decodeGuidanceCatalog, guidanceMissing, inventoryGuidanceConversation, resolveGuidance } from './guidance.ts'
import { guidanceCatalog, guidanceItem } from './test-fixtures/guidance.ts'
import { agentFixture } from './test-fixtures/agent.ts'
import { assetRoute } from './agent-view.ts'
import { conversationFixture, intentFixture } from './test-fixtures/conversation.ts'

const job = (conversation = 'conversation-one') => ({id:'job-one',conversation_id:conversation,status:'completed',view_seq:4,created_at:'2026-10-29T01:00:00Z',result_ref:'audit-one'})
test('shared catalog is a protected local GET and rejects mixed conversation or catalog versions', async t => {
 const calls:{url:string;init:RequestInit}[]=[]
 t.mock.method(globalThis,'fetch',async(url:string,init:RequestInit)=>{calls.push({url,init});return Response.json(guidanceCatalog())})
 const api=new WorkspaceClient(()=> 'public-access-only')
 await api.guidance({surface:'conversation',conversationID:'conversation-one',jobID:'job-one',reportID:'report-one'})
 assert.equal(calls[0]!.url,'/api/v1/guidance?surface=conversation&conversation_id=conversation-one&job_id=job-one&report_id=report-one')
 assert.equal(calls[0]!.init.body,undefined);assert.ok(!calls[0]!.init.method || calls[0]!.init.method==='GET');assert.equal(new Headers(calls[0]!.init.headers).get('X-App-Token'),'public-access-only')
 const wrong=guidanceCatalog();wrong.items[0]!.action.target.conversation_id='conversation-two'
 assert.throws(()=>decodeGuidanceCatalog(wrong,{surface:'conversation',conversationID:'conversation-one'}),/会话/)
 wrong.items[0]!.action.target.conversation_id='conversation-one';wrong.items[0]!.catalog_version='future-version'
 assert.throws(()=>decodeGuidanceCatalog(wrong,{surface:'conversation'}),/条目/)
})
test('catalog cannot register an execute action or an external navigation target',()=>{
 const value=guidanceCatalog()
 assert.throws(()=>decodeGuidanceCatalog({...value,items:[{...value.items[0],behavior:'execute_cloud'}]},{surface:'conversation'}),/条目/)
 value.items[0]!.action.target.id='https://private.invalid'
 assert.throws(()=>decodeGuidanceCatalog(value,{surface:'conversation'}),/条目/)
})
test('ordinary fill changes only local editor state and preserves unresolved original request body and key', async()=>{
 const workspace=createWorkspace();workspace.unlocked.value=true;workspace.conversationID.value='conversation-one';workspace.conversations.value=[conversationFixture()]
 const controller=createConversationController(workspace, String), item=guidanceItem();item.intent.mode='rules_only'
 applyGuidanceFill(controller.editor.value,item)
 assert.equal(controller.editor.value.text,item.intent.text);assert.equal(workspace.jobs.value.length,0);assert.equal(workspace.messages.value.length,0)
 const original={conversation_id:'conversation-one',request:{client_turn_id:'original-turn',text:'原始目标'}}
 controller.editor.value.turn.restorePending(original,'original-key')
 assert.throws(()=>applyGuidanceFill(controller.editor.value,item),/原提交/)
 assert.deepEqual(controller.editor.value.turn.pendingBody,original);assert.equal(controller.editor.value.turn.key,'original-key')
})
test('opening a saved plan checks both current goal revision and the exact plan using GETs only',async t=>{
 const urls:string[]=[]
 t.mock.method(globalThis,'fetch',async(url:string,init:RequestInit)=>{urls.push(url);assert.equal(init.body,undefined);return Response.json(url.endsWith('/intents/current')?intentFixture():conversationFixture())})
 const api=new WorkspaceClient(()=> 'public'), item=guidanceItem('navigate_local','plan_preview','plan-one');item.action.target.intent_revision=1
 const resolved=await resolveGuidance(item,{surface:'conversation',conversationID:'conversation-one'},api)
 assert.deepEqual(resolved,{kind:'route',route:{path:'/conversations',hash:'#proposal-heading'},conversationID:'conversation-one'})
 item.action.target.intent_revision=2
 await assert.rejects(resolveGuidance(item,{surface:'conversation',conversationID:'conversation-one'},api),/版本/)
 assert.ok(urls.every(url=>url.includes('/intents/current')))
})
test('historical report guidance keeps its exact producer and cannot open another conversation or a foreign snapshot',async t=>{
 let foreign=false
 t.mock.method(globalThis,'fetch',async(url:string,init:RequestInit)=>{
  assert.equal(init.body,undefined)
  if(url.includes('/reports/'))return Response.json({id:'report-one',audit_id:'audit-one',job_id:'job-one',format:'json',created_at:'2026-10-29T01:00:00Z',reviews:[]})
  if(url.endsWith('/result'))return Response.json({view_seq:4,is_final:true,snapshot_id:'snapshot-one'})
  return Response.json(job(foreign?'conversation-two':'conversation-one'))
 })
 const api=new WorkspaceClient(()=> 'public'), item=guidanceItem('open_plan_preview','report','report-one')
 assert.equal((await resolveGuidance(item,{surface:'report',reportID:'report-one'},api)).kind,'route')
 foreign=true
 await assert.rejects(resolveGuidance(item,{surface:'report',reportID:'report-one'},api),/会话/)
 foreign=false
 const snapshot=guidanceItem('navigate_local','snapshot','snapshot-other')
 await assert.rejects(resolveGuidance(snapshot,{surface:'audit',conversationID:'conversation-one',jobID:'job-one',snapshotID:'snapshot-other'},api),/固定资料/)
})
test('missing business draft is an explicit local gap and does not invent or create one',async()=>{
 const item=guidanceItem('navigate_local','business_draft')
 assert.match(guidanceMissing(item,{surface:'profile'})[0]!,/已保存的业务草稿/)
 const api=new WorkspaceClient(()=> 'public')
 await assert.rejects(resolveGuidance(item,{surface:'profile'},api),/业务草稿/)
})

test('metadata role candidates without a fabricated service retain an asset-only manual route',async t=>{
 const record=agentFixture();delete record.content.output!.roles[0]!.service_ref
 t.mock.method(globalThis,'fetch',async()=>Response.json({is_final:true,view_seq:6,agent_result:record}))
 const result=await new WorkspaceClient(()=> 'public').jobResult('job-one')
 assert.equal(result.agent_result?.content.output?.roles[0]?.service_ref,undefined)
 assert.deepEqual(assetRoute(result.agent_result,'job-one','asset_a','snapshot-one'),{name:'inventory',params:{id:'asset-real'},query:{snapshot_id:'snapshot-one'}})
})
test('same-conversation report navigation still requires the exact producer audit',async t=>{
 t.mock.method(globalThis,'fetch',async(url:string)=>Response.json(url.includes('/reports/')?{id:'report-one',audit_id:'audit-other',job_id:'job-one',format:'json',created_at:'2026-10-29T01:00:00Z',reviews:[]}:job()))
 await assert.rejects(resolveGuidance(guidanceItem('navigate_local','report','report-one'),{surface:'report',conversationID:'conversation-one',reportID:'report-one',jobID:'job-one'},new WorkspaceClient(()=> 'public')),/会话|任务/)
})

test('inventory guidance cannot attach a new target to an old same-scope snapshot',()=>{
 const conversation=conversationFixture();conversation.policy_scope_ref='scope-one'
 const intent=intentFixture(),snapshot={id:'snapshot-one',policy_scope_ref:'scope-one',input:{scope:{boundary:'internet',direction:'in'},assets:[{id:'asset-local',provider_resource_id:'i-public-one'}]}} as Snapshot
 assert.equal(inventoryGuidanceConversation(conversation,intent,snapshot),conversation.id)
 intent.direction='out'
 assert.equal(inventoryGuidanceConversation(conversation,intent,snapshot),'')
 intent.direction='in';intent.selection[0]!.resource_id='i-another-object'
 assert.equal(inventoryGuidanceConversation(conversation,intent,snapshot),'')
})
test('historical model explanation prefill verifies report A and selects its fixed snapshot instead of current B using reads only',async t=>{
 const calls:string[]=[],item=guidanceItem();item.id='GDE-05';item.intent={action:'explain_report',text:'请使用模型解释固定报告 report-one 的发现、依据和原时点限制。',context_report_id:'report-one',snapshot_id:'snapshot-a',mode:'rules_and_agent',data_policy:'pinned_snapshot'};item.effects.allow_model_requests=true
 let mismatch=false
 t.mock.method(globalThis,'fetch',async(url:string,init:RequestInit)=>{
  calls.push(url);assert.equal(init.body,undefined);assert.ok(!init.method||init.method==='GET')
  if(url.includes('/reports/'))return Response.json({id:'report-one',job_id:'job-one',audit_id:'audit-one',format:'markdown',reviews:[],created_at:'2026-10-29T01:00:00Z'})
  if(url.endsWith('/result'))return Response.json({view_seq:4,is_final:true,snapshot_id:mismatch?'snapshot-b':'snapshot-a',audit:{id:'audit-one',snapshot_id:'snapshot-a',findings:[],result:{findings:[],evidence:[],coverage:{},input_manifest:{policy_scope:{ref:'scope-a'},source_kind:'imported'}}}})
  if(url.includes('/snapshots/'))return Response.json({id:'snapshot-a',source_kind:'imported',input:{schema_version:1,source_kind:'imported',scope:{ref:'scope-a'},completeness:{},protection:{},selection:{asset_ids:[]},baseline:{},assets:[],policies:[]}})
  return Response.json(job())
 })
 const api=new WorkspaceClient(()=> 'public')
 assert.deepEqual(await resolveGuidance(item,{surface:'report',reportID:'report-one',snapshotID:'snapshot-b'},api),{kind:'fill',conversationID:'conversation-one',snapshotID:'snapshot-a'})
 assert.ok(calls.some(url=>url.includes('/reports/report-one'))&&calls.some(url=>url.endsWith('/jobs/job-one/result'))&&calls.some(url=>url.endsWith('/snapshots/snapshot-a')))
 mismatch=true;await assert.rejects(resolveGuidance(item,{surface:'report',reportID:'report-one'},api),/固定资料/)
 item.intent.snapshot_id='https://private.invalid';assert.throws(()=>decodeGuidanceCatalog(guidanceCatalog(item),{surface:'report'}),/关联记录/)
})
