import test from 'node:test'
import assert from 'node:assert/strict'
import { reactive } from 'vue'
import { ApiError } from './api.ts'
import { WorkspaceClient } from './workspace-api.ts'
import { Mutation, PendingSubmissionError, PlanDraft, LatestRead, cloneData, isDefiniteRejection, mergeJobs, parsePorts, parseAddresses, missingDependencies } from './workspace-state.ts'

test('accepted plans use an access header and a stable idempotency key without replaying POST', async t => {
  const calls: { url: string; init: RequestInit }[] = []
  t.mock.method(globalThis, 'fetch', async (url: string, init: RequestInit) => {
    calls.push({ url, init })
    if (calls.length === 1) throw new TypeError('synthetic connection lost')
    return Response.json({ id: 'job-1', conversation_id: 'conv-1', status: 'queued', view_seq: 1, created_at: '2026-10-28T00:00:00Z' })
  })
  const client = new WorkspaceClient(() => 'public-test-access')
  const mutation = new Mutation<{ plan_id: string }, unknown>(() => 'request-1')
  await assert.rejects(mutation.submit({ plan_id: 'plan-1' }, (body, key) => client.startJob(body.plan_id, key)))
  assert.equal(calls.length, 1)
  assert.equal(mutation.state, 'unsaved')
  await mutation.submit({ plan_id: 'plan-1' }, (body, key) => client.startJob(body.plan_id, key))
  assert.equal(calls.length, 2)
  for (const call of calls) {
    assert.equal(call.url, '/api/v1/jobs')
    const headers = new Headers(call.init.headers)
    assert.equal(headers.get('X-App-Token'), 'public-test-access')
    assert.equal(headers.get('Idempotency-Key'), 'request-1')
    assert.deepEqual(JSON.parse(String(call.init.body)), { plan_id: 'plan-1' })
    assert.ok(!call.url.includes('public-test-access'))
  }
})

test('failed review keeps its draft and rejects silently changing an unresolved submission', async () => {
  const mutation = new Mutation<{ comment: string }, unknown>(() => 'review-1')
  const body = { comment: '请先核对管理来源' }
  await assert.rejects(mutation.submit(body, async () => { throw new Error('synthetic storage failure') }))
  assert.deepEqual(mutation.pendingBody, body)
  await assert.rejects(mutation.submit({ comment: '不同的新意见' }, async () => assert.fail('must not submit changed body')), PendingSubmissionError)
  assert.equal(mutation.state, 'unsaved')
})

test('confirmation sends only the explicit selected section', async t => {
  let sent: unknown
  t.mock.method(globalThis, 'fetch', async (_url: string, init: RequestInit) => {
    sent = JSON.parse(String(init.body))
    return Response.json({ action_id: 'confirmation-1', draft_id: 'draft-1', status: 'confirmed', revision: 3, heads: [], saved_at: '2026-10-28T00:00:00Z' })
  })
  await new WorkspaceClient(() => 'public-test-access').confirmDraft('draft-1', 'preview-1', 2, ['roles'], 'confirm-1')
  assert.deepEqual(sent, { preview_id: 'preview-1', expected_draft_revision: 2, confirmed_sections: ['roles'] })
})

test('editing a plan invalidates the preview and cannot start without a new accepted preview', () => {
  const plan = new PlanDraft()
  plan.update({ snapshot: 'snapshot-a', conversation: 'conv-a', scope: 'scope-a', assets: ['asset-a'], mode: 'rules_only' })
  plan.acceptPreview({ id: 'plan-a', expires_at: '2099-01-01T00:00:00Z' })
  assert.equal(plan.startable, true)
  plan.update({ snapshot: 'snapshot-b', conversation: 'conv-a', scope: 'scope-a', assets: ['asset-b'], mode: 'rules_only' })
  assert.equal(plan.preview, null)
  assert.equal(plan.startable, false)
})

test('an in-flight preview cannot be installed after its selection changed', () => {
  const plan = new PlanDraft()
  const selection = { snapshot: 'snapshot-a', conversation: 'conv-a', scope: 'scope-a', assets: ['asset-a'], mode: 'rules_only' }
  plan.update(selection)
  const accept = plan.capturePreview()
  plan.update({ ...selection, assets: ['asset-b'] })
  assert.equal(accept({ id: 'old-plan', expires_at: '2099-01-01T00:00:00Z' }), false)
  assert.equal(plan.preview, null)
})

