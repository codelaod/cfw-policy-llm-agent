// Playwright CLI run-code snippet; local documentation prototype only.
// Open index.html first. Run from the repository root; artifacts stay in output/playwright/.
async (page) => {
  const errors = [], requests = [], checks = [];
  page.on('pageerror', e => errors.push(e.message));
  page.on('request', r => { if (/^https?:/.test(r.url())) requests.push(r.url()); });
  const verify = (value, label) => { if (!value) throw new Error(label); checks.push(label); };
  const state = () => page.evaluate(() => window.mockAuditPrototype.getState());
  const nav = async key => { await page.locator('.nav [data-page="'+key+'"]').click(); await page.locator('#page-'+key).waitFor({state:'visible'}); };
  const selectScenario = async value => {
    await nav('data');
    const details = page.locator('#page-data details');
    if (await details.getAttribute('open') === null) await details.locator('summary').click();
    await page.locator('#scenario').selectOption(value);
    await page.locator('#loadBtn').click();
    if (await page.locator('#resetDialog').isVisible()) await page.locator('#confirmResetBtn').click();
  };
  await page.setViewportSize({width:1440,height:1100});
  await page.reload();
  verify((await state()).loaded === false, 'fresh page has no pre-existing data');
  await page.getByRole('button',{name:'新建模拟审计',exact:true}).click();
  verify((await state()).tasks.length === 0, 'empty preflight cannot create a task');
  await page.getByRole('button',{name:'载入人工样例',exact:true}).click();
  await nav('assets');
  await page.locator('[data-asset="demo-b"]').click();
  await page.getByRole('button',{name:'生成候选（模拟）',exact:true}).click();
  verify(!(await state()).profiles['demo-b'], 'candidate is not a confirmed profile');
  verify(await page.locator('#sources').inputValue() === '', 'candidate does not invent authorization sources');
  await page.getByLabel('已确认服务协议／端口',{exact:true}).fill('TCP 3306');
  await page.locator('#completeSet').check();
  await page.getByRole('button',{name:'预览确认内容',exact:true}).click();
  verify(await page.locator('#profileError').textContent() !== '', 'missing source gets inline error');
  verify(!await page.locator('#confirmationDialog').isVisible(), 'invalid draft cannot open confirmation');
  await page.locator('#sources').fill('10.0.1.0/24（人工声明）');
  await page.locator('#exposure').selectOption('private');
  await page.locator('#previewBtn').click();
  verify(await page.locator('#confirmationDialog').isVisible(), 'confirmation dialog opens');
  await page.keyboard.press('Escape');
  verify(!await page.locator('#confirmationDialog').isVisible(), 'Escape dismisses native dialog');
  verify(!(await state()).profiles['demo-b'], 'dismissing preview is not confirmation');
  await page.locator('#previewBtn').click();
  await page.locator('#confirmBtn').click();
  verify((await state()).profileVersion === 1, 'explicit confirmation stores a new in-memory version');
  await page.screenshot({path:'output/playwright/03-assets-desktop.png',fullPage:true});
  await nav('audit');
  await page.locator('#startBtn').click();
  verify(await page.locator('#startBtn').isDisabled(), 'single running task protected in UI');
  await page.locator('#advanceBtn').click();
  await page.locator('#cancelBtn').click();
  let s = await state();
  verify(s.tasks[0].status === 'cancelled' && s.tasks[0].findings.length === 1, 'cancel keeps existing findings');
  await page.locator('#startBtn').click();
  await page.locator('#finishBtn').click();
  await page.locator('#viewFindingsBtn').click();
  const originalStatus = (await state()).tasks[1].findings[0].status;
  await page.locator('#planBtn').click();
  verify((await state()).tasks[1].findings[0].status === originalStatus, 'local plan does not mark original finding fixed');
  await page.locator('#deferBtn').click();
  await page.locator('#saveDeferBtn').click();
  verify(await page.locator('#deferError').textContent() !== '', 'deferral requires a reason');
  await page.locator('#deferReason').fill('等待业务负责人复核管理来源（人工示例）');
  await page.locator('#saveDeferBtn').click();
  await page.locator('#guideExplainBtn').click();
  verify(await page.locator('#assistantAnswer').isVisible(), 'fixed explanation is visible and explicitly simulated');
  await page.locator('#findingFilter').selectOption('insufficient');
  verify(!await page.locator('#findingDetail').isVisible(), 'empty filter does not show stale detail');
  await page.locator('#findingFilter').selectOption('all');
  await page.screenshot({path:'output/playwright/04-findings-desktop.png',fullPage:true});
  await page.locator('#openReportBtn').click();
  const downloadPromise = page.waitForEvent('download');
  await page.locator('#exportBtn').click();
  const download = await downloadPromise;
  await download.saveAs('output/playwright/downloaded-mock-report.md');
  verify(download.suggestedFilename().startsWith('mock-DEMO-'), 'download clearly names a mock report');
  const taskCount = (await state()).tasks.length;
  await page.locator('#reAuditBtn').click();
  verify((await state()).tasks.length === taskCount, 'prepare re-audit does not run it');
  await nav('assets');
  await page.locator('#businessNote').fill('新增说明，不改变旧任务（人工示例）');
  await page.locator('#previewBtn').click(); await page.locator('#confirmBtn').click();
  await nav('report');
  verify((await page.locator('#reportVersion').textContent()).includes('报告固定画像 v1；当前资料 v2'), 'old report remains pinned to old version');
  await page.screenshot({path:'output/playwright/05-report-desktop.png',fullPage:true});

  await nav('audit');
  await page.locator('#scope-outbound').check();
  await page.locator('#startBtn').click(); await page.locator('#finishBtn').click();
  s = await state();
  verify(s.tasks.at(-1).findings[0].kind === 'confirmation', 'broad outbound without business requirements stays pending');
  await page.locator('#scope-nat').check();
  await page.locator('#startBtn').click(); await page.locator('#finishBtn').click();
  s = await state();
  verify(s.tasks.at(-1).boundary.plane === 'nat' && s.tasks.at(-1).findings[0].kind === 'insufficient', 'NAT mapping gaps do not become a pass');
  verify(s.tasks[0].boundary.plane === 'internet', 'NAT selection does not mutate earlier internet task');
  await page.locator('#viewFindingsBtn').click();
  await page.screenshot({path:'output/playwright/06-nat-desktop.png',fullPage:true});

  await selectScenario('model-error');
  await nav('assets'); await page.locator('#inferBtn').click();
  verify((await page.locator('#assistant').textContent()).includes('模型不可用'), 'model failure offers manual fallback');
  await nav('audit'); await page.locator('#startBtn').click(); await page.locator('#finishBtn').click();
  verify((await state()).tasks[0].status === 'partial' && (await state()).tasks[0].findings.length === 1, 'model failure keeps rule result');
  await selectScenario('partial');
  await nav('assets'); await page.locator('[data-asset="demo-c"]').click();
  verify((await page.locator('#facts').textContent()).includes('不能将其他安全组的端口'), 'missing group association remains unknown');
  await nav('audit'); await page.locator('#startBtn').click(); await page.locator('#finishBtn').click();
  verify((await state()).tasks[0].status === 'partial', 'incomplete data stays partial');

  await selectScenario('normal');
  await nav('assets'); await page.locator('#inferBtn').click();
  await page.locator('#ports').fill('TCP 443');
  await page.locator('#previewBtn').click(); await page.locator('#confirmBtn').click();
  await nav('audit'); await page.locator('#startBtn').click(); await page.locator('#finishBtn').click();
  await nav('findings'); await page.locator('#planBtn').click();
  // Let transient status messages finish naturally before final visual captures.
  await page.locator('#notice').filter({hasText:/./}).waitFor({state:'hidden',timeout:10000}).catch(async()=>{
    await page.waitForFunction(()=>document.getElementById('notice').textContent === '');
  });
  for (const width of [1440,768,390]) {
    await page.setViewportSize({width,height:width===390?844:1100});
    for (const view of ['data','assets','audit','findings','report']) {
      await nav(view);
      const bounds = await page.evaluate(()=>({width:innerWidth,scroll:document.documentElement.scrollWidth}));
      verify(bounds.scroll <= bounds.width+1, 'no document overflow at '+width+'px / '+view);
      await page.screenshot({path:'output/playwright/final-'+view+'-'+width+'.png',fullPage:true});
    }
  }
  await page.locator('.skip').focus();
  await page.keyboard.press('Tab');
  await page.keyboard.press('Shift+Tab');
  verify(await page.locator('.skip').evaluate(el=>getComputedStyle(el).outlineStyle !== 'none'), 'keyboard focus has visible outline');
  await page.keyboard.press('Enter');
  verify(await page.locator('#main').evaluate(el=>document.activeElement === el), 'skip link moves focus to content');
  verify(errors.length === 0, 'no page JavaScript errors');
  verify(requests.length === 0, 'no HTTP or HTTPS requests from local prototype');
  await page.setViewportSize({width:1440,height:1100}); await nav('data');
  return {passed:checks.length,checks,pageErrors:errors,httpRequests:requests,limitations:'Documentation prototype only; not full WCAG, backend, real rule, model or cloud acceptance.'};
}
