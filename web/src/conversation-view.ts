import { ProtocolError, decodeJob } from './api.ts'
import type { Conversation, ConversationIntent, ConversationMessage, ConversationProposal, IntentTurnResponse, ResourceCandidate } from './workspace-types.ts'

const object = (value: unknown): value is Record<string, unknown> => value !== null && typeof value === 'object' && !Array.isArray(value)
const nonempty = (value: unknown): value is string => typeof value === 'string' && !!value.trim()
const revision = (value: unknown): value is number => Number.isSafeInteger(value) && Number(value) >= 0
function strings(value: unknown): string[] { if (value == null) return []; if (!Array.isArray(value) || value.some(item => typeof item !== 'string')) throw new ProtocolError('会话资料列表无效。'); return value }
function candidate(value: unknown): ResourceCandidate {
  if (!object(value) || !['id', 'resource_id', 'name', 'region', 'resource_type', 'source', 'observed_at'].every(field => typeof value[field] === 'string') || !nonempty(value.id) || !nonempty(value.resource_id) && !nonempty(value.local_asset_id)) throw new ProtocolError('资源候选缺少规范身份或来源。')
  return value as ResourceCandidate
}
export function decodeConversationProposal(value: unknown): ConversationProposal {
  if (!object(value) || !nonempty(value.id) || !revision(value.intent_revision) || typeof value.summary !== 'string' || !nonempty(value.status) || typeof value.requires_discovery !== 'boolean' || !object(value.effects) || typeof value.effects.allow_cloud_reads !== 'boolean' || typeof value.effects.allow_model_requests !== 'boolean') throw new ProtocolError('执行提议响应缺少范围或效果说明。')
  return { ...value, gaps: strings(value.gaps), warnings: strings(value.warnings) } as ConversationProposal
}
export function decodeConversationIntent(value: unknown, conversationID: string): ConversationIntent | null {
  if (value === null) return null
  if (!object(value) || !nonempty(value.id) || value.conversation_id !== conversationID || !revision(value.revision) || !nonempty(value.status) || typeof value.action !== 'string' || !object(value.query) || !object(value.catalogue) || typeof value.catalogue.complete !== 'boolean' || value.catalogue.observed_at !== undefined && typeof value.catalogue.observed_at !== 'string' || value.catalogue.source !== undefined && typeof value.catalogue.source !== 'string' || !['rules_only', 'rules_and_agent'].includes(String(value.mode)) || !object(value.data_policy) || !['prefer_fresh', 'force_refresh', 'pinned_snapshot'].includes(String(value.data_policy.mode)) || typeof value.boundary !== 'string' || typeof value.direction !== 'string') throw new ProtocolError('保存的目标与当前会话不一致或格式无效。')
  const selected = value.selection ?? [], candidates = value.candidates ?? []
  if (!Array.isArray(selected) || !Array.isArray(candidates)) throw new ProtocolError('会话候选范围无效。')
  const proposal = value.proposal == null ? null : decodeConversationProposal(value.proposal)
  if (proposal && value.mode === 'rules_only' && proposal.effects.allow_model_requests) throw new ProtocolError('只运行规则的提议不能增加模型请求效果。')
  if (proposal && proposal.intent_revision !== value.revision) throw new ProtocolError('执行提议引用了不同目标版本。')
  return { ...value, selection: selected.map(candidate), candidates: candidates.map(candidate), catalogue: { ...value.catalogue, observed_at: value.catalogue.observed_at ?? '', source: value.catalogue.source ?? '', limitations: strings(value.catalogue.limitations) }, proposal } as ConversationIntent
}
export function decodeConversationMessage(value: unknown, conversationID: string): ConversationMessage | null {
  if (value === null || value === undefined) return null
  if (!object(value) || !nonempty(value.id) || value.conversation_id !== conversationID || !Number.isSafeInteger(value.seq) || Number(value.seq) < 1 || !['user', 'assistant', 'event'].includes(String(value.role)) || !['user', 'application', 'model'].includes(String(value.origin)) || value.role === 'user' && value.origin !== 'user' || value.role === 'event' && value.origin !== 'application' || value.role === 'assistant' && value.origin === 'user' || typeof value.public_text !== 'string' || typeof value.created_at !== 'string' || !Number.isFinite(Date.parse(value.created_at))) throw new ProtocolError('公开消息未绑定到当前会话的持久记录。')
  return value as ConversationMessage
}
export function decodeIntentResponse(value: unknown, conversationID: string): IntentTurnResponse {
  if (!object(value) || !object(value.conversation) || value.conversation.id !== conversationID || !revision(value.conversation.revision) || typeof value.replayed !== 'boolean') throw new ProtocolError('未能核实会话目标已保存。')
  const message = decodeConversationMessage(value.message, conversationID), reply = decodeConversationMessage(value.reply, conversationID)
  if (message && message.role !== 'user' || reply && reply.role === 'user' || message && reply && reply.seq <= message.seq) throw new ProtocolError('会话消息顺序或身份无效。')
  const intent = decodeConversationIntent(value.intent, conversationID)
  const proposal = value.proposal == null ? intent?.proposal ?? null : decodeConversationProposal(value.proposal)
  if (proposal && intent?.mode === 'rules_only' && proposal.effects.allow_model_requests) throw new ProtocolError('只运行规则的提议不能增加模型请求效果。')
  if (proposal && (!intent || proposal.intent_revision !== intent.revision)) throw new ProtocolError('提议不属于这次保存的目标版本。')
  const job = value.job == null ? null : decodeJob(value.job)
  if (job && job.conversation_id !== conversationID || message?.job_id && job && message.job_id !== job.id) throw new ProtocolError('受理任务不属于当前会话。')
  return { conversation: value.conversation as Conversation, message, reply, intent: intent ? { ...intent, proposal } : null, proposal, job, replayed: value.replayed }
}
export const intentStatusLabel = (status: string) => ({ needs_clarification: '等待澄清', clarification_required: '等待澄清', ambiguous: '等待选择对象', needs_discovery: '需要准备目录', requires_discovery: '需要准备目录', ready: '等待确认计划', ready_for_confirmation: '等待确认计划', needs_data: '需要准备资料', intent_draft: '目标待明确', proposed: '等待确认计划', accepted: '计划已受理', executing: '执行中', blocked: '需要补充条件', completed: '已完成', draft: '目标已保存', unsupported: '当前能力未支持', rejected: '对应操作不允许' }[status] ?? '目标已保存')
export const dataPolicyLabel = (mode: string) => ({ prefer_fresh: '资料合格时复用，必要时只读更新', force_refresh: '重新采集本次范围资料', pinned_snapshot: '固定使用所选历史资料' }[mode] ?? '以保存的资料策略为准')
export const proposalStatusLabel = (status: string) => ({ ready: '等待接受', proposed: '等待接受', preview: '等待接受', accepted: '已受理', consumed: '已受理', superseded: '已被后续目标替代', stale: '需要重新核对', expired: '已过期', blocked: '条件未满足' }[status] ?? '需要核对状态')
export function canAcceptProposal(intent: ConversationIntent | null) { const proposal = intent?.proposal; return !!proposal && !!proposal.plan_id && ['ready', 'proposed', 'preview'].includes(proposal.status) && proposal.intent_revision === intent!.revision }

