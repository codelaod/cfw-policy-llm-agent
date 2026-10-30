import { ApiClient, ApiError, ProtocolError, decodeJob } from './api.ts'
import { decodeConversationIntent, decodeConversationMessage, decodeIntentResponse } from './conversation-view.ts'
import type { ConversationIntent, IntentChoiceRequest, IntentTurnRequest, ProposalAcceptance } from './workspace-types.ts'
import { abortScope } from './abort.ts'
import { decodeCloudRecord, decodeCollectionOptions } from './cloud-view.ts'
import { decodePreparationDiscovery } from './preparation-view.ts'
import { decodeBatchRequest } from './batch-view.ts'
import { decodeGroupPreview, decodeGroupResult } from './group-view.ts'
import { decodeBusinessContext, decodeBusinessResult } from './business-view.ts'
import { decodeGuidanceCatalog, type GuidanceQuery } from './guidance.ts'
import type { AgentRecord, Asset, AssetChange, AssetView, Capabilities, ConfirmationPreview, ConfirmationResult, Conversation, CurrentWorkspace, DataSource, DraftRebaseRequest, ExecutionPlan, FindingRecord, Job, JobResult, MessageAcceptance, MessagePage, MessageRequest, Page, PlanRequest, ProfileDraft, ProfileDraftCurrentBase, ReportRecord, ReviewAction, ReviewRequest, Snapshot } from './workspace-types.ts'

const object = (value: unknown): value is Record<string, unknown> => typeof value === 'object' && value !== null && !Array.isArray(value)
const hasID = <T>(value: unknown): T => { if (!object(value) || typeof value.id !== 'string' || !value.id) throw new ProtocolError('记录缺少有效标识。'); return value as T }
function page<T>(value: unknown): Page<T> { if (object(value) && value.items === null) value.items = []; if (!object(value) || !Array.isArray(value.items)) throw new ProtocolError('列表响应校验失败。'); return value as Page<T> }

