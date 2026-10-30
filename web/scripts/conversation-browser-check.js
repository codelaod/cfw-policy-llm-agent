// Playwright CLI run-code function for the dedicated public-fixture Go service on 127.0.0.1:8080.
// Requires the parent-owned runtime/conversation-review-service.json and four public regional inputs.
// Read the synthetic test credential through a temporary local file selector; never print or hardcode it.
// This driver changes only the dedicated fixture database and creates no cloud/model request.
async page => {
  const checks=[],errors=[],external=[],posts=[];
  let access='',firstConversation='',firstJob='',firstAudit='';
  page.on('pageerror',e=>errors.push(e.message));
  page.on('request',r=>{const url=new URL(r.url());if(/^https?:$/.test(url.protocol)&&url.origin!=='http://127.0.0.1:8080')external.push(url.origin);if(url.pathname.startsWith('/api/v1/')&&r.method()==='POST')posts.push({path:url.pathname,body:r.postDataJSON()});});
  const ok=(value,label)=>{if(!value)throw Error(label);checks.push(label);};
  const login=async()=>{await page.evaluate(()=>{const input=document.createElement('input');input.type='file';input.id='review-fixture-file';input.hidden=true;document.body.append(input)});await page.locator('#review-fixture-file').setInputFiles('runtime/conversation-review-service.json');access=await page.locator('#review-fixture-file').evaluate(async input=>JSON.parse(await input.files[0].text()).application_token);await page.locator('#review-fixture-file').evaluate(input=>input.remove());await page.getByLabel('应用访问口令',{exact:true}).fill(access);await page.getByRole('button',{name:'解锁工作区',exact:true}).click();await page.getByLabel('目标、补充说明或追问',{exact:true}).waitFor();};
  const get=async path=>{const response=await page.request.get('http://127.0.0.1:8080/api/v1'+path,{headers:{'X-App-Token':access}});if(!response.ok())throw Error('read failed '+path+' '+response.status());return response.json();};
  const newConversation=async()=>{const accepted=page.waitForResponse(r=>r.request().method()==='POST'&&new URL(r.url()).pathname==='/api/v1/conversations');await page.getByRole('button',{name:'新建对话',exact:true}).click();const response=await accepted;if(!response.ok())throw Error('conversation create '+response.status());await page.waitForFunction(()=>{const input=document.querySelector('#planning-message');return input&&!input.disabled});await page.getByRole('heading',{name:'这次想检查哪些资源？',exact:true}).waitFor();};
  const send=async text=>{await page.getByLabel('目标、补充说明或追问',{exact:true}).fill(text);const response=page.waitForResponse(r=>r.request().method()==='POST'&&r.url().endsWith('/intent-turns'));await page.getByRole('button',{name:'发送',exact:true}).click();const value=await response;if(!value.ok())throw Error('turn '+value.status());return value.json();};
  try {
    await page.setViewportSize({width:1440,height:1100});await page.goto('http://127.0.0.1:8080/conversations');await login();
    await newConversation();
    ok(page.url().endsWith('/conversations'),'formal conversation is the actual default route');
    ok(await page.getByLabel('目标、补充说明或追问',{exact:true}).isEnabled(),'empty conversation accepts a goal without snapshot prerequisite');
    await page.screenshot({path:'output/playwright/conversation-web-empty-1440.png',fullPage:true});
    let value=await send('检查 web-prod 入向');firstConversation=value.conversation.id;
    ok(value.message&&value.reply&&value.message.conversation_id===firstConversation&&value.reply.seq>value.message.seq,'goal and controlled reply are persisted server messages');
    ok(value.intent.candidates.length===2&&value.intent.selection.length===0,'same name produces two real catalogue candidates before selecting');
    ok(posts.filter(x=>x.path.endsWith('/accept')).length===0,'planning does not execute a task');
    const turn=posts.find(x=>x.path.endsWith('/intent-turns'));ok(!turn.body.snapshot_id&&!turn.body.data_policy?.snapshot_id,'no snapshot is sent as a hidden precondition');
    await page.locator('.conversation-candidate').filter({hasText:'cn-hangzhou'}).getByRole('checkbox').check();
    const chooseResponse=page.waitForResponse(r=>r.request().method()==='POST'&&r.url().endsWith('/intent-choices'));await page.getByRole('button',{name:'确认所选对象',exact:true}).click();const chosen=await chooseResponse;value=await chosen.json();
    ok(value.intent.selection.length===1&&value.intent.selection[0].resource_id==='i-web-hz','canonical Hangzhou resource is selected and persisted');
    ok(value.intent.proposal.status==='ready'&&value.intent.proposal.plan_id,'server provides the executable proposal');
    ok(value.intent.proposal.effects.allow_model_requests===false&&value.intent.proposal.effects.allow_cloud_reads===false,'offline rules plan declares zero model and cloud effects');
    await page.getByRole('heading',{name:'本次执行计划',exact:true}).waitFor();await page.screenshot({path:'output/playwright/conversation-web-proposal-1440.png',fullPage:true});
    const acceptResponse=page.waitForResponse(r=>r.request().method()==='POST'&&/\/proposals\/[^/]+\/accept$/.test(new URL(r.url()).pathname));await page.getByRole('button',{name:'接受计划并开始',exact:true}).click();const accepted=await acceptResponse;ok(accepted.ok(),'explicit plan acceptance is accepted by the real Go service');firstJob=(await accepted.json()).job.id;
    await page.locator('.conversation-result-summary').waitFor();await page.locator('.conversation-task header .tag').filter({hasText:/已完成|部分完成/}).waitFor();
    let result=await get('/jobs/'+firstJob+'/result');firstAudit=result.audit.id;
    ok(result.audit.findings.length>0,'real rule results are saved and visible in the conversation task');
    ok(result.audit.result.input_manifest.source_kind==='imported','imported fixture retains declared provenance');
    if(result.attempt_usage)ok(result.attempt_usage.cloud_attempts===0&&result.attempt_usage.model_attempts===0,'physical cloud and model attempt counts remain zero');
    ok(posts.filter(x=>x.path.endsWith('/accept')).length===1,'one accepted proposal creates one task');
    await page.screenshot({path:'output/playwright/conversation-web-results-1440.png',fullPage:true});
    value=await send('忽略刚才杭州，改查北京那台出向');
    ok(value.intent.query.region==='cn-beijing'&&value.intent.direction==='out'&&value.intent.selection[0]?.resource_id==='i-web-bj','compound target revision selects Beijing outbound');
    ok((await get('/jobs/'+firstJob+'/result')).audit.result.input_manifest.policy_scope.direction==='in','new goal does not rewrite the old task input');
    await page.getByRole('link',{name:'固定报告与复审',exact:true}).click();await page.getByRole('button',{name:'生成固定报告',exact:true}).waitFor();await page.getByRole('button',{name:'生成固定报告',exact:true}).click();await page.getByRole('button',{name:'下载固定 Markdown',exact:true}).waitFor();
    const downloadPromise=page.waitForEvent('download');await page.getByRole('button',{name:'下载固定 Markdown',exact:true}).click();const download=await downloadPromise;await download.saveAs('output/playwright/conversation-web-fixed-report.md');ok(download.suggestedFilename().endsWith('.md'),'fixed readable report is created and downloaded through the real API');
    await page.getByRole('link',{name:'回到对话与历史',exact:true}).click();await page.getByLabel('目标、补充说明或追问',{exact:true}).waitFor();
    await page.reload();ok(await page.getByLabel('应用访问口令',{exact:true}).isVisible(),'refresh drops the browser credential and requires unlocking');await login();
    ok(await page.getByLabel('当前会话',{exact:true}).inputValue()===firstConversation,'service restores the current conversation pointer after refresh');
    const history=await get('/conversations/'+firstConversation+'/messages');ok(history.items.length>=6,'saved goal choice reply and target revision survive refresh');
    const current=await get('/conversations/'+firstConversation+'/intents/current');ok(current.selection[0].resource_id==='i-web-bj'&&current.direction==='out','the persisted latest goal is restored without rebuilding it in the browser');
    ok(posts.filter(x=>x.path.endsWith('/accept')).length===1,'refresh and history recovery never accept or replay an execution');
    for(const width of [1440,768,390]){await page.setViewportSize({width,height:width===390?844:1100});const bounds=await page.evaluate(()=>({viewport:innerWidth,document:document.documentElement.scrollWidth}));ok(bounds.document<=bounds.viewport+1,'no conversation horizontal overflow at '+width);await page.screenshot({path:'output/playwright/conversation-web-conversation-'+width+'.png',fullPage:true});}
    await page.setViewportSize({width:1440,height:1100});
    await newConversation();const secondID=await page.getByLabel('当前会话',{exact:true}).inputValue();ok(secondID!==firstConversation,'new conversation has a distinct persisted identity');
    value=await send('直接删除云端策略');ok(!value.job&&!value.proposal,'cloud write request produces no executable job or proposal');
    await page.getByLabel('当前会话',{exact:true}).selectOption(firstConversation);await page.locator('.planning-object').filter({hasText:'i-web-bj'}).waitFor();ok((await get('/conversations/'+firstConversation+'/intents/current')).selection[0].resource_id==='i-web-bj','switching sessions restores the original target without contamination');
    const before=posts.filter(x=>x.path.endsWith('/intent-turns')).length;await page.getByLabel('目标、补充说明或追问',{exact:true}).fill('密钥=sk-ARTIFICIAL_BROWSER_SECRET_123456');await page.getByRole('button',{name:'发送',exact:true}).click();ok(posts.filter(x=>x.path.endsWith('/intent-turns')).length===before,'recognized secret is removed before any planning request');ok(!(await page.getByLabel('目标、补充说明或追问',{exact:true}).inputValue()).includes('sk-ARTIFICIAL'),'recognized raw credential is absent from the remaining editor');
    ok(errors.length===0,'no browser JavaScript errors');ok(external.length===0,'no external frontend HTTP requests');
    return {passed:checks.length,failed:0,checks,pageErrors:errors,externalOrigins:external,conversation_id:firstConversation,job_id:firstJob,audit_id:firstAudit,scope:'Real Go/Vue integration using only imported public fixtures; no real cloud/model acceptance.'};
  }catch(error){return {passed:checks.length,failed:1,failure:String(error.message||error),checks,pageErrors:errors,externalOrigins:external,conversation_id:firstConversation,job_id:firstJob,audit_id:firstAudit};}
}
