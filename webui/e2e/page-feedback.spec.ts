import {test,expect} from '@playwright/test';
import {mockAPI} from './api';
import {navigate} from './navigation';

for(const operation of ['file','song','public'] as const) {
  for(const timing of ['completed','away','returned'] as const) {
    for(const failed of [false,true]) {
      test(`${operation} feedback expires after leaving (${timing}, ${failed?'failure':'success'})`,async({page})=>{
        await mockAPI(page);
        let release!:()=>void;
        const held=new Promise<void>(resolve=>{release=resolve;});
        const path=operation==='file'?'cache/open':operation==='song'?'songs':'batch/scan';
        const text=failed?'本次操作失败。':operation==='file'?'已请求系统打开视频位置。':operation==='song'?'歌曲 1 已设为收藏。':'已开始检查缺失视频。';
        await page.route('**/api/'+path,async route=>{
          if(timing!=='completed')await held;
          await route.fulfill(failed?{status:400,body:text}:{json:{ok:true}});
        });
        await page.goto('/#/library');
        const trigger=operation==='file'?page.getByRole('button',{name:'定位文件',exact:true}).first():operation==='song'?page.getByRole('combobox',{name:'歌曲 1 的偏好',exact:true}):page.getByRole('button',{name:'检查缺失视频',exact:true});
        const requested=page.waitForRequest('**/api/'+path),responded=page.waitForResponse('**/api/'+path);
        if(operation==='song')await trigger.selectOption('favorite');else await trigger.click();
        await requested;
        const message=page.getByText(text,{exact:true});
        if(timing==='completed')await expect(message).toBeVisible();
        await navigate(page,'设置');
        if(timing==='returned') {
          await navigate(page,'曲库');
          await expect(trigger).toBeDisabled(); // Navigation does not release an in-flight operation.
        }
        release();await responded;
        if(timing!=='returned')await navigate(page,'曲库');
        await expect(trigger).toBeEnabled();
        await expect(message).not.toBeVisible();
      });
    }
  }
}
