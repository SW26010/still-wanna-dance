// Keep each page mounted: navigation must not reset drafts, selections or tasks.
(() => {
  const pages = {
    home: ['首页', '让下一支舞，提前就绪', '启用游戏加速，查看接入状态，准备房间里的下一首歌。'],
    monitor: ['监控', '查看正在发生的传输', '活动下载、上游速度与最近 HTTP 请求集中显示。'],
    cache: ['缓存', '了解并管理本地缓存', '查看播放统计、视频覆盖率，查找和清理缓存文件。'],
    library: ['曲库与下载', '按需准备你的曲库', '扫描缺失、完整校验，再下载补齐；切换页面不会停止任务。'],
    settings: ['设置', '配置存储、网络与服务', '调整缓存与任务设置，单独管理本地 CDN 和 hosts 接入。'],
  };
  const legacy = { activation: 'home', queue: 'home', overview: 'cache', cache: 'cache',
    downloads: 'monitor', recent: 'monitor', batch: 'library', preferences: 'settings', service: 'settings' };
  const panels = document.querySelectorAll('[data-page]');
  const links = document.querySelectorAll('[data-page-link]');
  let current = 'home';
  function navigate(initial = false) {
    const hash = window.location.hash.slice(1);
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
    document.title = label + ' · StepStash';
    if (!target && hash !== '/' + page) window.history.replaceState(null, '', '#/' + page);
    if (!initial && (changed || target)) (target || document.getElementById('main')).focus();
    if (target) target.scrollIntoView();
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
