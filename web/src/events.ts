import { createParser } from 'eventsource-parser'
import { abortScope } from './abort.ts'
import { ApiClient, ApiError, ProtocolError, decodeEvent, isTerminal } from './api.ts'
import type { Job, JobEvent } from './api.ts'

export type ObservationState = 'connecting' | 'live' | 'reconnecting' | 'polling' | 'paused' | 'ended' | 'auth_required' | 'forbidden' | 'protocol_error'
export type Mode = 'sse' | 'polling'
type Callbacks = { event: (event: JobEvent) => void; job: (job: Job) => void; state: (state: ObservationState) => void; error: (error: Error) => void }
class SequenceGap extends Error {}

function delay(ms: number, signal: AbortSignal): Promise<void> {
  return new Promise((resolve, reject) => {
    const cleanup = () => signal.removeEventListener('abort', aborted)
    const timer = setTimeout(() => { cleanup(); resolve() }, ms)
    const aborted = () => { clearTimeout(timer); cleanup(); reject(signal.reason) }
    signal.addEventListener('abort', aborted, { once: true })
    if (signal.aborted) aborted()
  })
}

/** Observation performs GET only. Stopping it never cancels or creates a job. */
export class EventObserver {
  private readonly api: ApiClient
  private readonly jobId: string
  private readonly callbacks: Callbacks
  private readonly controller = new AbortController()
  private cursor: number
  private terminal = false
  constructor(api: ApiClient, jobId: string, after: number, callbacks: Callbacks) {
    this.api = api; this.jobId = jobId; this.cursor = after; this.callbacks = callbacks
  }
  stop() { this.controller.abort() }
  private apply(event: JobEvent) {
    if (event.seq <= this.cursor) return
    if (event.seq !== this.cursor + 1) throw new SequenceGap()
    if (this.terminal) throw new ProtocolError('终态后出现了额外事件。')
    this.callbacks.event(event)
    this.cursor = event.seq
    if (event.type === 'job_terminal') this.terminal = true
  }
  private async refresh(): Promise<Job> {
    const job = await this.api.getJob(this.jobId, this.controller.signal)
    this.callbacks.job(job)
    return job
  }
  private async catchUp() {
    let pages = 0
    while (!this.controller.signal.aborted) {
      const page = await this.api.page(this.jobId, this.cursor, this.controller.signal)
      try { page.events.forEach(event => this.apply(event)) } catch (error) {
        if (error instanceof SequenceGap) throw new ProtocolError('事件页不连续，请重新读取记录。')
        throw error
      }
      if (!page.has_more) break
      // Yield even during a long replay; only one request is in flight.
      if (++pages % 5 === 0) await delay(0, this.controller.signal)
    }
    const job = await this.refresh()
    // A new observer can inherit an already applied terminal projection. An
    // empty page plus the authoritative terminal tail must also end polling.
    return isTerminal(job.status) && job.view_seq === this.cursor
  }
  private async stream(): Promise<'ended' | 'rotate'> {
    const signal = this.controller.signal
    const streamController = new AbortController()
    const scope = abortScope([signal, streamController.signal])
    let watchdog: ReturnType<typeof setTimeout> | undefined
    const resetWatchdog = () => { clearTimeout(watchdog); watchdog = setTimeout(() => streamController.abort(), 45000) }
    resetWatchdog()
    try {
      const response = await this.api.request(`${this.api.jobPath(this.jobId)}/events/stream?after_seq=${this.cursor}`, {
        headers: { Accept: 'text/event-stream' }, signal: scope.signal,
      }, 0)
      if (response.status === 204) {
        const job = await this.refresh()
        if (!isTerminal(job.status) || job.view_seq !== this.cursor) throw new ProtocolError('活动任务不能结束观察。')
        return 'ended'
      }
      if (!response.headers.get('Content-Type')?.startsWith('text/event-stream') || !response.body) throw new ProtocolError('服务端未返回事件流。')
      const lifetime = Number(response.headers.get('X-Subscription-Lifetime-Seconds'))
      const started = Date.now()
      let lastActivity = started
      this.callbacks.state('live')
      const parser = createParser({
        // The library counts UTF-16 characters; this conservative cap also stays
        // below the protocol's 64 KiB byte budget for incomplete UTF-8 frames.
        maxBufferSize: 16 * 1024,
        onEvent: frame => {
          if (new TextEncoder().encode(frame.data).byteLength > 4096) throw new ProtocolError('事件超过长度限制。')
          let data: unknown
          try { data = JSON.parse(frame.data) } catch { throw new ProtocolError('事件内容不是有效 JSON。') }
          const event = decodeEvent(data, this.jobId, this.api.businessEvents)
          if (frame.id !== String(event.seq) || frame.event !== event.type) throw new ProtocolError('事件头与正文不一致。')
          this.apply(event)
        },
        onError: () => { throw new ProtocolError('事件流格式错误。') },
      })
      const reader = response.body.getReader()
      const decoder = new TextDecoder('utf-8', { fatal: true })
      const decode = (value?: Uint8Array, streaming = false) => {
        try { return decoder.decode(value, { stream: streaming }) } catch { throw new ProtocolError('事件编码不是有效 UTF-8。') }
      }
      try {
        while (!signal.aborted) {
          const { value, done } = await reader.read()
          if (done) break
          lastActivity = Date.now()
          resetWatchdog()
          const previousCursor = this.cursor
          parser.feed(decode(value, true))
          // Merge all complete events from this chunk into one authoritative read.
          // Events append the timeline; they do not overwrite a newer task snapshot.
          const job = this.cursor !== previousCursor ? await this.refresh() : undefined
          if (this.terminal) {
            if (!job || !isTerminal(job.status) || job.view_seq !== this.cursor) throw new ProtocolError('终态与任务快照不一致。')
            return 'ended'
          }
        }
        parser.feed(decode())
      } finally { await reader.cancel().catch(() => {}); reader.releaseLock() }
      if (Number.isFinite(lifetime) && lifetime > 0 && lifetime <= 120 && Date.now() - started >= (lifetime - 2) * 1000
        && Date.now() - lastActivity < 45000) return 'rotate'
      // EOF alone says nothing about task completion. Replay committed events first.
      if (await this.catchUp()) return 'ended'
      throw new Error('事件连接已中断。')
    } finally { clearTimeout(watchdog); streamController.abort(); scope.dispose() }
  }
  async start(mode: Mode) {
    let failures = 0
    const signal = this.controller.signal
    try {
      while (!signal.aborted) {
        this.callbacks.state(mode === 'polling' ? 'polling' : failures ? 'reconnecting' : 'connecting')
        try {
          if (mode === 'polling') {
            if (await this.catchUp()) { this.callbacks.state('ended'); return }
            failures = 0
            await delay(1500, signal)
          } else {
            const outcome = await this.stream()
            if (outcome === 'ended') { this.callbacks.state('ended'); return }
            // A planned subscription rotation keeps the same cursor and failure count.
          }
        } catch (error) {
          if (signal.aborted) return
          if (error instanceof ProtocolError || error instanceof ApiError && [401, 403, 404, 422].includes(error.status)) throw error
          if (error instanceof SequenceGap) { await this.catchUp(); continue }
          failures++
          if (mode === 'sse' && failures >= 3) { mode = 'polling'; failures = 0; continue }
          this.callbacks.state(mode === 'polling' ? 'polling' : 'reconnecting')
          const retry = error instanceof ApiError ? error.retryAfter : 0
          const waits = mode === 'polling' ? [3000, 5000, 10000] : [1000, 2000, 5000]
          await delay(Math.max(retry, waits[Math.min(failures - 1, waits.length - 1)]! + Math.random() * 150), signal)
        }
      }
    } catch (error) {
      if (signal.aborted) return
      const state = error instanceof ApiError && error.status === 401 ? 'auth_required'
        : error instanceof ApiError && [403, 404].includes(error.status) ? 'forbidden' : 'protocol_error'
      this.callbacks.state(state)
      this.callbacks.error(error instanceof Error ? error : new Error('观察失败。'))
    }
  }
}
