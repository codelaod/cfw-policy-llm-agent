import test from 'node:test'
import assert from 'node:assert/strict'
import { ApiClient, ProtocolError, decodeEvent, isTerminal } from './api.ts'
import type { JobEvent } from './api.ts'
import { EventObserver } from './events.ts'
import type { ObservationState } from './events.ts'

const jobId = 'job_public_e0'
const at = '2026-10-28T10:00:00Z'
const events: JobEvent[] = [
  { schema_version: 1, job_id: jobId, seq: 1, type: 'job_status', occurred_at: at, payload: { status: 'running' } },
  { schema_version: 1, job_id: jobId, seq: 2, type: 'stage_changed', occurred_at: at, payload: { stage: '人工阶段' } },
  { schema_version: 1, job_id: jobId, seq: 3, type: 'job_terminal', occurred_at: at, payload: { status: 'completed' } },
]
const job = { id: jobId, conversation_id: 'public_conversation', status: 'completed', view_seq: 3, created_at: at }
const frame = (event: JobEvent) => `id: ${event.seq}\r\nevent: ${event.type}\r\ndata: ${JSON.stringify(event)}\r\n\r\n`
function stream(text: string) {
  const bytes = new TextEncoder().encode(text)
  return new Response(new ReadableStream({ start(controller) {
    // One-byte chunks exercise UTF-8, CRLF and frame boundaries independently.
    for (const byte of bytes) controller.enqueue(new Uint8Array([byte]))
    controller.close()
  } }), { headers: { 'Content-Type': 'text/event-stream', 'X-Subscription-Lifetime-Seconds': '120' } })
}
function harness() {
  const received: JobEvent[] = [], states: ObservationState[] = [], errors: Error[] = []
  const observer = new EventObserver(new ApiClient(() => 'public-test-token'), jobId, 0, {
    event: event => received.push(event), job: () => {}, state: state => states.push(state), error: error => errors.push(error),
  })
  return { observer, received, states, errors }
}

test('SSE handles fragmented UTF-8/CRLF and reads committed terminal state with an access header', async t => {
  const requests: string[] = []
  t.mock.method(globalThis, 'fetch', async (input: string, init: RequestInit) => {
    requests.push(input)
    assert.equal(new Headers(init.headers).get('X-App-Token'), 'public-test-token')
    assert.equal(init.method ?? 'GET', 'GET')
    assert.ok(!input.includes('public-test-token'))
    return input.includes('/stream') ? stream(': heartbeat\r\n\r\n' + events.map(frame).join('')) : Response.json(job)
  })
  const h = harness()
  await h.observer.start('sse')
  assert.deepEqual(h.received.map(event => event.seq), [1, 2, 3])
  assert.equal(h.received[1]!.payload.stage, '人工阶段')
  assert.equal(h.states.at(-1), 'ended')
  assert.deepEqual(h.errors, [])
  assert.equal(requests.length, 4)
})

test('EOF without a terminal event replays the event page instead of claiming completion', async t => {
  const requests: string[] = []
  t.mock.method(globalThis, 'fetch', async (input: string) => {
    requests.push(input)
    if (input.includes('/stream')) return stream(frame(events[0]!))
    if (input.includes('/events?')) return Response.json({ events: events.slice(1), next_seq: 3, has_more: false, last_seq: 3 })
    return Response.json(job)
  })
  const h = harness()
  await h.observer.start('sse')
  assert.deepEqual(h.received.map(event => event.seq), [1, 2, 3])
  assert.equal(h.states.at(-1), 'ended')
  assert.ok(requests.some(path => path.includes('after_seq=1&limit=100')))
})

test('running events refresh authoritative status before terminal arrives', async t => {
  let controller: ReadableStreamDefaultController<Uint8Array>
  let reads = 0
  const snapshots: string[] = []
  t.mock.method(globalThis, 'fetch', async (input: string) => {
    if (input.includes('/stream')) return new Response(new ReadableStream({ start(value) {
      controller = value
      value.enqueue(new TextEncoder().encode(frame(events[0]!)))
    } }), { headers: { 'Content-Type': 'text/event-stream' } })
    if (++reads === 1) {
      controller.enqueue(new TextEncoder().encode(events.slice(1).map(frame).join('')))
      controller.close()
      return Response.json({ ...job, status: 'running', view_seq: 1 })
    }
    return Response.json(job)
  })
  const observer = new EventObserver(new ApiClient(() => 'public-test-token'), jobId, 0, {
    event: () => {}, job: value => snapshots.push(value.status), state: () => {}, error: error => { throw error },
  })
  await observer.start('sse')
  assert.deepEqual(snapshots, ['running', 'completed'])
})

test('wrong job/schema stops observation without advancing or falling back', async t => {
  let count = 0
  t.mock.method(globalThis, 'fetch', async () => {
    count++
    return stream(frame({ ...events[0]!, job_id: 'another_job' }))
  })
  const h = harness()
  await h.observer.start('sse')
  assert.equal(count, 1)
  assert.deepEqual(h.received, [])
  assert.equal(h.states.at(-1), 'protocol_error')
  assert.equal(h.errors.length, 1)
})

