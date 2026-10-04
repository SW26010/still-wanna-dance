import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';
import vm from 'node:vm';

const html = readFileSync(new URL('../internal/console/index.html', import.meta.url), 'utf8');
const script = readFileSync(new URL('../internal/console/assets/navigation.js', import.meta.url), 'utf8');
const keys = ['home', 'monitor', 'upstream', 'cache', 'library', 'settings'];
function page(hash = '') {
  let focused, scrolls = 0, pageChanges = 0, expectedPage;
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
    dispatchEvent(event) {
      assert.equal(event.type, 'pagechange');
      if (expectedPage !== undefined)
        assert.deepEqual(panels.filter(n => !n.hidden).map(n => n.dataset.page), [expectedPage]);
      pageChanges++;
    },
    querySelectorAll: selector => selector === '[data-page]' ? panels : links,
    querySelector: () => ({ addEventListener(name, callback) { skipEvents[name] = callback; } }) };
  vm.runInNewContext(script, { window, document, Event });
  return { nodes, panels, links, document, window,
    get focused() { return focused; }, get scrolls() { return scrolls; },
    get pageChanges() { return pageChanges; },
    go(next, expected) { expectedPage = expected; window.location.hash = next; events.hashchange(); },
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
    assert.equal(p.document.title, p.nodes.get('pageLabel').textContent + ' · Still Wanna Dance');
  }
  assert.equal(p.focused.id, 'main');
});

test('only a changed page requests fresh data, after panel visibility updates', () => {
  const p = page('#/settings');
  assert.equal(p.pageChanges, 0);
  p.go('#/monitor', 'monitor');
  assert.equal(p.pageChanges, 1);
  p.go('#recent');
  p.skip();
  assert.equal(p.pageChanges, 1);
  p.go('#overview', 'cache');
  assert.equal(p.pageChanges, 2);
  p.go('#/missing', 'home');
  assert.equal(p.pageChanges, 3);
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
  for (const [section, key] of Object.entries({ activation: 'home', queue: 'settings', downloads: 'monitor',
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
  const groups = { home: ['activation'], monitor: ['downloads', 'recent'],
    upstream: ['healthCheck', 'healthCatalog', 'healthCatalogKiva', 'healthCatalogWanna', 'healthPlaybackHkg', 'healthPlaybackCf', 'healthResourceHkg', 'healthResourceCf'],
    cache: ['overview', 'cache'], library: ['batch'], settings: ['preferences', 'service', 'queue'] };
  const markupKeys = [...keys].sort((a, b) => html.indexOf(`id="page-${a}"`) - html.indexOf(`id="page-${b}"`));
  const positions = markupKeys.map(key => html.indexOf(`id="page-${key}"`));
  for (const [i, key] of markupKeys.entries()) {
    assert.ok(positions[i] > 0);
    const content = html.slice(positions[i], positions[i + 1] ?? html.indexOf('</main>'));
    for (const section of groups[key]) assert.ok(content.includes(`id="${section}"`), section);
    assert.match(content.split('>')[0], key === 'home' ? /data-page="home"$/ : / hidden$/);
  }
  const ids = [...html.matchAll(/\bid="([^"]+)"/g)].map(m => m[1]);
  assert.equal(new Set(ids).size, ids.length);
});