test('message failure leaves the text for manual retry and creates no optimistic public reply', async t => {
  let calls = 0
  t.mock.method(globalThis, 'fetch', async () => { calls++; return Response.json({ code: 'MODEL_NOT_CONFIGURED' }, { status: 503 }) })
  const body = { client_message_id: 'message-1', policy_scope_ref: 'scope-1', snapshot_id: 'snapshot-1', text: '解释这个发现', model_use_accepted: true as const }
  const mutation = new Mutation<typeof body, unknown>(() => 'message-request-1')
  await assert.rejects(mutation.submit(body, data => new WorkspaceClient(() => 'public-test-access').sendMessage('conv-1', data)))
  assert.equal(calls, 1)
  assert.equal(mutation.pendingBody?.text, body.text)
  assert.equal(mutation.state, 'unsaved')
  assert.equal(mutation.result, null)
})

test('a message success must identify its persisted user message and accepted job', async t => {
  t.mock.method(globalThis, 'fetch', async () => Response.json({ ok: true }))
  const body = { client_message_id: 'message-1', policy_scope_ref: 'scope-1', snapshot_id: 'snapshot-1', text: '解释这个发现', model_use_accepted: true as const }
  await assert.rejects(new WorkspaceClient(() => 'public-test-access').sendMessage('conv-1', body), /消息/)
})

test('nullable finding slices remain empty declarations without crashing the evidence view', async t => {
  t.mock.method(globalThis, 'fetch', async () => Response.json({ id: 'finding-1', finding: { assumptions: null, limitations: null, differences: null, evidence_refs: null } }))
  const value = await new WorkspaceClient(() => 'public-test-access').finding('finding-1')
  assert.deepEqual(value.finding.assumptions, [])
  assert.deepEqual(value.finding.differences, [])
  assert.deepEqual(value.finding.limitations, [])
  assert.deepEqual(value.finding.evidence_refs, [])
})

test('a persisted recheck keeps its comparison and normalizes absent reason and issue lists', async t => {
  t.mock.method(globalThis, 'fetch', async () => Response.json({ audit: null, is_final: true, view_seq: 5, recheck: { id: 'recheck-1', old_audit_id: 'old-audit', new_audit_id: 'new-audit', comparison: { comparable: false, reasons: null, issues: null } } }))
  const value = await new WorkspaceClient(() => 'public-test-access').jobResult('job-1')
  assert.equal(value.recheck?.comparison.comparable, false)
  assert.deepEqual(value.recheck?.comparison.reasons, [])
  assert.deepEqual(value.recheck?.comparison.issues, [])
})

test('persistent lists and review history can explicitly continue beyond the first page', async t => {
  const urls: string[] = []
  t.mock.method(globalThis, 'fetch', async (url: string) => { urls.push(url); return Response.json({ items: [], offset: 100, limit: 100 }) })
  const client = new WorkspaceClient(() => 'public-test-access')
  await client.jobs(100)
  await client.findings('audit-1', 100)
  await client.reviewHistory('finding-1', 100)
  assert.match(urls[0]!, /offset=100/)
  assert.match(urls[1]!, /offset=100.*audit_id=audit-1/)
  assert.match(urls[2]!, /after_revision=100/)
})

test('protected report downloads also require unlocking after a 401', async t => {
  let locked = false
  t.mock.method(globalThis, 'fetch', async () => Response.json({ code: 'UNAUTHORIZED' }, { status: 401 }))
  await assert.rejects(new WorkspaceClient(() => 'public-test-access', () => { locked = true }).reportContent('report-1'))
  assert.equal(locked, true)
})

test('port and address editors preserve explicit empty/unknown distinctions', () => {
  assert.deepEqual(parsePorts('22, 443, 8000-8010'), [{ from: 22, to: 22 }, { from: 443, to: 443 }, { from: 8000, to: 8010 }])
  assert.deepEqual(parsePorts(''), [])
  assert.throws(() => parsePorts('0,70000'))
  assert.deepEqual(parseAddresses('198.51.100.0/25\n203.0.113.9'), ['198.51.100.0/25', '203.0.113.9'])
  assert.deepEqual(parseAddresses(''), [])
})

