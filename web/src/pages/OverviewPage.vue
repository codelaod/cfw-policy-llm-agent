<script setup lang="ts">
import { ref } from 'vue'
import { useRouter } from 'vue-router'
import UButton from '@nuxt/ui/components/Button.vue'
import CloudCollection from '../CloudCollection.vue'
import { errorText, formatTime, jobLabel, scopeLabel, sourceLabel, useWorkspace } from '../workspace'

const workspace = useWorkspace(), router = useRouter()
const busy = ref(''), message = ref(''), error = ref(''), selectedFile = ref<File | null>(null)
const examples = [{ id: 'internet-in', title: '互联网入向', detail: 'Web、管理服务与数据库，保留服务级边界。' }, { id: 'internet-out', title: '有限互联网出向', detail: '比较明确的外部依赖与必要外联。' }, { id: 'nat-out', title: 'NAT 出向', detail: '包含一个网关及人工设定的关联证据。' }]
async function loadExample(name: string) {
  busy.value = name; error.value = ''; message.value = ''
  try { await workspace.addExample(name); message.value = '人工资料已保存。可以先确认业务要求，再预览审计计划。' }
  catch (cause) { error.value = errorText(cause) }
  finally { busy.value = '' }
}
async function importFile() {
  const file = selectedFile.value
  if (!file) return
  busy.value = 'import'; error.value = ''; message.value = ''
  try {
    if (file.size > 5 * 1024 * 1024) throw new Error('文件超过 5 MiB，请缩小输入后重试。')
    let data: unknown
    try { data = JSON.parse(await file.text()) } catch { throw new Error('文件不是有效 JSON，请使用规范化的离线输入。') }
    await workspace.importInput(data)
    message.value = '导入已保存；来源与完整性声明仍未经真实云观测核实。'
  } catch (cause) { error.value = errorText(cause) }
  finally { busy.value = '' }
}
async function useSnapshot(id: string) { try { await workspace.selectSnapshot(id); await router.push('/inventory') } catch (cause) { error.value = errorText(cause) } }
</script>

<template>
  <div class="workspace-page">
    <header class="page-title"><div><h1>数据与任务</h1><p>选定固定资料和范围，再确认业务要求、审计与审阅。</p></div><UButton color="neutral" variant="outline" :disabled="!workspace.snapshots.value.length" @click="router.push('/audits')">预览审计计划</UButton></header>
    <section class="preparation-layout" aria-labelledby="prepare-title"><div class="sample-section"><div class="section-title"><h2 id="prepare-title">先用人工案例走通流程</h2><span class="tag tag-neutral">不访问云或模型</span></div><p class="section-description">三个构造案例使用同一类资产，分别说明不同边界的判据；不是正式评价数据集。</p><div class="sample-options"><article v-for="example in examples" :key="example.id"><h3>{{ example.title }}</h3><p>{{ example.detail }}</p><UButton color="neutral" variant="outline" size="sm" :disabled="!!busy" @click="loadExample(example.id)">{{ busy === example.id ? '载入中…' : '载入人工资料' }}</UButton></article></div></div><div class="import-section"><h2>导入自己的离线资料</h2><p class="section-description">有界 JSON，最大 5 MiB。文件自称真实或完整不会获得只读观测保证。</p><label for="snapshot-file">规范化输入文件</label><input id="snapshot-file" type="file" accept=".json,application/json" :disabled="!!busy" @change="selectedFile = ($event.target as HTMLInputElement).files?.[0] ?? null" /><UButton color="neutral" variant="outline" :disabled="!!busy || !selectedFile" @click="importFile">{{ busy === 'import' ? '校验中…' : '校验并导入' }}</UButton><p class="field-hint">不填写云或模型密钥；真实只读连接由服务端单独配置。</p></div></section>
    <p v-if="message" class="notice notice-success" role="status">{{ message }} <RouterLink to="/inventory">确认业务资料</RouterLink></p><p v-if="error" class="notice notice-error" role="alert">{{ error }}</p>
    <CloudCollection />
    <section class="workspace-section" aria-labelledby="snapshots-title"><div class="section-title"><h2 id="snapshots-title">已保存的资料</h2><span class="muted">构造／采集时间与保存时间分别保留</span></div><div v-if="workspace.snapshots.value.length" class="table-scroll"><table class="workspace-table"><thead><tr><th>来源与范围</th><th>构造／采集时点</th><th>枚举完整性</th><th>对象</th><th><span class="sr-only">操作</span></th></tr></thead><tbody><tr v-for="snapshot in workspace.snapshots.value" :key="snapshot.id" :class="{ 'row-selected': snapshot.id === workspace.snapshotID.value }"><td><strong>{{ scopeLabel(snapshot.input.scope.boundary, snapshot.input.scope.direction) }}</strong><span class="cell-secondary">{{ sourceLabel(snapshot.source_kind) }}</span></td><td>{{ formatTime(snapshot.input.collected_at) }}<span class="cell-secondary">保存于 {{ formatTime(snapshot.created_at) }}</span></td><td><span :class="['tag', snapshot.input.completeness.enumeration_complete ? 'tag-neutral' : 'tag-warning']">{{ snapshot.input.completeness.enumeration_complete ? '声明完整' : '存在缺口' }}</span><span class="cell-secondary">{{ snapshot.source_kind === 'live_readonly' ? '按观测记录' : '声明未经现场核实' }}</span></td><td>{{ snapshot.input.assets.length }} 个已载入资产<span class="cell-secondary">{{ snapshot.input.policies.length }} 条已载入策略</span><span v-if="!snapshot.input.completeness.declared || !snapshot.input.completeness.enumeration_complete" class="cell-secondary">总量待核对</span></td><td><UButton color="neutral" variant="ghost" size="sm" @click="useSnapshot(snapshot.id)">选择并查看</UButton></td></tr></tbody></table><UButton v-if="workspace.listHasMore.value.snapshots" color="neutral" variant="outline" :disabled="workspace.listsLoading.value" @click="workspace.loadMore('snapshots')">读取更多资料</UButton></div><div v-else class="workspace-empty"><h3>还没有可审计资料</h3><p>载入一个人工案例，或导入规范化输入。没有运行时，不显示风险为零。</p></div></section>
    <section class="workspace-section" aria-labelledby="recent-title"><div class="section-title"><h2 id="recent-title">最近任务</h2><RouterLink to="/audits">查看审计过程</RouterLink></div><div v-if="workspace.jobs.value.length" class="table-scroll"><table class="workspace-table"><thead><tr><th>发起时间</th><th>会话</th><th>任务</th><th>状态</th><th><span class="sr-only">查看</span></th></tr></thead><tbody><tr v-for="job in workspace.jobs.value.slice(0, 12)" :key="job.id"><td>{{ formatTime(job.created_at) }}</td><td>{{ workspace.conversations.value.find(c => c.id === job.conversation_id)?.title ?? '已有会话' }}</td><td>{{ job.kind === 'assistant_turn' ? '助手轮次' : job.kind === 'collect' ? '只读采集' : job.kind === 'connection_check' ? '调用身份核对' : '审计任务' }}</td><td><span :class="['tag', job.status === 'failed' || job.status === 'partial' ? 'tag-warning' : 'tag-neutral']">{{ jobLabel(job.status) }}</span></td><td><RouterLink :to="`/audits/${job.id}`">查看任务</RouterLink></td></tr></tbody></table></div><div v-else class="workspace-empty compact"><h3>尚未发起审计</h3><p>资料就绪后，在审计页预览固定范围与效果，再明确启动。</p></div></section>
  </div>
</template>
