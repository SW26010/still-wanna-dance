import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';
import vm from 'node:vm';

const html = readFileSync(new URL('../internal/console/index.html', import.meta.url), 'utf8');
const script = readFileSync(new URL('../internal/console/assets/navigation.js', import.meta.url), 'utf8');
const keys = ['home', 'monitor', 'cache', 'library', 'settings'];
function page(hash = '') {
  let focused, scrolls = 0;
  const events = {}, skipEvents = {};
  const nodes = new Map([...html.matchAll(/\bid="([^"]+)"/g)].map(([, id]) => [id, {
    id, textContent: '', hidden: false, value: '',
    focus() { focused = this; }, scrollIntoView() { scrolls++; },
  }]));
  const panels = keys.map(key => Object.assign(nodes.get('page-' + key), { dataset: { page: key } }));
  const links = keys.map(key => ({ dataset: { pageLink: key },
    setAttribute(name, value) { this[name] = value; }, removeAttribute(name) { delete this[name]; } }));
  const window = { location: { hash }, history: { replaceState(_, __, next) { window.location.hash = next; } },
    addEventListener(name, callback) { events[name] = callback; }, scrollTo() { scrolls++; } };
  const document = { getElementById: id => nodes.get(id),
    querySelectorAll: selector => selector === '[data-page]' ? panels : links,
    querySelector: () => ({ addEventListener(name, callback) { skipEvents[name] = callback; } }) };
  vm.runInNewContext(script, { window, document });
  return { nodes, panels, links, document, window,
    get focused() { return focused; }, get scrolls() { return scrolls; },
    go(next) { window.location.hash = next; events.hashchange(); },
    skip() { let prevented = false; skipEvents.click({ preventDefault() { prevented = true; } }); return prevented; } };
}

test('all routes show exactly one page with matching navigation and title', () => {
  const p = page();
  assert.equal(p.window.location.hash, '#/home');
  assert.equal(p.focused, undefined);
  for (const key of [...keys, ...keys.toReversed()]) {
    p.go('#/' + key);
    assert.deepEqual(p.panels.filter(n => !n.hidden).map(n => n.dataset.page), [key]);
    assert.deepEqual(p.links.filter(n => n['aria-current'] === 'page').map(n => n.dataset.pageLink), [key]);
    assert.equal(p.document.title, p.nodes.get('pageLabel').textContent + ' · StepStash');
  }
  assert.equal(p.focused.id, 'main');
});

test('direct load and reload retain route; malformed hashes safely return home', () => {
  for (const key of keys) {
    const p = page('#/' + key);
    assert.equal(p.panels.find(n => !n.hidden).dataset.page, key);
    assert.equal(p.focused, undefined);
    assert.equal(p.window.location.hash, '#/' + key);
  }
  for (const hash of ['#/missing', '#/constructor', '#__proto__', '#%E0%A4%A']) {
    const p = page(hash);
    assert.equal(p.window.location.hash, '#/home');
    assert.equal(p.panels.find(n => !n.hidden).dataset.page, 'home');
  }
});

test('legacy deep links reveal their page and focus the requested section', () => {
  const p = page();
  for (const [section, key] of Object.entries({ activation: 'home', queue: 'home', downloads: 'monitor',
    recent: 'monitor', overview: 'cache', cache: 'cache', batch: 'library', preferences: 'settings', service: 'settings' })) {
    p.go('#' + section);
    assert.equal(p.panels.find(n => !n.hidden).dataset.page, key);
    assert.equal(p.focused.id, section);
  }
});

test('navigation keeps drafts, selection, confirmation and expanded details mounted; skip preserves route', () => {
  const p = page('#/settings');
  const input = p.nodes.get('storageDir'), confirmation = p.nodes.get('cacheConfirm');
  input.value = 'unsaved storage'; confirmation.hidden = false;
  p.nodes.get('cacheSearch').value = '舞曲'; p.nodes.get('failures').open = true;
  for (const key of keys) p.go('#/' + key);
  assert.equal(p.nodes.get('storageDir'), input);
  assert.equal(input.value, 'unsaved storage');
  assert.equal(confirmation.hidden, false);
  assert.equal(p.nodes.get('cacheSearch').value, '舞曲');
  assert.equal(p.nodes.get('failures').open, true);
  assert.equal(p.skip(), true);
  assert.equal(p.window.location.hash, '#/settings');
  assert.equal(p.focused.id, 'main');
});

test('markup groups every existing section once with only home initially visible', () => {
  const groups = { home: ['activation', 'queue'], monitor: ['downloads', 'recent'],
    cache: ['overview', 'cache'], library: ['batch'], settings: ['preferences', 'service'] };
  const positions = keys.map(key => html.indexOf(`id="page-${key}"`));
  for (const [i, key] of keys.entries()) {
    assert.ok(positions[i] > 0);
    const content = html.slice(positions[i], positions[i + 1] ?? html.indexOf('</main>'));
    for (const section of groups[key]) assert.ok(content.includes(`id="${section}"`), section);
    assert.match(content.split('>')[0], key === 'home' ? /data-page="home"$/ : / hidden$/);
  }
  const ids = [...html.matchAll(/\bid="([^"]+)"/g)].map(m => m[1]);
  assert.equal(new Set(ids).size, ids.length);
});
