// Keep each page mounted: navigation must not reset drafts, selections or tasks.
(() => {
  const pages = {
    home: ['开始使用', 'Load failed? But I still wanna dance!', '为 VRChat WannaDance 复用已下载视频，也可提前准备房间队列。'],
    monitor: ['运行状态', '运行状态', '查看下载进度、最近请求和网络连接。'],
    cache: ['本地缓存', '本地缓存', '查找已下载的视频，清理磁盘空间。'],
    library: ['准备歌曲', '准备歌曲', '按房间队列提前准备，或批量下载缺失视频。'],
    settings: ['设置', '设置', '调整存储、网络和启动偏好。'],
  };
  const legacy = { activation: 'home', queue: 'library', overview: 'cache', cache: 'cache',
    downloads: 'monitor', recent: 'monitor', network: 'monitor', batch: 'library', preferences: 'settings', queuePrefetchCount: 'settings', maxCacheGiB: 'settings', service: 'home' };
  const panels = document.querySelectorAll('[data-page]');
  const links = document.querySelectorAll('[data-page-link]');
  let current = 'home';
  function navigate(initial = false) {
    const hash = window.location.hash === '#/upstream' ? 'network' : window.location.hash.slice(1);
    // The skip link moves focus without changing the selected page.
    if (hash === 'main' && !initial) return;
    const page = Object.hasOwn(pages, hash.slice(1)) && hash.startsWith('/')
      ? hash.slice(1) : Object.hasOwn(legacy, hash) ? legacy[hash] : 'home';
    const target = Object.hasOwn(legacy, hash) ? document.getElementById(hash) : null;
    const changed = current !== page;
    current = page;
    for (const panel of panels) panel.hidden = panel.dataset.page !== page;
    for (const link of links) {
      if (link.dataset.pageLink === page) link.setAttribute('aria-current', 'page');
      else link.removeAttribute('aria-current');
    }
    const [label, title, description] = pages[page];
    document.getElementById('pageLabel').textContent = label;
    document.getElementById('pageTitle').textContent = title;
    document.getElementById('pageDescription').textContent = description;
    document.title = label + ' · Still Wanna Dance';
    if (!target && hash !== '/' + page) window.history.replaceState(null, '', '#/' + page);
    if (!initial && (changed || target)) (target || document.getElementById('main')).focus();
    if (target) {
      if (target.tagName === 'DETAILS') target.open = true;
      target.scrollIntoView();
    }
    else if (!initial && changed) window.scrollTo(0, 0);
    if (!initial && changed) document.dispatchEvent(new Event('pagechange'));
  }
  window.addEventListener('hashchange', () => navigate());
  // Preserve the current route when the skip link is used, including on reload.
  document.querySelector('.skip-link').addEventListener('click', event => {
    event.preventDefault();
    document.getElementById('main').focus();
  });
  navigate(true);
})();
