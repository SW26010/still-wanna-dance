import {test,expect} from '@playwright/test';
import {mockAPI} from './api';

test('the page fixture rejects unknown operations and wrong methods',async({page})=>{
  const api=await mockAPI(page);
  await page.goto('/about');
  const statuses=await page.evaluate(async()=>{
    const headers={'Content-Type':'application/json','X-SWD-Token':'browser-fixture-token'};
    const requests: [string,RequestInit][]=[
      ['/api/activation/enabel',{method:'POST',headers,body:'{}'}],
      ['/api/upstream/check/typo',{method:'POST',headers,body:'{}'}],
      ['/api/activation/enabel',{method:'POST',headers,body:'not-json'}],
      ['/api/activation/enable',{method:'GET',headers}],
      ['/api/settings',{method:'POST',headers,body:'not-json'}],
    ];
    return Promise.all(requests.map(async([url,init])=>(await fetch(url,init)).status));
  });
  expect(statuses).toEqual([404,404,404,404,400]);
  expect(api.state.running).toBe(false);
  const known=await page.evaluate(async()=>fetch('/api/activation/enable',{
    method:'POST',headers:{'Content-Type':'application/json','X-SWD-Token':'browser-fixture-token'},body:'{}',
  }).then(response=>response.status));
  expect(known).toBe(200);
  expect(api.state.running).toBe(true);
});

test('the fixture server rejects unknown direct API requests',async({request})=>{
  for(const path of ['/api/activation/enabel','/api/history/events']){
    const response=await request.post(path,{headers:{'X-SWD-Token':'browser-fixture-token'},data:{}});
    expect(response.status()).toBe(404);
  }
});
