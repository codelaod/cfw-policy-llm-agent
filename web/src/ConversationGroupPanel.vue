<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { createGroupController, groupConfirmRequest, groupIdentityLabel, groupMissingLabel, type GroupEditor } from './group-view'
import { catalogueSourceLabel } from './conversation-view'
import { errorText, formatTime, useWorkspace } from './workspace'

const props = defineProps<{ conversationId: string; disabled?: boolean }>()
const emit = defineEmits<{ state: [value: { pending: boolean; busy: boolean }] }>()
const workspace = useWorkspace()
const controller = createGroupController(workspace, () => props.conversationId, errorText, workspace.drafts.get('conversation-group-editors') as Map<string, GroupEditor> | undefined)
workspace.drafts.set('conversation-group-editors', controller.editors)
const state = controller.editor, shown = ref(20)
const selected = computed(() => workspace.currentIntent.value?.conversation_id === props.conversationId ? workspace.currentIntent.value.selection : [])
const blocked = computed(() => !!props.disabled || state.value.busy || !!state.value.mutation.pendingBody)
const confirmable = computed(() => { try { groupConfirmRequest(state.value.preview, workspace.currentIntent.value, state.value.approved, state.value.edited); return true } catch { return false } })
const latestVersion = computed(() => state.value.result?.version ?? null)
const refreshKey = computed(() => `${props.conversationId}/${workspace.unlocked.value}/${workspace.currentIntent.value?.revision ?? ''}/${workspace.messages.value.length}/${workspace.groupResults?.value[props.conversationId]?.sequence ?? 0}`)
watch(refreshKey, () => { void controller.load() }, { immediate: true })
watch(() => [!!state.value.mutation.pendingBody, state.value.busy, props.conversationId] as const, () => emit('state', { pending: !!state.value.mutation.pendingBody, busy: state.value.busy }), { immediate: true })
watch(() => state.value.preview?.id, () => { shown.value = 20 })
onBeforeUnmount(() => { controller.dispose(); emit('state', { pending: false, busy: false }) })
</script>

<template>
  <section v-if="conversationId" class="conversation-group" aria-labelledby="conversation-group-heading">
    <header><div><h2 id="conversation-group-heading">可信业务归属</h2><p>客户或业务组的归属单独确认，不改变角色、访问授权或云端配置。</p></div><button type="button" :disabled="state.loading || state.busy" @click="controller.load">读取归属预览</button></header>
    <p v-if="!selected.length" class="group-note">先在本会话解析并明确选择对象，再预览归属。普通名称或标签不能自动证明所属客户；用途未知仍可按明确对象检查独立基线。</p>
    <p v-else class="group-note">本会话有 {{ selected.length }} 个明确对象。下方表单只使用这些已选引用，不能自行增加成员。</p>
    <form v-if="selected.length" class="group-form" @submit.prevent="controller.preview">
      <label>客户或业务组名称<input v-model="state.name" maxlength="256" :disabled="blocked" placeholder="例如：客户甲或业务组支付" @input="controller.markChanged" /></label>
      <label>授权来源<textarea v-model="state.authority" rows="2" maxlength="2048" :disabled="blocked" placeholder="填写经业务负责人核对的工单或明确授权说明；未知时先保留澄清" @input="controller.markChanged" /></label>
      <p class="group-note">不要输入凭据。来源仍未知时，不会建立可信业务归属。</p>
      <button type="submit" class="group-preview-button" :disabled="blocked || !state.name.trim() || !state.authority.trim()">预览所选对象的归属</button>
    </form>
    <p v-if="state.loading" class="group-note" role="status">正在读取本会话保存的归属预览。</p>
    <section v-if="state.preview" class="group-preview" aria-label="归属确认预览">
      <h3>确认前核对：{{ state.preview.group_name }}</h3><dl><dt>资料来源／账号</dt><dd>{{ state.preview.source_ref }} / {{ state.preview.account_ref }}</dd><dt>当前目标修订</dt><dd>{{ state.preview.intent_revision }}</dd><dt>归属基版本</dt><dd>{{ state.preview.base_revision ? `修订 ${state.preview.base_revision}` : '尚无已确认版本，本次将创建首版' }}</dd><dt>预览有效至</dt><dd>{{ formatTime(state.preview.expires_at) }}</dd><dt>授权说明</dt><dd>{{ state.preview.authorization_source }}</dd></dl>
      <details class="group-members" :open="state.preview.members.length <= 3"><summary>核对 {{ state.preview.members.length }} 个明确成员与身份</summary><article v-for="member in state.preview.members.slice(0, shown)" :key="member.resource_ref"><strong>{{ member.name || '未命名对象' }}</strong><p>{{ member.region || '地域未知' }} / {{ member.resource_type || '类型未知' }} / {{ groupIdentityLabel(member) }}</p><p>{{ catalogueSourceLabel(member.source) }}；{{ formatTime(member.observed_at) }}</p><details><summary>来源限定引用</summary><p>{{ member.resource_ref }}</p><p>{{ member.identity_ref }}</p></details></article><button v-if="shown < state.preview.members.length" type="button" @click="shown += 20">继续显示本预览成员</button></details>
      <p v-if="state.edited" class="group-warning">表单已修改，先重新预览；不会用旧预览确认新内容。</p>
      <p v-else-if="workspace.currentIntent.value?.id !== state.preview.intent_id || workspace.currentIntent.value?.revision !== state.preview.intent_revision" class="group-warning">当前目标版本已变化，旧预览保留供核对，请重新预览。</p>
      <label class="group-ack"><input v-model="state.approved" type="checkbox" :disabled="blocked || state.edited" />我已核对本预览的成员、来源与授权说明</label>
      <button type="button" class="group-confirm" :disabled="blocked || !confirmable" @click="controller.confirm">确认本预览的归属</button>
    </section>
    <section v-if="latestVersion" class="group-saved" aria-label="已保存归属版本"><h3>{{ latestVersion.group_name }}：已保存版本 {{ latestVersion.revision }}</h3><p>{{ latestVersion.members.length }} 个对象；{{ latestVersion.source_ref }} / {{ latestVersion.account_ref }}；{{ formatTime(latestVersion.created_at) }}</p><p>仅后续目标可按这份归属检索，旧任务与已选对象保持原版本。</p></section>
    <ul v-if="state.result?.missing.length" class="group-missing"><li v-for="missing in state.result.missing" :key="missing">{{ groupMissingLabel(missing) }}</li></ul>
    <p v-if="state.message" class="group-note" role="status">{{ state.message }}</p><p v-if="state.error" class="group-warning" role="alert">{{ state.error }}</p>
    <div v-if="state.mutation.pendingBody && !state.busy" class="group-pending"><p>归属提交结果尚未核对，原会话、正文与提交标识已保留。</p><button type="button" :disabled="disabled || state.busy" @click="controller.submit()">以原内容核对归属提交</button><button type="button" :disabled="state.loading" @click="controller.load">先读取服务端预览</button></div>
  </section>
