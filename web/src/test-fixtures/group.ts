import type { ConversationIntent, ResourceGroupPreview, ResourceGroupResult, ResourceGroupVersion } from '../workspace-types.ts'
import { intentFixture } from './conversation.ts'
export function groupPreviewFixture(conversation = 'conversation-one', count = 2): ResourceGroupPreview {
 const created = new Date(), expires = new Date(created.getTime()+300000)
 return {id:'group-preview-one',group_id:'group-one',group_name:'客户甲',source_ref:'source-one',account_ref:'account-one',conversation_id:conversation,intent_id:'intent-'+conversation,intent_revision:1,base_revision:0,authorization_source:'工单 AUTH-42，经业务负责人核对',members:Array.from({length:count},(_,index)=>({resource_ref:'resource-'+index,identity_ref:'identity-'+index,provider:'aliyun',source_ref:'source-one',account_ref:'account-one',region:'cn-hangzhou',resource_type:'ecs',resource_id:'i-group-'+index,name:'public-node-'+index,source:'imported',observed_at:'2026-10-29T01:00:00Z'})),created_at:created.toISOString(),expires_at:expires.toISOString()}
}
export function groupVersionFixture(conversation = 'conversation-one'): ResourceGroupVersion {
 const {base_revision:_base,expires_at:_expires,...preview}=groupPreviewFixture(conversation)
 return {...preview,id:'group-version-one',preview_id:preview.id,revision:1,catalogue_revision:4}
}
export function groupIntentFixture(conversation = 'conversation-one',count=2): ConversationIntent {
 const intent=intentFixture(conversation),preview=groupPreviewFixture(conversation,count)
 intent.selection=preview.members.map(member=>({id:member.resource_ref,resource_id:member.resource_id!,name:member.name,region:member.region,resource_type:member.resource_type,source:member.source,observed_at:member.observed_at}))
 return intent
}
export function groupResultFixture(kind:'preview'|'confirmed'|'missing'='preview',conversation='conversation-one'): ResourceGroupResult {
 return {state:kind==='preview'?'preview_required':kind==='confirmed'?'confirmed':'needs_clarification',preview:kind==='preview'?groupPreviewFixture(conversation):null,version:kind==='confirmed'?groupVersionFixture(conversation):null,missing:kind==='missing'?['authorization_source']:[],replayed:false}
}