test('401 requires unlocking and never retries or switches channels', async t => {
  let count = 0
  t.mock.method(globalThis, 'fetch', async () => { count++; return Response.json({ code: 'UNAUTHORIZED' }, { status: 401 }) })
  const h = harness()
  await h.observer.start('sse')
  assert.equal(count, 1)
  assert.equal(h.states.at(-1), 'auth_required')
})

test('JSON polling drains all pages before ending', async t => {
  const paths: string[] = []
  t.mock.method(globalThis, 'fetch', async (input: string) => {
    paths.push(input)
    if (input.includes('after_seq=0')) return Response.json({ events: events.slice(0, 2), next_seq: 2, has_more: true, last_seq: 3 })
    if (input.includes('after_seq=2')) return Response.json({ events: [events[2]], next_seq: 3, has_more: false, last_seq: 3 })
    return Response.json(job)
  })
  const h = harness()
  await h.observer.start('polling')
  assert.deepEqual(h.received.map(event => event.seq), [1, 2, 3])
  assert.equal(h.states.at(-1), 'ended')
  assert.equal(paths.length, 3)
})

test('polling at an already applied terminal tail ends without rereading forever', async t => {
  let reads = 0
  t.mock.method(globalThis, 'fetch', async (input: string) => {
    reads++
    if (input.includes('/events?')) return Response.json({ events: [], next_seq: 3, has_more: false, last_seq: 3 })
    return Response.json(job)
  })
  const states: ObservationState[] = []
  const observer = new EventObserver(new ApiClient(() => 'public-test-token'), jobId, 3, {
    event: () => assert.fail('tail must not replay events'), job: () => {},
    state: state => states.push(state), error: error => { throw error },
  })
  const done = observer.start('polling')
  await Promise.race([done, new Promise(resolve => setTimeout(resolve, 50))])
  observer.stop()
  await done
  assert.equal(states.at(-1), 'ended')
  assert.equal(reads, 2)
})

test('a discontinuous JSON page stops without advancing the cursor', async t => {
  let count = 0
  t.mock.method(globalThis, 'fetch', async () => {
    count++
    return Response.json({ events: [events[1]], next_seq: 2, has_more: false, last_seq: 2 })
  })
  const h = harness()
  await h.observer.start('polling')
  assert.equal(count, 1)
  assert.deepEqual(h.received, [])
  assert.equal(h.states.at(-1), 'protocol_error')
})

test('cancelling remains non-terminal and event types enforce their status transitions', async t => {
  const cancelling: JobEvent = { ...events[0]!, payload: { status: 'cancelling' } }
  const cancelled: JobEvent = { ...events[2]!, seq: 2, payload: { status: 'cancelled' } }
  let controller: ReadableStreamDefaultController<Uint8Array>
  let reads = 0
  const snapshots: string[] = [], received: JobEvent[] = [], states: ObservationState[] = []
  t.mock.method(globalThis, 'fetch', async (input: string) => {
    if (input.includes('/stream')) return new Response(new ReadableStream({ start(value) {
      controller = value
      value.enqueue(new TextEncoder().encode(frame(cancelling)))
    } }), { headers: { 'Content-Type': 'text/event-stream' } })
    if (++reads === 1) {
      controller.enqueue(new TextEncoder().encode(frame(cancelled)))
      controller.close()
      return Response.json({ ...job, status: 'cancelling', view_seq: 1 })
    }
    return Response.json({ ...job, status: 'cancelled', view_seq: 2 })
  })
  const observer = new EventObserver(new ApiClient(() => 'public-test-token'), jobId, 0, {
    event: value => received.push(value), job: value => snapshots.push(value.status),
    state: value => states.push(value), error: error => { throw error },
  })
  await observer.start('sse')
  assert.equal(isTerminal('cancelling'), false)
  assert.deepEqual(received.map(value => value.seq), [1, 2])
  assert.deepEqual(snapshots, ['cancelling', 'cancelled'])
  assert.equal(states.at(-1), 'ended')
  assert.throws(() => decodeEvent({ ...cancelling, payload: { status: 'completed' } }, jobId), ProtocolError)
  assert.throws(() => decodeEvent({ ...cancelled, payload: { status: 'cancelling' } }, jobId), ProtocolError)
})

test('business observation restores findings and a partial terminal through the formal job path', async t => {
  const formal = [events[0], { schema_version: 1, job_id: jobId, seq: 2, type: 'findings_available', occurred_at: at, payload: { result_ref: 'audit-1', finding_count: 2 } }, { ...events[2], payload: { status: 'partial' } }] as unknown as JobEvent[]
  const paths: string[] = [], received: JobEvent[] = [], states: ObservationState[] = []
  t.mock.method(globalThis, 'fetch', async (input: string) => {
    paths.push(input)
    return input.includes('/stream') ? stream(formal.map(frame).join('')) : Response.json({ ...job, status: 'partial' })
  })
  const observer = new EventObserver(new ApiClient(() => 'public-test-token', '/jobs'), jobId, 0, {
    event: event => received.push(event), job: () => {}, state: state => states.push(state), error: () => {},
  })
  await observer.start('sse')
  assert.deepEqual(received.map(event => event.seq), [1, 2, 3])
  assert.equal(states.at(-1), 'ended')
  assert.ok(paths.every(path => path.startsWith('/api/v1/jobs/')))
})
