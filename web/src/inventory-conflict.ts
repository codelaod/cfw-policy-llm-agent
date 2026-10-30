import { businessAllowedLabel, businessRequiredLabel, businessRoleLabel } from './business-view.ts'
import { cloneData, formatPorts } from './workspace-state.ts'
import type { AssetChange, AssetHead, AssetView, BusinessException, Page, ProfileDraft, ProfileDraftCurrentBase, RoleChange, ServiceAccessChange } from './workspace-types.ts'

type Kind = 'role' | 'asset_allowed' | 'service_allowed' | 'service_required' | 'exceptions'
export type InventoryConflictChoice = { key: string; assetID: string; serviceID: string; kind: Kind; label: string; latestLines: string[]; localLines: string[]; local: unknown }
export type InventoryConflictReview = { original: ProfileDraft; local: AssetChange[]; latest: ProfileDraft; choices: InventoryConflictChoice[]; headsChanged: boolean }
const equal = (left: unknown, right: unknown) => JSON.stringify(left) === JSON.stringify(right)
const key = (asset: string, kind: Kind, service: string, index = 0) => JSON.stringify([asset, kind, service, index])
const allowed = (value: NonNullable<AssetChange['access']> | ServiceAccessChange) => ({ allowed_declared: value.allowed_declared, allowed_complete: value.allowed_complete, deny_all: value.deny_all, allowed: value.allowed, ...('public_access' in value ? { public_access: value.public_access } : {}) })
const required = (value: ServiceAccessChange) => ({ required_declared: value.required_declared, required_confirmed: value.required_confirmed, required: value.required })
const roleLines = (value?: RoleChange) => value ? [`${businessRoleLabel(value.role)}；${value.protocol.toUpperCase()} ${formatPorts(value.ports) || '端口待明确'}`] : ['最新保存稿没有此服务声明']
const exceptionLines = (values?: BusinessException[]) => values?.length ? values.map(value => `${value.check_id}${value.subcheck ? ` / ${value.subcheck}` : ''}；${value.reason || '理由待补'}；授权来源 ${value.authorization_source || '尚未声明'}；到期 ${value.expires_at || '尚未声明'}`) : ['尚无这部分例外声明']
function bound(original: ProfileDraft, local: AssetChange[], latest: ProfileDraft) {
 const ids = (values: AssetChange[]) => values.map(value => value.asset_id).sort()
 if (original.id !== latest.id || original.snapshot_id !== latest.snapshot_id || original.policy_scope_ref !== latest.policy_scope_ref || !equal(ids(original.changes),ids(local)) || !equal(ids(original.changes),ids(latest.changes)) || new Set(ids(local)).size !== local.length || new Set(ids(latest.changes)).size !== latest.changes.length) throw new Error('最新稿与原草稿的对象或固定资料不一致，原输入仍保留。')
}
export function inventoryHeadsChanged(expected: AssetHead[], current: AssetHead[]) {
 return expected.length !== current.length || expected.some(head => { const next = current.find(value => value.asset_id === head.asset_id); return !next || head.revision !== next.revision || head.profile_version_id !== next.profile_version_id || head.intent_version_id !== next.intent_version_id })
}
export async function readInventoryAssetPages(snapshotID: string, expectedIDs: string[], read: (snapshotID: string, offset: number) => Promise<Page<AssetView>>, current: () => boolean): Promise<AssetView[] | null> {
 const expected = new Set(expectedIDs), seen = new Set<string>(), out: AssetView[] = []
 if (expected.size !== expectedIDs.length) throw new Error('固定资料的资产身份重复，未应用业务版本。')
 for (let offset = 0; offset < expectedIDs.length; offset += 100) {
  const page = await read(snapshotID,offset)
  if (!current()) return null
  for (const row of page.items) {
   if (!expected.has(row.asset.id) || seen.has(row.asset.id)) throw new Error('业务版本分页含重复或其他资料对象，未用旧确认补齐。')
   seen.add(row.asset.id);out.push(row)
  }
  if (page.items.length < 100 && seen.size < expected.size) throw new Error('业务版本分页尚未覆盖全部固定对象，未用旧确认补齐。')
 }
 if (seen.size !== expected.size) throw new Error('业务版本分页缺少固定对象，未用旧确认补齐。')
 return out
}
export function buildInventoryRecoveryReview(original: ProfileDraft, local: AssetChange[], saved: ProfileDraft, base: ProfileDraftCurrentBase): InventoryConflictReview {
 if (base.draft_id !== saved.id || base.draft_revision !== saved.revision || base.snapshot_id !== saved.snapshot_id || base.policy_scope_ref !== saved.policy_scope_ref) throw new Error('读取期间草稿已再次变化，请重新核对；原输入仍保留。')
 if (!inventoryHeadsChanged(saved.base_revisions,base.base_revisions)) return buildInventoryConflictReview(original,local,saved,base.base_revisions)
 const latest = {...cloneData(saved),changes:cloneData(base.changes),base_revisions:cloneData(base.base_revisions)}
 bound(saved,saved.changes,latest)
 // Existing explicit pending statements remain in the same draft. New current
 // fields/services absent from that old draft are retained from the quiet base.
 if (['draft','previewed'].includes(saved.status)) {
  const pending = buildInventoryConflictReview(saved,saved.changes,latest,base.base_revisions)
  if (pending.choices.length) latest.changes = applyInventoryConflictChoices(pending,pending.choices.map(choice => choice.key))
 }
 const result = buildInventoryConflictReview(original,local,latest,base.base_revisions)
 result.headsChanged = true
 return result
}
export function buildInventoryConflictReview(original: ProfileDraft, local: AssetChange[], latest: ProfileDraft, currentHeads: AssetHead[]): InventoryConflictReview {
 bound(original,local,latest)
 const choices: InventoryConflictChoice[] = []
 const add = (choice: InventoryConflictChoice) => choices.push(choice)
 for (const change of local) {
  const before = original.changes.find(value => value.asset_id === change.asset_id)!, saved = latest.changes.find(value => value.asset_id === change.asset_id)!
  change.roles.forEach((role,index) => {
   const prior = role.service_id ? before.roles.find(value => value.service_id === role.service_id) : undefined
   if (role.declared === false || role.declared !== true && equal(role,prior)) return
   if (!role.service_id && saved.roles.some(value => value.service_id && !before.roles.some(old => old.service_id === value.service_id) && equal({role:value.role,protocol:value.protocol,ports:value.ports},{role:role.role,protocol:role.protocol,ports:role.ports}))) return
   const now = role.service_id ? saved.roles.find(value => value.service_id === role.service_id) : undefined
   if (equal(role,now)) return
   add({key:key(change.asset_id,'role',role.service_id,index),assetID:change.asset_id,serviceID:role.service_id,kind:'role',label:'角色与服务',latestLines:roleLines(now),localLines:roleLines(role),local:cloneData(role)})
  })
  const access = change.access, prior = before.access, now = saved.access
  if (!access) continue
  if (access.allowed_declared !== false && (access.allowed_declared === true || !equal(allowed(access),prior && allowed(prior))) && !equal(allowed(access),now && allowed(now))) add({key:key(change.asset_id,'asset_allowed',''),assetID:change.asset_id,serviceID:'',kind:'asset_allowed',label:'资产允许上界',latestLines:[businessAllowedLabel(now)],localLines:[businessAllowedLabel(access)],local:cloneData(allowed(access))})
  for (const service of access.services) {
   const oldService = prior?.services.find(value => value.service_id === service.service_id), latestService = now?.services.find(value => value.service_id === service.service_id)
   if (service.allowed_declared !== false && (service.allowed_declared === true || !equal(allowed(service),oldService && allowed(oldService))) && !equal(allowed(service),latestService && allowed(latestService))) add({key:key(change.asset_id,'service_allowed',service.service_id),assetID:change.asset_id,serviceID:service.service_id,kind:'service_allowed',label:'服务允许范围',latestLines:[businessAllowedLabel(latestService)],localLines:[businessAllowedLabel(service)],local:cloneData(allowed(service))})
   if (service.required_declared !== false && (service.required_declared === true || !equal(required(service),oldService && required(oldService))) && !equal(required(service),latestService && required(latestService))) add({key:key(change.asset_id,'service_required',service.service_id),assetID:change.asset_id,serviceID:service.service_id,kind:'service_required',label:'服务必要访问',latestLines:[businessRequiredLabel(latestService)],localLines:[businessRequiredLabel(service)],local:cloneData(required(service))})
  }
  if (access.exceptions_declared !== false && (access.exceptions_declared === true || !equal({declared:access.exceptions_declared,values:access.exceptions ?? []},{declared:prior?.exceptions_declared,values:prior?.exceptions ?? []})) && !equal({declared:access.exceptions_declared,values:access.exceptions ?? []},{declared:now?.exceptions_declared,values:now?.exceptions ?? []})) add({key:key(change.asset_id,'exceptions',''),assetID:change.asset_id,serviceID:'',kind:'exceptions',label:'本地业务例外',latestLines:exceptionLines(now?.exceptions),localLines:exceptionLines(access.exceptions),local:cloneData(access.exceptions ?? [])})
 }
 return {original:cloneData(original),local:cloneData(local),latest:cloneData(latest),choices,headsChanged:inventoryHeadsChanged(latest.base_revisions,currentHeads)}
}
/** Only explicit choices replace corresponding fields in the latest saved draft. */
export function applyInventoryConflictChoices(review: InventoryConflictReview, selected: string[]): AssetChange[] {
 bound(review.original,review.local,review.latest)
 if (!selected.length || new Set(selected).size !== selected.length || selected.some(value => !review.choices.some(choice => choice.key === value))) throw new Error('请明确选择要保留的本地改动；未选部分沿用最新保存稿。')
 const changes = cloneData(review.latest.changes)
 for (const choice of review.choices.filter(value => selected.includes(value.key))) {
  const target = changes.find(value => value.asset_id === choice.assetID)!
  if (choice.kind === 'role') {
   const role = cloneData(choice.local as RoleChange), index = choice.serviceID ? target.roles.findIndex(value => value.service_id === choice.serviceID) : -1
   role.declared = true
   if (index < 0) target.roles.push(role); else target.roles[index] = role
   continue
  }
  const access = target.access ??= {allowed_declared:false,exceptions_declared:false,allowed_complete:false,deny_all:false,allowed:[],services:[],exceptions:[]}
  if (choice.kind === 'asset_allowed') { Object.assign(access,cloneData(choice.local),{allowed_declared:true}); continue }
  if (choice.kind === 'exceptions') { access.exceptions = cloneData(choice.local as BusinessException[]); access.exceptions_declared = true; continue }
  let service = access.services.find(value => value.service_id === choice.serviceID)
  if (!service) {
   if (!target.roles.some(value => value.service_id === choice.serviceID)) throw new Error('要恢复访问要求的服务已不存在，请先核对对应角色；原输入仍保留。')
   service = {service_id:choice.serviceID,allowed_declared:false,required_declared:false,public_access:'unknown',allowed_complete:false,deny_all:false,allowed:[],required_confirmed:false,required:[]};access.services.push(service)
  }
  Object.assign(service,cloneData(choice.local),choice.kind === 'service_allowed' ? {allowed_declared:true} : {required_declared:true})
 }
 return changes
}