function asset(value: Asset): Asset {
  value.services = Array.isArray(value.services) ? value.services : []
  value.allowed = Array.isArray(value.allowed) ? value.allowed : []
  for (const service of value.services) { service.ports ??= []; service.allowed ??= []; service.required ??= [] }
  return value
}
function finding(value: unknown): FindingRecord {
  const record = hasID<FindingRecord>(value)
  if (!object(record.finding)) throw new ProtocolError('发现响应校验失败。')
  record.finding.assumptions ??= []; record.finding.limitations ??= []; record.finding.differences ??= []; record.finding.evidence_refs ??= []; record.evidence ??= []
  return record
}
function draftParts<T extends Pick<ProfileDraft,'changes'|'base_revisions'>>(record: T): T {
  record.changes ??= []; record.base_revisions ??= []
  for (const change of record.changes) {
    change.roles ??= []
    for (const role of change.roles) role.ports ??= []
    if (change.access) { change.access.allowed ??= []; change.access.services ??= []; change.access.exceptions ??= []; for (const service of change.access.services) { service.allowed ??= []; service.required ??= [] } }
  }
  return record
}
function draft(value: unknown): ProfileDraft { return draftParts(hasID<ProfileDraft>(value)) }
function draftBase(value: unknown, id: string): ProfileDraftCurrentBase {
  if (!object(value) || value.draft_id !== id || !Number.isSafeInteger(value.draft_revision) || Number(value.draft_revision) < 1 || typeof value.snapshot_id !== 'string' || !value.snapshot_id || typeof value.policy_scope_ref !== 'string' || !value.policy_scope_ref || !Array.isArray(value.changes) || !value.changes.length || !Array.isArray(value.base_revisions) || value.changes.length !== value.base_revisions.length || ['body','request_digest','raw_artifacts'].some(field=>field in value)) throw new ProtocolError('当前业务基版本未绑定原草稿与固定对象。')
  const assets = new Set<string>()
  for (const change of value.changes) { if (!object(change) || typeof change.asset_id !== 'string' || !change.asset_id.trim() || change.asset_id.length > 128 || assets.has(change.asset_id)) throw new ProtocolError('当前业务基版本的对象集合无效。'); assets.add(change.asset_id) }
  const heads = new Set<string>()
  for (const head of value.base_revisions) { if (!object(head) || typeof head.asset_id !== 'string' || !assets.has(head.asset_id) || heads.has(head.asset_id) || !Number.isSafeInteger(head.revision) || Number(head.revision) < 0 || typeof head.profile_version_id !== 'string' || typeof head.intent_version_id !== 'string') throw new ProtocolError('当前业务基版本的修订引用无效。'); heads.add(head.asset_id) }
  return draftParts(value as unknown as ProfileDraftCurrentBase)
}
function snapshot(value: unknown, expectedSource?: string): Snapshot {
  const saved = hasID<Snapshot>(value)
  if (!object(saved.input) || saved.input.schema_version !== 1 || !object(saved.input.scope) || typeof saved.input.scope.ref !== 'string' || !object(saved.input.completeness) || !object(saved.input.protection) || !object(saved.input.selection) || !object(saved.input.baseline)) throw new ProtocolError('固定资料响应校验失败。')
  if (!['synthetic', 'imported', 'live_readonly'].includes(saved.source_kind) || saved.input.source_kind !== saved.source_kind || expectedSource && saved.source_kind !== expectedSource) throw new ProtocolError('资料来源保证与本次操作不一致，不能升级导入声明。')
  saved.input.assets = Array.isArray(saved.input.assets) ? saved.input.assets : []
  saved.input.policies = Array.isArray(saved.input.policies) ? saved.input.policies : []
  saved.input.selection.asset_ids = Array.isArray(saved.input.selection.asset_ids) ? saved.input.selection.asset_ids : []
  saved.input.completeness.known_gaps = Array.isArray(saved.input.completeness.known_gaps) ? saved.input.completeness.known_gaps : []
  saved.input.baseline.exceptions = Array.isArray(saved.input.baseline.exceptions) ? saved.input.baseline.exceptions : []
  saved.input.assets = saved.input.assets.map(item => asset(item))
  return saved
}

function stringList(value: unknown): string[] {
  if (value === null || value === undefined) return []
  if (!Array.isArray(value) || value.some(item => typeof item !== 'string')) throw new ProtocolError('模型结果列表字段校验失败。')
  return value
}
function agentRecord(value: unknown, jobID: string): AgentRecord {
  const record = hasID<AgentRecord>(value)
  if (record.job_id !== jobID || !object(record.content) || record.content.job_id !== jobID || !object(record.content.input_manifest)) throw new ProtocolError('模型结果与当前任务不一致。')
  if (record.content.bindings !== undefined && !object(record.content.bindings)) throw new ProtocolError('本次模型引用绑定无效。')
  const output = record.content.output
  if (output) {
    if (!object(output) || record.content.error_code || typeof output.explanation !== 'string') throw new ProtocolError('模型结果缺少可核实的解释。')
    output.roles ??= []; output.recommendations ??= []; output.questions = stringList(output.questions)
    if (!Array.isArray(output.roles) || !Array.isArray(output.recommendations)) throw new ProtocolError('模型结构结果校验失败。')
    for (const role of output.roles) { if (!object(role) || typeof role.asset_ref !== 'string' || role.service_ref !== undefined && typeof role.service_ref !== 'string' || typeof role.role !== 'string') throw new ProtocolError('角色候选结构无效。'); role.supporting_evidence = stringList(role.supporting_evidence); role.contradicting_evidence = stringList(role.contradicting_evidence); role.unknowns = stringList(role.unknowns); role.questions = stringList(role.questions) }
    for (const item of output.recommendations) { if (!object(item) || typeof item.kind !== 'string' || typeof item.target_ref !== 'string' || typeof item.impact !== 'string' || typeof item.manual_next_step !== 'string') throw new ProtocolError('条件建议结构无效。'); item.conditions = stringList(item.conditions); item.evidence_refs = stringList(item.evidence_refs) }
  }
  return record
}

