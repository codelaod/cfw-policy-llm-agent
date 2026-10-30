import { computed, reactive, shallowReactive } from 'vue'
import { ProtocolError } from './api.ts'
import { Mutation, formatPorts, isDefiniteRejection, missingDependencies } from './workspace-state.ts'
import type { Workspace } from './workspace.ts'
import type { Access, AssetChange, BusinessCandidateEvidence, BusinessRoleCandidate, ConfirmationPreview, ConversationBusinessContext, ConversationBusinessRequest, ConversationBusinessResult, ConversationBusinessTarget, IntentTurnRequest, IntentTurnResponse, ProfileDraft } from './workspace-types.ts'

const object = (value: unknown): value is Record<string, unknown> => value !== null && typeof value === 'object' && !Array.isArray(value)
const id = (value: unknown): value is string => typeof value === 'string' && /^[A-Za-z0-9_.:-]{1,128}$/.test(value)
const revision = (value: unknown): value is number => Number.isSafeInteger(value) && Number(value) >= 0
const list = (value: unknown): string[] => { if (value == null) return []; if (!Array.isArray(value) || value.some(item => typeof item !== 'string')) throw new ProtocolError('业务资料列表无效。'); return value }
function portsValid(value: unknown): boolean { return Array.isArray(value) && value.every(item => object(item) && Number.isInteger(item.from) && Number.isInteger(item.to) && Number(item.from) >= 1 && Number(item.to) >= Number(item.from) && Number(item.to) <= 65535) }
function accessRows(value: unknown, scope: string): Access[] {
  if (value == null) return []
  if (!Array.isArray(value) || value.some(item => !object(item) || item.scope_ref !== scope || !['tcp', 'udp', 'any'].includes(String(item.protocol)) || !['applicable', 'not_applicable', 'unknown'].includes(String(item.applicability)) || !portsValid(item.ports) || !Array.isArray(item.addresses) || item.addresses.some(address => typeof address !== 'string'))) throw new ProtocolError('业务访问声明属于其他范围或格式无效。')
  return value as Access[]
}
function target(value: unknown, conversation: string): ConversationBusinessTarget {
  if (!object(value) || !id(value.ref) || value.conversation_id !== conversation || !id(value.job_id) || !id(value.snapshot_id) || !revision(value.intent_revision) || !object(value.scope) || !id(value.scope.ref) || !['internet', 'nat'].includes(String(value.scope.boundary)) || !['in', 'out'].includes(String(value.scope.direction))) throw new ProtocolError('业务资料未绑定当前会话的已封存对象。')
  const assets = list(value.asset_ids)
  if (!assets.length || assets.some(item => !id(item)) || new Set(assets).size !== assets.length) throw new ProtocolError('业务资料对象集合无效。')
  return { ...value, asset_ids: assets } as ConversationBusinessTarget
}
function evidence(value: unknown): BusinessCandidateEvidence[] {
  if (value == null) return []
  if (!Array.isArray(value) || value.some(item => !object(item) || !['input', 'result'].includes(String(item.document)) || typeof item.field !== 'string' || !item.field.startsWith('/'))) throw new ProtocolError('角色候选缺少固定证据位置。')
  return value as BusinessCandidateEvidence[]
}
function candidates(value: unknown, bound: ConversationBusinessTarget): BusinessRoleCandidate[] {
  if (value == null) return []
  if (!Array.isArray(value)) throw new ProtocolError('角色候选响应无效。')
  const seen = new Set<string>()
  return value.map(item => {
    if (!object(item) || !id(item.ref) || seen.has(item.ref) || !bound.asset_ids.includes(String(item.asset_id)) || item.service_id !== undefined && !id(item.service_id) || typeof item.role !== 'string' || typeof item.protocol !== 'string' || !portsValid(item.ports) || typeof item.conflicts_with_confirmed !== 'boolean' || !['pending', 'accepted_for_preview', 'rejected', 'unknown'].includes(String(item.decision))) throw new ProtocolError('角色候选超出当前对象或缺少选择状态。')
    seen.add(item.ref)
    return { ...item, evidence: evidence(item.evidence), contradicting_evidence: evidence(item.contradicting_evidence), unknowns: list(item.unknowns), questions: list(item.questions) } as BusinessRoleCandidate
  })
}
function draft(value: unknown, bound: ConversationBusinessTarget): ProfileDraft | null {
  if (value == null) return null
  if (!object(value) || !id(value.id) || value.snapshot_id !== bound.snapshot_id || value.policy_scope_ref !== bound.scope.ref || !revision(value.revision) || Number(value.revision) < 1 || !['draft', 'previewed', 'confirmed', 'rejected'].includes(String(value.status)) || !Array.isArray(value.changes) || !Array.isArray(value.base_revisions)) throw new ProtocolError('共用草稿与当前目标或固定资料不一致。')
  const seen = new Set<string>()
  for (const change of value.changes) {
    if (!object(change) || !bound.asset_ids.includes(String(change.asset_id)) || seen.has(String(change.asset_id)) || !Array.isArray(change.roles)) throw new ProtocolError('业务草稿含未选择对象。')
    seen.add(String(change.asset_id))
    for (const role of change.roles) if (!object(role) || !id(role.service_id) || typeof role.role !== 'string' || typeof role.protocol !== 'string' || !portsValid(role.ports) || role.declared !== undefined && typeof role.declared !== 'boolean') throw new ProtocolError('业务服务声明无效。')
    if (change.access != null && (!object(change.access) || !Array.isArray(change.access.services) || change.access.allowed_declared !== undefined && typeof change.access.allowed_declared !== 'boolean' || change.access.exceptions_declared !== undefined && typeof change.access.exceptions_declared !== 'boolean')) throw new ProtocolError('业务访问声明无效。')
    if (object(change.access)) {
      const allowed = change.access
      if (typeof allowed.allowed_complete !== 'boolean' || typeof allowed.deny_all !== 'boolean') throw new ProtocolError('允许集合完整性缺少明确声明。')
      allowed.allowed = accessRows(allowed.allowed, bound.scope.ref)
      const services = allowed.services as unknown[]
      const seenServices = new Set<string>()
      for (const service of services) {
        if (!object(service) || !id(service.service_id) || seenServices.has(service.service_id) || !change.roles.some(role => object(role) && role.service_id === service.service_id) || typeof service.allowed_complete !== 'boolean' || typeof service.deny_all !== 'boolean' || typeof service.required_confirmed !== 'boolean' || service.allowed_declared !== undefined && typeof service.allowed_declared !== 'boolean' || service.required_declared !== undefined && typeof service.required_declared !== 'boolean') throw new ProtocolError('服务访问声明缺少当前服务绑定。')
        seenServices.add(service.service_id); service.allowed = accessRows(service.allowed, bound.scope.ref); service.required = accessRows(service.required, bound.scope.ref)
      }
    }
  }
  if (seen.size !== bound.asset_ids.length) throw new ProtocolError('业务草稿缺少当前选择的对象。')
  return value as ProfileDraft
}
function preview(value: unknown, saved: ProfileDraft | null): ConfirmationPreview | null {
  if (value == null) return null
  if (!saved || !object(value) || !id(value.preview_id) || value.draft_id !== saved.id || value.draft_revision !== saved.revision || saved.preview_id && value.preview_id !== saved.preview_id || saved.status !== 'previewed' || !Array.isArray(value.diff)) throw new ProtocolError('差异预览已与共用草稿修订脱离。')
  const sections = list(value.confirmable_sections)
  if (sections.some(section => !['roles', 'access'].includes(section)) || new Set(sections).size !== sections.length) throw new ProtocolError('可确认业务部分无效。')
  const dependencies = value.section_dependencies ?? {}
  if (!object(dependencies) || Object.entries(dependencies).some(([section, required]) => !sections.includes(section) || list(required).some(item => !sections.includes(item)))) throw new ProtocolError('业务分项确认依赖无效。')
  for (const diff of value.diff) if (!object(diff) || !saved.changes.some(change => change.asset_id === diff.asset_id) || !['roles', 'access'].includes(String(diff.section))) throw new ProtocolError('预览包含其他对象的差异。')
  return { ...value, confirmable_sections: sections, section_dependencies: dependencies, validation_errors: value.validation_errors ?? [] } as ConfirmationPreview
}
export function decodeBusinessContext(value: unknown, conversation: string): ConversationBusinessContext | null {
  if (value == null) return null
  if (!object(value)) throw new ProtocolError('会话业务上下文无效。')
  const bound = target(value.target, conversation), saved = draft(value.draft, bound)
  return { target: bound, draft: saved, preview: preview(value.preview, saved), candidates: candidates(value.candidates, bound) }
}
export function decodeBusinessResult(value: unknown, conversation: string): ConversationBusinessResult | null {
  if (value == null) return null
  if (!object(value) || typeof value.reply !== 'string' || typeof value.replayed !== 'boolean' || !['draft', 'preview_required', 'confirmed', 'candidate_rejected', 'unknown_preserved', 'rejected', 'needs_clarification', 'conflict', 'sensitive_input_blocked', 'save_failed'].includes(String(value.state))) throw new ProtocolError('业务操作结果缺少已保存状态。')
  const bound = object(value.target) && value.target.ref ? target(value.target, conversation) : null
  if (!bound && (value.draft || value.preview || value.confirmation)) throw new ProtocolError('业务结果缺少对象绑定。')
  const saved = bound ? draft(value.draft, bound) : null, shown = preview(value.preview, saved)
  const confirmation = value.confirmation
  if (confirmation != null && (!object(confirmation) || !saved || confirmation.draft_id !== saved.id || !id(confirmation.action_id) || !['confirmed', 'rejected'].includes(String(confirmation.status)) || confirmation.revision !== saved.revision)) throw new ProtocolError('未能核实业务确认版本已保存。')
  return { ...value, target: bound, draft: saved, preview: shown, candidates: bound ? candidates(value.candidates, bound) : [], unknown_fields: list(value.unknown_fields), missing_inputs: list(value.missing_inputs) } as ConversationBusinessResult
}

