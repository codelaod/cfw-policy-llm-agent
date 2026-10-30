<script setup lang="ts">
import { computed } from 'vue'
import UButton from '@nuxt/ui/components/Button.vue'
import { cloudLimitationLabels, cloudOperationLabels, cloudRecordFor, cloudSnapshotID } from './cloud-view'
import { countText } from './agent-view'
import { formatTime } from './workspace'
import type { AttemptUsage, CloudRecord } from './workspace-types'

const props = withDefaults(defineProps<{ jobId: string; record?: CloudRecord | null; snapshotId?: string; usage?: AttemptUsage | null; showSnapshotActions?: boolean }>(), { record: null, snapshotId: '', showSnapshotActions: true })
const emit = defineEmits<{ openSnapshot: [id: string, destination: 'inventory' | 'audits'] }>()
const current = computed(() => cloudRecordFor(props.record, props.jobId))
const snapshot = computed(() => cloudSnapshotID(current.value, props.jobId, props.snapshotId))
const hasPartialObservations = computed(() => !!current.value && !current.value.content.product_ready && current.value.content.observations.length > 0)
function open(destination: 'inventory' | 'audits') { if (snapshot.value) emit('openSnapshot', snapshot.value, destination) }
</script>

<template>
  <section class="workspace-section cloud-results" aria-labelledby="cloud-results-title">
    <div class="section-title"><div><h2 id="cloud-results-title">本轮云读取结果</h2><p class="section-description">结果仅描述本任务显式范围及观测时点，身份核对与产品采集分别记录。</p></div><span class="tag tag-neutral">{{ current?.content.kind === 'connection_check' ? '仅核对调用身份' : current?.content.product_ready ? '已保存产品观测' : hasPartialObservations ? '已保存部分读取记录' : '尚无已保存产品观测' }}</span></div>
    <template v-if="current"><dl class="fact-grid"><div><dt>调用身份</dt><dd>{{ current.content.identity_verified ? '本次身份已核对' : '本次身份尚未核对' }}</dd></div><div><dt>固定范围</dt><dd>{{ current.content.policy_scope_ref || '分区尚未封存，请按原接受计划核对范围' }}</dd></div><div><dt>产品资料</dt><dd>{{ current.content.kind === 'connection_check' ? '本轮未执行产品采集' : current.content.product_ready ? '按下方观测与局限解释' : hasPartialObservations ? '已保存部分读取记录，尚未形成可审计的完整资料' : '尚未取得可用产品资料' }}</dd></div><div><dt>结果保存时点</dt><dd>{{ formatTime(current.created_at) }}</dd></div><div v-if="current.content.snapshot_id"><dt>本轮固定资料</dt><dd>{{ current.content.snapshot_id }}</dd></div></dl>
      <section v-if="current.content.observations.length" class="cloud-observations"><h3>已保存观测记录</h3><p class="field-hint">两轮一致只表示这些观测未见变化，不证明原子快照或完整路径可达。</p><div class="table-scroll"><table class="workspace-table compact-table"><thead><tr><th>轮次／读取内容</th><th>分页与完整性</th><th>观测时段</th><th>语义摘要</th></tr></thead><tbody><tr v-for="(row, index) in current.content.observations" :key="index"><td>第 {{ countText(row.round) }} 轮<span class="cell-secondary">{{ cloudOperationLabels[row.operation] ?? row.operation }}</span></td><td>{{ countText(row.pages) }} 页<span class="cell-secondary">{{ row.enumeration_complete ? '本项枚举完成' : '本项未穷尽' }}</span></td><td>{{ formatTime(row.started_at) }}<span class="cell-secondary">至 {{ formatTime(row.finished_at) }}</span></td><td><code class="cloud-semantic-hash">{{ row.semantic_sha256 }}</code></td></tr></tbody></table></div></section>
      <section v-if="current.content.limitations.length" class="cloud-limitations"><h3>本轮局限与待核实</h3><ul class="notice-list"><li v-for="code in current.content.limitations" :key="code">{{ cloudLimitationLabels[code] ?? code }}</li></ul></section>
      <p v-if="current.content.kind === 'connection_check'" class="notice notice-warning">本轮身份核对不代表产品权限、字段或完整性已经验证。需要另行明确预览并接受采集计划。</p>
      <template v-if="snapshot && showSnapshotActions"><p class="field-hint">新资料已独立保存。选择后仍需人工核对业务资料或预览审计计划，不自动确认或启动审计。</p><div class="cloud-result-actions"><UButton color="neutral" variant="outline" @click="open('inventory')">选择这份资料并确认业务</UButton><UButton color="neutral" variant="outline" @click="open('audits')">选择这份资料并预览审计</UButton></div></template>
    </template>
    <p v-else class="empty-inline">尚无与本任务绑定的持久云读取结果。未取得结果不表示身份、产品权限或完整性已验证。</p>
    <dl class="fact-grid cloud-usage"><div><dt>云／凭据 HTTP 实际尝试</dt><dd>{{ countText(usage?.cloud_attempts) }}</dd></div><div><dt>模型 HTTP 实际尝试</dt><dd>{{ countText(usage?.model_attempts) }}</dd></div><div><dt>送达结果未知的尝试</dt><dd>{{ countText(usage?.unknown_delivery) }}</dd></div></dl><p class="field-hint">次数来自本任务物理请求账本，包含受控失败；不据此估算费用。重开结果只恢复观察。</p>
  </section>
</template>
