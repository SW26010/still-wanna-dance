import {test,expect} from '@playwright/test';
import {mockAPI} from './api';

test('coverage keeps unresolved current songs in its denominator and distinguishes unknown catalog',async({page},info)=>{
  const api=await mockAPI(page);
  let known=true;
  await page.route('**/api/inventory',r=>r.fulfill({json:{coverageKnown:known,totalSongs:known?3:0,coveredSongs:known?1:0,unresolvedSongs:known?2:0,updated:'2026-10-29T00:00:00Z',catalogRevision:known?'20261029000000':''}}));
  api.state.batch={scanOnly:true,total:3,checked:3,hits:1,missing:0,unresolved:2,phase:'本地检查完成'};
  await page.setViewportSize({width:390,height:844});
  await page.goto('/#/library');
  const coverage=page.getByRole('group',{name:'曲库覆盖率',exact:true});
  await expect(coverage).toContainText('33.3%');
  await expect(coverage).toContainText('已覆盖 1 / 当前清单 3 首');
  await expect(coverage).toContainText('资源待识别 2 首');
  await expect(page.locator('#batch')).toContainText('缺失 0 · 资源待识别 2');
  for(const width of [390,320]){
    await page.setViewportSize({width,height:844});
    expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
    await page.getByRole('region',{name:'曲库概览'}).screenshot({path:info.outputPath(`coverage-${width}.png`)});
  }
  known=false;await page.reload();
  await expect(coverage).toContainText('—');
  await expect(coverage).not.toContainText('%');
  await expect(coverage).not.toContainText('已覆盖');
  expect(api.calls.some(c=>c.path==='inventory/scan')).toBe(false);
});
