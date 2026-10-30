import test from 'node:test'
import assert from 'node:assert/strict'
import { WorkspaceClient } from './workspace-api.ts'
import { cloudOptionsFixture, cloudRecordFixture } from './test-fixtures/cloud.ts'

test('cloud catalog/options reads use protected GETs and never send a plan or provider parameters', async t => {
  const calls: { url: string; init: RequestInit }[] = []
  t.mock.method(globalThis, 'fetch', async (url: string, init: RequestInit) => {
    calls.push({ url, init })
    return Response.json(url.endsWith('/collection-options') ? cloudOptionsFixture() : { items: [{ id: 'cloud-local', label: '显式连接', kind: 'aliyun', available: true, cloud_reads: true }] })
  })
  const client = new WorkspaceClient(() => 'synthetic-test-access')
  await client.sources(); await client.collectionOptions('cloud-local')
  assert.deepEqual(calls.map(call => call.url), ['/api/v1/data-sources', '/api/v1/data-sources/cloud-local/collection-options'])
  for (const call of calls) { assert.ok(!call.init.method || call.init.method === 'GET'); assert.equal(call.init.body, undefined); assert.equal(new Headers(call.init.headers).get('X-App-Token'), 'synthetic-test-access'); assert.ok(!call.url.includes('synthetic-test-access')) }
})

test('the authoritative job result adapter rejects a cloud result belonging to a different task', async t => {
  t.mock.method(globalThis, 'fetch', async () => Response.json({ is_final: true, view_seq: 5, cloud_result: cloudRecordFixture() }))
  const client = new WorkspaceClient(() => 'synthetic-test-access')
  assert.equal((await client.jobResult('job-cloud')).cloud_result?.id, 'cloud-record')
  await assert.rejects(client.jobResult('job-other'), /当前任务/)
})

test('audit preparation cloud records are accepted under their original audit task without becoming connection checks', async t => {
  const record = cloudRecordFixture(); record.content.kind = 'audit'
  t.mock.method(globalThis, 'fetch', async () => Response.json({ is_final: false, view_seq: 5, cloud_result: record }))
  const value = await new WorkspaceClient(() => 'public-test-access').jobResult('job-cloud')
  assert.equal(value.cloud_result?.content.kind, 'audit'); assert.equal(value.cloud_result?.job_id, 'job-cloud')
})