test('new service access dependencies require explicit role selection without auto-confirming it', () => {
  const selected = ['access']
  assert.deepEqual(missingDependencies(selected, { access: ['roles'] }), ['roles'])
  assert.deepEqual(selected, ['access'])
  assert.deepEqual(missingDependencies(['roles', 'access'], { access: ['roles'] }), [])
})

test('nullable Go slices render as absent declarations without trusting uploaded provenance', async t => {
  const value = { id: 'snapshot-1', source_kind: 'imported', input: { schema_version: 1, source_kind: 'imported', scope: { ref: 'scope-1' }, completeness: { declared: false, enumeration_complete: false, known_gaps: null }, protection: { state: 'unknown' }, selection: { kind: 'assets', asset_ids: ['asset-1'] }, baseline: { enabled_checks: [], risk_ports: [], exceptions: null }, policies: null, assets: [{ id: 'asset-1', services: null, allowed: null }] } }
  t.mock.method(globalThis, 'fetch', async () => Response.json(value))
  const saved = await new WorkspaceClient(() => 'public-test-access').importSnapshot({})
  assert.deepEqual(saved.input.policies, [])
  assert.deepEqual(saved.input.assets[0]?.services, [])
  assert.equal(saved.input.completeness.declared, false)
  value.source_kind = 'live_readonly'; value.input.source_kind = 'live_readonly'
  await assert.rejects(new WorkspaceClient(() => 'public-test-access').importSnapshot({}), /来源/)
})

test('confirmation cannot claim saved without a durable action marker', async t => {
  t.mock.method(globalThis, 'fetch', async () => Response.json({ draft_id: 'draft-1', status: 'confirmed', revision: 3, heads: [] }))
  await assert.rejects(new WorkspaceClient(() => 'public-test-access').confirmDraft('draft-1', 'preview-1', 2, ['roles'], 'key-1'), /保存/)
})

test('a restored unknown submission retains its original target, request and key', async () => {
  const mutation = new Mutation<{ finding_id: string; comment: string }, unknown>(() => assert.fail('must reuse the original key'))
  const body = { finding_id: 'finding-old', comment: '核对原对象' }
  mutation.restorePending(body, 'review-original')
  body.comment = 'later editor change'
  assert.equal(mutation.state, 'unsaved')
  await mutation.submit(mutation.pendingBody!, async (request, key) => {
    assert.deepEqual(request, { finding_id: 'finding-old', comment: '核对原对象' })
    assert.equal(key, 'review-original')
  })
})

test('only the latest asynchronous selection may update the displayed report', () => {
  const reads = new LatestRead()
  const first = reads.begin(), second = reads.begin()
  assert.equal(first(), false)
  assert.equal(second(), true)
  reads.invalidate()
  assert.equal(second(), false)
})

test('plain DTO copies preserve unsaved Vue editor values without sharing reactive objects', () => {
  const changes = reactive([{ roles: [{ ports: [{ from: 443, to: 443 }] }] }])
  const copy = cloneData(changes)
  changes[0]!.roles[0]!.ports[0]!.to = 444
  assert.equal(copy[0]!.roles[0]!.ports[0]!.to, 443)
})

test('a proven plan rejection can be edited while a storage or network failure keeps its original key', () => {
  assert.equal(isDefiniteRejection(new ApiError(409, 'PLAN_STALE')), true)
  assert.equal(isDefiniteRejection(new ApiError(503, 'STORAGE_UNAVAILABLE')), false)
  assert.equal(isDefiniteRejection(new TypeError('connection lost')), false)
  assert.equal(isDefiniteRejection(new ApiError(409, 'IDEMPOTENCY_CONFLICT')), false)
})

test('recent jobs stay newest first and an older list read cannot roll back a newer event state', () => {
  const old = { id: 'old', conversation_id: 'c1', status: 'completed' as const, view_seq: 8, created_at: '2026-10-28T00:00:00Z' }
  const newest = { id: 'new', conversation_id: 'c2', status: 'running' as const, view_seq: 2, created_at: '2026-10-28T01:00:00Z' }
  const result = mergeJobs([old], [newest, { ...old, status: 'running', view_seq: 4 }])
  assert.deepEqual(result.map(job => job.id), ['new', 'old'])
  assert.equal(result[1]?.status, 'completed')
  assert.equal(result[1]?.view_seq, 8)
})
