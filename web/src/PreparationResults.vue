<script setup lang="ts">
import { computed, ref } from 'vue'
import { useRouter } from 'vue-router'
import { createConversationController } from './conversation-state'
import { prefillConversationEditor } from './guidance'
import { preparationEmptyText, preparationFor, preparationGoal, preparationTitle } from './preparation-view'
import { catalogueSourceLabel } from './conversation-view'
import { errorText, formatTime, useWorkspace } from './workspace'
import type { PreparationDiscovery, ResourceCandidate } from './workspace-types'

const props = defineProps<{ jobId: string; conversationId: string; record?: PreparationDiscovery | null }>()
const workspace = useWorkspace(), router = useRouter(), error = ref(''), shown = ref(20)
const current = computed(() => preparationFor(props.record, props.jobId))
const sameConversation = computed(() => props.conversationId === workspace.conversationID.value)
async function prepare(resource: ResourceCandidate) {
  error.value = ''
  if (!sameConversation.value || !current.value?.resources.some(item => item.id === resource.id)) { error.value = '先在任务所属会话核对原记录，未改动其他会话目标。'; return }
  try {
    const controller = (workspace.drafts.get('conversation-controller') as ReturnType<typeof createConversationController> | undefined) ?? createConversationController(workspace, errorText)
    workspace.drafts.set('conversation-controller', controller)
    prefillConversationEditor(controller.editor.value, preparationGoal(resource))
    await router.push({ path: '/conversations', hash: '#planning-message' })
  } catch (cause) { error.value = errorText(cause) }
}
</script>

<template>
  <section v-if="current" class="workspace-section preparation-results" aria-label="本任务保存的资料准备记录">
    <h3>{{ preparationTitle(current) }}</h3><p class="field-hint">保存于 {{ formatTime(current.created_at) }}。这是目录准备记录，不能替代封存审计输入或规则结果；旧计划与已接受范围保持原版本。</p>
    <p v-if="current.code" class="preparation-code">准备状态：{{ current.code }}</p>
    <div v-for="(coverage, index) in current.coverages" :key="index" class="preparation-coverage"><strong>{{ coverage.selector.region || coverage.selector.regions?.join('、') || '地域待核对' }} / {{ coverage.selector.resource_type === 'ecs' ? 'ECS' : coverage.selector.resource_type === 'nat_gateway' ? 'NAT 网关' : '类型待核对' }}</strong><p>{{ catalogueSourceLabel(coverage.source) }}；{{ formatTime(coverage.observed_at) }}；{{ coverage.complete ? '此有限查询已穷尽' : '此查询未知或未穷尽' }}</p><p v-if="coverage.selector.name">查询名称：{{ coverage.selector.name }}</p><ul v-if="coverage.limitations.length"><li v-for="code in coverage.limitations" :key="code">{{ code }}</li></ul></div>
    <p v-if="!current.coverages.length" class="field-hint">尚无可核对的目录覆盖记录，不能宣称完整。</p>
    <p v-if="!current.resources.length" class="notice notice-warning">{{ preparationEmptyText(current) }}</p>
    <template v-else><p class="field-hint">已保存 {{ current.resources.length }} 个候选。使用明确 ID 只预填新目标，仍需发送、核对新计划并接受；不会重启旧任务。</p><article v-for="resource in current.resources.slice(0, shown)" :key="resource.id" class="preparation-resource"><strong>{{ resource.name || '未命名对象' }}</strong><p>{{ resource.region }} / {{ resource.resource_type === 'ecs' ? 'ECS' : 'NAT 网关' }} / {{ resource.resource_id }}</p><p>{{ catalogueSourceLabel(resource.source) }}；{{ formatTime(resource.observed_at) }}</p><p v-if="current.unverified_resource_refs.includes(resource.id)" class="notice notice-warning">名称或标签筛选依据未核实；仅将已见 ID 作为待核对线索。</p><button type="button" class="text-action" :disabled="!sameConversation" @click="prepare(resource)">用此 ID 准备新目标</button></article><button v-if="shown < current.resources.length" type="button" class="text-action" @click="shown += 20">继续显示已保存候选</button><p v-if="!sameConversation" class="field-hint">先在任务所属会话核对上下文，才可预填新目标。</p></template>
    <p class="field-hint">受控只读路径标记不代表真实账号权限、字段和完整性已验收。名称与标签仅作数据线索。</p><p v-if="error" class="error-text" role="status">{{ error }}</p>
  </section>
</template>

<style scoped>
.preparation-results{padding:16px;margin:16px 0}.preparation-code{font-size:11px;color:#92400e;overflow-wrap:anywhere}.preparation-coverage,.preparation-resource{padding:12px 0;border-top:1px solid #e2e8f0;font-size:12px;overflow-wrap:anywhere}.preparation-coverage p,.preparation-resource p{font-size:11px;color:#64748b;margin:4px 0}.preparation-coverage ul{padding-left:18px;font-size:11px;color:#92400e}.preparation-resource button{margin-top:7px}.preparation-resource button:disabled{color:#64748b}
</style>
