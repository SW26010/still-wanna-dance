import {chromium,expect} from '@playwright/test';
import {mkdir,writeFile} from 'node:fs/promises';
import {join} from 'node:path';

const [baseURL,key,output]=process.argv.slice(2);
await mkdir(output,{recursive:true});
const browser=await chromium.launch({channel:process.env.CI?'chromium':'msedge'});
try {
  const page=await browser.newPage({viewport:{width:1280,height:900}});
  if(baseURL==='generate') {
    // A synthetic video keeps this test independent of private/user media.
    const video=await page.evaluate(async()=>{
      const mime='video/mp4';
      if(!MediaRecorder.isTypeSupported(mime))throw new Error('MP4 MediaRecorder unavailable');
      const canvas=document.createElement('canvas');canvas.width=320;canvas.height=180;
      const ctx=canvas.getContext('2d'),stream=canvas.captureStream(15),chunks=[];
      const recorder=new MediaRecorder(stream,{mimeType:mime,videoBitsPerSecond:300000});
      const finished=new Promise(resolve=>{recorder.ondataavailable=e=>chunks.push(e.data);recorder.onstop=resolve;});
      recorder.start();
      let frame=0;
      const timer=setInterval(()=>{ctx.fillStyle=`hsl(${frame*3%360} 60% 40%)`;ctx.fillRect(0,0,320,180);ctx.fillStyle='#fff';ctx.font='24px sans-serif';ctx.fillText('SWD preview '+frame++,24,90);},66);
      await new Promise(resolve=>setTimeout(resolve,12000));
      recorder.stop();await finished;clearInterval(timer);stream.getTracks().forEach(track=>track.stop());
      return [...new Uint8Array(await new Blob(chunks,{type:mime}).arrayBuffer())];
    });
    await writeFile(join(output,'preview.mp4'),Buffer.from(video));
    console.log(JSON.stringify({generated:true,bytes:video.length}));
  } else {
    const errors=[],mediaRequests=[];
    page.on('pageerror',e=>errors.push(e.message));
    page.on('request',r=>{if(r.url().includes('/media/preview?'))mediaRequests.push({url:r.url(),range:r.headers().range,site:r.headers()['sec-fetch-site']});});
    await page.goto(baseURL+'/#/library');
    const songs=page.getByRole('table',{name:'曲库歌曲'});
    await expect(songs.getByRole('button',{name:'预览',exact:true})).toHaveCount(2);
    expect(mediaRequests).toHaveLength(0);
    await songs.getByRole('button',{name:'预览',exact:true}).first().click();
    const video=page.getByLabel('曲库预览播放器');
    await expect.poll(()=>video.evaluate(v=>v.currentTime),{timeout:30000}).toBeGreaterThan(0.2);
    const source=await video.evaluate(v=>v.currentSrc);
    expect(new URL(source).origin).toBe(new URL(baseURL).origin);
    await video.evaluate(v=>{v.pause();v.currentTime=8;});
    await expect.poll(()=>video.evaluate(v=>v.seeking)).toBe(false);
    await video.evaluate(v=>v.play());
    await expect.poll(()=>video.evaluate(v=>v.currentTime)).toBeGreaterThan(8.2);
    await video.evaluate(v=>v.pause());
    // Pause/resume crosses the ordinary 2-second handoff without inventing a session.
    await page.waitForTimeout(3000);
    await video.evaluate(v=>{v.currentTime=2;return v.play();});
    await expect.poll(()=>video.evaluate(v=>v.currentTime)).toBeGreaterThan(2.2);
    await page.screenshot({path:join(output,'preview-desktop.png')});
    await page.getByRole('button',{name:'关闭预览',exact:true}).click();
    await expect(video).toHaveCount(0);
    await page.getByRole('tab',{name:'按资源'}).click();
    await page.getByRole('table',{name:'曲库资源'}).getByRole('button',{name:'预览',exact:true}).click();
    await expect.poll(()=>video.evaluate(v=>v.currentTime)).toBeGreaterThan(0.2);
    await page.setViewportSize({width:390,height:844});
    await page.emulateMedia({colorScheme:'dark'});
    expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
    await page.screenshot({path:join(output,'preview-mobile.png')});
    // Changing the route while the preview is open must release its native source.
    await page.evaluate(()=>{location.hash='/history';});
    await expect(video).toHaveCount(0);
    expect(errors).toEqual([]);
    expect(mediaRequests.every(r=>new URL(r.url).origin===new URL(baseURL).origin)).toBe(true);
    // A second origin cannot embed even a valid capability.
    const hostile=await browser.newPage();
    await hostile.route('http://localhost:18474/',route=>route.fulfill({contentType:'text/html',body:'<!doctype html><title>Cross-origin probe</title>'}));
    await hostile.goto('http://localhost:18474/');
    let crossOriginStatus=null;
    hostile.on('response',r=>{if(r.url()===source)crossOriginStatus=r.status();});
    const blocked=await hostile.evaluate(src=>new Promise((resolve,reject)=>{
      const v=document.createElement('video');v.controls=true;
      const timer=setTimeout(()=>reject(new Error('cross-origin media did not settle')),10000);
      v.onerror=()=>{clearTimeout(timer);resolve(v.readyState===0);};
      v.onloadeddata=()=>{clearTimeout(timer);resolve(false);};
      v.src=src;document.body.append(v);v.load();
    }),source);
    expect(blocked).toBe(true);
    if(crossOriginStatus!==null)expect(crossOriginStatus).toBe(403);
    await writeFile(join(output,'results.json'),JSON.stringify({resource:key,decoded:true,seek:true,pauseResume:true,resourceReplay:true,navigationCleanup:true,crossOriginBlocked:true,crossOriginStatus,mediaRequests:mediaRequests.length,errors},null,2));
  }
}finally{await browser.close();}
