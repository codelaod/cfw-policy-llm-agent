import type { AgentRecord, AttemptUsage, AuditRecord, Capabilities, JobEvent, LocalBinding } from './workspace-types.ts'
import { LatestRead } from './workspace-state.ts'
import { ApiError, ProtocolError } from './api.ts'

export const roleLabels: Record<string, string> = { management: '管理入口', database: '数据库', web: 'Web', application: '应用服务', cache: '缓存', background: '后台任务', unknown: '未知' }
export const recommendationLabels: Record<string, string> = { policy_change: '策略调整建议', coverage_followup: '保护覆盖核实', component_consideration: '组件适用性评估', confirm_information: '补充／确认资料' }
export const basisLabels: Record<string, string> = { known_gap: '已有规则缺口', applicability_assessment: '适用性评估', unknown_followup: '信息待核实' }
export const actionLabels: Record<string, string> = { confirm_intent: '人工确认业务要求', collect_missing_evidence: '补充缺失证据', review_rule: '人工复核规则', conditional_restrict: '有条件收窄配置', conditional_allow_required: '核对必要访问的条件' }
export const toolLabels: Record<string, string> = { get_partition_context: '查询当前分区', get_asset_context: '查询资产上下文', get_security_group_rules: '查询已关联安全组', get_business_intent: '查询固定业务要求', get_policy: '查询相关策略', resolve_reference: '展开已获准引用', get_finding_evidence: '查询原始发现证据', get_rule_definition: '查询已采用判据', get_nat_association: '查询 NAT 关联证据' }
export const toolStatusLabels: Record<string, string> = { started: '已开始', succeeded: '成功', completed: '成功', failed: '失败', rejected: '已拒绝', truncated: '结果超限', cancelled: '已取消' }

const count = (value: unknown): value is number => Number.isSafeInteger(value) && Number(value) >= 0
export const countText = (value: unknown) => count(value) ? value.toLocaleString('zh-CN') : '未知'
export function usageView(value?: Partial<AttemptUsage> | null) {
  const tokens = value?.reported_tokens
  const validTokens = !value?.usage_totals_overflow && tokens && [tokens.prompt_tokens, tokens.completion_tokens, tokens.total_tokens].every(count)
  const unknown = value?.unknown_usage_attempts
  const tokenState: 'unknown' | 'partial' | 'reported' = !validTokens ? 'unknown' : unknown === 0 ? 'reported' : 'partial'
  const coverage = value?.usage_totals_overflow ? '汇总超出可精确展示范围，用量保持未知。' : count(unknown) && unknown > 0 ? `${countText(unknown)} 次模型尝试未报告用量。` : !value ? '服务尚未提供本次用量记录。' : !validTokens ? '没有可核实的 token 报告。' : unknown === 0 ? '所有已记录模型尝试的已报告用量。' : '未报告尝试数尚未提供，仅展示已报告部分。'
  return { modelAttempts: countText(value?.model_attempts), cloudAttempts: countText(value?.cloud_attempts), unknownDelivery: countText(value?.unknown_delivery), knownResponses: countText(value?.known_usage_responses), tokenState, coverage, tokens: validTokens ? { prompt: countText(tokens.prompt_tokens), completion: countText(tokens.completion_tokens), total: countText(tokens.total_tokens) } : null }
}

export function modelCapability(value: Capabilities) {
  const ready = value.model_available === true || value.model_available === undefined && (value.models ?? []).some(model => model.available === true)
  const label = ready ? '模型已就绪' : value.model_available === false ? value.model_configured === false ? '模型未配置' : '模型当前未就绪' : value.model_configured === true ? '模型已配置，可用状态未知' : value.model_configured === false ? '模型未配置' : '模型状态未知'
  return { ready, label, mode: typeof value.model_mode === 'string' && value.model_mode ? value.model_mode : '未知' }
}

export function recordFor(record: AgentRecord | null | undefined, jobID: string): AgentRecord | null {
  return record?.id && record.job_id === jobID && record.content?.job_id === jobID && !(record.content.output && record.content.error_code) ? record : null
}
export function bindingFor(record: AgentRecord | null | undefined, jobID: string, alias: string, requireReturned = true): LocalBinding | null {
  const current = recordFor(record, jobID), bindings = current?.content.bindings
  const binding = bindings && Object.prototype.hasOwnProperty.call(bindings, alias) ? bindings[alias] : undefined
  if (!binding || !['input', 'result'].includes(binding.document) || typeof binding.field !== 'string' || !pointerParts(binding.field) || requireReturned && binding.returned !== true) return null
  return binding
}
export function assetRoute(record: AgentRecord | null | undefined, jobID: string, alias: string, snapshotID: string, serviceAlias?: string) {
  if (!snapshotID) return null
  const binding = bindingFor(record, jobID, alias, false)
  if (!binding?.asset_ref) return null
  let service = binding.service_ref
  if (serviceAlias) { const selected = bindingFor(record, jobID, serviceAlias, false); if (!selected?.service_ref || selected.asset_ref !== binding.asset_ref) return null; service = selected.service_ref }
  return { name: 'inventory', params: { id: binding.asset_ref }, query: { snapshot_id: snapshotID, ...(service ? { service } : {}) } }
}
function pointerParts(pointer: string): string[] | null {
  if (pointer === '') return []
  if (!pointer.startsWith('/')) return null
  const parts = pointer.slice(1).split('/')
  if (parts.some(part => /~(?:[^01]|$)/.test(part))) return null
  return parts.map(part => part.replace(/~1/g, '/').replace(/~0/g, '~'))
}
export type PointerValue = { found: true; value: unknown } | { found: false }
export function readPointer(document: unknown, pointer: string): PointerValue {
  const parts = pointerParts(pointer)
  if (!parts) return { found: false }
  let value = document
  for (const part of parts) {
    if (!value || typeof value !== 'object' || Array.isArray(value) && (!/^(0|[1-9]\d*)$/.test(part) || Number(part) >= value.length) || !Object.prototype.hasOwnProperty.call(value, part)) return { found: false }
    value = (value as Record<string, unknown>)[part]
  }
  return { found: true, value }
}
export function needsResultRefresh(event: JobEvent): boolean {
  return ['findings_available', 'report_available', 'message_available', 'tool_started', 'tool_finished', 'job_terminal'].includes(event.type) || event.type === 'stage_changed' && ['agent_result_saved', 'cloud_result_saved'].includes(String(event.payload.stage))
}

