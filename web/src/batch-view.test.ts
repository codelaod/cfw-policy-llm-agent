import test from 'node:test'
import assert from 'node:assert/strict'
import { WorkspaceClient } from './workspace-api.ts'
import { decodeBatchRequest, batchEvidenceLabel, resolveBatchSegment } from './batch-view.ts'
import { needsResultRefresh } from './agent-view.ts'
import { batchFixture, batchReadsFixture } from './test-fixtures/batch.ts'

test('batch decoding preserves all five objects across three exact segments without inventing a merged audit',async t=>{
 const value=batchFixture();assert.equal(decodeBatchRequest(value,'parent-one').covered.length,5)
 t.mock.method(globalThis,'fetch',async()=>Response.json({batch:value,view_seq:9,is_final:true}))
 assert.equal((await new WorkspaceClient(()=> 'public').jobResult('parent-one')).batch?.segments.length,3)
 await assert.rejects(new WorkspaceClient(()=> 'public').jobResult('foreign'),/父任务/)
 t.mock.method(globalThis,'fetch',async()=>Response.json({batch:value,audit:{id:'audit-one',result:{findings:[],evidence:[],coverage:{}},findings:[]},view_seq:9,is_final:true}))
 await assert.rejects(new WorkspaceClient(()=> 'public').jobResult('parent-one'),/单段结果/)
 value.status='accepted';t.mock.method(globalThis,'fetch',async()=>Response.json({batch:value,view_seq:9,is_final:true}))
 await assert.rejects(new WorkspaceClient(()=> 'public').jobResult('parent-one'),/终态与分段记录/)
})
test('batch decoder rejects truncated or duplicated members, mismatched coverage and foreign manifest',()=>{
 for(const mutate of [
  (value:ReturnType<typeof batchFixture>)=>{value.segments.pop()},
  (value:ReturnType<typeof batchFixture>)=>{value.segments[1]!.resources[0]=value.segments[0]!.resources[0]!},
  (value:ReturnType<typeof batchFixture>)=>{value.covered=value.covered.slice(0,2)},
  (value:ReturnType<typeof batchFixture>)=>{value.segments[0]!.input_manifest!.policy_scope.account_ref='other-account'},
  (value:ReturnType<typeof batchFixture>)=>{value.segments[1]!.child_job_id=value.segments[0]!.child_job_id},
  (value:ReturnType<typeof batchFixture>)=>{delete (value.spec.resources[0] as {resource_id?:string}).resource_id},
  (value:ReturnType<typeof batchFixture>)=>{Object.assign(value,{raw_artifacts:[]})},
 ]){const value=batchFixture();mutate(value);assert.throws(()=>decodeBatchRequest(value,'parent-one'))}
})
test('offline material identities remain opaque and retain punctuation supported by the server',async()=>{
 const value=batchFixture();value.spec.resources[0]!.asset_id='vm.web.0';value.spec.resources[0]!.resource_id='';value.spec.resources[0]!.region=''
 assert.equal(decodeBatchRequest(value,'parent-one').segments[0]!.resources[0]!.asset_id,'vm.web.0')
 const subject=batchReadsFixture(value)
 assert.deepEqual(await resolveBatchSegment(value,1,'conversation-one','object',subject.api,'vm.web.0'),{path:'/inventory/vm.web.0',query:{snapshot_id:'snapshot-1'}})
})
test('partial batch keeps covered evidence, failures and unprocessed objects separate and preserves unknown usage',()=>{
 const value=batchFixture();value.status='partial';const failed=value.segments[1]!,pending=value.segments[2]!
 for(const row of [failed,pending]){delete row.snapshot_id;delete row.audit_ref;delete row.report_ref;delete row.input_manifest}
 failed.status='failed';pending.status='unprocessed';delete pending.child_job_id
 value.covered=value.spec.resources.slice(0,2);value.failed=value.spec.resources.slice(2,4);value.unprocessed=value.spec.resources.slice(4);value.usage.usage_unknown=true;value.usage.unknown_reasons=['LOGICAL_USAGE_NOT_SAVED']
 const decoded=decodeBatchRequest(value,'parent-one');assert.deepEqual([decoded.covered.length,decoded.failed.length,decoded.unprocessed.length],[2,2,1]);assert.equal(decoded.usage.recorded_model_calls,0);assert.equal(decoded.usage.usage_unknown,true);assert.equal(batchEvidenceLabel(decoded.segments[0]!),'已有审计证据')
})
test('segment report and object links resolve exact parent-child evidence using reads only',async()=>{
 const subject=batchReadsFixture()
 assert.deepEqual(await resolveBatchSegment(subject.batch,2,'conversation-one','report',subject.api),{path:'/reports/report-2'})
 assert.deepEqual(subject.calls,['job:parent-one','result:parent-one','job:child-2','result:child-2','report:report-2'])
 subject.calls.length=0
 assert.deepEqual(await resolveBatchSegment(subject.batch,1,'conversation-one','object',subject.api,'asset-1'),{path:'/inventory/asset-1',query:{snapshot_id:'snapshot-1'}})
 assert.deepEqual(subject.calls,['job:parent-one','result:parent-one','job:child-1','result:child-1','snapshot:snapshot-1'])
})
test('foreign child, report, stale audit or cross-segment object cannot become an actionable route',async()=>{
 for(const kind of ['child','report','audit','object','conversation']){
  const subject=batchReadsFixture()
  if(kind==='child')subject.children[0]!.parent_job_id='foreign-parent'
  if(kind==='report')subject.reports[0]!.job_id='foreign-child'
  if(kind==='audit')subject.audits[0]!.snapshot_id='foreign-snapshot'
  await assert.rejects(resolveBatchSegment(subject.batch,1,kind==='conversation'?'foreign':'conversation-one',kind==='object'?'object':'report',subject.api,kind==='object'?'asset-4':''),/不一致/)
 }
})
test('batch persisted progress stages request result refresh without executing work',()=>{
 for(const stage of ['batch_segment_started','batch_segment_finished','batch_request_stopped'])assert.equal(needsResultRefresh({schema_version:1,job_id:'parent-one',seq:4,type:'stage_changed',occurred_at:'2026-10-29T01:00:00Z',payload:{stage}}),true)
})