export const businessRoleLabel = (role: string) => ({ web: 'Web 服务', database: '数据库', management: '管理入口', cache: '缓存', middleware: '中间件', queue: '消息队列', background: '后台服务', unknown: '用途未知' }[role] ?? role)
export const businessSectionLabel = (section: string) => section === 'roles' ? '角色与服务' : '访问要求与例外'
export const businessDecisionLabel = (decision: string) => ({ pending: '待人工核对', accepted_for_preview: '已加入草稿，尚未确认', rejected: '已拒绝候选', unknown: '保留未知' }[decision] ?? '待人工核对')
/** Declare only the field the user edited; reading or editing another service
 * cannot turn an unknown allowed set into a confirmed empty set. */
export function markBusinessDeclaration(changes: AssetChange[], target: object, kind: 'allowed' | 'required' | 'exceptions' = 'allowed') {
  for (const change of changes) {
    for (const role of change.roles) if (role === target) { role.declared = true; return }
    const access = change.access
    if (!access) continue
    if (access.exceptions === target || access.exceptions?.some(item => item === target)) { access.exceptions_declared = true; return }
    if (access === target) { if (kind === 'exceptions') access.exceptions_declared = true; else access.allowed_declared = true; return }
    if (access.allowed === target || access.allowed.some(item => item === target)) { access.allowed_declared = true; return }
    for (const service of access.services) {
      if (service === target) { if (kind === 'required') service.required_declared = true; else service.allowed_declared = true; return }
      if (service.allowed === target || service.allowed.some(item => item === target)) { service.allowed_declared = true; return }
      if (service.required === target || service.required.some(item => item === target)) { service.required_declared = true; return }
    }
  }
}
export function businessAllowedLabel(value: { allowed_declared?: boolean; allowed_complete: boolean; deny_all: boolean; allowed: Access[] } | undefined) {
  if (!value) return '允许范围尚未声明'
  if (value.deny_all && value.allowed_complete && !value.allowed?.length) return '明确空集：不允许所审访问'
  if (!value.allowed?.length) return '允许来源／目标未知，空白不代表禁止全部'
  return `${value.allowed_complete ? '完整允许上界' : '部分允许线索'}：${value.allowed.map(item => `${item.protocol.toUpperCase()} ${formatPorts(item.ports) || '端口未知'}；${item.addresses?.join('、') || '来源／目标未知'}`).join('；')}`
}
export function businessRequiredLabel(value: { required_confirmed: boolean; required: Access[] } | undefined) {
  if (!value?.required?.length) return value?.required_confirmed ? '已明确无必要访问' : '必要访问尚未声明'
  return value.required.map(item => `${item.protocol.toUpperCase()} ${formatPorts(item.ports) || '端口未知'}；${item.addresses?.join('、') || '来源／目标未知'}${item.applicability === 'unknown' ? '，适用性待核对' : ''}`).join('；')
}
export function businessDiffLines(value: unknown, section: string): string[] {
  if (!object(value)) return ['原值未提供']
  const lines: string[] = []
  if (section === 'roles' && Array.isArray(value.services)) for (const service of value.services) if (object(service)) lines.push(`${businessRoleLabel(String(service.role ?? 'unknown'))}：${String(service.protocol ?? 'unknown').toUpperCase()} ${formatPorts(Array.isArray(service.ports) ? service.ports : []) || '端口待明确'}`)
  if (section === 'access') {
    lines.push(businessAllowedLabel(value as unknown as Parameters<typeof businessAllowedLabel>[0]))
    if (Array.isArray(value.services)) for (const service of value.services) if (object(service)) { lines.push(`${String(service.id ?? '服务')}允许：${businessAllowedLabel(service as unknown as Parameters<typeof businessAllowedLabel>[0])}`); lines.push(`${String(service.id ?? '服务')}必要：${businessRequiredLabel(service as unknown as Parameters<typeof businessRequiredLabel>[0])}`) }
    if (Array.isArray(value.exceptions)) for (const exception of value.exceptions) if (object(exception)) lines.push(`例外 ${String(exception.check_id ?? '')}：${String(exception.reason ?? '理由待补')}；授权来源 ${String(exception.authorization_source || '尚未声明')}；到期 ${String(exception.expires_at || '尚未声明')}`)
  }
  return lines.length ? lines : ['尚无这部分业务声明']
}
export function businessConfirmRequest(context: ConversationBusinessContext, sections: string[]): ConversationBusinessRequest {
  const saved = context.draft, shown = context.preview
  if (!saved || !shown || saved.status !== 'previewed' || shown.draft_id !== saved.id || shown.draft_revision !== saved.revision || !sections.length || new Set(sections).size !== sections.length || sections.some(section => !shown.confirmable_sections.includes(section))) throw new Error('请读取当前差异预览，并明确选择要确认的部分。')
  if (missingDependencies(sections, shown.section_dependencies).length) throw new Error('所选访问要求依赖角色部分，请分别核对并明确选择；不会自动勾选。')
  return { action: 'confirm', expected_target_ref: context.target.ref, expected_draft_revision: saved.revision, preview_id: shown.preview_id, sections: [...sections] }
}