</template>

<style scoped>
.conversation-group{margin-top:20px;padding:18px 0;border-top:1px solid #dce6e1;font-size:12px;line-height:1.75;min-width:0}.conversation-group>header{display:flex;align-items:start;justify-content:space-between;gap:12px}.conversation-group h2{font-size:16px;margin:0}.conversation-group h3{font-size:13px;margin:0 0 8px}.conversation-group p{margin:5px 0 10px;overflow-wrap:anywhere}.conversation-group header p,.group-note{font-size:11px;color:#64748b}.conversation-group button{padding:0;border:0;background:none;color:#047857;font-size:11px;line-height:1.7;text-align:left}.conversation-group button:disabled{color:#94a3b8;cursor:default}.group-form{display:grid;gap:12px;margin-top:14px}.group-form label{font-size:11px}.group-form input,.group-form textarea{font-size:12px}.group-preview{margin-top:17px;padding:14px 12px;background:#f5faf7;border-left:2px solid #bdd5c8}.group-preview dl{display:grid;grid-template-columns:86px minmax(0,1fr);gap:7px 9px;margin:12px 0;font-size:11px}.group-preview dt{color:#64748b}.group-preview dd{margin:0;overflow-wrap:anywhere}.group-members{margin:14px 0;font-size:11px}.conversation-group summary{cursor:pointer;overflow-wrap:anywhere}.group-members article{padding:11px 0;border-bottom:1px solid #dfe9e3}.group-members article p{font-size:10px;color:#64748b;margin:5px 0}.group-ack{display:flex!important;align-items:start;gap:8px;margin:13px 0;font-size:11px!important}.group-ack input{margin-top:4px!important}.conversation-group .group-confirm{background:#047857;color:white;padding:8px 11px;border-radius:5px}.conversation-group .group-confirm:disabled{background:#e2e8f0;color:#64748b}.group-saved{margin-top:15px;padding:12px;background:#f1f8f4}.group-saved p{font-size:11px;color:#526c60}.group-warning,.group-missing{font-size:11px;color:#92400e}.group-missing{padding-left:17px}.group-pending{padding:11px;margin-top:13px;background:#fff7e7;color:#7d5513;font-size:11px}.group-pending button+button{margin-left:12px}.conversation-group button:focus-visible,.conversation-group summary:focus-visible{outline:3px solid #2563eb;outline-offset:3px}@media(max-width:600px){.group-preview dl{grid-template-columns:1fr;gap:2px}.group-preview dd{margin-bottom:7px}}
</style>