/** A protected fixed-input read belongs to one job and one persisted result. */
export class AgentEvidenceReader {
  jobID = ''
  recordID = ''
  selection: { alias: string; binding: LocalBinding } | null = null
  value: PointerValue = { found: false }
  busy = false
  failure: unknown = null
  private reads = new LatestRead()
  private input: unknown = undefined
  reset(jobID: string, recordID: string) { this.reads.invalidate(); this.jobID = jobID; this.recordID = recordID; this.selection = null; this.value = { found: false }; this.busy = false; this.failure = null; this.input = undefined }
  close() { this.reads.invalidate(); this.selection = null; this.value = { found: false }; this.failure = null; this.busy = false }
  async inspect(jobID: string, record: AgentRecord, alias: string, audit: AuditRecord | null | undefined, loadInput?: () => Promise<unknown>) {
    if (this.jobID !== jobID || this.recordID !== record.id) return
    const binding = bindingFor(record, jobID, alias)
    if (!binding) return
    const current = this.reads.begin()
    this.selection = { alias, binding }; this.value = { found: false }; this.failure = null; this.busy = true
    try {
      let document: unknown
      if (binding.document === 'result') document = audit?.result
      else if (audit?.input) document = audit.input
      else if (this.input !== undefined) document = this.input
      else if (loadInput) { document = await loadInput(); if (current()) this.input = document }
      if (current() && this.jobID === jobID && this.recordID === record.id) this.value = document === undefined ? { found: false } : readPointer(document, binding.field)
    } catch (cause) { if (current()) this.failure = cause }
    finally { if (current()) this.busy = false }
  }
}

export function localEvidenceError(error: unknown) {
  if (error instanceof ApiError) { if (error.code === 'INPUT_NOT_SEALED') return '任务输入尚未封存，请等待已保存输入后再读取。'; if (error.status === 401) return '访问口令已失效，请重新解锁。'; if (error.status === 403) return '此固定输入不可访问。'; if (error.status === 404) return '此服务尚未提供对应的固定输入。' }
  if (error instanceof ProtocolError) return error.message
  return '固定字段暂未读取，请核对后重试。'
}
export function toolRuns(events: JobEvent[], jobID: string) {
  const rows = new Map<string, { callID: string; name: string; status: string; elapsedMS?: number; evidenceRefs: string[] }>()
  for (const event of events) {
    if (event.job_id !== jobID || !['tool_started', 'tool_finished'].includes(event.type)) continue
    const id = event.payload.call_id, name = event.payload.tool_name
    if (!id || !name) continue
    const old = rows.get(id)
    if (old && old.name !== name) { old.status = 'mismatch'; old.evidenceRefs = []; continue }
    const row = old ?? { callID: id, name, status: 'unknown', evidenceRefs: [] }
    row.status = typeof event.payload.status === 'string' ? event.payload.status : 'unknown'
    const elapsed = event.payload.elapsed_ms ?? event.payload.duration_ms
    if (count(elapsed)) row.elapsedMS = elapsed
    if (Array.isArray(event.payload.evidence_refs)) row.evidenceRefs = event.payload.evidence_refs.filter((ref): ref is string => typeof ref === 'string')
    rows.set(id, row)
  }
  return [...rows.values()]
}

export function inventoryFocus(assets: readonly { id: string; services: readonly { id: string }[] }[], requestedAsset: unknown, requestedService: unknown, effectiveServices: readonly { id: string }[] = []) {
  const target = typeof requestedAsset === 'string' ? requestedAsset : ''
  const asset = target ? assets.find(item => item.id === target) : assets[0]
  const serviceID = typeof requestedService === 'string' ? requestedService : ''
  return { assetID: asset?.id ?? '', missingAsset: !!target && !asset, serviceID, missingService: !!serviceID && !(asset?.services.some(service => service.id === serviceID) || effectiveServices.some(service => service.id === serviceID)) }
}
