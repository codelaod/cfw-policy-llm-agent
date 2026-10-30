import { ProtocolError } from './api.ts'
import type { WorkspaceClient } from './workspace-api.ts'
import type { AuditResult, BatchRequest, BatchSegment, PreparedResource, Scope } from './workspace-types.ts'

const object = (value: unknown): value is Record<string, unknown> => !!value && typeof value === 'object' && !Array.isArray(value)
const id = (value: unknown): value is string => typeof value === 'string' && /^[A-Za-z0-9_-]{1,128}$/.test(value)
const count = (value: unknown): value is number => Number.isSafeInteger(value) && Number(value) >= 0
const text = (value: unknown, max = 256): value is string => typeof value === 'string' && value.length <= max
const time = (value: unknown): value is string => typeof value === 'string' && Number.isFinite(Date.parse(value))
const optionalID = (value: unknown) => value === undefined || value === '' || id(value)
const optionalResourceID = (value: unknown) => value === undefined || value === '' || typeof value === 'string' && value.length <= 128 && !!value.trim()
const optionalTime = (value: unknown) => value === undefined || value === '' || time(value)
const code = (value: unknown) => value === undefined || value === '' || typeof value === 'string' && /^[A-Z0-9_]{1,128}$/.test(value)
const scopeFields = ['ref', 'provider', 'account_ref', 'control_plane_ref', 'ip_family', 'boundary', 'direction', 'policy_set_ref', 'gateway_ref'] as const
const sameScope = (left: Scope, right: Scope) => scopeFields.every(field => (left[field] ?? '') === (right[field] ?? ''))
function scope(value: unknown): value is Scope { return object(value) && scopeFields.filter(field => field !== 'gateway_ref').every(field => text(value[field]) && !!value[field]) && optionalID(value.gateway_ref) }
function phases(value: unknown) { return object(value) && ['collection_ms', 'rules_ms', 'model_ms', 'finalize_ms'].every(field => count(value[field])) }
function resourceKey(value: PreparedResource) { return JSON.stringify([value.asset_id ?? '', value.region, value.resource_type, value.resource_id]) }
function resources(value: unknown): value is PreparedResource[] {
 if (!Array.isArray(value) || value.length > 10000) return false
 const seen = new Set<string>(), assets = new Set<string>(), providerIDs = new Set<string>()
 return value.every(row => { if (!object(row) || row.resource_type !== 'ecs' || !optionalResourceID(row.asset_id) || typeof row.resource_id !== 'string' || !optionalResourceID(row.resource_id) || typeof row.region !== 'string' || !optionalResourceID(row.region) || !row.asset_id && !row.resource_id) return false; const key = resourceKey(row as PreparedResource), providerID = JSON.stringify([row.region,row.resource_id]); if (seen.has(key) || row.asset_id && assets.has(String(row.asset_id)) || row.resource_id && providerIDs.has(providerID)) return false; seen.add(key); if (row.asset_id) assets.add(String(row.asset_id)); if (row.resource_id) providerIDs.add(providerID); return true })
}
function sameResources(left: PreparedResource[], right: PreparedResource[]) { return left.length === right.length && left.every((row, index) => resourceKey(row) === resourceKey(right[index]!)) }
function manifest(value: unknown, expected: Scope): value is AuditResult['input_manifest'] {
 return object(value) && ['snapshot_version', 'profile_version', 'intent_version', 'baseline_version', 'normalizer_version', 'kernel_version'].every(field => text(value[field]) && !!value[field]) && scope(value.policy_scope) && sameScope(value.policy_scope, expected) && ['synthetic', 'imported', 'live_readonly'].includes(String(value.source_kind)) && time(value.evaluation_at) && time(value.collected_at)
}
export function decodeBatchRequest(value: unknown, parentID: string): BatchRequest {
 if (!object(value) || value.parent_job_id !== parentID || !id(value.plan_id) || !id(value.source_ref) || !scope(value.scope) || !object(value.spec) || !resources(value.spec.resources) || !count(value.spec.size) || value.spec.size < 1 || value.spec.resources.length <= value.spec.size || !object(value.workload) || !object(value.effects) || !time(value.accepted_at) || !['accepted','running','completed','partial','failed','cancelled','interrupted'].includes(String(value.status)) || !code(value.error_code) || !Array.isArray(value.segments) || value.segments.length !== Math.ceil(value.spec.resources.length / value.spec.size) || !resources(value.covered) || !resources(value.failed) || !resources(value.unprocessed) || ['body','raw','raw_artifacts','config_revision','request_digest','heads_json'].some(field => field in value)) throw new ProtocolError('分批记录未绑定本次完整对象集与父任务。')
 const covered: PreparedResource[] = [], failed: PreparedResource[] = [], unprocessed: PreparedResource[] = [], children = new Set<string>()
 for (let index = 0; index < value.segments.length; index++) {
  const row = value.segments[index]
  if (!object(row) || row.ordinal !== index + 1 || !resources(row.resources) || !sameResources(row.resources,value.spec.resources.slice(index * value.spec.size,(index + 1) * value.spec.size)) || !['pending','running','completed','partial','failed','cancelled','interrupted','unprocessed'].includes(String(row.status)) || !optionalID(row.child_job_id) || row.child_job_id === parentID || !optionalID(row.snapshot_id) || !optionalID(row.audit_ref) || !optionalID(row.report_ref) || !code(row.error_code) || !optionalTime(row.started_at) || !optionalTime(row.finished_at) || !count(row.execution_ms) || !phases(row.phases) || row.phase_source !== undefined && row.phase_source !== '' && !['measured','persisted_stage_windows','unknown'].includes(String(row.phase_source)) || ['body','raw','raw_artifacts','request_digest'].some(field => field in row)) throw new ProtocolError('分段对象、状态或证据引用无效。')
  if (row.child_job_id) { if (children.has(String(row.child_job_id))) throw new ProtocolError('分段重复关联同一个子任务。'); children.add(String(row.child_job_id)) }
  if (row.input_manifest != null && !manifest(row.input_manifest,value.scope) || (row.audit_ref || row.report_ref || row.snapshot_id || row.input_manifest) && !row.child_job_id || row.report_ref && (!row.audit_ref || !row.input_manifest) || row.audit_ref && (!row.input_manifest || !row.snapshot_id) || row.status === 'pending' && row.child_job_id || row.status === 'running' && !row.child_job_id) throw new ProtocolError('分段审计证据与子任务关联冲突。')
  if (row.audit_ref && row.input_manifest) covered.push(...row.resources)
  else if (row.child_job_id && ['completed','partial','failed','cancelled','interrupted'].includes(String(row.status))) failed.push(...row.resources)
  else unprocessed.push(...row.resources)
 }
 if (!sameResources(covered,value.covered) || !sameResources(failed,value.failed) || !sameResources(unprocessed,value.unprocessed)) throw new ProtocolError('分批覆盖集合与逐段证据不一致。')
 const usage = value.usage
 if (!object(usage) || typeof usage.usage_unknown !== 'boolean' || !Array.isArray(usage.unknown_reasons) || usage.unknown_reasons.some(reason => !code(reason) || !reason) || ['cloud_attempts','model_attempts','recorded_model_calls','recorded_tool_calls','execution_ms'].some(field => !count(usage[field])) || !phases(usage.phases) || !usage.usage_unknown && usage.unknown_reasons.length) throw new ProtocolError('分批用量缺少可核对的记录或未知说明。')
 return value as unknown as BatchRequest
}
export const batchStatusLabel = (value: string) => ({accepted:'已接受',pending:'尚未开始',running:'执行中',completed:'已完成',partial:'部分完成',failed:'失败',cancelled:'已取消',interrupted:'已中断',unprocessed:'未处理'}[value] ?? '状态待核对')
export function batchFor(value: BatchRequest | undefined | null, parentID: string) { return value?.parent_job_id === parentID ? value : null }
export function batchResourceLabel(value: PreparedResource) { return [value.region || '地域未声明', value.resource_id || `${value.asset_id}（本地对象）`].join(' / ') }
export function batchEvidenceLabel(segment: BatchSegment) { return segment.audit_ref && segment.input_manifest ? '已有审计证据' : segment.child_job_id && ['completed','partial','failed','cancelled','interrupted'].includes(segment.status) ? '执行未取得审计证据' : '尚未取得审计证据' }
export type BatchDestination = 'process' | 'findings' | 'report' | 'recheck' | 'object'
type BatchReads = Pick<WorkspaceClient,'job'|'jobResult'|'report'|'snapshot'>
/** Every destination is resolved through protected reads of the accepted parent and exact child. */
export async function resolveBatchSegment(batch: BatchRequest, ordinal: number, conversationID: string, destination: BatchDestination, api: BatchReads, assetID = '') {
 const stale = () => { throw new ProtocolError('分段与父任务、固定证据或所选对象不一致，请重新读取。') }
 const old = batch.segments.find(row => row.ordinal === ordinal)
 if (!old?.child_job_id) return stale()
 const parent = await api.job(batch.parent_job_id)
 if (parent.id !== batch.parent_job_id || parent.parent_job_id || parent.conversation_id !== conversationID) return stale()
 const latest = (await api.jobResult(parent.id)).batch
 if (!latest || latest.plan_id !== batch.plan_id || latest.source_ref !== batch.source_ref || !sameScope(latest.scope,batch.scope)) return stale()
 const segment = latest.segments.find(row => row.ordinal === ordinal)
 if (!segment || segment.child_job_id !== old.child_job_id || !sameResources(segment.resources,old.resources)) return stale()
 const child = await api.job(segment.child_job_id!)
 if (child.id !== segment.child_job_id || child.parent_job_id !== parent.id || child.conversation_id !== conversationID) return stale()
 if (destination === 'process') return {path:`/audits/${encodeURIComponent(child.id)}`}
 if (!segment.audit_ref || !segment.input_manifest || !segment.snapshot_id) return stale()
 const result = await api.jobResult(child.id), audit = result.audit
 if (!audit || audit.id !== segment.audit_ref || child.result_ref !== segment.audit_ref || audit.snapshot_id !== segment.snapshot_id || result.snapshot_id && result.snapshot_id !== segment.snapshot_id || !manifest(audit.result.input_manifest,latest.scope) || ['snapshot_version','profile_version','intent_version','baseline_version','normalizer_version','kernel_version','source_kind','evaluation_at','collected_at'].some(field => audit.result.input_manifest[field as keyof AuditResult['input_manifest']] !== segment.input_manifest![field as keyof AuditResult['input_manifest']])) return stale()
 if (destination === 'findings') return {path:'/findings',query:{audit_id:audit.id}}
 if (destination === 'object') {
  if (!assetID || !segment.resources.some(row => row.asset_id === assetID)) return stale()
  const saved = await api.snapshot(segment.snapshot_id)
  if (saved.id !== segment.snapshot_id || !saved.input.assets.some(row => row.id === assetID) || !sameScope(saved.input.scope,latest.scope)) return stale()
  return {path:`/inventory/${encodeURIComponent(assetID)}`,query:{snapshot_id:saved.id}}
 }
 if (!segment.report_ref) return stale()
 const report = await api.report(segment.report_ref)
 if (report.id !== segment.report_ref || report.audit_id !== audit.id || report.job_id !== child.id) return stale()
 return {path:`/reports/${encodeURIComponent(report.id)}`,...(destination === 'recheck' ? {hash:'#report-recheck'} : {})}
}