type BusinessWorkspace = Pick<Workspace, 'api' | 'unlocked' | 'conversationID' | 'currentIntent' | 'captureContext' | 'applyIntentResponse' | 'loadMessages'>
type BusinessEnvelope = { conversation_id: string; request: IntentTurnRequest }
export type BusinessEditor = { context: ConversationBusinessContext | null; loading: boolean; busy: boolean; error: string; message: string; selected: string[]; mutation: Mutation<BusinessEnvelope, IntentTurnResponse>; reads: number }
export function createBusinessController(workspace: BusinessWorkspace, conversation: () => string, formatError: (cause: unknown) => string, savedEditors?: Map<string, BusinessEditor>) {
  const editors = shallowReactive(savedEditors ?? new Map<string, BusinessEditor>())
  let disposed = false
  function forConversation(id: string) { let editor = editors.get(id); if (!editor) { editor = reactive({ context: null, loading: false, busy: false, error: '', message: '', selected: [], mutation: shallowReactive(new Mutation<BusinessEnvelope, IntentTurnResponse>()), reads: 0 }) as BusinessEditor; editors.set(id, editor) }; return editor }
  const editor = computed(() => forConversation(conversation()))
  async function load() {
    const owner = conversation(), state = forConversation(owner), seq = ++state.reads, valid = workspace.captureContext()
    if (!owner || !workspace.unlocked.value) { state.context = null; state.loading = false; return }
    state.loading = true; state.error = ''
    const current = () => !disposed && conversation() === owner && workspace.conversationID.value === owner && seq === state.reads && valid()
    try { const value = await workspace.api.businessContext(owner); if (current()) { const old = `${state.context?.target.ref}/${state.context?.draft?.revision}/${state.context?.preview?.preview_id}`, next = `${value?.target.ref}/${value?.draft?.revision}/${value?.preview?.preview_id}`; if (state.context?.target.ref !== value?.target.ref) state.message = ''; state.context = value; if (old !== next) state.selected = [] } }
    catch (cause) { if (current()) { state.context = null; state.selected = []; state.error = formatError(cause) } }
    finally { if (seq === state.reads) state.loading = false }
  }
  async function submit(request?: ConversationBusinessRequest) {
    const owner = conversation(), state = forConversation(owner), valid = workspace.captureContext()
    if (!owner || state.busy || !workspace.unlocked.value) return
    state.busy = true; state.error = ''; state.message = ''
    const current = () => !disposed && conversation() === owner && workspace.conversationID.value === owner && valid()
    try {
      let body = state.mutation.pendingBody
      if (!body) {
        if (!request || !state.context) throw new Error('先读取当前会话的业务对象与草稿。')
        const business = { ...request, expected_target_ref: state.context.target.ref }
        body = { conversation_id: owner, request: { client_turn_id: crypto.randomUUID(), text: '', business, ...(workspace.currentIntent.value?.conversation_id === owner ? { expected_revision: workspace.currentIntent.value.revision } : {}) } }
      } else if (request) throw new Error('先以原内容核对上次业务提交，不能改变原请求。')
      const response = await state.mutation.submit(body, (saved, key) => workspace.api.intentTurn(saved.conversation_id, saved.request, key))
      if (!current()) return
      workspace.applyIntentResponse(response); state.selected = []; state.message = response.business?.reply || '业务动作已保存，请重新核对草稿状态。'
      await workspace.loadMessages()
      if (current()) await load()
    } catch (cause) { if (isDefiniteRejection(cause)) state.mutation.discard(); if (current()) state.error = formatError(cause) }
    finally { state.busy = false }
  }
  async function confirm() { if (!editor.value.context) return; try { await submit(businessConfirmRequest(editor.value.context, editor.value.selected)) } catch (cause) { editor.value.error = formatError(cause) } }
  function clearRead() { const state = editor.value; state.reads++; state.context = null; state.selected = [] }
  function dispose() { disposed = true; clearRead() }
  return { editor, editors, load, submit, confirm, clearRead, dispose }
}