export function resourceIdentityLabel(resource: ResourceCandidate) { return resource.identity_kind === 'artifact_local' || !resource.resource_id ? `${resource.local_asset_id || resource.id}（本地对象；未取得云 ID）` : resource.resource_id }

export function conversationIssueLabel(value: string) {
  const labels: Record<string, string> = { MODEL_NOT_CONFIGURED: '模型尚未配置，仍可使用规则检查与人工业务确认。', SNAPSHOT_NOT_AVAILABLE: '尚无覆盖所选对象与方向的审计资料，需要先准备资料。', CATALOGUE_UNKNOWN: '资源目录尚未准备。', CATALOGUE_INCOMPLETE: '资源目录尚未穷尽。', CATALOGUE_STALE: '目录时点较旧，需要核对当前对象。', NO_PROVIDER_METADATA: '当前资料缺少可核对的云资源身份。', CATALOGUE_COVERAGE_UNKNOWN: '目录覆盖范围尚未确认。', RESOURCE_ASSOCIATION_UNKNOWN: '对象关联资料尚未取得，相关判断保留信息不足。' }
  return labels[value] ?? (/^[A-Z][A-Z0-9_]{2,}$/.test(value) ? '仍有一项资料或执行条件需要核对，本次保留对应限制。' : value)
}
export function catalogueSourceLabel(value: string) { return ({ synthetic: '人工构造资料', imported: '导入资料声明', live_readonly: '受控只读观测', synthetic_snapshot: '人工构造资料目录', imported_snapshot: '导入资料目录', snapshot: '已保存资料目录' }[value] ?? value) || '来源尚未取得' }
