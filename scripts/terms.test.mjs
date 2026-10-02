import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import vm from 'node:vm';

function setup(fetchImpl = async () => ({ ok: true })) {
  const source = readFileSync(new URL('../internal/console/terms.go', import.meta.url), 'utf8');
  const script = source.match(/<script>([\s\S]*?)<\/script>/)[1]
    .replace('{{.Token}}', '"test-token"').replace('{{.Version}}', '"test-version"').replace('{{.Hash}}', '"test-hash"');
  const elements = Object.fromEntries(['consent', 'agree', 'rights', 'accept', 'decline', 'restore', 'stop', 'result'].map(id => [id, {
    checked: false, disabled: id === 'accept', textContent: '', listeners: {},
    addEventListener(name, fn) { this.listeners[name] = fn; },
  }]));
  const calls = [], navigations = [];
  vm.runInNewContext(script, {
    document: { getElementById: id => elements[id] },
    fetch: async (...args) => { calls.push(args); return fetchImpl(...args); },
    location: { replace: path => navigations.push(path) },
  });
  return { elements, calls, navigations, submit: () => elements.consent.listeners.submit({ preventDefault() {} }), change: () => elements.consent.listeners.change() };
}

test('consent requires both active choices; decline clears them without submitting', async () => {
  const s = setup();
  await s.submit(); assert.equal(s.calls.length, 0);
  s.elements.rights.checked = true; s.change();
  assert.equal(s.elements.accept.disabled, true);
  await s.submit(); assert.equal(s.calls.length, 0);
  s.elements.agree.checked = true; s.change();
  assert.equal(s.elements.accept.disabled, false);
  s.elements.decline.onclick();
  assert.equal(s.elements.agree.checked, false);
  assert.equal(s.elements.rights.checked, false);
  assert.equal(s.elements.accept.disabled, true);
  assert.equal(s.calls.length, 0);
});

test('only a successful authenticated current-text acceptance navigates into the app', async () => {
  const s = setup();
  s.elements.rights.checked = s.elements.agree.checked = true; s.change();
  await s.submit();
  assert.equal(s.calls[0][0], '/api/terms/accept');
  assert.equal(s.calls[0][1].headers['X-StepStash-Token'], 'test-token');
  assert.deepEqual(JSON.parse(s.calls[0][1].body), { version: 'test-version', sha256: 'test-hash', agree: true, contentRights: true });
  assert.deepEqual(s.navigations, ['/']);
});

test('save failure keeps the gate visible, prevents duplicate submits and permits retry', async () => {
  let resolve;
  const s = setup(() => new Promise(r => { resolve = r; }));
  s.elements.rights.checked = s.elements.agree.checked = true;
  const pending = s.submit(); await s.submit();
  assert.equal(s.calls.length, 1); assert.equal(s.elements.accept.disabled, true);
  resolve({ ok: false, text: async () => '无法保存同意记录' }); await pending;
  assert.deepEqual(s.navigations, []);
  assert.equal(s.elements.accept.disabled, false);
  assert.equal(s.elements.result.textContent, '无法保存同意记录');
});
