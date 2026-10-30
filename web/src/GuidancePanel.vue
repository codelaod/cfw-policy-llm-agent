<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { applyGuidanceFill, guidanceMissing, resolveGuidance, type GuidanceCatalog, type GuidanceContext, type GuidanceItem, type GuidanceSurface } from './guidance'
import { createConversationController } from './conversation-state'
import { errorText, useWorkspace } from './workspace'

const props = defineProps<{ surface: GuidanceSurface; conversationId?: string; jobId?: string; reportId?: string; snapshotId?: string; draftId?: string; disabled?: boolean }>()
const workspace = useWorkspace(), router = useRouter()
const catalog = ref<GuidanceCatalog | null>(null), loading = ref(false), error = ref(''), actionError = ref(''), busy = ref('')
const context = computed<GuidanceContext>(() => ({ surface: props.surface, conversationID: props.conversationId || '', jobID: props.jobId || '', reportID: props.reportId || '', snapshotID: props.snapshotId || '', draftID: props.draftId || '' }))
const contextKey = computed(() => JSON.stringify(context.value))
const refreshKey = computed(() => `${contextKey.value}/${workspace.unlocked.value}/${props.conversationId && workspace.currentIntent.value?.conversation_id === props.conversationId ? workspace.currentIntent.value.revision : ''}/${workspace.jobs.value.find(job => job.id === props.jobId)?.status ?? ''}/${workspace.jobs.value.find(job => job.id === props.jobId)?.result_ref ?? ''}`)
let reads = 0, actions = 0, disposed = false
async function load() {
  const version = ++reads, key = contextKey.value, valid = workspace.captureContext()
  catalog.value = null; error.value = ''; actionError.value = ''
  if (!workspace.unlocked.value) { loading.value = false; return }
  loading.value = true
  try { const value = await workspace.api.guidance(context.value); if (!disposed && version === reads && contextKey.value === key && valid()) catalog.value = value }
  catch (cause) { if (!disposed && version === reads && contextKey.value === key && valid()) error.value = errorText(cause) }
  finally { if (!disposed && version === reads) loading.value = false }
}
async function follow(item: GuidanceItem) {
  if (busy.value || props.disabled || !catalog.value?.items.includes(item)) return
  const version = ++actions, key = contextKey.value, valid = workspace.captureContext()
  const current = () => !disposed && version === actions && contextKey.value === key && valid()
  busy.value = item.id; actionError.value = ''
  try {
    const resolved = await resolveGuidance(item, { ...context.value }, workspace.api)
    if (!current()) return
    if (resolved.conversationID && workspace.conversationID.value !== resolved.conversationID) {
      const saved = await workspace.api.conversation(resolved.conversationID)
      if (!current()) return
      if (saved.id !== resolved.conversationID || saved.archived) throw new Error('原会话不可用。')
      await workspace.selectConversation(saved.id)
      if (!valid() || disposed || version !== actions) return
    }
    if (resolved.kind === 'fill') {
      const controller = (workspace.drafts.get('conversation-controller') as ReturnType<typeof createConversationController> | undefined) ?? createConversationController(workspace, errorText)
      workspace.drafts.set('conversation-controller', controller)
      const editor = controller.editor.value, owner = workspace.conversationID.value
      if (editor.busy || editor.turn.pendingBody || editor.choice.pendingBody || editor.acceptance.pendingBody) throw new Error('先核对尚未确认结果的原提交，原内容与标识已保留。')
      if (resolved.snapshotID) {
        await workspace.selectSnapshot(resolved.snapshotID, false)
        if (!current() || workspace.conversationID.value !== owner || workspace.snapshotID.value !== resolved.snapshotID) return
      }
      applyGuidanceFill(editor, item)
      await router.push({ path: '/conversations', hash: '#planning-message' })
      await nextTick(); document.getElementById('planning-message')?.focus()
    } else {
      if (resolved.snapshotID) {
        await workspace.selectSnapshot(resolved.snapshotID, false)
        if (!current() || workspace.snapshotID.value !== resolved.snapshotID) return
      }
      await router.push(resolved.route)
      await nextTick()
      if (resolved.route.hash) document.getElementById(resolved.route.hash.slice(1))?.scrollIntoView({ block: 'nearest' })
      if (current() && resolved.message) actionError.value = resolved.message
    }
  } catch (cause) { if (current()) actionError.value = errorText(cause) }
  finally { if (!disposed && version === actions) busy.value = '' }
}
watch(refreshKey, () => { actions++; busy.value = ''; void load() }, { immediate: true })
onBeforeUnmount(() => { disposed = true; reads++; actions++ })
</script>

<template>
  <section class="guidance-panel" aria-label="当前资料的下一步引导" :data-catalog-version="catalog?.catalog_version">
    <div class="guidance-title"><strong>下一步</strong><span>按当前保存记录提供；点击不会直接执行云或模型任务。</span><button type="button" :disabled="loading || !!busy" @click="load">重新读取</button></div>
    <p v-if="loading" class="guidance-note" role="status">正在读取已保存上下文的引导。</p>
    <ul v-else-if="catalog?.items.length" class="guidance-items"><li v-for="item in catalog.items" :key="item.id"><button type="button" :disabled="disabled || !!busy || guidanceMissing(item, context).length > 0" @click="follow(item)">{{ busy === item.id ? '核对关联记录…' : item.display_text }}</button><p v-if="guidanceMissing(item, context).length" class="guidance-missing">{{ guidanceMissing(item, context).join('；') }}</p><p v-else-if="item.behavior === 'open_plan_preview' || item.confirmation.required" class="guidance-note">打开后仍需核对{{ item.effects.allow_cloud_reads || item.effects.allow_model_requests ? '本次读取与模型效果，并明确接受计划' : '对象、版本与确认内容' }}。</p><p v-else-if="item.behavior === 'fill_input'" class="guidance-note">只预填输入，核对后再发送。</p></li></ul>
    <p v-else-if="!error" class="guidance-note">当前没有可用的关联引导。可以继续描述目标或查看已保存记录。</p>
    <p v-if="error || actionError" class="guidance-missing" role="status">{{ error || actionError }}</p>
  </section>
</template>

<style scoped>
.guidance-panel{margin:16px 0;padding:14px 16px;border:1px solid #d8e4de;border-radius:6px;background:#f7fbf9;min-width:0}.guidance-title{display:flex;align-items:baseline;gap:12px;flex-wrap:wrap;font-size:12px}.guidance-title>span{font-size:11px;color:#64748b}.guidance-title>button{margin-left:auto;padding:2px 0;border:0;background:none;color:#047857;font-size:11px;text-decoration:underline;text-underline-offset:3px}.guidance-items{display:grid;gap:10px;list-style:none;padding:0;margin:12px 0 0}.guidance-items>li>button{padding:0;border:0;background:none;text-align:left;color:#065f46;font-size:12px;line-height:1.7}.guidance-items>li>button:disabled{color:#64748b;cursor:default}.guidance-note,.guidance-missing{font-size:11px;line-height:1.7;margin:4px 0 0;color:#64748b;overflow-wrap:anywhere}.guidance-missing{color:#92400e}.guidance-panel button:focus-visible{outline:2px solid #047857;outline-offset:4px}
</style>
