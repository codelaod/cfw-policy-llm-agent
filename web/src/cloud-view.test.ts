import test from 'node:test'
import assert from 'node:assert/strict'
import { cloudPlanRequest, cloudRecordFor, cloudSnapshotID, decodeCloudRecord, decodeCollectionOptions } from './cloud-view.ts'
import { needsResultRefresh } from './agent-view.ts'
import { cloudOptionsFixture, cloudRecordFixture, cloudConversationFixture } from './test-fixtures/cloud.ts'

test('cloud plans require explicit allowed objects and a same-scope unarchived conversation', () => {
  const options = cloudOptionsFixture(), conversation = cloudConversationFixture()
  assert.throws(() => cloudPlanRequest(options, 'cloud-local', 'scope-in', [], false, conversation, 'collect'))
  assert.throws(() => cloudPlanRequest(options, 'cloud-local', 'scope-in', ['asset-other'], false, conversation, 'collect'))
  assert.throws(() => cloudPlanRequest(options, 'cloud-local', 'scope-in', ['asset-one'], false, { ...conversation, policy_scope_ref: 'scope-other' }, 'collect'))
  assert.throws(() => cloudPlanRequest(options, 'cloud-local', 'scope-in', ['asset-one'], false, { ...conversation, archived: true }, 'collect'))
  assert.throws(() => cloudPlanRequest({ ...options, available: false }, 'cloud-local', 'scope-in', ['asset-one'], false, conversation, 'collect'))
  const request = cloudPlanRequest(options, 'cloud-local', 'scope-in', ['asset-one'], false, conversation, 'collect')
  assert.deepEqual(request, { kind: 'collect', conversation_id: conversation.id, data_source_id: 'cloud-local', policy_scope_ref: 'scope-in', selection: { kind: 'assets', asset_ids: ['asset-one'] }, data_policy: { mode: 'force_refresh', allow_stale_fallback: false }, baseline_version_id: 'B2-v1', mode: 'rules_only' })
})

test('NAT gateway-only collection uses the configured opaque gateway and never expands an empty asset list', () => {
  const options = cloudOptionsFixture(), conversation = { ...cloudConversationFixture(), policy_scope_ref: 'scope-nat' }
  const request = cloudPlanRequest(options, 'cloud-local', 'scope-nat', [], true, conversation, 'collect')
  assert.deepEqual(request.selection, { kind: 'gateway_policy', asset_ids: [], gateway_ref: 'gateway-opaque' })
  assert.throws(() => cloudPlanRequest(options, 'cloud-local', 'scope-in', [], true, cloudConversationFixture(), 'collect'))
})
test('identity checks need no asset catalog and never inherit previous asset or gateway choices',()=>{
 const options=cloudOptionsFixture();options.scopes[0]!.assets=[]
 assert.deepEqual(cloudPlanRequest(options,'cloud-local','scope-in',[],false,cloudConversationFixture(),'connection_check').selection,{kind:'identity_only',asset_ids:[]})
 const nat={...cloudConversationFixture(),policy_scope_ref:'scope-nat'}
 assert.deepEqual(cloudPlanRequest(options,'cloud-local','scope-nat',['stale-asset'],true,nat,'connection_check').selection,{kind:'identity_only',asset_ids:[]})
 assert.throws(()=>cloudPlanRequest(options,'cloud-local','scope-in',[],false,cloudConversationFixture(),'collect'))
})

test('local collection options cannot invent a verified identity or accept another source catalog', () => {
  const options = cloudOptionsFixture()
  assert.equal(decodeCollectionOptions(options, 'cloud-local').identity_verified, false)
  assert.throws(() => decodeCollectionOptions({ ...options, identity_verified: true }, 'cloud-local'))
  assert.throws(() => decodeCollectionOptions(options, 'different-source'))
})
test('server-selected larger HTTP profiles remain valid local options without a legacy hundred-attempt cap',()=>{
 const options=cloudOptionsFixture();options.max_http_attempts=250
 assert.equal(decodeCollectionOptions(options,'cloud-local').max_http_attempts,250)
 assert.throws(()=>decodeCollectionOptions({...options,max_http_attempts:Infinity},'cloud-local'))
})
test('explicit directory membership supports more than fifty selected assets without weakening source or duplicate checks',()=>{
 const options=cloudOptionsFixture();options.scopes[0]!.assets=Array.from({length:75},(_,i)=>({id:'asset-'+i,region:'region-one'}))
 const ids=options.scopes[0]!.assets.map(asset=>asset.id)
 assert.equal(cloudPlanRequest(options,'cloud-local','scope-in',ids,false,cloudConversationFixture(),'collect').selection.asset_ids.length,75)
 assert.throws(()=>cloudPlanRequest(options,'cloud-local','scope-in',[...ids,'foreign'],false,cloudConversationFixture(),'collect'))
 assert.throws(()=>cloudPlanRequest(options,'cloud-local','scope-in',[...ids,ids[0]!],false,cloudConversationFixture(),'collect'))
})

test('cloud result actions stay bound to the same task and returned snapshot', () => {
  const record = cloudRecordFixture()
  assert.equal(cloudSnapshotID(record, 'job-cloud', 'snapshot-cloud'), 'snapshot-cloud')
  assert.equal(cloudSnapshotID(record, 'job-other', 'snapshot-cloud'), '')
  assert.equal(cloudSnapshotID(record, 'job-cloud', 'snapshot-other'), '')
  assert.equal(cloudRecordFor({ ...record, content: { ...record.content, job_id: 'job-other' } }, 'job-cloud'), null)
  assert.throws(() => decodeCloudRecord(record, 'job-other'))
  assert.throws(() => decodeCloudRecord({ ...record, content: { ...record.content, kind: 'connection_check' } }, 'job-cloud'))
})

test('identity-only results never expose a snapshot action and missing lists remain empty', () => {
  const record = cloudRecordFixture()
  const identity = { ...record, content: { ...record.content, kind: 'connection_check', snapshot_id: undefined, product_ready: false, observations: null, limitations: ['IDENTITY_ONLY'] } }
  const decoded = decodeCloudRecord(identity, 'job-cloud')
  assert.deepEqual(decoded.content.observations, [])
  assert.equal(cloudSnapshotID(decoded, 'job-cloud'), '')
})

test('a committed cloud result event refreshes the authoritative result projection', () => {
  assert.equal(needsResultRefresh({ schema_version: 1, job_id: 'job-cloud', seq: 3, type: 'stage_changed', occurred_at: '2026-10-28T00:00:00Z', payload: { stage: 'cloud_result_saved', cloud_result_ref: 'cloud-record' } }), true)
})
