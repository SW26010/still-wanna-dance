import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import vm from 'node:vm';

// First-use acceptance now lives in setup.js and is covered by setup.test.mjs.
// This template is only the independently opened, already-accepted legal text.
for (const [id,path,message] of [['restore','/api/hosts/disable','hosts 恢复操作已完成。'],['stop','/api/stop','缓存服务已停止。']]) {
  test('readonly terms retains authenticated '+id+' and displays errors',async()=>{
    const source=readFileSync(new URL('../internal/console/terms.go',import.meta.url),'utf8');
    const script=source.match(/<script>([\s\S]*?)<\/script>/)[1].replace('{{.Token}}','"test-token"');
    const nodes={restore:{},stop:{},result:{}};
    const calls=[];let ok=false;
    vm.runInNewContext(script,{document:{getElementById:id=>nodes[id]},fetch:async(url,options)=>{calls.push({url,options});return {ok,text:async()=>'操作失败'};}});
    await nodes[id].onclick();assert.equal(nodes.result.textContent,'操作失败');
    ok=true;await nodes[id].onclick();assert.equal(nodes.result.textContent,message);
    assert.ok(calls.every(c=>c.url===path&&c.options.headers['X-SWD-Token']==='test-token'));
    assert.doesNotMatch(script,/location\.|terms\/accept/);
  });
}
