import type { ProfileDraft } from '../workspace-types.ts'

export function inventoryDraftFixture():ProfileDraft {
 const service=(id:string)=>({service_id:id,allowed_declared:false,required_declared:false,public_access:'unknown',allowed_complete:false,deny_all:false,allowed:[],required_confirmed:false,required:[]})
 return {id:'draft-one',snapshot_id:'snapshot-one',policy_scope_ref:'scope-one',revision:1,status:'draft',base_revisions:[{asset_id:'asset-one',revision:0,profile_version_id:'',intent_version_id:''}],created_at:'2026-10-29T01:00:00Z',updated_at:'2026-10-29T01:00:00Z',changes:[{asset_id:'asset-one',roles:[{service_id:'https',declared:false,role:'web',protocol:'tcp',ports:[{from:443,to:443}]},{service_id:'management',declared:false,role:'management',protocol:'tcp',ports:[{from:22,to:22}]}],access:{allowed_declared:false,exceptions_declared:false,allowed_complete:false,deny_all:false,allowed:[],services:[service('https'),service('management')],exceptions:[]}}]}
}
