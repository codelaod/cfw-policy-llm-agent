// Run through Playwright CLI against the dedicated public-fixture Go service.
// Credentials stay inside the temporary local file input and protected headers.
async page => {
  const checks=[],errors=[],external=[],writes=[];let access='';
  const ok=(value,label)=>{if(!value)throw Error(label);checks.push(label)};
  page.on('pageerror',e=>errors.push(e.message));
  page.on('request',r=>{const url=new URL(r.url());if(/^https?:$/.test(url.protocol)&&url.origin!=='http://127.0.0.1:8080')external.push(url.origin);if(url.pathname.startsWith('/api/v1/')&&['POST','PATCH','PUT','DELETE'].includes(r.method()))writes.push({path:url.pathname,method:r.method()})});
  const get=async path=>{const r=await page.request.get('http://127.0.0.1:8080/api/v1'+path,{headers:{'X-App-Token':access}});if(!r.ok())throw Error('local read '+r.status()+' '+path);return r.json()};
  const login=async()=>{await page.evaluate(()=>{const input=document.createElement('input');input.type='file';input.id='guidance-public-fixture';input.hidden=true;document.body.append(input)});await page.locator('#guidance-public-fixture').setInputFiles('runtime/conversation-review-service.json');access=await page.locator('#guidance-public-fixture').evaluate(async input=>JSON.parse(await input.files[0].text()).application_token);await page.locator('#guidance-public-fixture').evaluate(input=>input.remove());await page.getByLabel('应用访问口令',{exact:true}).fill(access);await page.getByRole('button',{name:'解锁工作区',exact:true}).click();await page.locator('.guidance-panel[data-catalog-version]').waitFor()};
  const panel=()=>page.locator('.guidance-panel');
  try {
    await page.setViewportSize({width:1440,height:1100});await page.goto('http://127.0.0.1:8080/conversations');await login();
    const previous=(await get('/workspaces/current')).current_conversation_id;
    const created=page.waitForResponse(r=>r.request().method()==='POST'&&new URL(r.url()).pathname==='/api/v1/conversations');await page.getByRole('button',{name:'新建对话',exact:true}).click();const fresh=(await (await created).json()).id;
    await page.waitForFunction(id=>document.querySelector('#planning-conversation')?.value===id,fresh);
    await page.locator('.guidance-panel[data-catalog-version]').waitFor();
    const blank=await get('/guidance?surface=conversation&conversation_id='+fresh);
    ok(blank.items.length>0&&blank.items.every(x=>x.catalog_version===blank.catalog_version),'new conversation reads one bounded versioned server catalog');
    ok(await panel().getAttribute('data-catalog-version')===blank.catalog_version,'conversation renders the actual server catalog version');
    const fill=blank.items.find(x=>x.behavior==='fill_input'&&x.state==='available');ok(!!fill,'blank context has a local fill suggestion');
    const beforeFill=writes.length;await panel().getByRole('button',{name:fill.display_text,exact:true}).click();
    ok(await page.getByLabel('目标、补充说明或追问',{exact:true}).inputValue()===fill.intent.text,'fill suggestion only prefills its server text');
    ok(writes.length===beforeFill,'ordinary fill sends no task, plan, cancellation or business write');
    ok((await get('/conversations/'+fresh+'/messages')).items.length===0,'fill creates no public message or fabricated reply');
    await page.getByLabel('目标、补充说明或追问',{exact:true}).fill('');
    await page.getByRole('link',{name:'数据与任务',exact:true}).click();await page.locator('.guidance-panel[data-catalog-version]').waitFor();
    const overview=await get('/guidance?surface=overview&conversation_id='+fresh);ok(await panel().getAttribute('data-catalog-version')===overview.catalog_version,'overview uses the same catalog contract');
    const sample=overview.items.find(x=>x.action.target.type==='sample_catalog'&&x.state==='available');if(sample){const count=writes.length;await panel().getByRole('button',{name:sample.display_text,exact:true}).click();ok(new URL(page.url()).pathname==='/overview','sample guidance opens the existing local sample section');ok(writes.length===count,'sample navigation does not import or execute anything')}
    await page.getByRole('link',{name:'回到对话与历史',exact:true}).click();await page.getByLabel('当前会话',{exact:true}).selectOption(previous);await page.locator('.guidance-panel[data-catalog-version]').waitFor();
    const current=await get('/conversations/'+previous+'/intents/current'),steps=await get('/guidance?surface=conversation&conversation_id='+previous);
    const preview=steps.items.find(x=>x.state==='available'&&x.action.target.type==='plan_preview');
    if(preview){const count=writes.length,revision=current.revision;await panel().getByRole('button',{name:preview.display_text,exact:true}).click();await page.getByRole('heading',{name:'本次执行计划',exact:true}).waitFor();ok(writes.length===count,'current plan guidance opens the saved preview without accepting or creating a plan');ok((await get('/conversations/'+previous+'/intents/current')).revision===revision,'opening preview keeps the saved goal revision unchanged')}
    ok((await get('/conversations/'+fresh+'/messages')).items.length===0,'switching conversations does not transfer the prior prefill into a saved message');
    const reports=await get('/reports');ok(reports.items.length>0,'dedicated fixture has a real persisted report');const report=reports.items[0];
    await page.goto('http://127.0.0.1:8080/reports/'+report.id);await login();await page.getByRole('heading',{name:'报告与复审',exact:true}).waitFor();await page.getByRole('button',{name:/^下载固定/}).waitFor();
    const historical=await get('/guidance?surface=report&report_id='+report.id);
    ok(await panel().getAttribute('data-catalog-version')===historical.catalog_version,'report uses versioned guidance bound to its fixed report');
    const explain=historical.items.find(x=>x.state==='available'&&x.action.target.type==='report'&&x.intent.action==='explain_report');
    if(explain){const count=writes.length;await panel().getByRole('button',{name:explain.display_text,exact:true}).click();ok(new URL(page.url()).pathname==='/reports/'+report.id,'historical explanation opens that exact saved report');ok(writes.length===count,'historical explanation navigation starts no online explanation task')}
    const producer=report.job_id?await get('/jobs/'+report.job_id):null,result=producer?await get('/jobs/'+producer.id+'/result'):null;
    if(result?.audit?.snapshot_id){await page.goto('http://127.0.0.1:8080/inventory?snapshot_id='+result.audit.snapshot_id);await login();await page.getByRole('heading',{name:'资产与业务确认',exact:true}).waitFor();await page.locator('.guidance-panel[data-catalog-version]').waitFor();ok(!!(await panel().getAttribute('data-catalog-version')),'inventory loads the shared catalog with its existing local context');await page.screenshot({path:'output/playwright/guidance-inventory-1440.png',fullPage:true})}
    for(const width of [1440,390]){await page.setViewportSize({width,height:width===390?844:1100});const bounds=await page.evaluate(()=>({viewport:innerWidth,document:document.documentElement.scrollWidth}));ok(bounds.document<=bounds.viewport+1,'shared guidance has no horizontal overflow at '+width);await page.screenshot({path:'output/playwright/guidance-local-'+width+'.png',fullPage:true})}
    ok(await page.locator('#workspace-main > .notice-error').count()===0,'persisted application event messages reload without global history errors');
    const taskWrites=writes.filter(x=>x.path.endsWith('/accept')||x.path==='/api/v1/jobs'||x.path.endsWith('/cancel')||x.path==='/api/v1/execution-plans'||x.path.endsWith('/confirm'));
    ok(taskWrites.length===0,'all guidance interactions create no execution or confirmation effects');ok(errors.length===0,'guidance interactions have no browser JavaScript error');ok(external.length===0,'guidance interfaces load no external frontend resource');
    return {passed:checks.length,failed:0,checks,pageErrors:errors,externalOrigins:external,scope:'Real local Go/Vue guidance using public imported fixtures; no cloud/model configured. Preparation-record projection has separate component tests.'};
  }catch(error){return {passed:checks.length,failed:1,failure:String(error.message||error),checks,pageErrors:errors,externalOrigins:external}}
}
