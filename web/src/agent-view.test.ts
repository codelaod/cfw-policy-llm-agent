import test from 'node:test'
import assert from 'node:assert/strict'
import { AgentEvidenceReader, assetRoute, bindingFor, inventoryFocus, modelCapability, needsResultRefresh, readPointer, toolRuns, usageView } from './agent-view.ts'
import type { AttemptUsage } from './workspace-types.ts'

import { agentFixture } from './test-fixtures/agent.ts'

const usage = (changes: Partial<AttemptUsage> = {}): AttemptUsage => ({ cloud_attempts: 0, model_attempts: 2, unknown_delivery: 0, known_usage_responses: 0, unknown_usage_attempts: 2, reported_tokens: null, usage_totals_overflow: false, ...changes })

test('missing and null physical token usage stay unknown rather than zero or money', () => {
  assert.equal(usageView(undefined).tokenState, 'unknown')
  const view = usageView(usage())
  assert.equal(view.modelAttempts, '2')
  assert.equal(view.tokenState, 'unknown')
  assert.equal(view.tokens, null)
  assert.match(view.coverage, /2.*未报告/)
})

test('partial reported usage preserves the reported subset and labels unknown attempts', () => {
  const view = usageView(usage({ known_usage_responses: 1, unknown_usage_attempts: 1, reported_tokens: { prompt_tokens: 10, completion_tokens: 4, total_tokens: 14 } }))
  assert.equal(view.tokenState, 'partial')
  assert.equal(view.tokens?.total, '14')
  assert.match(view.coverage, /1.*未报告/)
  assert.equal(usageView(usage({ usage_totals_overflow: true })).tokenState, 'unknown')
})

test('local asset links use actual same-job bindings and an explicit stored snapshot', () => {
  const record = agentFixture()
  assert.deepEqual(assetRoute(record, 'job-one', 'asset_a', 'snapshot-real', 'service_a'), { name: 'inventory', params: { id: 'asset-real' }, query: { snapshot_id: 'snapshot-real', service: 'service-real' } })
  assert.equal(assetRoute(record, 'other-job', 'asset_a', 'snapshot-real'), null)
  assert.equal(assetRoute(record, 'job-one', 'asset_a', ''), null)
  assert.equal(bindingFor(record, 'job-one', 'asset_a'), null)
  assert.equal(bindingFor(record, 'job-one', 'evidence_a')?.field, '/assets/0')
  assert.equal(bindingFor(record, 'job-one', 'foreign_alias'), null)
})

test('JSON pointers read only own fixed-document fields and decode escaped keys', () => {
  const value = { 'a/b': { '~name': [null, 9] } }
  assert.deepEqual(readPointer(value, '/a~1b/~0name/1'), { found: true, value: 9 })
  assert.deepEqual(readPointer(value, '/a~1b/~0name/0'), { found: true, value: null })
  assert.deepEqual(readPointer(value, '/constructor'), { found: false })
  assert.deepEqual(readPointer(value, '/a~1b/~0name/01'), { found: false })
  assert.deepEqual(readPointer(value, '/bad~2escape'), { found: false })
})

test('agent save and committed tool events trigger authoritative result reads', () => {
  for (const type of ['tool_started', 'tool_finished', 'job_terminal', 'message_available']) assert.equal(needsResultRefresh({ type, payload: {} } as never), true)
  assert.equal(needsResultRefresh({ type: 'stage_changed', payload: { stage: 'agent_result_saved' } } as never), true)
  assert.equal(needsResultRefresh({ type: 'stage_changed', payload: { stage: 'rules_started' } } as never), false)
})

test('switching jobs discards a late protected input read and cannot rebind its alias', async () => {
  const reader = new AgentEvidenceReader()
  const record = agentFixture()
  reader.reset('job-one', record.id)
  let release!: (value: unknown) => void
  const first = reader.inspect('job-one', record, 'evidence_a', null, () => new Promise(resolve => { release = resolve }))
  assert.equal(reader.busy, true)
  reader.reset('job-two', 'agent-two')
  release({ assets: [{ id: 'old-private-asset' }] })
  await first
  assert.equal(reader.selection, null)
  assert.deepEqual(reader.value, { found: false })
  assert.equal(reader.busy, false)
  await reader.inspect('job-two', record, 'evidence_a', null, async () => assert.fail('foreign record must not fetch'))
  assert.equal(reader.selection, null)
})

test('configured presence never overrides an explicit unavailable capability', () => {
  const view = modelCapability({ model_configured: true, model_available: false, model_mode: 'configured' })
  assert.equal(view.ready, false)
  assert.match(view.label, /未就绪/)
  assert.equal(modelCapability({ model_configured: true }).ready, false)
  assert.equal(modelCapability({ model_available: true }).ready, true)
})

test('fixed asset focus cannot silently choose a different object or invent a missing service', () => {
  const assets = [{ id: 'asset-one', services: [{ id: 'service-one' }] }, { id: 'asset-two', services: [] }]
  assert.deepEqual(inventoryFocus(assets, 'asset-two', 'service-old'), { assetID: 'asset-two', missingAsset: false, serviceID: 'service-old', missingService: true })
  assert.deepEqual(inventoryFocus(assets, 'foreign', ''), { assetID: '', missingAsset: true, serviceID: '', missingService: false })
})

test('tool completion without a controlled status does not become a fabricated success', () => {
  const events = [{ job_id: 'job-one', seq: 1, type: 'tool_started', payload: { call_id: 'same-id', tool_name: 'get_asset_context', status: 'started' } }, { job_id: 'foreign-job', seq: 2, type: 'tool_finished', payload: { call_id: 'same-id', tool_name: 'get_asset_context', status: 'succeeded' } }, { job_id: 'job-one', seq: 3, type: 'tool_finished', payload: { call_id: 'same-id', tool_name: 'get_asset_context' } }]
  const rows = toolRuns(events as never, 'job-one')
  assert.equal(rows.length, 1)
  assert.equal(rows[0]?.status, 'unknown')
  assert.equal(rows[0]?.elapsedMS, undefined)
})

test('a record cannot expose actions when it contradicts its own persisted output state', () => {
  const record = agentFixture()
  record.content.error_code = 'MODEL_OUTPUT_INVALID'
  assert.equal(assetRoute(record, 'job-one', 'asset_a', 'snapshot-real'), null)
})
