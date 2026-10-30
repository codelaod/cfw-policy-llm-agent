import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { compileScript, parse } from '@vue/compiler-sfc'
import ts from 'typescript'
import * as Vue from 'vue'
import { renderToString } from 'vue/server-renderer'
import * as batchView from './batch-view.ts'
import { batchFixture } from './test-fixtures/batch.ts'

async function render(unknown=false){
 const filename=new URL('./BatchResults.vue',import.meta.url),{descriptor}=parse(await readFile(filename,'utf8'),{filename:filename.pathname})
 const compiled=compileScript(descriptor,{id:'batch-component',inlineTemplate:true}),js=ts.transpileModule(compiled.content,{compilerOptions:{module:ts.ModuleKind.CommonJS,target:ts.ScriptTarget.ES2022}}).outputText
 const record=batchFixture();record.usage.usage_unknown=unknown;if(unknown)record.usage.unknown_reasons=['LOGICAL_USAGE_NOT_SAVED'];record.segments[0]!.input_manifest!.profile_version='<img src=x onerror=external()>'
 const modules:Record<string,unknown>={vue:Vue,'vue-router':{useRouter:()=>({push:()=>{throw Error('render must not navigate')}})},'./batch-view':batchView,'./workspace':{useWorkspace:()=>({api:{}}),errorText:String,formatTime:(value:string)=>value,scopeLabel:()=> '互联网入向',sourceLabel:()=> '导入资料声明'}}
 const module={exports:{} as {default?:Vue.Component}};new Function('require','exports','module',js)((name:string)=>{if(!(name in modules))throw Error(`unexpected batch dependency ${name}`);return modules[name]},module.exports,module)
 return renderToString(Vue.createSSRApp(module.exports.default!,{jobId:'parent-one',conversationId:'conversation-one',record}))
}
test('batch card displays the complete set and each evidence manifest without treating coverage as a pass',async()=>{
 const html=await render();for(const label of ['5 个明确对象，共 3 段','这里没有代表全组的合并审计','不等于规则通过','第 1 段','第 2 段','第 3 段','audit-3','report-3','复审请明确选择原段'])assert.ok(html.includes(label),label)
 for(let i=1;i<=5;i++)assert.ok(html.includes('i-public-'+i));assert.ok(html.includes('&lt;img'));assert.ok(!html.includes('<img src=x'))
})
test('unknown logical usage is visible beside recorded physical and logical counts',async()=>{
 const html=await render(true);for(const label of ['物理 HTTP 尝试','已保存逻辑调用','部分逻辑调用或阶段用时未知','已记录的零不能解释为全部实际用量为零','LOGICAL_USAGE_NOT_SAVED'])assert.ok(html.includes(label),label)
})
