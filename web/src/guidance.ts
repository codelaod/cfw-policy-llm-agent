import { ProtocolError } from './api.ts'
import type { ConversationEditor } from './conversation-state.ts'
import type { Conversation, ConversationIntent, Snapshot } from './workspace-types.ts'
import type { WorkspaceClient } from './workspace-api.ts'

export type GuidanceSurface = 'conversation' | 'overview' | 'profile' | 'audit' | 'finding' | 'report'
export type GuidanceQuery = { surface: GuidanceSurface; conversationID?: string; jobID?: string; reportID?: string }
export type GuidanceContext = GuidanceQuery & { snapshotID?: string; draftID?: string }
export type GuidanceItem = {
  id: string; catalog_version: string; display_text: string; behavior: 'fill_input' | 'navigate_local' | 'open_plan_preview';
  intent: { action: string; text?: string; mode?: 'rules_only' | 'rules_and_agent'; data_policy?: 'prefer_fresh' | 'force_refresh' | 'pinned_snapshot'; context_report_id?: string; snapshot_id?: string; recheck_of_job_id?: string; fresh_after?: string };
  required_refs: string[]; effects: { allow_cloud_reads: boolean; allow_model_requests: boolean }; state: 'available' | 'needs_input'; missing_inputs: string[];
  action: { type: string; target: { type: string; id?: string; conversation_id?: string; intent_revision?: number } }; confirmation: { required: boolean; conditions: string[] };
}
export type GuidanceCatalog = { catalog_version: string; state: string; items: GuidanceItem[] }
export type GuidanceResolution = { kind: 'fill'; conversationID: string; snapshotID?: string } | { kind: 'route'; route: { path: string; query?: Record<string, string>; hash?: string }; conversationID?: string; snapshotID?: string; message?: string }
const object = (v: unknown): v is Record<string, unknown> => !!v && typeof v === 'object' && !Array.isArray(v)
const code = (v: unknown): v is string => typeof v === 'string' && /^[A-Za-z0-9_-]{1,128}$/.test(v)
const optionalRef = (v: unknown) => v === undefined || v === '' || code(v)
const codes = (v: unknown): v is string[] => Array.isArray(v) && v.length <= 32 && v.every(code)
const targets = ['composer', 'sample_catalog', 'offline_import', 'snapshot', 'business_draft', 'report', 'job_rules', 'conversation_history', 'report_review', 'finding', 'intent', 'plan_preview', 'intent_candidates', 'job_observation', 'job', 'job_cancel_confirmation', 'report_comparison', 'submission', 'submission_retry_confirmation', 'draft_diff', 'draft_preview', 'draft_save_status', 'draft_retry_confirmation']
export function decodeGuidanceCatalog(value: unknown, query: GuidanceQuery): GuidanceCatalog {
  if (!object(value) || !code(value.catalog_version) || !code(value.state) || !Array.isArray(value.items) || value.items.length > 3) throw new ProtocolError('下一步引导目录无效。')
  const ids = new Set<string>()
  for (const item of value.items) {
    if (!object(item) || !code(item.id) || ids.has(item.id) || item.catalog_version !== value.catalog_version || typeof item.display_text !== 'string' || !item.display_text.trim() || item.display_text.length > 600 || !['fill_input', 'navigate_local', 'open_plan_preview'].includes(String(item.behavior)) || !object(item.intent) || !code(item.intent.action) || item.intent.text !== undefined && (typeof item.intent.text !== 'string' || item.intent.text.length > 4000) || item.intent.mode !== undefined && item.intent.mode !== '' && !['rules_only', 'rules_and_agent'].includes(String(item.intent.mode)) || item.intent.data_policy !== undefined && item.intent.data_policy !== '' && !['prefer_fresh', 'force_refresh', 'pinned_snapshot'].includes(String(item.intent.data_policy)) || !codes(item.required_refs) || !codes(item.missing_inputs) || !['available', 'needs_input'].includes(String(item.state)) || !object(item.effects) || typeof item.effects.allow_cloud_reads !== 'boolean' || typeof item.effects.allow_model_requests !== 'boolean' || !object(item.action) || item.action.type !== item.behavior || !object(item.action.target) || !targets.includes(String(item.action.target.type)) || !optionalRef(item.action.target.id) || !optionalRef(item.action.target.conversation_id) || item.action.target.intent_revision !== undefined && (!Number.isSafeInteger(item.action.target.intent_revision) || Number(item.action.target.intent_revision) < 0) || !object(item.confirmation) || typeof item.confirmation.required !== 'boolean' || !codes(item.confirmation.conditions)) throw new ProtocolError('下一步引导条目无效。')
    if (query.conversationID && item.action.target.conversation_id && item.action.target.conversation_id !== query.conversationID) throw new ProtocolError('引导不属于此会话。')
    if (!optionalRef(item.intent.context_report_id) || !optionalRef(item.intent.snapshot_id) || !optionalRef(item.intent.recheck_of_job_id)) throw new ProtocolError('引导关联记录无效。')
    ids.add(item.id)
  }
  return value as GuidanceCatalog
}
const missingLabels: Record<string, string> = { conversation_id: '需要先明确会话', intent_id: '还没有已保存目标', intent_revision: '需要核对目标版本', proposal_id: '还没有当前执行提议', plan_id: '还没有可打开的计划', snapshot_id: '还没有本范围的固定资料', job_id: '还没有关联任务', report_id: '还没有固定报告', finding_id: '还没有关联发现', draft_id: '还没有已保存的业务草稿', submission_id: '需要核对原提交标识', model_mode_selection: '需要先选择本次模型模式', model_available: '模型尚未就绪', readonly_connection: '只读连接尚未就绪', current_data_or_readonly_connection: '需要当前资料或可用只读连接', change_completed_at: '需要明确人工修改完成时点' }
export function guidanceMissingLabel(value: string) { return missingLabels[value] ?? '还需要核对本次对象、资料或确认条件' }
export function guidanceMissing(item: GuidanceItem, context: GuidanceContext): string[] {
  const out = item.missing_inputs.map(guidanceMissingLabel)
  const target = item.action.target
  if (!['composer', 'sample_catalog', 'offline_import'].includes(target.type) && !target.id && !(target.type.startsWith('draft_') || target.type === 'business_draft') && !out.length) out.push('还没有可打开的关联记录')
  if ((target.type.startsWith('draft_') || target.type === 'business_draft') && !target.id && !context.draftID && !out.length) out.push('还没有已保存的业务草稿；请从本任务资料选择对象并创建草稿')
  if (item.state === 'needs_input' && !out.length) out.push('请先核对缺失的执行条件')
  return out
}
function missing(message: string): never { throw new Error(message) }
/** Resolves existing records using protected local reads; never creates a plan, task or draft. */
export async function resolveGuidance(item: GuidanceItem, context: GuidanceContext, api: Pick<WorkspaceClient, 'conversation' | 'currentIntent' | 'job' | 'jobResult' | 'report' | 'snapshot' | 'draft' | 'finding'>): Promise<GuidanceResolution> {
  const gaps = guidanceMissing(item, context)
  if (gaps.length) missing(gaps.join('；'))
  const target = item.action.target
  if (context.conversationID && target.conversation_id && target.conversation_id !== context.conversationID) missing('引导不属于当前会话，请重新读取。')
  let conversation = context.conversationID ?? '', jobID = context.jobID ?? '', snapshotID = context.snapshotID ?? '', auditID = '', producerAuditID = ''
  if (context.reportID) {
    const report = await api.report(context.reportID)
    if (report.id !== context.reportID || jobID && report.job_id && report.job_id !== jobID) missing('报告与关联任务不一致。')
    auditID = report.audit_id; jobID ||= report.job_id ?? ''
  }
  if (jobID) {
    const job = await api.job(jobID)
    if (job.id !== jobID || conversation && job.conversation_id !== conversation || target.conversation_id && job.conversation_id !== target.conversation_id || auditID && job.result_ref !== auditID) missing('引导任务不属于此会话。')
    conversation = job.conversation_id; producerAuditID = job.result_ref ?? ''
  }
  if (!conversation && target.conversation_id) {
    const saved = await api.conversation(target.conversation_id)
    if (saved.id !== target.conversation_id || saved.archived) missing('关联会话不可用。')
    conversation = saved.id
  }
  if (item.behavior === 'fill_input') {
    if (!item.intent.text?.trim()) missing('引导没有可预填的内容。')
    if (item.intent.action === 'explain_report') {
      const reportID = item.intent.context_report_id, fixedID = item.intent.snapshot_id
      if (!reportID || !fixedID || !item.intent.text.includes(reportID) || context.reportID && context.reportID !== reportID || item.effects.allow_cloud_reads || item.intent.mode !== 'rules_and_agent' || item.intent.data_policy !== 'pinned_snapshot') missing('历史解释缺少原报告、固定资料或明确的零云模式。')
      const report = await api.report(reportID!)
      if (report.id !== reportID || !report.job_id || jobID && report.job_id !== jobID || auditID && report.audit_id !== auditID) missing('历史解释报告与原任务不一致。')
      const producer = await api.job(report.job_id!)
      if (producer.id !== report.job_id || producer.result_ref !== report.audit_id || conversation && producer.conversation_id !== conversation) missing('历史解释任务不属于原会话。')
      const result = await api.jobResult(producer.id)
      if (!result.audit || result.audit.id !== report.audit_id || result.audit.snapshot_id !== fixedID || result.snapshot_id && result.snapshot_id !== fixedID) missing('历史解释必须使用原报告的固定资料。')
      const saved = await api.snapshot(fixedID!)
      if (saved.id !== fixedID || saved.input.scope.ref !== result.audit!.result.input_manifest.policy_scope.ref || saved.source_kind !== result.audit!.result.input_manifest.source_kind) missing('历史解释的资料来源或分区与原报告不一致。')
      return { kind: 'fill', conversationID: producer.conversation_id, snapshotID: fixedID }
    }
    return { kind: 'fill', conversationID: conversation }
  }
  if (target.type === 'sample_catalog' || target.type === 'offline_import') return { kind: 'route', route: { path: '/overview', hash: target.type === 'sample_catalog' ? '#prepare-title' : '#snapshot-file' } }
  const id = target.id ?? ''
  if (['plan_preview', 'intent', 'intent_candidates'].includes(target.type)) {
    if (!conversation) missing('需要先明确原会话。')
    const intent: ConversationIntent | null = await api.currentIntent(conversation)
    if (!intent || target.intent_revision && intent.revision !== target.intent_revision || (target.type === 'plan_preview' ? intent.proposal?.plan_id !== id : intent.id !== id)) missing('原目标或计划版本已变化，请重新读取引导。')
    if (target.type !== 'intent_candidates' && !intent!.proposal) missing('当前目标尚无可打开的计划，请先在对话中核对缺项。')
    return { kind: 'route', route: { path: '/conversations', hash: target.type === 'intent_candidates' ? '#candidate-heading' : '#proposal-heading' }, conversationID: conversation }
  }
  if (target.type === 'conversation_history') {
    if (!conversation || id !== conversation) missing('会话引用不一致。')
    return { kind: 'route', route: { path: '/conversations' }, conversationID: conversation }
  }
  if (['job', 'job_rules', 'job_observation', 'job_cancel_confirmation'].includes(target.type)) {
    if (jobID && id !== jobID) missing('引导指向的不是所选任务。')
    const job = await api.job(id)
    if (job.id !== id || conversation && job.conversation_id !== conversation) missing('任务不属于原会话。')
    return { kind: 'route', route: { path: `/audits/${encodeURIComponent(id)}` }, message: target.type === 'job_cancel_confirmation' ? '已打开原任务；取消仍需点击该任务的明确操作。' : '' }
  }
  if (['report', 'report_review', 'report_comparison'].includes(target.type)) {
    if (context.reportID && id !== context.reportID) missing('引导指向的不是所选报告。')
    const report = await api.report(id)
    if (report.id !== id || jobID && report.job_id && report.job_id !== jobID || jobID && report.audit_id !== producerAuditID || auditID && report.audit_id !== auditID) missing('报告与原任务不一致。')
    if (report.job_id) { const producer = await api.job(report.job_id); if (conversation && producer.conversation_id !== conversation) missing('报告不属于原会话。') }
    return { kind: 'route', route: target.type === 'report_review' ? { path: '/findings', query: { audit_id: report.audit_id } } : target.type === 'report_comparison' && report.job_id ? { path: `/audits/${encodeURIComponent(report.job_id)}` } : { path: `/reports/${encodeURIComponent(id)}`, ...(item.behavior === 'open_plan_preview' ? { hash: '#report-recheck' } : {}) }, message: item.behavior === 'open_plan_preview' ? '已打开原报告。核对对象与资料后，仍需单独预览并接受新计划。' : '' }
  }
  if (jobID) {
    const result = await api.jobResult(jobID), fixed = result.snapshot_id || result.audit?.snapshot_id || ''
    if (snapshotID && fixed && snapshotID !== fixed) missing('所选资料不是原任务的固定资料。')
    snapshotID = fixed; auditID ||= result.audit?.id ?? ''
  }
  if (!snapshotID && conversation) { const intent = await api.currentIntent(conversation); snapshotID = intent?.data_policy.snapshot_id ?? '' }
  if (target.type === 'snapshot') {
    if (!snapshotID || id !== snapshotID) missing('还不能将这份资料绑定到本次会话或任务，请先核对来源。')
    const saved = await api.snapshot(id)
    if (saved.id !== id) missing('固定资料引用不一致。')
    return { kind: 'route', route: item.behavior === 'open_plan_preview' ? { path: '/audits' } : { path: '/inventory', query: { snapshot_id: id } }, snapshotID: id }
  }
  if (target.type === 'business_draft' || target.type.startsWith('draft_')) {
    const draftID = id || context.draftID || '', saved = await api.draft(draftID)
    if (saved.id !== draftID || !snapshotID || saved.snapshot_id !== snapshotID) missing('业务草稿不属于本次固定资料，请核对原对象。')
    return { kind: 'route', route: { path: '/inventory', query: { draft: draftID } } }
  }
  if (target.type === 'finding') {
    const finding = await api.finding(id)
    if (finding.id !== id || !auditID || finding.audit_id !== auditID) missing('发现不属于本次固定审计。')
    return { kind: 'route', route: { path: '/findings', query: { audit_id: auditID, finding_id: id } } }
  }
  return missing('还没有可用的原提交核对入口，请在原会话保留原内容与提交标识。')
}
/** Prefill is local editing only. Pending requests remain byte-for-byte unchanged. */
export function applyGuidanceFill(editor: ConversationEditor, item: GuidanceItem) {
  if (editor.busy || editor.turn.pendingBody || editor.choice.pendingBody || editor.acceptance.pendingBody) missing('先核对尚未确认结果的原提交，原内容与标识已保留。')
  if (item.behavior !== 'fill_input' || !item.intent.text?.trim()) missing('此引导不能作为输入预填。')
  prefillConversationEditor(editor, item.intent.text, item.intent.mode, item.intent.data_policy)
}
export function prefillConversationEditor(editor: ConversationEditor, text: string, mode?: GuidanceItem['intent']['mode'], dataPolicy?: GuidanceItem['intent']['data_policy']) {
  if (editor.busy || editor.turn.pendingBody || editor.choice.pendingBody || editor.acceptance.pendingBody) missing('先核对尚未确认结果的原提交，原内容与标识已保留。')
  if (!text.trim() || text.length > 4000) missing('新目标缺少有效内容。')
  editor.text = text
  if (mode) { editor.mode = mode; editor.modeEdited = true; editor.optionsInitialized = true }
  if (dataPolicy) { editor.dataPolicy = dataPolicy; editor.dataPolicyEdited = true; editor.optionsInitialized = true }
}

/** A scope alias alone does not relate a new conversation goal to this fixed inventory. */
export function inventoryGuidanceConversation(conversation: Conversation | null, intent: ConversationIntent | null, snapshot: Snapshot | null): string {
 if (!conversation || !snapshot || conversation.archived || conversation.policy_scope_ref !== snapshot.policy_scope_ref) return ''
 if (!intent) return conversation.id
 if (intent.conversation_id !== conversation.id || intent.boundary !== snapshot.input.scope.boundary || intent.direction !== snapshot.input.scope.direction) return ''
 if (intent.data_policy.snapshot_id === snapshot.id) return conversation.id
 if (!intent.selection.length) return ''
 const identities = new Set(snapshot.input.assets.flatMap(asset => [asset.id, asset.provider_resource_id ?? '']).filter(Boolean))
 return intent.selection.every(resource => identities.has(resource.local_asset_id || resource.resource_id)) ? conversation.id : ''
}
