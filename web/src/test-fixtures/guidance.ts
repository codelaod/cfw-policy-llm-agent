import type { GuidanceCatalog, GuidanceItem } from '../guidance.ts'
import type { PreparationDiscovery } from '../workspace-types.ts'
export function guidanceItem(type: GuidanceItem['behavior'] = 'fill_input', targetType = 'composer', targetID = ''): GuidanceItem {
 return { id:'GDE-TEST',catalog_version:'guidance-m0-v1',display_text:'核对下一步',behavior:type,intent:{action:type==='fill_input'?'audit':'review_plan',text:type==='fill_input'?'只检查规则，不用模型':undefined},required_refs:[],effects:{allow_cloud_reads:false,allow_model_requests:false},state:'available',missing_inputs:[],action:{type,target:{type:targetType,id:targetID,conversation_id:'conversation-one'}},confirmation:{required:type==='open_plan_preview',conditions:[]} }
}
export function guidanceCatalog(item = guidanceItem()): GuidanceCatalog {return {catalog_version:'guidance-m0-v1',state:'ready',items:[item]}}
export function preparationFixture(): PreparationDiscovery {
 return {id:'discovery-one',job_id:'job-one',created_at:'2026-10-29T01:00:00Z',resources:[{id:'resource-one',resource_id:'i-public-one',name:'web-prod',region:'cn-hangzhou',resource_type:'ecs',source:'live_readonly',observed_at:'2026-10-29T01:00:00Z'}],coverages:[{selector:{region:'cn-hangzhou',resource_type:'ecs',name:'web-prod'},complete:true,observed_at:'2026-10-29T01:00:00Z',source:'live_readonly',limitations:[],revision:1}],unverified_resource_refs:[],complete:true}
}
