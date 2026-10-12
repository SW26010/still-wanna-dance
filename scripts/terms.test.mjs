import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import vm from 'node:vm';

function setup(fetchImpl = async () => ({ ok: true })) {
  const source = readFileSync(new URL('../internal/console/terms.go', import.meta.url), 'utf8');
  const script = source.match(/<script>([\s\S]*?)<\/script>/)[1]
    .replace('{{.Token}}', '"test-token"').replace('{{.Version}}', '"test-version"').replace('{{.Hash}}', '"test-hash"').replace('{{.PageID}}', '"test-page"');
  const elements = Object.fromEntries(['consent', 'accept', 'decline', 'restore', 'stop', 'result', 'readTerms', 'backWelcome', 'welcome', 'termsPanel', 'welcomeTitle', 'termsTitle', 'gateWelcome', 'gateTerms'].map(id => [id, {
    disabled: false, textContent: '', listeners: {},
    hidden: id === 'termsPanel', focus() { this.focused = true; },
    addEventListener(name, fn) { this.listeners[name] = fn; },
    dataset: {}, setAttribute(name, value) { this[name] = value; }, removeAttribute(name) { delete this[name]; },
  }]));
  const calls = [], navigations = [];
  const listeners = {};
  vm.runInNewContext(script, {
    window: { addEventListener: (name, fn) => { listeners[name] = fn; } },
    document: { getElementById: id => elements[id] },
    fetch: async (...args) => { calls.push(args); return fetchImpl(...args); },
    location: { replace: path => navigations.push(path) },
  });
  return { elements, calls, navigations, listeners, submit: () => elements.consent.listeners.submit({ preventDefault() {} }) };
}

test('leaving sends authenticated keepalive; back-forward cache restore cancels exit', () => {
  const s = setup();
  assert.equal(s.listeners.visibilitychange, undefined);
  s.listeners.pageshow({ persisted: false });
  assert.equal(s.calls.length, 0);
  s.listeners.pagehide();
  assert.equal(s.calls[0][0], '/api/terms/leave');
  assert.equal(s.calls[0][1].keepalive, true);
  assert.equal(s.calls[0][1].headers['X-SWD-Token'], 'test-token');
  assert.deepEqual(JSON.parse(s.calls[0][1].body), { pageID: 'test-page' });
  s.listeners.pageshow({ persisted: true });
  assert.equal(s.calls[1][0], '/api/terms/return');
});

test('reading and declining do not submit; explicit confirmation replaces checkboxes', async () => {
  const s = setup();
  s.elements.readTerms.onclick();
  assert.equal(s.elements.accept.disabled, false);
  s.elements.decline.onclick();
  assert.match(s.elements.result.textContent,/未同意/);
  assert.equal(s.calls.length, 0);
});

test('only a successful authenticated current-text acceptance navigates into the app', async () => {
  const s = setup();
  await s.submit();
  assert.equal(s.calls[0][0], '/api/terms/accept');
  assert.equal(s.calls[0][1].headers['X-SWD-Token'], 'test-token');
  assert.deepEqual(JSON.parse(s.calls[0][1].body), { version: 'test-version', sha256: 'test-hash', agree: true, contentRights: true });
  assert.equal(s.calls[1][0], '/api/setup');
  assert.deepEqual(JSON.parse(s.calls[1][1].body), { schemaVersion: 1, step: 'terms', state: 'active' });
  assert.deepEqual(s.navigations, ['/#/setup']);
});

for(const id of ['backWelcome','gateWelcome'])test(id+' returns to welcome without accepting or validating terms', async () => {
  const s = setup();
  s.elements.readTerms.onclick();
  assert.equal(s.elements.welcome.hidden, true);
  assert.equal(s.elements.termsPanel.hidden, false);
  await s.elements[id].onclick();
  assert.equal(s.elements.termsPanel.hidden, true);
  assert.equal(s.elements.welcome.hidden, false);
  assert.equal(s.elements.welcomeTitle.focused, true);
  assert.equal(s.elements.result.textContent, '');
  assert.equal(s.calls.length, 0);
  s.elements.gateTerms.onclick();
  await s.elements[id].onclick();
  assert.equal(s.elements.termsPanel.hidden, true);
  assert.equal(s.calls.length, 0);
  assert.deepEqual(s.navigations, []);
});

test('progress write failure does not undo durable consent or block entry', async () => {
  const s = setup(async path => ({ ok: path === '/api/terms/accept', text: async () => 'progress unavailable' }));
  await s.submit();
  assert.deepEqual(s.navigations, ['/#/setup']);
  assert.equal(s.calls.length, 2);
});

test('save failure keeps the gate visible, prevents duplicate submits and permits retry', async () => {
  let resolve;
  const s = setup(() => new Promise(r => { resolve = r; }));
  const pending = s.submit(); await s.submit();
  assert.equal(s.calls.length, 1); assert.equal(s.elements.accept.disabled, true);
  resolve({ ok: false, text: async () => '无法保存同意记录' }); await pending;
  assert.deepEqual(s.navigations, []);
  assert.equal(s.elements.accept.disabled, false);
  assert.equal(s.elements.result.textContent, '无法保存同意记录');
});
