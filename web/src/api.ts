import { abortScope } from './abort.ts'

export type JobStatus = 'queued' | 'running' | 'cancelling' | 'completed' | 'partial' | 'failed' | 'cancelled' | 'interrupted'
export type Job = {
  id: string
  conversation_id: string
  status: JobStatus
  view_seq: number
  created_at: string
  started_at?: string | null
  finished_at?: string | null
  kind?: string
  result_ref?: string
  error_code?: string
}
export type JobEvent = {
  schema_version: 1
  job_id: string
  seq: number
  type: 'job_status' | 'stage_changed' | 'job_terminal' | 'tool_started' | 'tool_finished' | 'findings_available' | 'message_available' | 'report_available'
  occurred_at: string
  payload: { status?: JobStatus; stage?: string; result_ref?: string; message_ref?: string; report_ref?: string; call_id?: string; tool_name?: string; summary?: string; finding_count?: number; [key: string]: unknown }
}
export type EventPage = { events: JobEvent[]; next_seq: number; has_more: boolean; last_seq: number }

const progressStatuses: JobStatus[] = ['queued', 'running', 'cancelling']
const terminalStatuses: JobStatus[] = ['completed', 'partial', 'failed', 'cancelled', 'interrupted']
const statuses: JobStatus[] = [...progressStatuses, ...terminalStatuses]
export const isTerminal = (status: JobStatus) => terminalStatuses.includes(status)
export class ApiError extends Error {
  readonly status: number
  readonly code: string
  readonly retryAfter: number
  constructor(status: number, code: string, retryAfter = 0) {
    super(code)
    this.name = 'ApiError'
    this.status = status
    this.code = code
    this.retryAfter = retryAfter
  }
}
export class ProtocolError extends Error {
  constructor(message = '事件协议校验失败，请重新读取记录。') { super(message); this.name = 'ProtocolError' }
}

function record(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}
const validTime = (value: unknown) => typeof value === 'string' && Number.isFinite(Date.parse(value))
async function readJSON(response: Response): Promise<unknown> {
  try { return await response.json() } catch (error) {
    if (error instanceof SyntaxError) throw new ProtocolError('服务端响应不是有效 JSON。')
    throw error
  }
}
export function decodeJob(value: unknown, id?: string): Job {
  if (!record(value) || typeof value.id !== 'string' || (id && value.id !== id)
    || typeof value.conversation_id !== 'string' || !statuses.includes(value.status as JobStatus)
    || !Number.isSafeInteger(value.view_seq) || Number(value.view_seq) < 0 || !validTime(value.created_at)) {
    throw new ProtocolError('任务响应校验失败。')
  }
  return value as Job
}
export function decodeEvent(value: unknown, jobId: string, business = false): JobEvent {
  if (!record(value) || value.schema_version !== 1 || value.job_id !== jobId
    || !Number.isSafeInteger(value.seq) || Number(value.seq) < 1 || !validTime(value.occurred_at)
    || !(business ? ['job_status', 'stage_changed', 'job_terminal', 'tool_started', 'tool_finished', 'findings_available', 'message_available', 'report_available'] : ['job_status', 'stage_changed', 'job_terminal']).includes(String(value.type)) || !record(value.payload)) {
    throw new ProtocolError()
  }
  if (value.type === 'stage_changed') {
    if (typeof value.payload.stage !== 'string') throw new ProtocolError()
  } else if (value.type === 'job_status' || value.type === 'job_terminal') {
    if (!(value.type === 'job_terminal' ? terminalStatuses : progressStatuses).includes(value.payload.status as JobStatus)) throw new ProtocolError()
  } else if (value.type === 'tool_started' || value.type === 'tool_finished') {
    if (typeof value.payload.call_id !== 'string' || typeof value.payload.tool_name !== 'string') throw new ProtocolError()
  } else if (typeof value.payload[value.type === 'findings_available' ? 'result_ref' : value.type === 'message_available' ? 'message_ref' : 'report_ref'] !== 'string') {
    throw new ProtocolError()
  }
  return value as JobEvent
}

export class ApiClient {
  private readonly token: () => string
  private readonly jobBase: string
  readonly businessEvents: boolean
  constructor(token: () => string, jobBase = '/e0/jobs') {
    if (!['/e0/jobs', '/jobs'].includes(jobBase)) throw new Error('Unknown job namespace')
    this.token = token; this.jobBase = jobBase; this.businessEvents = jobBase === '/jobs'
  }
  jobPath(id: string) { return `${this.jobBase}/${encodeURIComponent(id)}` }

  async request(path: string, init: RequestInit = {}, timeout = 10000): Promise<Response> {
    const headers = new Headers(init.headers)
    headers.set('X-App-Token', this.token())
    const scope = timeout ? abortScope([init.signal], timeout) : undefined
    try {
      const response = await fetch(`/api/v1${path}`, { ...init, headers, signal: scope?.signal ?? init.signal, credentials: 'omit', cache: 'no-store' })
      if (!response.ok) {
        let code = `HTTP_${response.status}`
        try {
          const body: unknown = await response.json()
          if (record(body) && typeof body.code === 'string') code = body.code
        } catch { /* A status code is sufficient when the body is unavailable. */ }
        const rawRetry = response.headers.get('Retry-After')
        const seconds = Number(rawRetry)
        const retryAfter = rawRetry ? Math.min(10000, Math.max(0, Number.isFinite(seconds) ? seconds * 1000 : Date.parse(rawRetry) - Date.now())) : 0
        throw new ApiError(response.status, code, retryAfter)
      }
      return response
    } finally { scope?.dispose() }
  }
  private async requestJSON(path: string, init: RequestInit = {}): Promise<unknown> {
    // Own the timer through the body read, not just until response headers arrive.
    const scope = abortScope([init.signal], 10000)
    try { return await readJSON(await this.request(path, { ...init, signal: scope.signal }, 0)) }
    finally { scope.dispose() }
  }

  async health(): Promise<void> { await this.requestJSON('/health') }
  async createJob(conversationId: string, requestId: string): Promise<Job> {
    const value = await this.requestJSON('/e0/jobs', {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ conversation_id: conversationId, client_request_id: requestId }),
    })
    return decodeJob(value)
  }
  async getJob(id: string, signal?: AbortSignal): Promise<Job> {
    return decodeJob(await this.requestJSON(this.jobPath(id), { signal }), id)
  }
  async page(id: string, after: number, signal?: AbortSignal): Promise<EventPage> {
    const value = await this.requestJSON(`${this.jobPath(id)}/events?after_seq=${after}&limit=100`, { signal })
    if (!record(value) || !Array.isArray(value.events) || value.events.length > 100
      || !Number.isSafeInteger(value.next_seq) || !Number.isSafeInteger(value.last_seq)
      || Number(value.last_seq) < after || typeof value.has_more !== 'boolean') throw new ProtocolError()
    const events = value.events.map(event => decodeEvent(event, id, this.businessEvents))
    const expected = events.length ? events[events.length - 1]!.seq : after
    if (value.next_seq !== expected || expected > Number(value.last_seq)
      || value.has_more !== (expected < Number(value.last_seq)) || (value.has_more && events.length === 0)) throw new ProtocolError()
    return { events, next_seq: expected, has_more: value.has_more, last_seq: Number(value.last_seq) }
  }
}
