import type { AuditRecord, BatchRequest, Job, JobResult, PreparedResource, ReportRecord, Scope, Snapshot } from '../workspace-types.ts'
export const batchScope: Scope = {ref:'scope-one',provider:'aliyun',account_ref:'account-one',control_plane_ref:'control-one',ip_family:'ipv4',boundary:'internet',direction:'in',policy_set_ref:'policy-one'}
export function batchFixture(): BatchRequest {
 const all: PreparedResource[] = Array.from({length:5},(_,i)=>({asset_id:`asset-${i+1}`,resource_id:`i-public-${i+1}`,region:'cn-hangzhou',resource_type:'ecs'}))
 const phases = {collection_ms:1,rules_ms:2,model_ms:0,finalize_ms:1}
 const segments = Array.from({length:3},(_,i)=>({ordinal:i+1,resources:all.slice(i*2,i*2+2),child_job_id:`child-${i+1}`,status:'completed',started_at:'2026-10-29T01:00:00Z',finished_at:'2026-10-29T01:00:01Z',snapshot_id:`snapshot-${i+1}`,audit_ref:`audit-${i+1}`,report_ref:`report-${i+1}`,input_manifest:{snapshot_version:`snapshot-v${i+1}`,profile_version:'profile-v1',intent_version:'intent-v1',baseline_version:'baseline-v1',normalizer_version:'normalizer-v1',kernel_version:'kernel-v1',policy_scope:{...batchScope},evaluation_at:'2026-10-29T01:00:00Z',collected_at:'2026-10-29T00:00:00Z',source_kind:'imported'},phases:{...phases},execution_ms:4,phase_source:'measured'}))
 return {parent_job_id:'parent-one',plan_id:'plan-one',source_ref:'local-offline',scope:{...batchScope},spec:{resources:all,size:2},workload:{},effects:{},accepted_at:'2026-10-29T01:00:00Z',status:'completed',segments,covered:all,failed:[],unprocessed:[],usage:{usage_unknown:false,unknown_reasons:[],cloud_attempts:0,model_attempts:0,recorded_model_calls:0,recorded_tool_calls:0,execution_ms:12,phases:{collection_ms:3,rules_ms:6,model_ms:0,finalize_ms:3}}}
}
export function batchReadsFixture(batch = batchFixture()) {
 const calls: string[] = []
 const parent: Job = {id:batch.parent_job_id,conversation_id:'conversation-one',status:'completed',view_seq:9,created_at:batch.accepted_at}
 const children = batch.segments.map(segment=>({...parent,id:segment.child_job_id!,parent_job_id:parent.id,result_ref:segment.audit_ref}))
 const audits = batch.segments.map(segment=>({id:segment.audit_ref!,snapshot_id:segment.snapshot_id!,created_at:batch.accepted_at,result:{schema_version:1,input_manifest:segment.input_manifest!,findings:[],evidence:[],coverage:{complete:true,evaluated_queries:1,evaluated_cells:1,limitations:[]}},findings:[]} as AuditRecord))
 const reports = batch.segments.map((segment,index)=>({id:segment.report_ref!,audit_id:audits[index]!.id,job_id:children[index]!.id,format:'json' as const,created_at:batch.accepted_at,reviews:[]}))
 const snapshots = batch.segments.map(segment=>({id:segment.snapshot_id!,input:{scope:batch.scope,assets:segment.resources.map(resource=>({id:resource.asset_id}))}} as Snapshot))
 const api = {
  job:async(id:string):Promise<Job>=>{calls.push('job:'+id);return id===parent.id?parent:children.find(row=>row.id===id)!},
  jobResult:async(id:string):Promise<JobResult>=>{calls.push('result:'+id);const audit=audits.find(row=>row.id===children.find(child=>child.id===id)?.result_ref);return id===parent.id?{batch,view_seq:9,is_final:true}:{audit,snapshot_id:audit?.snapshot_id,view_seq:9,is_final:true}},
  report:async(id:string):Promise<ReportRecord>=>{calls.push('report:'+id);return reports.find(row=>row.id===id)!},
  snapshot:async(id:string):Promise<Snapshot>=>{calls.push('snapshot:'+id);return snapshots.find(row=>row.id===id)!},
 }
 return {batch,parent,children,audits,reports,snapshots,calls,api}
}
