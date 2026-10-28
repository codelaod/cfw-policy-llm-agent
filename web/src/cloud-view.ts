import { ProtocolError } from './api.ts'
import type { CloudRecord, CollectionOptions, Conversation, PlanRequest } from './workspace-types.ts'

const object = (value: unknown): value is Record<string, unknown> => value !== null && typeof value === 'object' && !Array.isArray(value)
const text = (value: unknown): value is string => typeof value === 'string' && value.length > 0
const count = (value: unknown): value is number => Number.isSafeInteger(value) && Number(value) >= 0

export function decodeCollectionOptions(value: unknown, sourceID: string): CollectionOptions {
  if (!object(value) || value.source_ref !== sourceID || typeof value.configured !== 'boolean' || typeof value.available !== 'boolean' || value.identity_verified !== false || !count(value.max_http_attempts) || value.max_http_attempts > 100 || !Array.isArray(value.scopes)) throw new ProtocolError('本地云连接选项校验失败。')
  const scopes = new Set<string>()
  for (const item of value.scopes) {
    if (!object(item) || !object(item.scope) || !text(item.scope.ref) || scopes.has(item.scope.ref) || !Array.isArray(item.assets)) throw new ProtocolError('显式云范围目录无效。')
    scopes.add(item.scope.ref)
    const assets = new Set<string>()
    for (const asset of item.assets) { if (!object(asset) || !text(asset.id) || !text(asset.region) || assets.has(asset.id)) throw new ProtocolError('显式资产目录无效。'); assets.add(asset.id) }
  }
  return value as CollectionOptions
}

export function cloudPlanRequest(options: CollectionOptions | null, sourceID: string, scopeID: string, assetIDs: string[], gatewayOnly: boolean, conversation: Conversation | null, kind: 'collect' | 'connection_check'): PlanRequest {
  if (!options?.configured || !options.available || options.source_ref !== sourceID || options.max_http_attempts < 1) throw new Error('先选择可用的显式服务端只读连接。')
  const scope = options.scopes.find(item => item.scope.ref === scopeID)
  if (!scope) throw new Error('请选择本次明确读取的范围。')
  if (!conversation || conversation.archived || conversation.policy_scope_ref !== scopeID) throw new Error('请选择同一范围的未归档会话。')
  if (gatewayOnly ? scope.scope.boundary !== 'nat' || !scope.scope.gateway_ref || assetIDs.length !== 0 : assetIDs.length === 0 || assetIDs.length > 50 || new Set(assetIDs).size !== assetIDs.length || assetIDs.some(id => !scope.assets.some(asset => asset.id === id))) throw new Error('请选择目录中的明确资产，或明确选择当前 NAT 网关策略集。')
  return { kind, conversation_id: conversation.id, data_source_id: sourceID, policy_scope_ref: scopeID, selection: gatewayOnly ? { kind: 'gateway_policy', asset_ids: [], gateway_ref: scope.scope.gateway_ref } : { kind: 'assets', asset_ids: [...assetIDs].sort() }, data_policy: { mode: 'force_refresh', allow_stale_fallback: false }, baseline_version_id: 'B2-v1', mode: 'rules_only' }
}

export function cloudRecordFor(value: CloudRecord | null | undefined, jobID: string): CloudRecord | null {
  return value?.id && value.job_id === jobID && value.content?.job_id === jobID && ['collect', 'connection_check'].includes(value.content.kind) ? value : null
}
export function decodeCloudRecord(value: unknown, jobID: string): CloudRecord {
  if (!object(value) || !text(value.id) || value.job_id !== jobID || !object(value.content) || value.content.job_id !== jobID || !['collect', 'connection_check'].includes(String(value.content.kind)) || !text(value.content.policy_scope_ref) || typeof value.content.identity_verified !== 'boolean' || typeof value.content.product_ready !== 'boolean' || value.content.snapshot_id !== undefined && !text(value.content.snapshot_id)) throw new ProtocolError('云读取结果与当前任务不一致。')
  const content = value.content
  if (content.kind === 'connection_check' && (content.product_ready || content.snapshot_id)) throw new ProtocolError('身份核对结果不能替代产品采集。')
  content.observations ??= []; content.limitations ??= []
  if (!Array.isArray(content.observations) || !Array.isArray(content.limitations) || content.limitations.some(code => typeof code !== 'string')) throw new ProtocolError('云读取结果结构无效。')
  for (const row of content.observations) if (!object(row) || !count(row.round) || !text(row.operation) || !count(row.pages) || !text(row.started_at) || !text(row.finished_at) || typeof row.enumeration_complete !== 'boolean' || typeof row.semantic_sha256 !== 'string') throw new ProtocolError('云读取观测记录无效。')
  return value as CloudRecord
}
export function cloudSnapshotID(record: CloudRecord | null | undefined, jobID: string, resultSnapshotID = ''): string {
  const current = cloudRecordFor(record, jobID)
  const id = current?.content.snapshot_id
  return current?.content.kind === 'collect' && current.content.product_ready && id && (!resultSnapshotID || resultSnapshotID === id) ? id : ''
}
export const cloudOperationLabels: Record<string, string> = { DescribeAssetList: '防火墙资产目录', DescribeControlPolicy: '互联网边界策略', DescribeNatFirewallList: 'NAT 防火墙目录', DescribeNatFirewallControlPolicy: 'NAT 边界策略', DescribeAddressBook: '策略引用地址组', DescribeInstances: '显式 ECS 对象', DescribeSecurityGroupAttribute: '已关联安全组规则' }
export const cloudLimitationLabels: Record<string, string> = { IDENTITY_ONLY: '本轮仅核对调用身份，产品权限、字段与完整性尚未验证。', COLLECTION_INCOMPLETE: '采集未穷尽，受影响的审计检查仍需补充资料。', CONFIGURATION_NOT_STABLE: '两轮观测未确认稳定，不能作为原子快照。', OBSERVATION_INCOMPLETE: '部分观测未完整取得。', SEMANTICS_UNVERIFIED: '产品默认行为或策略语义尚未核实。' }
