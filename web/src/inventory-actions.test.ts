import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { compileScript, parse } from '@vue/compiler-sfc'
import ts from 'typescript'
import * as Vue from 'vue'
import * as agentView from './agent-view.ts'
import * as state from './workspace-state.ts'

test('opening a candidate from a different current snapshot preserves its exact asset, service and source route', async () => {
  const filename = new URL('./pages/InventoryPage.vue', import.meta.url)
  const { descriptor } = parse(await readFile(filename, 'utf8'), { filename: filename.pathname })
  const compiled = compileScript(descriptor, { id: 'inventory-actions-test' })
  const js = ts.transpileModule(compiled.content, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } }).outputText
  const route = Vue.reactive({ params: { id: 'asset-fixed' }, query: { snapshot_id: 'snapshot-fixed', service: 'service-fixed' } })
  const asset = { id: 'asset-fixed', services: [{ id: 'service-fixed' }] }
  const currentSnapshot = Vue.ref<{ id: string; input: { assets: { id: string; services: { id: string }[] }[] } }>({ id: 'snapshot-other', input: { assets: [{ id: 'asset-other', services: [] }] } })
  const snapshotID = Vue.ref('snapshot-other'), replacements: unknown[] = []
  const workspace = { currentSnapshot, snapshotID, unlocked: Vue.ref(true), drafts: new Map(), setSourceGuard: () => {}, api: { assets: async () => ({ items: [{ asset, local_confirmed_sections: [] }] }) }, selectSnapshot: async (id: string) => { snapshotID.value = id; currentSnapshot.value = { id, input: { assets: [asset] } } } }
  const modules: Record<string, unknown> = { vue: { ...Vue, onBeforeUnmount: () => {}, onMounted: () => {} }, 'vue-router': { useRoute: () => route, useRouter: () => ({ replace: async (value: unknown) => { replacements.push(value) } }), onBeforeRouteLeave: () => {}, onBeforeRouteUpdate: () => {} }, '@nuxt/ui/components/Button.vue': {}, '../agent-view': agentView, '../workspace-state': state, '../workspace': { useWorkspace: () => workspace, errorText: (value: unknown) => String(value) } }
  const module = { exports: {} as { default?: { setup: (...args: unknown[]) => unknown } } }
  new Function('require', 'exports', 'module', js)((name: string) => { if (!(name in modules)) throw new Error(`unexpected page dependency ${name}`); return modules[name] }, module.exports, module)
  const scope = Vue.effectScope()
  const view = scope.run(() => module.exports.default!.setup({}, { expose() {} })) as { followFixedRoute: () => Promise<void>; activeAsset: Vue.Ref<string>; focusService: Vue.Ref<string>; routeLoading: Vue.Ref<boolean> }
  try {
    assert.deepEqual(replacements, [])
    await view.followFixedRoute()
    await Vue.nextTick()
    assert.equal(snapshotID.value, 'snapshot-fixed')
    assert.equal(view.activeAsset.value, 'asset-fixed')
    assert.equal(view.focusService.value, 'service-fixed')
    assert.equal(view.routeLoading.value, false)
    assert.deepEqual(replacements, [])
  } finally { scope.stop() }
})
