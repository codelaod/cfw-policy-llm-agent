import test from 'node:test'
import assert from 'node:assert/strict'
import { applyInventoryConflictChoices, buildInventoryConflictReview, buildInventoryRecoveryReview, readInventoryAssetPages } from './inventory-conflict.ts'
import { cloneData } from './workspace-state.ts'
import type { AssetView, ProfileDraft, ProfileDraftCurrentBase } from './workspace-types.ts'
import { inventoryDraftFixture } from './test-fixtures/inventory-conflict.ts'


function localChanges(original=inventoryDraftFixture()) {const local=cloneData(original.changes);local[0]!.roles[0]!.declared=true;local[0]!.roles[0]!.role='application';const service=local[0]!.access!.services[0]!;service.required_declared=true;service.required_confirmed=true;service.required=[{scope_ref:'scope-one',protocol:'tcp',ports:[{from:443,to:443}],addresses:[],applicability:'unknown'}];return local}

test('conflict choices isolate role and required edits while preserving newer fields and added services',()=>{
 const original=inventoryDraftFixture(),local=localChanges(original),latest=cloneData(original);latest.revision=2;latest.changes[0]!.roles[1]!.role='database';latest.changes[0]!.roles.push({service_id:'new-cache',declared:true,role:'cache',protocol:'tcp',ports:[{from:6379,to:6379}]});latest.changes[0]!.access!.services[0]!.allowed_declared=true;latest.changes[0]!.access!.services[0]!.deny_all=true
 const before=JSON.stringify({original,local,latest}),review=buildInventoryConflictReview(original,local,latest,latest.base_revisions)
 assert.deepEqual(review.choices.map(value=>value.kind),['role','service_required'])
 assert.throws(()=>applyInventoryConflictChoices(review,[]),/明确选择/)
 const role=applyInventoryConflictChoices(review,[review.choices.find(value=>value.kind==='role')!.key])
 assert.equal(role[0]!.roles[0]!.role,'application');assert.equal(role[0]!.roles[1]!.role,'database');assert.equal(role[0]!.roles[2]!.service_id,'new-cache');assert.equal(role[0]!.access!.services[0]!.deny_all,true);assert.equal(role[0]!.access!.services[0]!.required_confirmed,false)
 const required=applyInventoryConflictChoices(review,[review.choices.find(value=>value.kind==='service_required')!.key]);assert.equal(required[0]!.roles[0]!.role,'web');assert.equal(required[0]!.access!.services[0]!.required_confirmed,true)
 assert.equal(JSON.stringify({original,local,latest}),before)
})
test('head recovery preserves new current services and the latest draft pending declarations',()=>{
 const original=inventoryDraftFixture(),local=localChanges(original),latest=cloneData(original);latest.revision=2;latest.changes[0]!.roles[1]!.declared=true;latest.changes[0]!.roles[1]!.role='database'
 const current:ProfileDraftCurrentBase={draft_id:latest.id,draft_revision:latest.revision,snapshot_id:latest.snapshot_id,policy_scope_ref:latest.policy_scope_ref,changes:cloneData(original.changes),base_revisions:[{asset_id:'asset-one',revision:1,profile_version_id:'profile-new',intent_version_id:''}]}
 current.changes[0]!.roles.push({service_id:'new-current',declared:false,role:'background',protocol:'tcp',ports:[{from:9000,to:9000}]})
 const review=buildInventoryRecoveryReview(original,local,latest,current)
 assert.equal(review.headsChanged,true);assert.equal(review.latest.changes[0]!.roles[1]!.role,'database');assert.equal(review.latest.changes[0]!.roles[2]!.service_id,'new-current')
 const merged=applyInventoryConflictChoices(review,[review.choices.find(value=>value.kind==='role')!.key]);assert.equal(merged[0]!.roles[0]!.role,'application');assert.equal(merged[0]!.roles[1]!.role,'database');assert.equal(merged[0]!.roles[2]!.service_id,'new-current')
})
test('foreign or raced recovery bases and unknown selections cannot become a write',()=>{
 const original=inventoryDraftFixture(),local=localChanges(original),latest=cloneData(original);latest.revision=2
 for(const mutate of [(value:ProfileDraft)=>{value.id='other'},(value:ProfileDraft)=>{value.snapshot_id='other'},(value:ProfileDraft)=>{value.changes[0]!.asset_id='other'}]){const bad=cloneData(latest);mutate(bad);assert.throws(()=>buildInventoryConflictReview(original,local,bad,latest.base_revisions),/不一致/)}
 const review=buildInventoryConflictReview(original,local,latest,latest.base_revisions)
 assert.throws(()=>applyInventoryConflictChoices(review,['unknown']),/明确选择/);assert.throws(()=>applyInventoryConflictChoices(review,[review.choices[0]!.key,review.choices[0]!.key]),/明确选择/)
 const base:ProfileDraftCurrentBase={draft_id:latest.id,draft_revision:3,snapshot_id:latest.snapshot_id,policy_scope_ref:latest.policy_scope_ref,changes:latest.changes,base_revisions:latest.base_revisions};assert.throws(()=>buildInventoryRecoveryReview(original,local,latest,base),/再次变化/)
})
test('reading a server-assigned new service after an unknown save does not offer an identical duplicate',()=>{
 const original=inventoryDraftFixture(),local=cloneData(original.changes),latest=cloneData(original)
 local[0]!.roles.push({service_id:'',declared:true,role:'application',protocol:'tcp',ports:[{from:8443,to:8443}]})
 latest.revision=2;latest.changes[0]!.roles.push({service_id:'assigned-service',declared:true,role:'application',protocol:'tcp',ports:[{from:8443,to:8443}]})
 const review=buildInventoryConflictReview(original,local,latest,latest.base_revisions)
 assert.deepEqual(review.choices,[]);assert.equal(review.latest.changes[0]!.roles.at(-1)?.service_id,'assigned-service')
})
test('asset views read through the explicit 101-object snapshot without a first-page fallback',async()=>{
 const ids=Array.from({length:101},(_,i)=>'asset-'+i),offsets:number[]=[]
 const views=await readInventoryAssetPages('snapshot-one',ids,async(_id,offset)=>{offsets.push(offset);return{items:ids.slice(offset,offset+100).map(id=>({asset:{id},local_confirmed_sections:id==='asset-100'?['roles']:[]} as unknown as AssetView)),offset,limit:100}},()=>true)
 assert.deepEqual(offsets,[0,100]);assert.deepEqual(views?.find(value=>value.asset.id==='asset-100')?.local_confirmed_sections,['roles'])
})
test('a switched snapshot, missing page or duplicate object cannot publish mixed current views',async()=>{
 const ids=Array.from({length:101},(_,i)=>'asset-'+i);let valid=true,reads=0
 const stale=await readInventoryAssetPages('snapshot-one',ids,async(_id,offset)=>{reads++;if(offset===100)valid=false;return{items:ids.slice(offset,offset+100).map(id=>({asset:{id}} as AssetView)),offset,limit:100}},()=>valid);assert.equal(stale,null);assert.equal(reads,2)
 await assert.rejects(readInventoryAssetPages('snapshot-one',ids,async(_id,offset)=>({items:offset===0?ids.slice(0,100).map(id=>({asset:{id}} as AssetView)):[],offset,limit:100}),()=>true),/尚未覆盖/)
 await assert.rejects(readInventoryAssetPages('snapshot-one',['one','two'],async()=>({items:[{asset:{id:'one'}},{asset:{id:'one'}}] as AssetView[],offset:0,limit:100}),()=>true),/重复/)
})
