import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { compileScript, parse } from '@vue/compiler-sfc'
import ts from 'typescript'
import * as Vue from 'vue'
import { renderToString } from 'vue/server-renderer'
import * as groupView from './group-view.ts'
import * as conversationView from './conversation-view.ts'
import { Mutation } from './workspace-state.ts'
import { groupIntentFixture, groupPreviewFixture, groupResultFixture } from './test-fixtures/group.ts'
import type { GroupEditor } from './group-view.ts'

async function render(kind:'empty'|'preview'|'missing',malicious=false) {
 const filename=new URL('./ConversationGroupPanel.vue',import.meta.url),{descriptor}=parse(await readFile(filename,'utf8'),{filename:filename.pathname})
 const compiled=compileScript(descriptor,{id:'group-component',inlineTemplate:true}),js=ts.transpileModule(compiled.content,{compilerOptions:{module:ts.ModuleKind.CommonJS,target:ts.ScriptTarget.ES2022}}).outputText
 const preview=groupPreviewFixture('conversation-one',20),intent=groupIntentFixture('conversation-one',20)
 if(kind==='empty')intent.selection=[]
 if(malicious){preview.members[0]!.name='<img src=x onerror=external()>';preview.authorization_source='<script>external()</script>'}
 const state=Vue.reactive({name:'客户甲',authority:preview.authorization_source,edited:false,approved:false,preview:kind==='preview'?preview:null,result:kind==='missing'?groupResultFixture('missing'):null,resultSequence:2,loading:false,busy:false,error:'',message:'',reads:0,mutation:Vue.shallowReactive(new Mutation())}) as GroupEditor
 const drafts=new Map<string,unknown>([['conversation-group-editors',new Map([['conversation-one',state]])]])
 const workspace={unlocked:Vue.ref(true),conversationID:Vue.ref('conversation-one'),currentIntent:Vue.ref(intent),messages:Vue.ref([]),groupResults:Vue.ref({}),drafts,captureContext:()=>()=>true,api:{resourceGroupPreview:async()=>kind==='preview'?preview:null}}
 const modules:Record<string,unknown>={vue:Vue,'./group-view':groupView,'./conversation-view':conversationView,'./workspace':{useWorkspace:()=>workspace,errorText:String,formatTime:(value:string)=>value}}
 const module={exports:{} as {default?:Vue.Component}}
 new Function('require','exports','module',js)((name:string)=>{if(!(name in modules))throw new Error(`unexpected group component dependency ${name}`);return modules[name]},module.exports,module)
 return renderToString(Vue.createSSRApp(module.exports.default!,{conversationId:'conversation-one'}))
}
test('group card shows all twenty fixed members, source/account, authorization and base version before any explicit confirmation',async()=>{
 const html=await render('preview')
 for(const label of ['核对 20 个明确成员','source-one / account-one','工单 AUTH-42','归属基版本','尚无已确认版本','确认本预览的归属'])assert.ok(html.includes(label),label)
 for(let i=0;i<20;i++)assert.ok(html.includes('i-group-'+i))
 assert.ok(!/<input[^>]+checked/.test(html));assert.ok(!html.includes('已保存版本'))
})
test('group card with no explicit selection guides object resolution without accepting arbitrary member inputs',async()=>{
 const html=await render('empty')
 assert.ok(html.includes('先在本会话解析并明确选择对象'));assert.ok(!html.includes('预览所选对象的归属'));assert.ok(!html.includes('确认本预览的归属'));assert.ok(!html.includes('resource_refs'))
})
test('unknown authority remains a visible missing condition and external member/source text stays escaped data',async()=>{
 const missing=await render('missing');assert.ok(missing.includes('授权来源尚未明确'));assert.ok(!missing.includes('确认本预览的归属'));assert.ok(!missing.includes('已保存版本'))
 const html=await render('preview',true);assert.ok(html.includes('&lt;img'));assert.ok(html.includes('&lt;script&gt;'));assert.ok(!html.includes('<script>external'))
})
