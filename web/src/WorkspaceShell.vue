<script setup lang="ts">
import { computed, onBeforeUnmount, provide, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import UButton from '@nuxt/ui/components/Button.vue'
import { createWorkspace, errorText, formatTime, scopeLabel, sourceLabel, workspaceKey } from './workspace'
import './conversation.css'

const workspace = createWorkspace()
provide(workspaceKey, workspace)
const route = useRoute(), candidate = ref(''), accessError = ref(''), menu = ref(false)
const nav = [{ path: '/conversations', title: '审计对话' }, { path: '/overview', title: '数据与任务' }, { path: '/inventory', title: '资产与业务确认' }, { path: '/audits', title: '审计过程' }, { path: '/findings', title: '发现与审阅' }, { path: '/reports', title: '报告与复审' }]
const activeNav = computed(() => nav.find(item => route.path.startsWith(item.path)) ?? nav[0]!)
const isConversation = computed(() => route.path.startsWith('/conversations'))
const scope = computed(() => workspace.currentSnapshot.value?.input.scope)
async function unlock() { accessError.value = ''; try { await workspace.unlock(candidate.value); candidate.value = '' } catch (error) { accessError.value = errorText(error) } }
async function selectSnapshot(event: Event) { const select = event.target as HTMLSelectElement; try { await workspace.selectSnapshot(select.value) } catch (error) { workspace.error.value = errorText(error) } finally { select.value = workspace.snapshotID.value } }
watch(() => route.path, () => { menu.value = false })
onBeforeUnmount(() => workspace.lock())
</script>

<template>
  <a class="workspace-skip" href="#workspace-main">跳到主要内容</a>
  <div :class="['workspace-shell', { 'is-conversation-workspace': isConversation }]">
    <aside class="workspace-sidebar" :class="{ 'is-open': menu }"><button v-if="menu" type="button" class="mobile-nav-close" aria-label="关闭导航" @click="menu = false">关闭</button><RouterLink to="/conversations" class="workspace-brand" aria-label="云防火墙审计对话首页"><svg viewBox="0 0 28 32" aria-hidden="true"><path d="M14 2 25 6v10c0 6-5 10-11 14C8 26 3 22 3 16V6Z" fill="none" stroke="currentColor" stroke-width="2"/><path d="M9 12h10M9 17h10" stroke="currentColor" stroke-width="2"/></svg><span>云防火墙策略审计<small>固定证据，人工决策</small></span></RouterLink><nav aria-label="审计工作区"><RouterLink v-for="(item, index) in nav" :key="item.path" :to="item.path" :aria-current="route.path.startsWith(item.path) ? 'page' : undefined" :class="{ active: route.path.startsWith(item.path), 'conversation-nav-primary': index === 0 }" @click="menu = false"><span>{{ item.title }}</span><span v-if="item.path === '/audits' && workspace.activeJobs.value.length" class="nav-count">{{ workspace.activeJobs.value.length }}</span></RouterLink></nav><p class="sidebar-support-label">资料、过程与报告辅助同一轮对话</p><div class="sidebar-bottom"><span class="status-dot" /><span>云端只读 · 人工应用策略</span></div></aside>
    <div class="workspace-body"><header class="workspace-toolbar"><button class="mobile-menu" type="button" :aria-expanded="menu" aria-label="打开导航" @click="menu = !menu"><svg viewBox="0 0 20 20" width="20" height="20" aria-hidden="true"><path d="M3 5h14M3 10h14M3 15h14" stroke="currentColor" stroke-width="1.5"/></svg></button><span class="toolbar-section">{{ activeNav.title }}</span><div class="toolbar-actions"><RouterLink v-if="workspace.unlocked.value && !isConversation" to="/conversations">回到对话与历史</RouterLink><UButton v-if="workspace.unlocked.value" color="neutral" variant="ghost" size="sm" @click="workspace.lock()">锁定</UButton><span v-else class="muted">本机工作区未解锁</span></div></header>
      <main id="workspace-main" tabindex="-1" class="workspace-main"><section v-if="!workspace.unlocked.value" class="workspace-unlock" aria-labelledby="unlock-title"><div class="unlock-rule" /><h1 id="unlock-title">打开审计工作区</h1><p>输入本机服务的应用访问口令。目标、资料与历史保存在本机服务，刷新后需要重新解锁。</p><form @submit.prevent="unlock"><label for="workspace-token">应用访问口令</label><div class="input-action"><input id="workspace-token" v-model="candidate" type="password" autocomplete="off" required :disabled="workspace.loading.value" /><UButton type="submit" :disabled="workspace.loading.value || !candidate">{{ workspace.loading.value ? '验证中…' : '解锁工作区' }}</UButton></div></form><p v-if="accessError || workspace.error.value" role="alert" class="error-text">{{ accessError || workspace.error.value }}</p></section><template v-else><section v-if="!isConversation" class="source-strip" aria-label="辅助视图所选固定资料"><div class="source-select"><label for="active-snapshot">所选资料</label><select id="active-snapshot" :value="workspace.snapshotID.value" @change="selectSnapshot"><option value="">选择固定资料</option><option v-for="item in workspace.snapshots.value" :key="item.id" :value="item.id">{{ sourceLabel(item.source_kind) }} / {{ scopeLabel(item.input.scope.boundary, item.input.scope.direction) }} / {{ formatTime(item.input.collected_at) }}</option></select></div><div class="source-meta"><span class="source-kind">{{ sourceLabel(workspace.currentSnapshot.value?.source_kind) }}</span><span>{{ scopeLabel(scope?.boundary, scope?.direction) }}</span><span v-if="workspace.currentSnapshot.value">构造／采集于 {{ formatTime(workspace.currentSnapshot.value.input.collected_at) }}</span><span v-else>无资料也可先在对话表达目标</span></div></section><p v-if="workspace.error.value" class="notice notice-error" role="alert">{{ workspace.error.value }} <button type="button" class="text-action" @click="workspace.refresh">重新读取</button></p><RouterView /></template></main>
    </div>
  </div>
</template>
