import { computed, reactive, shallowReactive } from 'vue'
import { ApiError, ProtocolError } from './api.ts'
import { Mutation, isDefiniteRejection } from './workspace-state.ts'
import { redactKnownCredentials } from './conversation-state.ts'
import type { Workspace } from './workspace.ts'
import type { ConversationIntent, IntentTurnRequest, IntentTurnResponse, ResourceGroupMember, ResourceGroupPreview, ResourceGroupRequest, ResourceGroupResult, ResourceGroupVersion } from './workspace-types.ts'

const object = (value: unknown): value is Record<string, unknown> => !!value && typeof value === 'object' && !Array.isArray(value)
const id = (value: unknown): value is string => typeof value === 'string' && /^[A-Za-z0-9_.:-]{1,160}$/.test(value)
const opaque = (value: unknown): value is string => typeof value === 'string' && value.length <= 128 && !!value.trim()
const revision = (value: unknown): value is number => Number.isSafeInteger(value) && Number(value) >= 0
const text = (value: unknown, max: number): value is string => typeof value === 'string' && value.length <= max
const time = (value: unknown): value is string => typeof value === 'string' && Number.isFinite(Date.parse(value))
function groupBase(value: unknown, conversation: string): Record<string, unknown> {
 if (!object(value) || ['body','raw','raw_artifacts','request_digest','target_digest'].some(field=>field in value) || !id(value.id) || !id(value.group_id) || !text(value.group_name,256) || !value.group_name.trim() || !id(value.source_ref) || !opaque(value.account_ref) || value.conversation_id !== conversation || !id(value.intent_id) || !revision(value.intent_revision) || value.intent_revision < 1 || !text(value.authorization_source,2048) || !value.authorization_source.trim() || !time(value.created_at) || !Array.isArray(value.members) || value.members.length < 1 || value.members.length > 1000) throw new ProtocolError('归属记录未绑定当前会话的明确对象与来源。')
 const refs = new Set<string>()
 for (const member of value.members) {
  if (!object(member) || !id(member.resource_ref) || refs.has(member.resource_ref) || !id(member.identity_ref) || !id(member.provider) || member.source_ref !== value.source_ref || member.account_ref !== value.account_ref || !text(member.region,128) || !text(member.resource_type,128) || !text(member.name,512) || member.resource_id !== undefined && member.resource_id !== '' && !opaque(member.resource_id) || !member.resource_id && (!opaque(member.local_asset_id) || !opaque(member.identity_context)) || !['synthetic','imported','live_readonly'].includes(String(member.source)) || !time(member.observed_at)) throw new ProtocolError('归属成员缺少来源限定身份或属于其他来源／账号。')
  refs.add(member.resource_ref)
 }
 return value
}
export function decodeGroupPreview(value: unknown, conversation: string): ResourceGroupPreview | null {
 if (value === null) return null
 const row = groupBase(value,conversation)
 if (!revision(row.base_revision) || !time(row.expires_at) || Date.parse(row.expires_at) <= Date.parse(String(row.created_at))) throw new ProtocolError('归属预览缺少基版本或有效期。')
 return row as unknown as ResourceGroupPreview
}
export function decodeGroupVersion(value: unknown, conversation: string): ResourceGroupVersion {
 const row = groupBase(value,conversation)
 if (!id(row.preview_id) || !revision(row.revision) || row.revision < 1 || !revision(row.catalogue_revision)) throw new ProtocolError('归属保存结果缺少不可变版本。')
 return row as unknown as ResourceGroupVersion
}
export function decodeGroupResult(value: unknown, conversation: string): ResourceGroupResult | null {
 if (value == null) return null
 if (!object(value) || !['preview','preview_required','confirmed','needs_clarification'].includes(String(value.state)) || typeof value.replayed !== 'boolean' || !Array.isArray(value.missing) || value.missing.some(item=>typeof item !== 'string' || !/^[A-Za-z0-9_]{1,128}$/.test(item))) throw new ProtocolError('归属动作结果无效。')
 const preview = value.preview == null ? null : decodeGroupPreview(value.preview,conversation), version = value.version == null ? null : decodeGroupVersion(value.version,conversation)
 if (preview && version || ['preview','preview_required'].includes(String(value.state)) && !preview || value.state === 'confirmed' && !version || value.state === 'needs_clarification' && (preview || version) || (preview || version) && value.missing.length) throw new ProtocolError('归属动作状态与保存记录不一致。')
 return {...value,preview,version} as ResourceGroupResult
}
export const groupMissingLabel = (value: string) => ({authorization_source:'授权来源尚未明确，不能保存可信归属。',group_name:'需要明确客户或业务组名称。',selected_resources:'先在本会话解析并明确选择资源对象。',group_preview:'需要先预览本次归属。',non_sensitive_group_input:'可能凭据未发送保存，请用非敏感业务说明。',use_selected_resource_refs:'仅使用本会话已选对象，不能用自由文字扩充成员。'}[value] ?? '仍有归属确认条件需要核对。')
export function groupIdentityLabel(member: ResourceGroupMember) { return member.resource_id || `${member.local_asset_id}（本地对象；未取得云 ID）` }
export function groupConfirmRequest(preview: ResourceGroupPreview | null, intent: ConversationIntent | null, approved: boolean, edited: boolean, now = Date.now()): ResourceGroupRequest {
 if (!preview || !intent || preview.conversation_id !== intent.conversation_id || preview.intent_id !== intent.id || preview.intent_revision !== intent.revision || Date.parse(preview.expires_at) <= now || edited || !approved) throw new Error('请先核对当前目标与本预览的成员、来源和授权说明；过期或修改后需要重新预览。')
 const selected = new Set(intent.selection.map(resource=>resource.id))
 if (!selected.size || preview.members.some(member=>!selected.has(member.resource_ref))) throw new Error('预览成员不属于当前明确选择，不能确认旧对象。')
 return {action:'confirm',preview_id:preview.id,expected_revision:preview.intent_revision}
}
type GroupEnvelope = {conversation_id:string;request:IntentTurnRequest}
type GroupWorkspace = Pick<Workspace,'api'|'unlocked'|'conversationID'|'currentIntent'|'captureContext'|'applyIntentResponse'|'loadMessages'|'containsAppCredential'|'groupResults'>
export type GroupEditor = {name:string;authority:string;edited:boolean;approved:boolean;preview:ResourceGroupPreview|null;result:ResourceGroupResult|null;resultSequence:number;loading:boolean;busy:boolean;error:string;message:string;reads:number;mutation:Mutation<GroupEnvelope,IntentTurnResponse>}
export function createGroupController(workspace: GroupWorkspace, conversation:()=>string, formatError:(cause:unknown)=>string, cached?:Map<string,GroupEditor>) {
 const editors = shallowReactive(cached ?? new Map<string,GroupEditor>());let disposed=false
 function forConversation(owner:string) {let value=editors.get(owner);if(!value){value=reactive({name:'',authority:'',edited:false,approved:false,preview:null,result:null,resultSequence:0,loading:false,busy:false,error:'',message:'',reads:0,mutation:shallowReactive(new Mutation<GroupEnvelope,IntentTurnResponse>())}) as GroupEditor;editors.set(owner,value)}return value}
 const editor = computed(()=>forConversation(conversation()))
 function adopt(state:GroupEditor,result:ResourceGroupResult,sequence:number) {if(sequence>=state.resultSequence){state.result=result;state.resultSequence=sequence}}
 async function load() {
  const owner=conversation(),state=forConversation(owner),seq=++state.reads,valid=workspace.captureContext()
  if(!owner || !workspace.unlocked.value){state.loading=false;state.preview=null;return}
  const receipt=workspace.groupResults?.value[owner];if(receipt)adopt(state,receipt.result,receipt.sequence)
  state.loading=true;state.error=''
  const current=()=>!disposed && conversation()===owner && workspace.conversationID.value===owner && seq===state.reads && valid()
  try {const value=await workspace.api.resourceGroupPreview(owner);if(current()){if(state.preview?.id!==value?.id)state.approved=false;state.preview=value;if(value && !state.edited && !state.mutation.pendingBody){state.name=value.group_name;state.authority=value.authorization_source}}}
  catch(cause){if(current()){state.preview=null;state.error=formatError(cause)}}
  finally{if(seq===state.reads)state.loading=false}
 }
 async function submit(request?:ResourceGroupRequest) {
  const owner=conversation(),state=forConversation(owner),valid=workspace.captureContext()
  if(!owner || state.busy || !workspace.unlocked.value)return
  state.busy=true;state.error='';state.message=''
  const current=()=>!disposed && conversation()===owner && workspace.conversationID.value===owner && valid()
  try {
   let body=state.mutation.pendingBody
   if(!body){
    const intent=workspace.currentIntent.value
    if(!request || !intent || intent.conversation_id!==owner || !intent.selection.length)throw new Error('先明确选择本会话的对象，再预览归属。')
    const known=[state.name,state.authority].join('\n')
    if(redactKnownCredentials(known)!==known || workspace.containsAppCredential(known)){state.authority='';state.name='';throw new Error('检测到可能凭据，内容未发送；请重新填写非敏感业务说明。')}
    body={conversation_id:owner,request:{client_turn_id:crypto.randomUUID(),text:'',expected_revision:request.expected_revision,group:request}}
   }else if(request)throw new Error('先沿原内容和原标识核对上一次归属提交。')
   const response=await state.mutation.submit(body,async(saved,key)=>{const value=await workspace.api.intentTurn(saved.conversation_id,saved.request,key);if(!value.group)throw new ProtocolError('未能核实归属动作已保存。');return value})
   adopt(state,response.group!,response.reply?.seq ?? response.message?.seq ?? 0)
   if(!current())return
   workspace.applyIntentResponse(response);state.approved=false
   if(response.group!.preview){state.preview=response.group!.preview;state.name=response.group!.preview.group_name;state.authority=response.group!.preview.authorization_source;state.edited=false;state.message='预览已保存，尚未确认归属。'}
   else if(response.group!.version){state.preview=null;state.edited=false;state.message='归属版本已保存。旧任务、角色与访问要求保持原版本。'}
   else{state.preview=null;state.message=response.group!.missing.map(groupMissingLabel).join(' ')}
   await workspace.loadMessages()
  }catch(cause){if(cause instanceof ApiError && cause.code==='CREDENTIAL_IN_PAYLOAD'){state.name='';state.authority='';state.mutation.discard()}else if(isDefiniteRejection(cause))state.mutation.discard();if(current())state.error=formatError(cause)}
  finally{state.busy=false}
 }
 async function preview() {const state=editor.value,intent=workspace.currentIntent.value;if(!intent)return;await submit({action:'preview',group_name:state.name.trim(),authorization_source:state.authority.trim(),resource_refs:[...new Set(intent.selection.map(item=>item.id))].sort(),expected_revision:intent.revision})}
 async function confirm() {try{await submit(groupConfirmRequest(editor.value.preview,workspace.currentIntent.value,editor.value.approved,editor.value.edited))}catch(cause){editor.value.error=formatError(cause)}}
 function markChanged(){editor.value.edited=true;editor.value.approved=false;editor.value.message=''}
 function dispose(){disposed=true;editor.value.reads++}
 return {editor,editors,load,submit,preview,confirm,markChanged,dispose}
}
