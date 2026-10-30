import { ProtocolError } from './api.ts'
import type { PreparationDiscovery, ResourceCandidate } from './workspace-types.ts'
const object = (v: unknown): v is Record<string, unknown> => !!v && typeof v === 'object' && !Array.isArray(v)
const text = (v: unknown, max = 512): v is string => typeof v === 'string' && v.length <= max
const identity = (v: unknown): v is string => typeof v === 'string' && /^[A-Za-z0-9_-]{1,128}$/.test(v)
const time = (v: unknown): v is string => typeof v === 'string' && Number.isFinite(Date.parse(v))
const strings = (v: unknown): v is string[] => Array.isArray(v) && v.every(item => typeof item === 'string')
export function decodePreparationDiscovery(value: unknown, jobID: string): PreparationDiscovery {
  if (!object(value) || !identity(value.id) || value.job_id !== jobID || !time(value.created_at) || typeof value.complete !== 'boolean' || value.code !== undefined && (typeof value.code !== 'string' || !/^[A-Z0-9_]{1,128}$/.test(value.code)) || ['raw', 'raw_artifacts', 'body', 'config_revision', 'request_digest'].some(field => field in value)) throw new ProtocolError('资料准备记录与当前任务不一致。')
  value.resources ??= []; value.coverages ??= []; value.unverified_resource_refs ??= []
  if (!Array.isArray(value.resources) || !Array.isArray(value.coverages) || !strings(value.unverified_resource_refs)) throw new ProtocolError('资料准备候选或覆盖结构无效。')
  const identities = new Set<string>()
  for (const row of value.resources) {
    if (!object(row) || !identity(row.id) || identities.has(row.id) || !identity(row.resource_id) || !text(row.name) || !identity(row.region) || !['ecs', 'nat_gateway'].includes(String(row.resource_type)) || row.source !== 'live_readonly' || !time(row.observed_at)) throw new ProtocolError('资料准备候选缺少规范身份、来源或时点。')
    identities.add(row.id)
  }
  for (const coverage of value.coverages) {
    if (!object(coverage) || !object(coverage.selector) || typeof coverage.complete !== 'boolean' || !time(coverage.observed_at) || !text(coverage.source, 128) || !strings(coverage.limitations) || !Number.isSafeInteger(coverage.revision) || Number(coverage.revision) < 0) throw new ProtocolError('资料准备覆盖记录无效。')
    const selector = coverage.selector
    if (selector.region !== undefined && !identity(selector.region) || selector.regions !== undefined && !strings(selector.regions) || selector.resource_type !== undefined && !['ecs', 'nat_gateway'].includes(String(selector.resource_type)) || selector.name !== undefined && !text(selector.name) || selector.resource_id !== undefined && selector.resource_id !== '' && !identity(selector.resource_id) || selector.resource_ids !== undefined && !strings(selector.resource_ids)) throw new ProtocolError('目录查询范围无效。')
  }
  if (value.unverified_resource_refs.some(ref => !identities.has(ref)) || value.complete && (value.code || value.unverified_resource_refs.length || value.coverages.length === 0 || value.coverages.some(coverage => !(coverage as Record<string, unknown>).complete))) throw new ProtocolError('目录完整性与候选核验状态冲突。')
  return value as PreparationDiscovery
}
export function preparationFor(value: PreparationDiscovery | null | undefined, jobID: string) { return value?.job_id === jobID ? value : null }
export function preparationTitle(value: PreparationDiscovery) {
  return ({ DISCOVERY_NEEDS_CLARIFICATION: '目录发现多个候选，需要重新明确对象', DISCOVERY_NO_MATCH: '本次目录条件未匹配对象', DISCOVERY_MISSING_OBJECTS: '部分指定对象尚未取得', DISCOVERY_FAILED: '目录准备未完整完成', CANCELLED: '目录准备已取消' }[value.code ?? ''] ?? (value.complete ? '本次有限目录准备已保存' : '本次目录准备仍有缺口'))
}
export function preparationEmptyText(value: PreparationDiscovery) {
  return value.coverages.length > 0 && value.coverages.every(item => item.complete) && value.code === 'DISCOVERY_NO_MATCH' ? '已记录的有限查询范围已穷尽且未匹配。可核对名称、地域或明确 ID；这不证明其他范围的资源不存在。' : '尚未取得候选。目录未知、缺页或失败不能当作资源不存在，请保留本次记录并重新核对范围。'
}
export function preparationGoal(resource: ResourceCandidate): string {
  if (!/^(i|ngw)-[A-Za-z0-9_-]{1,124}$/.test(resource.resource_id) || !/^[a-z0-9-]{1,128}$/.test(resource.region) || !['ecs', 'nat_gateway'].includes(resource.resource_type)) throw new Error('此候选缺少可用于新目标的明确云 ID 与地域。')
  return `审计 ${resource.region} 的 ${resource.resource_type === 'ecs' ? 'ECS' : 'NAT 网关'}，资源 ID ${resource.resource_id}；请核对本次边界、资料与执行计划`
}
