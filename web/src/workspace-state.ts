import type { ExecutionPlan, Job, PortRange } from './workspace-types.ts'
import { ApiError } from './api.ts'

/** These errors prove the attempted write was rejected before acceptance. */
export function isDefiniteRejection(cause: unknown) {
  return cause instanceof ApiError && ['INVALID_REQUEST', 'VERSION_CONFLICT', 'PLAN_STALE', 'PLAN_CONSUMED', 'CONVERSATION_ARCHIVED', 'CONVERSATION_BUSY', 'MODEL_NOT_CONFIGURED', 'SOURCE_NOT_CONFIGURED', 'SCOPE_DENIED', 'QUEUE_FULL', 'INTENT_STALE', 'PROPOSAL_STALE', 'CANDIDATE_STALE', 'CREDENTIAL_IN_PAYLOAD'].includes(cause.code)
}

/** JSON DTOs can contain Vue proxies while an editor is open. */
export function cloneData<T>(value: T): T { return JSON.parse(JSON.stringify(value)) as T }

/** Late reads from a previous selection must not replace the current view. */
export class LatestRead {
  private generation = 0
  begin() { const generation = ++this.generation; return () => this.generation === generation }
  invalidate() { this.generation++ }
}

export function mergeJobs(items: Job[], incoming: Job[]): Job[] {
  const records = new Map(items.map(job => [job.id, job]))
  for (const job of incoming) { const previous = records.get(job.id); if (!previous || job.view_seq >= previous.view_seq) records.set(job.id, job) }
  return [...records.values()].sort((a, b) => b.created_at.localeCompare(a.created_at) || b.id.localeCompare(a.id))
}

export class PendingSubmissionError extends Error {
  constructor() { super('上次提交结果尚未核实，请先以原内容重试或核对服务端记录。'); this.name = 'PendingSubmissionError' }
}

function canonical(value: unknown): string {
  return JSON.stringify(value, (_key, item: unknown) => {
    if (item !== null && typeof item === 'object' && !Array.isArray(item)) return Object.fromEntries(Object.entries(item).sort(([a], [b]) => a.localeCompare(b)))
    return item
  })
}

/** A failed/unknown POST is never replayed in the background or assigned a new key. */
export class Mutation<T, R> {
  state: 'idle' | 'saving' | 'saved' | 'unsaved' = 'idle'
  result: R | null = null
  pendingBody: T | null = null
  key: string | null = null
  private fingerprint = ''
  private readonly keyFactory: () => string
  constructor(keyFactory: () => string = () => crypto.randomUUID()) { this.keyFactory = keyFactory }
  async submit(body: T, operation: (body: T, key: string) => Promise<R>): Promise<R> {
    if (this.state === 'saving') throw new Error('该操作正在保存。')
    const fingerprint = canonical(body)
    if (this.key && fingerprint !== this.fingerprint) throw new PendingSubmissionError()
    this.key ??= this.keyFactory()
    this.fingerprint = fingerprint
    this.pendingBody = cloneData(body)
    this.state = 'saving'
    try {
      const result = await operation(cloneData(body), this.key)
      this.result = result; this.state = 'saved'; this.key = null; this.pendingBody = null; this.fingerprint = ''
      return result
    } catch (error) { this.state = 'unsaved'; throw error }
  }
  /** Called only after an explicit discard or a proven server rejection. */
  discard() { if (this.state === 'saving') return; this.key = null; this.pendingBody = null; this.fingerprint = ''; this.state = 'idle'; this.result = null }
  restorePending(body: T, key: string) { this.pendingBody = cloneData(body); this.fingerprint = canonical(body); this.key = key; this.state = 'unsaved'; this.result = null }
}

type PlanSelection = { snapshot: string; conversation: string; scope: string; assets: string[]; mode: string; kind?: string }
export class PlanDraft {
  preview: ExecutionPlan | null = null
  private fingerprint = ''
  private generation = 0
  update(selection: PlanSelection) {
    const fingerprint = canonical({ ...selection, assets: [...selection.assets].sort() })
    if (this.fingerprint !== fingerprint) { this.preview = null; this.generation++ }
    this.fingerprint = fingerprint
  }
  acceptPreview(preview: ExecutionPlan) { this.preview = preview }
  capturePreview() { const generation = this.generation; return (preview: ExecutionPlan) => { if (generation !== this.generation) return false; this.acceptPreview(preview); return true } }
  get startable() { return !!this.preview && Date.parse(this.preview.expires_at) > Date.now() }
  invalidate() { this.preview = null; this.generation++ }
}

export function parsePorts(input: string): PortRange[] {
  if (input.length > 4096) throw new Error('端口输入过长。')
  if (!input.trim()) return []
  return input.trim().split(/[\s,，]+/).filter(Boolean).map(value => {
    if (!/^\d+(?:[-:]\d+)?$/.test(value)) throw new Error('端口请填写数字或范围，例如 443、8000-8010。')
    const [first, last = first] = value.split(/[-:]/)
    const from = Number(first), to = Number(last)
    if (!Number.isInteger(from) || from < 1 || to < from || to > 65535) throw new Error('端口范围须在 1～65535 内。')
    return { from, to }
  })
}
export function formatPorts(ports: PortRange[] | undefined) { return (ports ?? []).map(p => p.from === p.to ? String(p.from) : `${p.from}-${p.to}`).join(', ') }
export function parseAddresses(input: string): string[] {
  if (input.length > 20000) throw new Error('地址输入过长，请缩小本次声明。')
  return input.trim() ? input.trim().split(/[\s,，]+/).filter(Boolean) : []
}

export function missingDependencies(selected: string[], dependencies: Record<string, string[]> = {}): string[] {
  return [...new Set(selected.flatMap(section => dependencies[section] ?? []).filter(section => !selected.includes(section)))].sort()
}