export class WorkspaceClient {
  readonly transport: ApiClient
  private readonly authRequired?: () => void
  constructor(token: () => string, authRequired?: () => void) { this.transport = new ApiClient(token, '/jobs'); this.authRequired = authRequired }
  async json<T>(path: string, init: RequestInit = {}): Promise<T> {
    const scope = abortScope([init.signal], 15000)
    try {
      const response = await this.transport.request(path, { ...init, signal: scope.signal }, 0)
      try { return await response.json() as T } catch (error) { if (error instanceof SyntaxError) throw new ProtocolError('服务端未返回有效 JSON。'); throw error }
    } catch (error) { if (error instanceof ApiError && error.status === 401) this.authRequired?.(); throw error }
    finally { scope.dispose() }
  }
  private post<T>(path: string, body: unknown, key?: string): Promise<T> {
    const headers: Record<string, string> = { 'Content-Type': 'application/json' }
    if (key) headers['Idempotency-Key'] = key
    return this.json<T>(path, { method: 'POST', headers, body: JSON.stringify(body) })
  }
  async guidance(query: GuidanceQuery) {
    const params = new URLSearchParams({ surface: query.surface })
    if (query.conversationID) params.set('conversation_id', query.conversationID)
    if (query.jobID) params.set('job_id', query.jobID)
    if (query.reportID) params.set('report_id', query.reportID)
    return decodeGuidanceCatalog(await this.json(`/guidance?${params}`), query)
  }
  health() { return this.json('/health') }
  capabilities() { return this.json<Capabilities>('/capabilities') }
  async snapshots(offset = 0) { const result = page<Snapshot>(await this.json(`/snapshots?offset=${offset}&limit=100`)); result.items = result.items.map(item => snapshot(item)); return result }
  async snapshot(id: string) { return snapshot(await this.json(`/snapshots/${encodeURIComponent(id)}`)) }
  async example(name: string) { return snapshot(await this.post(`/snapshots/examples/${encodeURIComponent(name)}`, {}), 'synthetic') }
  async importSnapshot(input: unknown) { return snapshot(await this.post('/snapshots/import', input), 'imported') }
  async assets(id: string, offset = 0) { const result = page<AssetView>(await this.json(`/snapshots/${encodeURIComponent(id)}/assets?offset=${offset}&limit=100`)); result.items = result.items.map(item => ({ ...item, asset: asset(item.asset), declared_asset: asset(item.declared_asset), local_confirmed_sections: item.local_confirmed_sections ?? [] })); return result }
  scopes() { return this.json<{ items: unknown[] }>('/policy-scopes') }
  async sources() {
    const result = await this.json<{ items: DataSource[] }>('/data-sources')
    if (!object(result) || !Array.isArray(result.items) || result.items.some(item => !object(item) || typeof item.id !== 'string' || !item.id || typeof item.label !== 'string' || typeof item.kind !== 'string' || typeof item.available !== 'boolean' || typeof item.cloud_reads !== 'boolean')) throw new ProtocolError('资料连接目录校验失败。')
    return result
  }
  async collectionOptions(id: string) { return decodeCollectionOptions(await this.json(`/data-sources/${encodeURIComponent(id)}/collection-options`), id) }
  workspace() { return this.json<CurrentWorkspace>('/workspaces/current') }
  setCurrentConversation(id: string, revision: number) { return this.json<CurrentWorkspace>('/workspaces/current', { method: 'PATCH', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ conversation_id: id, expected_revision: revision }) }) }
  async conversations(offset = 0) { return page<Conversation>(await this.json(`/conversations?offset=${offset}&limit=100`)) }
  async conversation(id: string) { return hasID<Conversation>(await this.json(`/conversations/${encodeURIComponent(id)}`)) }
  async createConversation(title: string, scope = '', key?: string) { return hasID<Conversation>(await this.post('/conversations', { title, policy_scope_ref: scope }, key)) }
  async patchConversation(id: string, body: { expected_revision: number; title?: string; policy_scope_ref?: string; archived?: boolean }) { return hasID<Conversation>(await this.json(`/conversations/${encodeURIComponent(id)}`, { method: 'PATCH', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) })) }
  async messages(id: string, after = 0): Promise<MessagePage> {
    const value: unknown = await this.json(`/conversations/${encodeURIComponent(id)}/messages?after_seq=${after}&limit=100`)
    if (!object(value) || value.items !== null && !Array.isArray(value.items) || !Number.isSafeInteger(value.next_seq) || Number(value.next_seq) < after || !Number.isSafeInteger(value.last_seq) || Number(value.last_seq) < Number(value.next_seq) || typeof value.has_more !== 'boolean') throw new ProtocolError('持久会话消息页无效。')
    const items = (value.items ?? []) as unknown[]
    let seq = after
    const messages = items.map(item => { const message = decodeConversationMessage(item, id); if (!message || message.seq !== ++seq) throw new ProtocolError('持久会话消息不连续。'); return message })
    if (value.next_seq !== seq) throw new ProtocolError('消息页游标与正文不一致。')
    return { items: messages, next_seq: seq, has_more: value.has_more, last_seq: Number(value.last_seq) }
  }
  async sendMessage(id: string, body: MessageRequest, key?: string): Promise<MessageAcceptance> {
    const value: unknown = await this.post(`/conversations/${encodeURIComponent(id)}/messages`, body, key)
    if (!object(value) || !object(value.message) || !object(value.job) || typeof value.replayed !== 'boolean') throw new ProtocolError('未能核实消息与任务已保存。')
    const message = value.message, job = decodeJob(value.job)
    if (typeof message.id !== 'string' || !message.id || message.conversation_id !== id || job.conversation_id !== id || message.job_id !== job.id || message.role !== 'user' || message.public_text !== body.text || message.policy_scope_ref !== body.policy_scope_ref || !Number.isSafeInteger(message.seq) || Number(message.seq) < 1 || typeof message.created_at !== 'string' || !Number.isFinite(Date.parse(message.created_at))) throw new ProtocolError('未能核实原会话消息已保存。')
    return { job, message: message as MessageAcceptance['message'], replayed: value.replayed }
  }
  async currentIntent(id: string): Promise<ConversationIntent | null> {
    return decodeConversationIntent(await this.json(`/conversations/${encodeURIComponent(id)}/intents/current`), id)
  }
  async intentTurn(id: string, body: IntentTurnRequest, key: string) {
    const value = await this.post(`/conversations/${encodeURIComponent(id)}/intent-turns`, body, key)
    const response = decodeIntentResponse(value, id)
    return { ...response, business: object(value) ? decodeBusinessResult(value.business, id) : null, group: object(value) ? decodeGroupResult(value.group,id) : null }
  }
  async resourceGroupPreview(id: string) { return decodeGroupPreview(await this.json(`/conversations/${encodeURIComponent(id)}/resource-group-preview`),id) }
  async businessContext(id: string) { return decodeBusinessContext(await this.json(`/conversations/${encodeURIComponent(id)}/business-context`), id) }
  async chooseIntent(id: string, body: IntentChoiceRequest, key: string) {
    return decodeIntentResponse(await this.post(`/conversations/${encodeURIComponent(id)}/intent-choices`, body, key), id)
  }
  async acceptProposal(id: string, proposalID: string, intentRevision: number, key: string): Promise<ProposalAcceptance> {
    const value: unknown = await this.post(`/conversations/${encodeURIComponent(id)}/proposals/${encodeURIComponent(proposalID)}/accept`, { expected_intent_revision: intentRevision }, key)
    if (!object(value)) throw new ProtocolError('未能核实计划已受理。')
    const job = decodeJob(value.job)
    if (job.conversation_id !== id) throw new ProtocolError('受理任务与当前会话不一致。')
    return { job }
  }
  async previewPlan(body: PlanRequest) { return hasID<ExecutionPlan>(await this.post('/execution-plans', body)) }
  async startJob(planID: string, key: string): Promise<Job> { return decodeJob(await this.post('/jobs', { plan_id: planID }, key)) }
  async jobs(offset = 0) { const result = page<Job>(await this.json(`/jobs?offset=${offset}&limit=100`)); result.items = result.items.map(item => decodeJob(item)); return result }
  async job(id: string) { return decodeJob(await this.json(`/jobs/${encodeURIComponent(id)}`), id) }
  async cancelJob(id: string) { return decodeJob(await this.post(`/jobs/${encodeURIComponent(id)}/cancel`, {}), id) }
  async jobResult(id: string) {
    const value = await this.json<JobResult>(`/jobs/${encodeURIComponent(id)}/result`)
    if (!object(value) || !Number.isSafeInteger(value.view_seq) || value.view_seq < 0 || typeof value.is_final !== 'boolean') throw new ProtocolError('任务结果版本校验失败。')
    if (value.audit) { value.audit.findings = (value.audit.findings ?? []).map(item => finding(item)); value.audit.result.findings ??= []; value.audit.result.evidence ??= []; value.audit.result.coverage.limitations ??= [] }
    if (value.recheck) { value.recheck.comparison.reasons ??= []; value.recheck.comparison.issues ??= [] }
    if (value.agent_result) value.agent_result = agentRecord(value.agent_result, id)
    if (value.cloud_result) value.cloud_result = decodeCloudRecord(value.cloud_result, id)
    if (value.preparation_discovery) value.preparation_discovery = decodePreparationDiscovery(value.preparation_discovery, id)
    if (value.batch) { value.batch = decodeBatchRequest(value.batch,id); if (value.audit || value.recheck || value.agent_result) throw new ProtocolError('分批主任务不能用单段结果代表整个集合。'); if (value.is_final && ['accepted','running'].includes(value.batch.status)) throw new ProtocolError('分批主任务的终态与分段记录不一致，请重新核对。') }
    return value
  }
  async jobInput(id: string): Promise<unknown> {
    const value: unknown = await this.json(`/jobs/${encodeURIComponent(id)}/input`)
    if (!object(value) || value.schema_version !== 1 || !object(value.scope) || typeof value.scope.ref !== 'string') throw new ProtocolError('任务封存输入校验失败。')
    // Keep exact field values. Applying current-head overlays or normalizing null
    // arrays would change the evidence represented by a persisted JSON Pointer.
    return value
  }
  async findings(auditID = '', offset = 0) { const result = page<FindingRecord>(await this.json(`/findings?offset=${offset}&limit=100${auditID ? `&audit_id=${encodeURIComponent(auditID)}` : ''}`)); result.items = result.items.map(item => finding(item)); return result }
  async finding(id: string) { return finding(await this.json(`/findings/${encodeURIComponent(id)}`)) }
  async reviewHistory(id: string, after = 0) {
    const value: unknown = await this.json(`/findings/${encodeURIComponent(id)}/review-actions?after_revision=${after}&limit=100`)
    if (Array.isArray(value)) return value as ReviewAction[]
    if (object(value) && Array.isArray(value.items)) return value.items as ReviewAction[]
    throw new ProtocolError('审阅历史响应校验失败。')
  }
  async review(id: string, body: ReviewRequest, key: string) {
    const value = await this.post<ReviewAction>(`/findings/${encodeURIComponent(id)}/review-actions`, body, key)
    if (!value.action_id || !Number.isSafeInteger(value.review_revision) || !value.saved_at) throw new ProtocolError('未能核实审阅已保存。')
    return value
  }
  async reports(offset = 0) { return page<ReportRecord>(await this.json(`/reports?offset=${offset}&limit=100`)) }
  async report(id: string) { return hasID<ReportRecord>(await this.json(`/reports/${encodeURIComponent(id)}`)) }
  async createReport(auditID: string, format: 'json' | 'markdown', key: string) { return hasID<ReportRecord>(await this.post('/reports', { audit_id: auditID, format }, key)) }
  async reportContent(id: string): Promise<Blob> { const scope = abortScope([], 15000); try { const response = await this.transport.request(`/reports/${encodeURIComponent(id)}/content`, { signal: scope.signal }, 0); return await response.blob() } catch (error) { if (error instanceof ApiError && error.status === 401) this.authRequired?.(); throw error } finally { scope.dispose() } }
  recheck(jobID: string, snapshotID: string, conversationID: string) { return this.post<ExecutionPlan>(`/audits/${encodeURIComponent(jobID)}/rechecks`, { snapshot_id: snapshotID, conversation_id: conversationID, reuse_evaluation_at: true }) }
  async createDraft(snapshotID: string, assets: string[]) { return draft(await this.post('/profile-drafts', { snapshot_id: snapshotID, asset_ids: assets })) }
  async draft(id: string) { return draft(await this.json(`/profile-drafts/${encodeURIComponent(id)}`)) }
  async draftCurrentBase(id: string) { return draftBase(await this.json(`/profile-drafts/${encodeURIComponent(id)}/current-base`),id) }
  async rebaseDraft(id: string, request: DraftRebaseRequest, key: string) {
    const saved = draft(await this.post(`/profile-drafts/${encodeURIComponent(id)}/rebase`,request,key)), expected = new Map(request.expected_base_revisions.map(head=>[head.asset_id,head]))
    if (saved.id !== id || saved.revision !== request.expected_draft_revision + 1 || saved.status !== 'draft' || saved.preview_id || saved.confirmation_id || saved.base_revisions.length !== expected.size || new Set(saved.base_revisions.map(head=>head.asset_id)).size !== expected.size || saved.base_revisions.some(head=>{const original=expected.get(head.asset_id);return !original || original.revision!==head.revision || original.profile_version_id!==head.profile_version_id || original.intent_version_id!==head.intent_version_id})) throw new ProtocolError('未能核实已按所核对的业务版本保存原草稿。')
    return saved
  }
  async updateDraft(id: string, revision: number, changes: AssetChange[]) { return draft(await this.json(`/profile-drafts/${encodeURIComponent(id)}`, { method: 'PATCH', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ expected_draft_revision: revision, changes }) })) }
  async previewDraft(id: string) { const value = await this.post<ConfirmationPreview>(`/profile-drafts/${encodeURIComponent(id)}/preview`, {}); if (!value.preview_id) throw new ProtocolError('确认预览缺少有效标识。'); value.diff ??= []; value.confirmable_sections ??= []; value.validation_errors ??= []; return value }
  async confirmDraft(id: string, previewID: string, revision: number, sections: string[], key: string) {
    const value = await this.post<ConfirmationResult>(`/profile-drafts/${encodeURIComponent(id)}/confirm`, { preview_id: previewID, expected_draft_revision: revision, confirmed_sections: sections }, key)
    return this.confirmationMarker(value, id)
  }
  async rejectDraft(id: string, revision: number, key: string) { return this.confirmationMarker(await this.post<ConfirmationResult>(`/profile-drafts/${encodeURIComponent(id)}/reject`, { expected_draft_revision: revision }, key), id) }
  private confirmationMarker(value: ConfirmationResult, id: string) { if (value.draft_id !== id || !value.action_id || !Number.isSafeInteger(value.revision) || !value.saved_at || !Number.isFinite(Date.parse(value.saved_at))) throw new ProtocolError('未能核实确认动作已保存，请先核对服务端记录。'); return value }
}
