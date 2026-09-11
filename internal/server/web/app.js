/* Emby-go 管理控制台前端 —— 单文件，无依赖 */
let token = localStorage.getItem('emby_token');
const content = document.querySelector('#content');
const titleEl = document.querySelector('#page-title');
const crumbEl = document.querySelector('#page-eyebrow');
const toasts = document.querySelector('#toasts');

const esc = value => String(value ?? '').replace(/[&<>'"]/g, char => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', "'": '&#39;', '"': '&quot;' }[char]));
const icon = name => {
  const paths = {
    refresh: '<path d="M21 12a9 9 0 1 1-2.6-6.4"/><path d="M21 3v6h-6"/>',
    trash: '<path d="M3 6h18"/><path d="M8 6V4h8v2"/><path d="M19 6l-1 14H6L5 6"/><path d="M10 11v6M14 11v6"/>',
    plus: '<path d="M12 5v14M5 12h14"/>',
    film: '<rect x="3" y="4" width="18" height="16" rx="2"/><path d="M3 9h18M8 4v5M16 4v5M8 15v5M16 15v5"/>',
    archive: '<path d="M3 4h18v5H3z"/><path d="M5 9v11h14V9"/><path d="M10 13h4"/>',
    alert: '<circle cx="12" cy="12" r="9"/><path d="M12 8v4M12 16h.01"/>',
    search: '<circle cx="11" cy="11" r="7"/><path d="m21 21-4.3-4.3"/>',
    probe: '<path d="M4 12h2.5l2-5.5 3 11 2.5-7 1.5 1.5H20"/>',
    play: '<path d="M8 5v14l11-7z"/>'
  };
  return `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">${paths[name] || ''}</svg>`;
};

function toast(message, type = 'ok') {
  const el = document.createElement('div');
  el.className = `toast ${type}`;
  el.textContent = message;
  toasts.appendChild(el);
  setTimeout(() => { el.classList.add('out'); setTimeout(() => el.remove(), 260); }, 3200);
}

// debounce 返回防抖包装函数：连续触发时只在停止触发 wait 毫秒后执行一次（用于搜索输入等高频事件）。
function debounce(fn, wait = 400) {
  let timer = null;
  return (...args) => {
    clearTimeout(timer);
    timer = setTimeout(() => { timer = null; fn(...args); }, wait);
  };
}

async function api(path, options = {}) {
  const headers = { 'X-Emby-Token': token, ...(options.headers || {}) };
  if (!(options.body instanceof FormData) && !headers['Content-Type']) headers['Content-Type'] = 'application/json';
  const response = await fetch('/api/admin' + path, { ...options, headers });
  if (response.status === 401) {
    localStorage.removeItem('emby_token');
    window.location.replace('/');
    throw new Error('登录已失效');
  }
  if (!response.ok) {
    let message = '请求失败';
    try { message = (await response.json()).error || message; } catch { /* ignore */ }
    throw new Error(message);
  }
  return response.status === 204 ? null : response.json();
}

const STATUS_TEXT = { success: '可播放', manual: '手动录入', pending: '待补录', incompatible: '不兼容', failed: '失败' };
const statusBadge = status => {
  const key = Object.prototype.hasOwnProperty.call(STATUS_TEXT, status) ? status : 'manual';
  return `<span class="badge ${esc(key)}">${esc(STATUS_TEXT[key] || status)}</span>`;
};
const protoBadge = proto => proto ? `<span class="protocol ${esc(String(proto).toLowerCase())}">${esc(proto)}</span>` : '<span class="protocol">—</span>';

function empty(title, desc) {
  return `<div class="empty">${icon('archive')}<p>${esc(title)}</p><small>${esc(desc || '')}</small></div>`;
}

function skeletonPanel(lines = 4) {
  return `<div class="panel"><div class="skeleton" style="height:18px;width:36%;margin-bottom:16px"></div>
    ${Array.from({ length: lines }, () => `<div class="skeleton" style="height:30px;margin-top:10px"></div>`).join('')}</div>`;
}

const META = {
  overview: { title: '总览', crumb: 'Archive / Overview' },
  libraries: { title: '媒体库', crumb: 'Archive / Libraries' },
  items: { title: '媒体墙', crumb: 'Archive / Wall' },
  manual: { title: '手动补录', crumb: 'Archive / Manual Ingest' },
  settings: { title: '设置', crumb: 'Archive / Settings' },
  apikeys: { title: 'API 密钥', crumb: 'Archive / API Keys' },
  scheduled: { title: '计划任务', crumb: 'Archive / Scheduled' },
  scrape: { title: '刮削', crumb: 'Archive / Scrape' },
  tasks: { title: '任务', crumb: 'Archive / Tasks' },
  probe: { title: '接口探针', crumb: 'Archive / Probe' }
};

function setMeta(name) {
  const meta = META[name] || { title: '管理后台', crumb: 'Archive' };
  titleEl.textContent = meta.title;
  crumbEl.textContent = meta.crumb;
  document.querySelectorAll('.nav-item').forEach(btn => {
    btn.classList.toggle('is-active', btn.dataset.page === name);
  });
}

/* ---------------------------------------------------------------- 总览 */
async function pageOverview() {
  const [libraries, success, status] = await Promise.all([
    api('/libraries'),
    api('/items?status=success'),
    api('/status')
  ]);
  const pendingCount = status.pending || 0;
  const incompatibleCount = status.incompatible || 0;
  const rows = [
    ['success', status.success || 0],
    ['manual', status.manual || 0],
    ['pending', pendingCount],
    ['incompatible', incompatibleCount]
  ].filter(([, n]) => n > 0);
  content.innerHTML = `
    <div class="cards">
      ${[
        ['媒体库', libraries.total ?? libraries.items?.length ?? 0, 'Collection Folder'],
        ['已入库影片', success.total || 0, 'NFO 真源 · 可见于 Emby'],
        ['待补录', pendingCount, 'http(s) 但缺 NFO'],
        ['不兼容源', incompatibleCount, 'ed2k / 其它 scheme']
      ].map(([label, value, sub]) => `<div class="card"><small>${esc(label)}</small><strong>${esc(value)}</strong><span>${esc(sub)}</span></div>`).join('')}
    </div>
    <section class="panel">
      <div class="panel-head"><h2>档案状态</h2>
        <div class="panel-actions">
          <button id="reindex" class="btn">${icon('refresh')}<span>重建索引</span></button>
        </div>
      </div>
      ${rows.length ? `
        <div class="table-wrap"><table>
          <thead><tr><th>状态</th><th style="width:80px">数量</th></tr></thead>
          <tbody>${rows.map(([status, n]) => `<tr><td>${statusBadge(status)}</td><td class="num">${n}</td></tr>`).join('')}</tbody>
        </table></div>` : empty('档案为空', '添加媒体库并开始扫描。')}
      <p class="hint">NFO 是元数据真源；仅 http/https .strm 进入 Emby。不兼容源需更换为 http(s) 后重读源。</p>
    </section>`;
  document.querySelector('#reindex').addEventListener('click', async () => {
    const btn = document.querySelector('#reindex');
    btn.disabled = true;
    try { const r = await api('/reindex', { method: 'POST' }); toast(`重建完成：${r.success} 成功 / ${r.pending} 待补录 / ${r.incompatible} 不兼容`); }
    catch (e) { toast(e.message, 'error'); }
    btn.disabled = false;
    pageOverview();
  });
}

/* ---------------------------------------------------------------- 媒体库 */
async function pageLibraries() {
  const data = await api('/libraries');
  const items = data.items || [];
  const total = data.total ?? items.length;
  content.innerHTML = `
    <section class="panel">
      <div class="panel-head"><h2>媒体库</h2><span class="hint" style="margin:0">共 ${esc(total)} 个 · 扫描/浏览均以库为单位</span></div>
      ${items.length ? `
        <div class="table-wrap"><table>
          <thead><tr><th>名称</th><th>路径</th><th class="lib-id">ID</th><th style="text-align:right">操作</th></tr></thead>
          <tbody>${items.map(item => `<tr>
            <td><strong class="title">${esc(item.Name)}</strong></td>
            <td class="mono"><span class="lib-path">${esc(item.Path)}</span></td>
            <td class="num lib-id">${esc(item.Id)}</td>
            <td><div class="row-actions">
              <button class="btn btn-sm" data-lib-scan="${esc(item.Id)}" title="仅扫描该媒体库">${icon('refresh')}<span>扫描</span></button>
              <button class="icon-btn danger" data-lib-delete="${esc(item.Id)}" title="删除媒体库索引（不删文件）">${icon('trash')}</button>
            </div></td>
          </tr>`).join('')}</tbody>
        </table></div>` : empty('还没有媒体库', '先在下方登记一个存放 .strm 的目录。')}
    </section>
    <section class="panel">
      <h2>添加媒体库</h2>
      <form id="library-form" class="field-grid">
        <div class="field"><label for="lib-name">名称</label><input id="lib-name" name="Name" placeholder="如 AV / FC2" required></div>
        <div class="field"><label for="lib-path">目录路径</label><input id="lib-path" name="Path" placeholder="服务器上的绝对路径" required></div>
        <div class="form-foot" style="grid-column:1/-1;margin:2px 0 0"><button class="btn btn-accent">${icon('plus')}<span>添加</span></button></div>
      </form>
    </section>`;
  document.querySelector('#library-form').addEventListener('submit', async event => {
    event.preventDefault();
    const body = Object.fromEntries(new FormData(event.target));
    try {
      const lib = await api('/libraries', { method: 'POST', body: JSON.stringify(body) });
      toast(`已添加媒体库「${lib.Name}」`);
      pageLibraries();
    } catch (e) { toast(e.message, 'error'); }
  });
  document.querySelectorAll('[data-lib-scan]').forEach(button => button.addEventListener('click', () => runScan(button.dataset.libScan)));
  document.querySelectorAll('[data-lib-delete]').forEach(button => button.addEventListener('click', async () => {
    if (!confirm('删除该媒体库及其影片索引？不会删除磁盘文件，但该库影片的播放进度/收藏会一并清除。')) return;
    try { await api('/libraries/' + button.dataset.libDelete, { method: 'DELETE' }); toast('媒体库已删除', 'ok'); pageLibraries(); }
    catch (e) { toast(e.message, 'error'); }
  }));
}

/* ---------------------------------------------------------------- 媒体墙 */
function wallCard(item, ud) {
  const st = item.Status || item.status || '';
  const title = item.Title || '';
  const playable = st === 'success' || st === 'manual';
  const poster = item.PosterPath ? `/Items/${item.id}/Images/Primary`
    : (item.LandscapePath ? `/Items/${item.id}/Images/Thumb` : '');
  const sub = [item.Number, item.Year, item.OriginalTitle].filter(Boolean).join(' · ')
    || (item.source_protocol || item.SourceProtocol || '');
  const progress = ud && item.RuntimeSeconds > 0 && ud.position_ticks > 0
    ? Math.min(100, Math.round(ud.position_ticks / (item.RuntimeSeconds * 10000000) * 100)) : 0;
  const badges = [
    st !== 'success' ? `<span class="wall-badge ${esc(st)}">${esc(STATUS_TEXT[st] || st)}</span>` : '',
    ud && ud.played ? '<span class="wall-badge played">已看</span>' : '',
    ud && ud.favorite ? '<span class="wall-badge fav">♥</span>' : '',
    (item.AdditionalParts || []).length ? `<span class="wall-badge multi">CD×${(item.AdditionalParts || []).length + 1}</span>` : ''
  ].join('');
  const body = poster
    ? `<img loading="lazy" src="${esc(poster)}" alt="">`
    : `<span class="wall-path" title="${esc(item.source_path)}">${esc(item.source_path || title || '—')}</span>`;
  const label = title || String(item.source_path || '').split(/[\\/]/).pop() || '—';
  const a11y = ` tabindex="0" role="button" aria-label="查看 ${esc(label)} 详情"`;
  return `
    <article class="wall-card" data-play="${item.id}"${a11y}>
      <div class="wall-poster">
        ${body}
        <div class="wall-badges">${badges}</div>
        ${progress ? `<div class="wall-progress"><i style="width:${progress}%"></i></div>` : ''}
        <div class="wall-actions">
          <button class="icon-btn" data-probe="${item.id}" title="探测媒体信息（ffprobe，写回 NFO）">${icon('probe')}</button>
          <button class="icon-btn" data-reread="${item.id}" title="重读 .strm 与 NFO">${icon('refresh')}</button>
          <button class="icon-btn danger" data-delete="${item.id}" title="删除索引（不删文件）">${icon('trash')}</button>
        </div>
        ${playable ? `<button class="wall-play" data-quickplay="${item.id}" title="直接播放">${icon('play')}</button>` : ''}
      </div>
      <div class="wall-meta">
        <strong title="${esc(title || item.source_path)}">${esc(label)}</strong>
        <small>${esc(sub)}</small>
      </div>
    </article>`;
}

const WALL_PAGE_SIZE = 100;
let wallState = null;
let wallGen = 0; // 列表请求代号，单调递增；用于丢弃被新搜索/筛选作废的过期响应

async function pageItems() {
  const params = new URLSearchParams(location.search);
  const status = params.get('status') || '';
  const search = params.get('search') || '';
  const sort = params.get('sort') || 'datecreated';
  const order = params.get('order') || (sort === 'title' ? 'asc' : 'desc');
  // 库筛选：URL 里的 library_id 若指向已删除的库，回退「全部」并同步清洗 URL，
  // 避免刷新后一直停在空列表（旧书签/前进后退同样适用）。
  const libraries = ((await api('/libraries')).items || []);
  const requestedLib = params.get('library_id') || '';
  const libraryID = libraries.some(lib => String(lib.Id) === requestedLib) ? requestedLib : '';
  if (requestedLib && !libraryID) {
    params.delete('library_id');
    history.replaceState({}, '', location.pathname + (params.toString() ? '?' + params.toString() : '') + location.hash);
  }
  const activeLib = libraries.find(lib => String(lib.Id) === libraryID);
  // 实体筛选（详情抽屉里点演员/类型/厂商/合集跳过来的），单值，URL 可见且可一键清除。
  const scrape = params.get('scrape') || '';
  const entity = ENTITY_PARAMS.map(([key, label]) => [key, label, params.get(key) || '']).find(item => item[2]) || null;
  wallState = {
    status, search, sort, order, libraryID, libraryName: activeLib ? activeLib.Name : '',
    scrape, entity, offset: 0, total: 0, loading: false, done: false, items: [], userdata: {}
  };
  wallGen += 1;

  const pills = [['', '全部'], ['success', '可播放'], ['manual', '手动'], ['pending', '待补录'], ['incompatible', '不兼容']];
  const sorts = [['datecreated', '最近入库'], ['title', '标题'], ['year', '年份'], ['communityrating', '评分']];
  // 库少时平铺成 Tab，库多（>8）转下拉，避免筛选行被撑爆。
  const libFilter = libraries.length <= 8
    ? `<button class="pill ${libraryID === '' ? 'is-active' : ''}" data-lib="">全部库</button>` +
      libraries.map(lib => `<button class="pill ${libraryID === String(lib.Id) ? 'is-active' : ''}" data-lib="${esc(lib.Id)}">${esc(lib.Name)}</button>`).join('')
    : `<select id="item-lib"><option value="">全部库</option>${libraries.map(lib =>
        `<option value="${esc(lib.Id)}" ${libraryID === String(lib.Id) ? 'selected' : ''}>${esc(lib.Name)}</option>`).join('')}</select>`;
  content.innerHTML = `
    <section class="panel">
      <div class="panel-head">
        <h2>媒体墙</h2>
        <div class="panel-actions">
          <span class="hint" id="wall-count" style="margin:0"></span>
          <button class="btn" id="probe-media" title="调用系统 ffprobe 读取码率/分辨率/编码等真实参数并写回 NFO（与扫库相互独立）">${icon('probe')}<span>探测媒体信息</span></button>
        </div>
      </div>
      ${libraries.length ? `<div class="filters">${libFilter}</div>` : ''}
      ${entity ? `<div class="filters"><span class="filter-chip">${esc(entity[1])}：${esc(entity[2])}
        <button class="chip-x" id="entity-clear" title="清除该筛选">✕</button></span></div>` : ''}
      <div class="filters">
        ${pills.map(([value, label]) => `<button class="pill ${status === value ? 'is-active' : ''}" data-status="${value}">${esc(label)}</button>`).join('')}
        <button class="pill ${scrape === 'failed' ? 'is-active' : ''}" data-scrape="failed">刮削失败</button>
        <button class="pill ${scrape === 'confirm' ? 'is-active' : ''}" data-scrape="confirm">待人工确认</button>
        <input id="item-search" placeholder="搜索标题 / 番号 / 原名" value="${esc(search)}" style="min-width:200px">
        <select id="item-sort">${sorts.map(([value, label]) => `<option value="${value}" ${sort === value ? 'selected' : ''}>${esc(label)}</option>`).join('')}</select>
      </div>
      <div id="probe-progress" class="probe-progress" hidden>
        <div class="scan-progress-head"><strong id="probe-progress-title">探测中…</strong><span id="probe-progress-count"></span></div>
        <div class="scan-progress-bar"><i id="probe-progress-fill"></i></div>
        <small id="probe-progress-detail"></small>
      </div>
      <div class="wall" id="wall"></div>
      <div id="wall-empty"></div>
      <div id="wall-more" class="wall-more"></div>
      <p class="hint">点击海报打开详情抽屉（抽屉内可播放、刮削、探测）；海报上的播放图标为快捷播放。不兼容源不会进入 Emby，更换为 http(s) .strm 后「重读源」可重新判定。</p>
    </section>`;

  // 离散筛选（状态/库/排序）压入历史，浏览器前进/后退可逐步回退到上一个筛选组合。
  // 始终保留 hash，否则一次筛选就会让「刷新」离开媒体墙页。
  const queryURL = qs => location.pathname + (qs.toString() ? '?' + qs.toString() : '') + location.hash;
  const pushQuery = qs => history.pushState({}, '', queryURL(qs));
  const replaceQuery = qs => history.replaceState({}, '', queryURL(qs));
  document.querySelectorAll('.pill[data-status]').forEach(button => button.addEventListener('click', () => {
    const qs = new URLSearchParams(location.search);
    const value = button.dataset.status;
    if (value) qs.set('status', value); else qs.delete('status');
    qs.delete('search');
    pushQuery(qs);
    pageItems();
  }));
  // 刮削结果筛选：与其它条件叠加，点第二次取消。
  document.querySelectorAll('.pill[data-scrape]').forEach(button => button.addEventListener('click', () => {
    const qs = new URLSearchParams(location.search);
    const value = button.dataset.scrape;
    if (qs.get('scrape') === value) qs.delete('scrape'); else qs.set('scrape', value);
    pushQuery(qs);
    pageItems();
  }));
  // 库筛选 Tab：与状态/搜索/排序叠加，只动 library_id 一个参数。
  document.querySelectorAll('.pill[data-lib]').forEach(button => button.addEventListener('click', () => {
    const qs = new URLSearchParams(location.search);
    const value = button.dataset.lib;
    if (value) qs.set('library_id', value); else qs.delete('library_id');
    pushQuery(qs);
    pageItems();
  }));
  const libSelect = document.querySelector('#item-lib');
  if (libSelect) libSelect.addEventListener('change', () => {
    const qs = new URLSearchParams(location.search);
    if (libSelect.value) qs.set('library_id', libSelect.value); else qs.delete('library_id');
    pushQuery(qs);
    pageItems();
  });
  const searchInput = document.querySelector('#item-search');
  searchInput.addEventListener('input', debounce(() => {
    const q = new URLSearchParams(location.search);
    const value = searchInput.value.trim();
    if (value) q.set('search', value); else q.delete('search');
    // 搜索输入用 replace：逐字输入不该产生大量历史记录。
    replaceQuery(q);
    wallState.search = value;
    reloadWall();
  }));
  const sortSelect = document.querySelector('#item-sort');
  if (sortSelect) sortSelect.addEventListener('change', () => {
    const q = new URLSearchParams(location.search);
    q.set('sort', sortSelect.value);
    q.delete('order');
    pushQuery(q);
    pageItems();
  });
  const entityClear = document.querySelector('#entity-clear');
  if (entityClear) entityClear.addEventListener('click', () => {
    const qs = new URLSearchParams(location.search);
    ENTITY_PARAMS.forEach(([key]) => qs.delete(key));
    pushQuery(qs);
    pageItems();
  });
  const probeButton = document.querySelector('#probe-media');
  if (probeButton) probeButton.addEventListener('click', runProbeAll);
  // 事件委托：卡片按页追加，统一在容器上处理，避免每页重新绑定。
  document.querySelector('#wall').addEventListener('click', async event => {
    const probe = event.target.closest('[data-probe]');
    if (probe) {
      event.stopPropagation();
      probeItem(probe.dataset.probe);
      return;
    }
    const reread = event.target.closest('[data-reread]');
    if (reread) {
      event.stopPropagation();
      try { const r = await api('/items/' + reread.dataset.reread + '/reread', { method: 'POST' }); toast(`状态已更新：${STATUS_TEXT[r.status] || r.status}`); pageItems(); }
      catch (e) { toast(e.message, 'error'); }
      return;
    }
    const remove = event.target.closest('[data-delete]');
    if (remove) {
      event.stopPropagation();
      if (!confirm('仅删除数据库索引，不删除源文件。继续？')) return;
      try { await api('/items/' + remove.dataset.delete, { method: 'DELETE' }); toast('已删除索引', 'ok'); pageItems(); }
      catch (e) { toast(e.message, 'error'); }
      return;
    }
    // hover 播放图标 = 快捷播放；点卡片其它位置 = 打开详情抽屉。
    const quick = event.target.closest('[data-quickplay]');
    if (quick) {
      event.stopPropagation();
      const target = wallState.items.find(m => String(m.id) === quick.dataset.quickplay);
      if (target) openPlayer(target);
      return;
    }
    const card = event.target.closest('.wall-card');
    if (!card) return;
    const item = wallState.items.find(m => String(m.id) === card.dataset.play);
    if (item) openDetail(item);
  });
  // 键盘可达：卡片获得焦点后回车/空格打开详情（桌面端无障碍）。
  document.querySelector('#wall').addEventListener('keydown', event => {
    if (event.key !== 'Enter' && event.key !== ' ') return;
    const card = event.target.closest('.wall-card');
    if (!card) return;
    event.preventDefault();
    const item = wallState.items.find(m => String(m.id) === card.dataset.play);
    if (item) openDetail(item);
  });
  await loadWallPage();
}

// reloadWall 只重置列表并重新拉取，保留筛选栏与搜索框焦点（搜索输入走此路径，避免整页重渲染打断输入）。
function reloadWall() {
  if (!wallState) return;
  wallGen += 1; // 作废尚未返回的旧请求
  wallState.offset = 0;
  wallState.total = 0;
  wallState.items = [];
  wallState.userdata = {};
  wallState.done = false;
  wallState.loading = false;
  const wall = document.querySelector('#wall');
  if (wall) wall.innerHTML = '';
  const emptyBox = document.querySelector('#wall-empty');
  if (emptyBox) emptyBox.innerHTML = '';
  const more = document.querySelector('#wall-more');
  if (more) more.textContent = '';
  loadWallPage();
}

// wallMaybeLoadMore 在哨兵接近视口时加载下一页（无限滚动）。
function wallMaybeLoadMore() {
  if (!wallState || wallState.loading || wallState.done) return;
  const more = document.querySelector('#wall-more');
  if (!more) return;
  if (more.getBoundingClientRect().top <= window.innerHeight + 600) loadWallPage();
}

async function loadWallPage() {
  if (!wallState || wallState.loading || wallState.done) return;
  wallState.loading = true;
  const gen = wallGen;
  const more = document.querySelector('#wall-more');
  if (more) more.textContent = '加载中…';
  try {
    const query = new URLSearchParams({ limit: String(WALL_PAGE_SIZE), offset: String(wallState.offset), sort: wallState.sort, order: wallState.order });
    if (wallState.status) query.set('status', wallState.status);
    if (wallState.search) query.set('search', wallState.search);
    if (wallState.libraryID) query.set('library_id', wallState.libraryID);
    if (wallState.entity) query.set(wallState.entity[0], wallState.entity[2]);
    if (wallState.scrape) query.set('scrape', wallState.scrape);
    const data = await api('/items?' + query.toString());
    if (!wallState || wallGen !== gen) return; // 已被新的搜索/筛选作废，丢弃过期响应
    wallState.total = data.total;
    Object.assign(wallState.userdata, data.userdata || {});
    const items = data.items || [];
    wallState.items.push(...items);
    wallState.offset += items.length;
    wallState.done = items.length === 0 || wallState.offset >= data.total;
    if (items.length) {
      document.querySelector('#wall').insertAdjacentHTML('beforeend',
        items.map(item => wallCard(item, wallState.userdata[String(item.id)])).join(''));
    }
    const count = document.querySelector('#wall-count');
    const scope = [
      wallState.libraryName,
      wallState.entity ? `${wallState.entity[1]}：${wallState.entity[2]}` : '',
      wallState.status ? (STATUS_TEXT[wallState.status] || wallState.status) : ''
    ].filter(Boolean).join(' · ');
    if (count) count.textContent = `共 ${data.total} 条${scope ? ' · ' + scope : ''} · 已加载 ${wallState.items.length}`;
    const emptyBox = document.querySelector('#wall-empty');
    if (emptyBox) {
      const what = wallState.entity ? `「${wallState.entity[2]}」的影片`
        : (wallState.libraryName ? `「${wallState.libraryName}」的影片` : '影片');
      emptyBox.innerHTML = data.total === 0
        ? empty(wallState.status ? `没有 ${STATUS_TEXT[wallState.status] || wallState.status} 的${what}` : `没有符合条件的${what}`,
            wallState.search ? '试试其它关键词。' : '开始扫描或手动补录后再来看看。')
        : '';
    }
    if (more) more.textContent = wallState.done && data.total > 0 ? `已全部加载（共 ${data.total} 条）` : '';
  } catch (error) {
    if (!wallState || wallGen !== gen) return;
    if (more) more.textContent = '加载失败：' + (error.message || error);
  } finally {
    if (wallState && wallGen === gen) {
      wallState.loading = false;
      // 首屏未填满时继续加载，直到撑满视口。
      wallMaybeLoadMore();
    }
  }
}

/* ------------------------------------------------------------ 详情抽屉 */
// 实体筛选参数：抽屉里点演员/类型/厂商/合集会带着其中一个参数回到媒体墙。
const ENTITY_PARAMS = [['person', '演员'], ['genre', '类型'], ['studio', '厂商'], ['collection', '合集']];

const detailOverlay = document.querySelector('#detail-overlay');
const detailDrawer = document.querySelector('#detail-drawer');

function closeDetail() {
  detailOverlay.hidden = true;
  detailDrawer.innerHTML = '';
}

// gotoEntity 跳到媒体墙并按该实体过滤（单值筛选，URL 可见、抽屉关闭）。
function gotoEntity(key, value) {
  const qs = new URLSearchParams();
  ENTITY_PARAMS.forEach(([k]) => qs.delete(k));
  qs.set(key, value);
  if (wallState && wallState.libraryID) qs.set('library_id', wallState.libraryID);
  history.pushState({}, '', location.pathname + '?' + qs.toString() + '#items');
  closeDetail();
  pageItems();
}

const fmtSize = bytes => {
  if (!bytes) return '—';
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  let value = bytes, unit = 0;
  while (value >= 1024 && unit < units.length - 1) { value /= 1024; unit += 1; }
  return `${value.toFixed(value < 10 && unit > 0 ? 1 : 0)} ${units[unit]}`;
};

const fmtDuration = seconds => {
  if (!seconds) return '';
  const total = Math.round(seconds);
  const h = Math.floor(total / 3600);
  const m = Math.floor((total % 3600) / 60);
  return h ? `${h} 小时 ${m} 分` : `${m} 分钟`;
};

// streamTable 把 Emby 的 MediaStream 数组渲染成表格（视频/音频/字幕分节）。
function streamTable(streams) {
  if (!streams || !streams.length) return '<p class="hint" style="margin:8px 0 0">未探测</p>';
  const rows = streams.map(s => {
    const type = { Video: '视频', Audio: '音频', Subtitle: '字幕' }[s.Type] || s.Type || '—';
    const detail = s.Type === 'Video'
      ? [s.Codec && String(s.Codec).toUpperCase(), s.Width && s.Height ? `${s.Width}×${s.Height}` : '',
         s.BitRate ? `${Math.round(s.BitRate / 1000)} kbps` : '', s.Profile, s.BitDepth ? `${s.BitDepth}bit` : '']
      : [s.Codec && String(s.Codec).toUpperCase(), s.ChannelLayout || (s.Channels ? `${s.Channels}ch` : ''),
         s.BitRate ? `${Math.round(s.BitRate / 1000)} kbps` : '', s.Language, s.SampleRate ? `${s.SampleRate} Hz` : ''];
    return `<tr><td class="mono">#${esc(s.Index ?? '')}</td><td>${esc(type)}</td>
      <td class="mono">${esc(detail.filter(Boolean).join(' · ') || '—')}</td>
      <td class="mono">${esc([s.IsDefault ? '默认' : '', s.IsForced ? '强制' : '', s.IsExternal ? '外挂' : ''].filter(Boolean).join(' ') || '—')}</td></tr>`;
  }).join('');
  return `<div class="table-wrap"><table>
    <thead><tr><th>#</th><th>类型</th><th>参数</th><th>标记</th></tr></thead>
    <tbody>${rows}</tbody></table></div>`;
}

function detailMetaRow(label, value, key) {
  if (!value) return '';
  const body = Array.isArray(value)
    ? value.filter(Boolean).map(v => (key ? `<button class="entity-link" data-entity-key="${esc(key)}" data-entity-value="${esc(v)}">${esc(v)}</button>` : esc(v))).join(' ')
    : (key ? `<button class="entity-link" data-entity-key="${esc(key)}" data-entity-value="${esc(value)}">${esc(value)}</button>` : esc(value));
  return `<div class="detail-meta-row"><dt>${esc(label)}</dt><dd>${body}</dd></div>`;
}

async function openDetail(item) {
  detailDrawer.innerHTML = '<div class="detail-body"><p class="hint">加载中…</p></div>';
  detailOverlay.hidden = false;
  let data;
  try {
    data = await api(`/items/${item.id}/detail`);
  } catch (error) {
    detailDrawer.innerHTML = `<div class="detail-body"><p class="hint">${esc(error.message || error)}</p></div>`;
    return;
  }
  // 注意：store.Movie 的 JSON 字段名大小写并不统一——只有少数几个字段带小写下划线 tag
  // （id / collection / official_rating / source_path / source_protocol / created_at …），
  // 其余保持 Go 字段名。取值时按接口实际返回的键来写。
  const m = data.movie || {};
  const title = m.Title || String(item.id);
  const st = m.Status || '';
  const playable = st === 'success' || st === 'manual';
  const poster = m.PosterPath ? `/Items/${m.id}/Images/Primary` : '';
  const backdrop = m.BackdropPath ? `/Items/${m.id}/Images/Backdrop` : (m.LandscapePath ? `/Items/${m.id}/Images/Thumb` : poster);
  const head = [m.Number, m.Year, fmtDuration(m.RuntimeSeconds), m.Rating ? `★ ${m.Rating}` : ''].filter(Boolean).join(' · ');
  const actors = data.actors || [];

  detailDrawer.innerHTML = `
    <div class="detail-hero">
      ${backdrop ? `<img class="detail-backdrop" src="${esc(backdrop)}" alt="">` : ''}
      <button id="detail-close" class="icon-btn detail-close" title="关闭 (Esc)">✕</button>
      <div class="detail-hero-body">
        ${poster ? `<img class="detail-poster" src="${esc(poster)}" alt="">` : ''}
        <div class="detail-hero-text">
          <h2>${esc(title)}</h2>
          <p class="detail-sub">${esc(head || '—')}</p>
          <div class="detail-badges">${statusBadge(st)}${m.collection ? `<span class="badge manual">合集 ${esc(m.collection)}</span>` : ''}</div>
        </div>
      </div>
    </div>
    <div class="detail-body">
      <div class="detail-actions">
        ${playable
          ? `<button id="detail-play" class="btn btn-accent">${icon('play')}<span>播放</span></button>`
          : `<span class="hint" style="margin:0">该影片不可播放（待补录 / 协议不兼容）</span>`}
        <button id="detail-probe" class="btn">${icon('probe')}<span>探测媒体信息</span></button>
        <button id="detail-scrape" class="btn">${icon('search')}<span>刮削</span></button>
        <button id="detail-reread" class="btn">${icon('refresh')}<span>重读源</span></button>
      </div>

      ${m.Plot ? `<section class="detail-section"><h3>简介</h3><p class="detail-plot">${esc(m.Plot)}</p></section>` : ''}

      <section class="detail-section"><h3>元数据</h3>
        <dl class="detail-meta">
          ${detailMetaRow('番号', m.Number)}
          ${detailMetaRow('原名', m.OriginalTitle)}
          ${detailMetaRow('年份', m.Year ? String(m.Year) : '')}
          ${detailMetaRow('分级', m.official_rating)}
          ${detailMetaRow('类型', m.Genres, 'genre')}
          ${detailMetaRow('标签', m.Tags, 'tag')}
          ${detailMetaRow('厂商', m.Studios && m.Studios.length ? m.Studios : (m.Maker || m.Label), 'studio')}
          ${detailMetaRow('导演', m.Director)}
          ${detailMetaRow('合集', m.collection, 'collection')}
          ${detailMetaRow('媒体库', data.library_name)}
          ${detailMetaRow('来源', m.source_protocol ? String(m.source_protocol).toUpperCase() : '')}
        </dl>
      </section>

      <section class="detail-section"><h3>演员 <small>${actors.length}</small></h3>
        ${actors.length ? `<div class="avatar-grid">${actors.map(actor => `
          <button class="avatar-card" data-entity-key="person" data-entity-value="${esc(actor.name)}">
            ${actor.has_image
              ? `<img loading="lazy" src="/Items/${esc(entityIdOf('person', actor.name))}/Images/Primary?maxWidth=200${actor.image_tag ? '&tag=' + esc(actor.image_tag) : ''}" alt="">`
              : `<span class="avatar-fallback">${esc((actor.name || '?').trim().slice(0, 1))}</span>`}
            <small>${esc(actor.name)}</small>
          </button>`).join('')}</div>`
          : '<p class="hint" style="margin:8px 0 0">NFO 中没有演员信息。</p>'}
      </section>

      <section class="detail-section"><h3>媒体信息 <small>${(data.files || []).length} 个文件</small></h3>
        ${(data.files || []).map(file => `
          <div class="detail-file">
            <div class="detail-file-head">
              <strong>${file.role === 'main' ? '主文件' : `分段 ${file.index - 1}`}</strong>
              <span class="mono">${esc(file.name)}</span>
              <span class="hint" style="margin:0">${esc(fmtSize(file.size))}</span>
              ${file.probed ? '' : '<span class="badge pending">未探测</span>'}
            </div>
            ${streamTable(file.streams)}
            <p class="detail-path mono">${esc(file.path)}</p>
          </div>`).join('')}
      </section>

      <section class="detail-section"><h3>文件与时间</h3>
        <dl class="detail-meta">
          ${detailMetaRow('源文件', m.source_path)}
          ${detailMetaRow('NFO', m.NFOPath)}
          ${detailMetaRow('最后修改', fmtTime(data.modified_at))}
          ${detailMetaRow('入库时间', fmtTime(m.created_at))}
          ${detailMetaRow('上次刮削', fmtTime(m.last_scrape_at))}
          ${detailMetaRow('刮削结果', m.last_scrape_error)}
        </dl>
      </section>
    </div>`;

  document.querySelector('#detail-close').addEventListener('click', closeDetail);
  const playBtn = document.querySelector('#detail-play');
  if (playBtn) playBtn.addEventListener('click', () => { closeDetail(); openPlayer(item); });
  document.querySelector('#detail-probe').addEventListener('click', async () => {
    await probeItem(m.id);
    openDetail(item);
  });
  // 单条刮削走「预览 → 人工确认」，取消则零写入零请求（需求 5j）。
  document.querySelector('#detail-scrape').addEventListener('click', () => openScrapePreview(item));
  document.querySelector('#detail-reread').addEventListener('click', async () => {
    try {
      const r = await api(`/items/${m.id}/reread`, { method: 'POST' });
      toast(`状态已更新：${STATUS_TEXT[r.status] || r.status}`);
      closeDetail();
      pageItems();
    } catch (e) { toast(e.message, 'error'); }
  });
  // 实体跳转：统一委托，演员卡片与元数据里的类型/标签/厂商/合集共用一条路径。
  detailDrawer.querySelectorAll('[data-entity-key]').forEach(el => el.addEventListener('click', () => {
    gotoEntity(el.dataset.entityKey, el.dataset.entityValue);
  }));
}

// entityIdOf 生成与后端一致的虚拟实体 id（小写类型 + base64url 名称）。
function entityIdOf(kind, name) {
  const bytes = new TextEncoder().encode(name);
  let binary = '';
  bytes.forEach(byte => { binary += String.fromCharCode(byte); });
  return `${kind}:${btoa(binary).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')}`;
}

/* ---------------------------------------------------------------- 在线播放 */
let artInstance = null;

function closePlayer() {
  if (artInstance) { try { artInstance.destroy(); } catch { /* ignore */ } artInstance = null; }
  const overlay = document.querySelector('#player-overlay');
  if (overlay) overlay.hidden = true;
  document.body.classList.remove('player-open');
}

function openPlayer(item) {
  const overlay = document.querySelector('#player-overlay');
  const title = item.Title || String(item.source_path || '').split(/[\\/]/).pop() || '播放';
  document.querySelector('#player-title').textContent = title;
  overlay.hidden = false;
  document.body.classList.add('player-open');
  const stage = document.querySelector('#player-stage');
  stage.innerHTML = '';
  const container = String(item.source_container || item.SourceContainer || '').toLowerCase();
  const type = container === 'webm' ? 'webm' : 'mp4';
  let usedProxy = false;
  artInstance = new Artplayer({
    container: stage,
    url: '/Videos/' + item.id + '/stream',
    type,
    title,
    autoplay: true,
    volume: 1,
    playbackRate: true,
    aspectRatio: true,
    fullscreen: true,
    fullscreenWeb: true,
    setting: true,
    hotkey: true,
    pip: true,
    theme: '#e0a44b',
    lang: 'zh-cn'
  });
  // 直链 302 在 HTTPS 页面可能被混合内容/跨域拦截；失败时回退服务端代理端点。
  artInstance.on('error', () => {
    if (usedProxy) { toast('播放失败：浏览器可能不支持该编码（如 H.265/MKV）', 'error'); return; }
    usedProxy = true;
    artInstance.url = '/Videos/' + item.id + '/proxy';
    // 回退发生在 error 回调里（非用户手势），移动端需静音才允许自动播放。
    artInstance.muted = true;
    artInstance.play().catch(() => { /* 用户可手动点播放 */ });
  });
}

/* ---------------------------------------------------------------- 手动补录 */
async function pageManual() {
  const libs = (await api('/libraries')).items || [];
  content.innerHTML = `
    <section class="panel">
      <div class="panel-head"><h2>手动补录</h2><span class="hint" style="margin:0">写入 .strm + 生成 NFO，即时进入 Emby</span></div>
      <p>为一条 <code>http(s)</code> 直链登记影片：系统会在此服务器上写入源文件、同目录 NFO，并把记录标记为「可播放」。源地址必须为 http/https。</p>
      <form id="manual-form" class="field-grid">
        <div class="field"><label for="m-title">标题 *</label><input id="m-title" name="title" placeholder="展示标题" required></div>
        <div class="field"><label for="m-number">番号</label><input id="m-number" name="number" placeholder="如 ABF-018"></div>
        <div class="field"><label for="m-year">年份</label><input id="m-year" name="year" type="number" min="1900" max="2100" placeholder="2024"></div>
        <div class="field full"><label for="m-original">原名</label><input id="m-original" name="original_title" placeholder="日文原名（可选）"></div>
        <div class="field full"><label for="m-path">.strm 目标路径 *</label><input id="m-path" name="source_path" placeholder="服务器上源文件绝对路径，如 /data/media/AV/A/ABF-018/ABF-018.strm" required></div>
        <div class="field full"><label for="m-url">媒体直链（http/https）*</label><input id="m-url" name="source_url" type="url" placeholder="https://…/ABF-018.mp4" required></div>
        <div class="field full"><label for="m-plot">简介</label><textarea id="m-plot" name="plot" placeholder="影片简介 / 剧情（可选）"></textarea></div>
        <div class="field"><label for="m-genres">类型（逗号分隔）</label><input id="m-genres" name="genres" placeholder="剧情, 偶像"></div>
        <div class="field"><label for="m-tags">标签</label><input id="m-tags" name="tags" placeholder="标签1, 标签2"></div>
        <div class="field"><label for="m-studios">制作商</label><input id="m-studios" name="studios" placeholder="制作商"></div>
        <div class="form-foot" style="grid-column:1/-1">
          <button id="manual-submit" class="btn btn-accent">${icon('film')}<span>写入并入库</span></button>
          ${libs.length ? `<span class="hint" style="margin:0">将归入媒体库：${esc(libs[0].Name)}</span>` : ''}
        </div>
      </form>
    </section>`;
  document.querySelector('#manual-form').addEventListener('submit', async event => {
    event.preventDefault();
    const form = event.target;
    const fd = new FormData(form);
    const split = key => String(fd.get(key) || '').split(/[,，]/).map(s => s.trim()).filter(Boolean);
    const body = {
      source_path: fd.get('source_path'),
      source_url: fd.get('source_url'),
      title: fd.get('title'),
      number: fd.get('number'),
      original_title: fd.get('original_title'),
      plot: fd.get('plot'),
      genres: split('genres'),
      tags: split('tags'),
      studios: split('studios'),
      year: Number(fd.get('year')) || 0
    };
    const submitBtn = document.querySelector('#manual-submit');
    submitBtn.disabled = true;
    try {
      await api('/items/manual', { method: 'POST', body: JSON.stringify(body) });
      toast(`「${body.title}」已入库`);
      form.reset();
    } catch (e) { toast(e.message, 'error'); }
    submitBtn.disabled = false;
  });
}

/* ---------------------------------------------------------------- 设置 */
async function pageSettings() {
  const s = await api('/settings');
  const fields = [
    ['监听地址', s.listen],
    ['数据库', s.db_path],
    ['缓存后端', s.cache + (s.redis_addr ? ` · ${s.redis_addr}/${s.redis_db}` : '')],
    ['Redis 在线', s.redis_online ? '是' : '否']
  ];
  content.innerHTML = `
    <section class="panel">
      <div class="panel-head"><h2>服务设置</h2></div>
      <div class="table-wrap"><table>
        <tbody>${fields.map(([k, v]) => `<tr><th style="width:160px">${esc(k)}</th><td class="mono">${esc(v ?? '—')}</td></tr>`).join('')}</tbody>
      </table></div>
      <p class="hint">配置修改后需重启服务生效。浏览海报墙请通过 Emby 客户端连接本服务（System/Info/Public 的 ServerId 用于标识实例）。</p>
    </section>`;
}

/* ---------------------------------------------------------------- API 密钥 */
async function pageAPIKeys() {
  const data = await api('/apikeys');
  const items = data.items || [];
  content.innerHTML = `
    <section class="panel">
      <div class="panel-head"><h2>API 密钥</h2><span class="hint" style="margin:0">共 ${esc(data.total ?? items.length)} 个 · 供脚本/第三方客户端直接调用 Emby API</span></div>
      ${items.length ? `
        <div class="table-wrap"><table>
          <thead><tr><th>名称</th><th>密钥</th><th>创建时间</th><th style="text-align:right">操作</th></tr></thead>
          <tbody>${items.map(item => `<tr>
            <td><strong class="title">${esc(item.name || '—')}</strong></td>
            <td class="mono">${esc(item.key)}</td>
            <td class="mono">${esc(item.created_at || '—')}</td>
            <td><div class="row-actions">
              <button class="btn btn-sm" data-copy="${esc(item.key)}">复制</button>
              <button class="icon-btn danger" data-key-delete="${esc(item.key)}" title="删除密钥">${icon('trash')}</button>
            </div></td>
          </tr>`).join('')}</tbody>
        </table></div>` : empty('还没有 API 密钥', '在下方创建后即可用 X-Emby-Token 调用接口。')}
    </section>
    <section class="panel">
      <h2>创建密钥</h2>
      <form id="apikey-form" class="field-grid">
        <div class="field"><label for="key-name">名称</label><input id="key-name" name="name" placeholder="如 yamby / 脚本" required></div>
        <div class="form-foot" style="grid-column:1/-1;margin:2px 0 0"><button class="btn btn-accent">${icon('plus')}<span>创建</span></button></div>
      </form>
      <p class="hint">调用示例：<code>curl -H "X-Emby-Token: &lt;密钥&gt;" http://host:18080/Users/1/Views</code>，也可用 <code>?api_key=&lt;密钥&gt;</code>。密钥即凭据，请勿外泄。</p>
    </section>`;
  document.querySelector('#apikey-form').addEventListener('submit', async event => {
    event.preventDefault();
    const name = String(new FormData(event.target).get('name') || '').trim();
    try { await api('/apikeys', { method: 'POST', body: JSON.stringify({ name }) }); toast('密钥已创建'); pageAPIKeys(); }
    catch (e) { toast(e.message, 'error'); }
  });
  document.querySelectorAll('[data-copy]').forEach(button => button.addEventListener('click', async () => {
    try { await navigator.clipboard.writeText(button.dataset.copy); toast('已复制密钥', 'ok'); }
    catch { toast('复制失败，请手动选择', 'error'); }
  }));
  document.querySelectorAll('[data-key-delete]').forEach(button => button.addEventListener('click', async () => {
    if (!confirm('删除该 API 密钥？使用它的客户端将立即失效。')) return;
    try { await api('/apikeys/' + encodeURIComponent(button.dataset.keyDelete), { method: 'DELETE' }); toast('密钥已删除', 'ok'); pageAPIKeys(); }
    catch (e) { toast(e.message, 'error'); }
  }));
}

/* ---------------------------------------------------------------- 任务 */
async function pageTasks() {
  const data = await api('/tasks');
  const running = data.running;
  const items = data.items || [];
  const runLabel = s => RUN_STATUS_TEXT[s] || s;
  content.innerHTML = `
    <section class="panel">
      <div class="panel-head"><h2>任务日志</h2>
        <div class="panel-actions">
          <button id="task-refresh" class="btn btn-sm">刷新</button>
          <button id="task-scan" class="btn btn-accent btn-sm">${icon('film')}<span>立即扫描</span></button>
        </div>
      </div>
      ${running ? '<p class="hint" style="color:var(--accent)">有任务正在进行…</p>' : ''}
      ${items.length ? `
        <div class="table-wrap"><table>
          <thead><tr><th>类型</th><th>状态</th><th>开始时间</th><th>结束时间</th><th>错误</th></tr></thead>
          <tbody>${items.map(item => `
            <tr>
              <td>${esc(TASK_TYPE_TEXT[item.type] || item.type)}</td>
              <td><span class="badge ${esc(item.status)}">${esc(runLabel(item.status))}</span></td>
              <td class="mono">${esc(fmtTime(item.started_at))}</td>
              <td class="mono">${esc(fmtTime(item.ended_at))}</td>
              <td class="mono">${esc(item.error || '—')}</td>
            </tr>`).join('')}</tbody>
        </table></div>` : empty('暂无任务', '点击「立即扫描」开始索引。')}
    </section>`;
  document.querySelector('#task-scan').addEventListener('click', () => runScan());
  document.querySelector('#task-refresh').addEventListener('click', pageTasks);
}

/* ---------------------------------------------------------------- 探针 */
async function pageProbe() {
  const data = await api('/probe');
  const items = data.items || [];
  content.innerHTML = `
    <section class="panel">
      <div class="panel-head"><h2>未知接口探针</h2>
        <div class="panel-actions"><button id="clear-probes" class="btn btn-sm">清空记录</button></div>
      </div>
      <p class="hint">记录客户端发来但本服务未注册的 Emby 请求，用于补齐端点。上限 1000 条。</p>
      ${items.length ? `
        <div class="table-wrap"><table>
          <thead><tr><th>方法</th><th>路径</th><th>时间</th></tr></thead>
          <tbody>${items.map(item => `<tr><td><span class="protocol">${esc(item.method)}</span></td><td class="mono">${esc(item.path)}</td><td class="mono">${esc(item.created_at)}</td></tr>`).join('')}</tbody>
        </table></div>` : empty('暂无探针记录', '当客户端请求了未注册的 Emby 接口后会显示在这里。')}
    </section>`;
  document.querySelector('#clear-probes').addEventListener('click', async () => {
    try { await api('/probe', { method: 'DELETE' }); toast('探针记录已清空'); pageProbe(); }
    catch (e) { toast(e.message, 'error'); }
  });
}

/* ---------------------------------------------------------------- 刮削 */
// 配置存 DB（config.yaml 不设 scrape 段）；密钥只写不回显，读取时返回掩码。
async function pageScrape() {
  const [settings, libs] = await Promise.all([api('/scrape/settings'), api('/libraries')]);
  const libraries = libs.items || [];
  const tr = settings.translate || {};
  const progress = await api('/scrape/progress').catch(() => ({}));
  const libOptions = libraries.map(lib => `<option value="${esc(lib.Id)}">${esc(lib.Name)}</option>`).join('');

  content.innerHTML = `
    <section class="panel">
      <div class="panel-head"><h2>刮削配置</h2>
        <div class="panel-actions">
          <button id="test-metatube" class="btn btn-sm">${icon('probe')}<span>测试 MetaTube</span></button>
          <button id="test-translate" class="btn btn-sm">${icon('probe')}<span>测试翻译</span></button>
        </div>
      </div>
      <form id="scrape-form" class="field-grid">
        <div class="field full"><label for="sc-url">MetaTube 地址</label>
          <input id="sc-url" class="mono" placeholder="https://metatube.example.com" value="${esc(settings.metatube_url || '')}"></div>
        <div class="field full"><label for="sc-token">MetaTube Token</label>
          <input id="sc-token" class="mono" placeholder="${settings.metatube_token ? '已配置（留空表示不修改）' : 'Bearer token'}" value="${esc(settings.metatube_token || '')}"></div>
        <div class="field"><label for="sc-timeout">请求超时（秒）</label>
          <input id="sc-timeout" type="number" min="1" max="600" value="${esc(settings.timeout_seconds ?? 30)}"></div>
        <div class="field"><label for="sc-concurrency">并发数</label>
          <input id="sc-concurrency" type="number" min="1" max="8" value="${esc(settings.concurrency ?? 2)}"></div>
        <div class="field"><label for="sc-quality">图片质量</label>
          <input id="sc-quality" type="number" min="1" max="100" value="${esc(settings.image_quality ?? 90)}"></div>
        <div class="field full"><label for="sc-avatars">头像目录</label>
          <input id="sc-avatars" class="mono" value="${esc(settings.avatars_dir || '')}" placeholder="留空 = 与数据库同级 avatars/"></div>
        <div class="field"><label>图片下载</label>
          <label class="check"><input id="sc-images" type="checkbox"${settings.download_images ? ' checked' : ''}> 下载并写入海报/背景图</label></div>
        <div class="field"><label>默认覆盖策略</label>
          <label class="check"><input id="sc-overwrite" type="checkbox"${settings.overwrite ? ' checked' : ''}> 强制覆盖（默认只补缺失）</label></div>

        <div class="field full"><label style="color:var(--accent)">翻译（内嵌，直连自建 Deepl-Proxy）</label></div>
        <div class="field"><label>翻译标题</label>
          <label class="check"><input id="tr-title" type="checkbox"${tr.title ? ' checked' : ''}> 译文写入 Title</label></div>
        <div class="field"><label>翻译简介</label>
          <label class="check"><input id="tr-summary" type="checkbox"${tr.summary ? ' checked' : ''}> 译文写入 Plot</label></div>
        <div class="field"><label for="tr-lang">目标语言</label>
          <input id="tr-lang" class="mono" value="${esc(tr.target_lang || 'ZH')}" placeholder="ZH / ZH-HANT / JA"></div>
        <div class="field"><label for="tr-timeout">翻译超时（秒）</label>
          <input id="tr-timeout" type="number" min="1" max="300" value="${esc(tr.timeout_seconds ?? 30)}"></div>
        <div class="field full"><label for="tr-url">翻译服务地址</label>
          <input id="tr-url" class="mono" placeholder="http://127.0.0.1:8080" value="${esc(tr.api_url || '')}"></div>
        <div class="field full"><label for="tr-key">翻译网关 Token</label>
          <input id="tr-key" class="mono" placeholder="${tr.api_key ? '已配置（留空表示不修改）' : 'gateway_token'}" value="${esc(tr.api_key || '')}"></div>
        <div class="form-foot" style="grid-column:1/-1">
          <button id="scrape-save" class="btn btn-accent" type="submit">${icon('plus')}<span>保存并即时生效</span></button>
          <span class="hint" style="margin:0">保存后无需重启；密钥留空表示不修改。</span>
        </div>
      </form>
    </section>

    <section class="panel">
      <div class="panel-head"><h2>立即刮削</h2>
        <div class="panel-actions"><span class="hint" id="scrape-count" style="margin:0"></span></div>
      </div>
      <div class="filters">
        <select id="sc-lib"><option value="0">全部媒体库</option>${libOptions}</select>
        <label class="check"><input id="sc-only-missing" type="checkbox" checked> 仅处理缺失元数据的影片</label>
        <label class="check"><input id="sc-force" type="checkbox"> 本次强制覆盖</label>
        <button id="scrape-start" class="btn btn-accent btn-sm">${icon('film')}<span>开始刮削</span></button>
        <button id="scrape-cancel" class="btn btn-sm">中止</button>
        <button id="avatar-start" class="btn btn-sm">${icon('plus')}<span>补演员头像</span></button>
      </div>
      <div id="scrape-progress" class="probe-progress" hidden>
        <div class="scan-progress-head"><strong id="scrape-progress-title">刮削中…</strong><span id="scrape-progress-count"></span></div>
        <div class="scan-progress-bar"><i id="scrape-progress-fill"></i></div>
        <small id="scrape-progress-detail"></small>
      </div>
      <div id="scrape-failures"></div>
      <p class="hint">批量与定时为全自动：遇到「非番号精确命中」会跳过并标记「待人工确认」，可在媒体墙按刮削结果筛出后逐条手动处理。</p>
    </section>`;

  const num = id => Number(document.querySelector(id).value) || 0;
  document.querySelector('#scrape-form').addEventListener('submit', async event => {
    event.preventDefault();
    const body = {
      metatube_url: document.querySelector('#sc-url').value,
      metatube_token: document.querySelector('#sc-token').value,
      timeout_seconds: num('#sc-timeout'),
      concurrency: num('#sc-concurrency'),
      image_quality: num('#sc-quality'),
      avatars_dir: document.querySelector('#sc-avatars').value,
      download_images: document.querySelector('#sc-images').checked,
      overwrite: document.querySelector('#sc-overwrite').checked,
      translate: {
        title: document.querySelector('#tr-title').checked,
        summary: document.querySelector('#tr-summary').checked,
        target_lang: document.querySelector('#tr-lang').value,
        api_url: document.querySelector('#tr-url').value,
        api_key: document.querySelector('#tr-key').value,
        timeout_seconds: num('#tr-timeout')
      }
    };
    try { await api('/scrape/settings', { method: 'PUT', body: JSON.stringify(body) }); toast('配置已保存并即时生效'); pageScrape(); }
    catch (e) { toast(e.message, 'error'); }
  });

  const testTarget = async target => {
    try {
      const result = await api('/scrape/test', { method: 'POST', body: JSON.stringify({ target }) });
      toast(result.ok ? `${result.detail}（${result.elapsed_ms}ms）` : `连接失败：${result.error}`, result.ok ? 'ok' : 'error');
    } catch (e) { toast(e.message, 'error'); }
  };
  document.querySelector('#test-metatube').addEventListener('click', () => testTarget('metatube'));
  document.querySelector('#test-translate').addEventListener('click', () => testTarget('translate'));

  const refreshCount = async () => {
    const q = new URLSearchParams({ library_id: document.querySelector('#sc-lib').value, only_missing: String(document.querySelector('#sc-only-missing').checked) });
    try {
      const result = await api('/scrape/candidates?' + q.toString());
      document.querySelector('#scrape-count').textContent = `本次将处理 ${result.total} 条`;
    } catch { /* ignore */ }
  };
  document.querySelector('#sc-lib').addEventListener('change', refreshCount);
  document.querySelector('#sc-only-missing').addEventListener('change', refreshCount);
  refreshCount();

  document.querySelector('#scrape-start').addEventListener('click', async () => {
    const body = {
      library_id: Number(document.querySelector('#sc-lib').value) || 0,
      only_missing: document.querySelector('#sc-only-missing').checked,
      overwrite: document.querySelector('#sc-force').checked
    };
    try {
      const result = await api('/scrape/run', { method: 'POST', body: JSON.stringify(body) });
      toast(`刮削已启动（${result.total} 条）`);
      startScrapePolling();
    } catch (e) { toast(e.message, 'error'); }
  });
  document.querySelector('#avatar-start').addEventListener('click', async () => {
    try {
      const result = await api('/scrape/avatars', { method: 'POST', body: JSON.stringify({ library_id: Number(document.querySelector('#sc-lib').value) || 0 }) });
      toast(`头像任务已启动（${result.total} 个演员）`);
      startScrapePolling();
    } catch (e) { toast(e.message, 'error'); }
  });
  document.querySelector('#scrape-cancel').addEventListener('click', async () => {
    try { await api('/scrape/cancel', { method: 'POST' }); toast('已请求中止'); }
    catch (e) { toast(e.message, 'error'); }
  });

  renderScrapeProgress(progress);
  if (progress && progress.running) startScrapePolling();
}

let scrapeTimer = null;

function scrapeEls() {
  return {
    panel: document.querySelector('#scrape-progress'),
    title: document.querySelector('#scrape-progress-title'),
    count: document.querySelector('#scrape-progress-count'),
    fill: document.querySelector('#scrape-progress-fill'),
    detail: document.querySelector('#scrape-progress-detail'),
    failures: document.querySelector('#scrape-failures')
  };
}

function renderScrapeProgress(p) {
  const el = scrapeEls();
  if (!el.panel) return;
  if (!p || (!p.running && !p.finished_at)) { el.panel.hidden = true; return; }
  el.panel.hidden = false;
  el.title.textContent = p.running ? (p.kind === 'scrape_avatars' ? '正在补演员头像' : '正在刮削') : '刮削结束';
  const total = p.total || 0;
  el.count.textContent = total ? `${p.done || 0}/${total}` : String(p.done || 0);
  el.fill.classList.toggle('is-indeterminate', !total && p.running);
  el.fill.style.width = total ? `${Math.min(100, Math.round((p.done || 0) / total * 100))}%` : '100%';
  const parts = [];
  if (p.success) parts.push(`成功 ${p.success}`);
  if (p.skipped) parts.push(`跳过 ${p.skipped}`);
  if (p.failed) parts.push(`失败 ${p.failed}`);
  if (p.running && p.current) parts.push(`当前 ${p.current}`);
  if (p.error) parts.push(`错误：${p.error}`);
  el.detail.textContent = parts.join(' · ') || (p.running ? '正在处理…' : '');
  if (el.failures) {
    const samples = p.failures || [];
    el.failures.innerHTML = samples.length
      ? `<details class="scrape-fail-samples"><summary>失败/待确认样例（${samples.length}）</summary>
          <ul>${samples.map(line => `<li class="mono">${esc(line)}</li>`).join('')}</ul></details>`
      : '';
  }
}

function stopScrapePolling() { if (scrapeTimer) { clearInterval(scrapeTimer); scrapeTimer = null; } }

function startScrapePolling() {
  stopScrapePolling();
  scrapeTimer = setInterval(async () => {
    try {
      const p = await api('/scrape/progress');
      renderScrapeProgress(p);
      if (p && !p.running) { stopScrapePolling(); }
    } catch { stopScrapePolling(); }
  }, 1500);
}

/* ------------------------------------------------------------ 单条刮削预览 */
let scrapeState = null;

function closeScrapePreview() {
  const overlay = document.querySelector('#scrape-overlay');
  if (overlay) overlay.hidden = true;
  scrapeState = null;
}

// openScrapePreview 打开单条刮削预览：先取候选，再自动 inspect 推荐项。
async function openScrapePreview(item) {
  const overlay = document.querySelector('#scrape-overlay');
  const body = document.querySelector('#scrape-body');
  overlay.hidden = false;
  body.innerHTML = '<p class="hint">正在搜索候选…</p>';
  scrapeState = { movieId: item.id, preview: null, inspect: null, provider: '', id: '' };
  try {
    const preview = await api(`/items/${item.id}/scrape/preview`);
    scrapeState.preview = preview;
    if (!preview.candidates || !preview.candidates.length) {
      body.innerHTML = `<p class="hint">没有搜到候选（搜索词：${esc(preview.query || '—')}）。可先在详情里补番号，或在 NFO 里填好番号后重试。</p>`;
      return;
    }
    const pick = preview.candidates[preview.recommended >= 0 ? preview.recommended : 0];
    await loadScrapeInspect(pick.provider, pick.id);
  } catch (e) {
    body.innerHTML = `<p class="hint" style="color:var(--danger)">${esc(e.message)}</p>`;
  }
}

async function loadScrapeInspect(provider, id) {
  const body = document.querySelector('#scrape-body');
  body.innerHTML = '<p class="hint">正在读取详情…</p>';
  const inspect = await api(`/items/${scrapeState.movieId}/scrape/inspect`, {
    method: 'POST', body: JSON.stringify({ provider, id })
  });
  scrapeState.inspect = inspect;
  scrapeState.provider = provider;
  scrapeState.id = id;
  renderScrapePreview();
}

function renderScrapePreview() {
  const body = document.querySelector('#scrape-body');
  const state = scrapeState;
  if (!state || !state.preview || !state.inspect) return;
  const preview = state.preview;
  const inspect = state.inspect;

  const candidates = preview.candidates.map((item, index) => `
    <button class="scrape-candidate ${item.provider === state.provider && item.id === state.id ? 'is-active' : ''}"
            data-idx="${index}">
      ${item.thumb ? `<img src="${esc(item.thumb)}" alt="" loading="lazy">` : '<span class="noimg"></span>'}
      <span>
        <strong>${esc(item.title || '—')}</strong>
        <small>${esc([item.number, item.provider, item.score ? '★ ' + item.score : '', item.exact ? '番号命中' : ''].filter(Boolean).join(' · '))}</small>
      </span>
    </button>`).join('');

  const diffRows = inspect.fields.map(field => `
    <div class="scrape-diff-row ${field.change ? 'is-change' : ''}">
      <dt>${esc(field.label)}</dt>
      <dd>${field.old ? `<span class="old">${esc(field.old)}</span> ` : ''}${field.next ? `<span class="next">${esc(field.next)}</span>` : '<span class="old">（不写入）</span>'}</dd>
    </div>`).join('');

  const images = (inspect.images || []).map(image => `
    <div class="scrape-image ${image.exists ? 'is-kept' : ''}">
      ${image.preview ? `<img src="${esc(image.preview)}" alt="" loading="lazy">` : '<span class="noimg"></span>'}
      <span>${esc(image.name)}${image.exists ? '（已存在，只补缺失时不覆盖）' : ''}</span>
    </div>`).join('');

  body.innerHTML = `
    <div class="scrape-cols">
      <div>
        <p class="hint" style="margin:0 0 8px">候选 ${preview.candidates.length} 条 · 搜索词 <code>${esc(preview.query)}</code>${preview.expected ? ` · 预期番号 <code>${esc(preview.expected)}</code>` : ''}</p>
        <div class="scrape-candidates">${candidates}</div>
      </div>
      <div class="scrape-detail">
        <div class="scrape-detail-head">
          ${inspect.poster ? `<img src="${esc(inspect.poster)}" alt="" loading="lazy">` : ''}
          <div>
            <h3 style="margin:0 0 6px">${esc(inspect.title || '—')}</h3>
            <p class="hint" style="margin:0 0 8px">${esc([inspect.number, inspect.provider, inspect.release, inspect.runtime ? inspect.runtime + ' 分钟' : '', inspect.score ? '★ ' + inspect.score : ''].filter(Boolean).join(' · '))}</p>
            <p class="detail-plot">${esc(inspect.summary || '（无简介）')}</p>
          </div>
        </div>
        <div>
          <h3 class="scrape-h3">字段差异（预览只显示原文，翻译在确认后执行）</h3>
          <dl class="scrape-diff">${diffRows}</dl>
        </div>
        <div>
          <h3 class="scrape-h3">图片</h3>
          <div class="scrape-images">${images}</div>
        </div>
      </div>
    </div>
    <div class="scrape-foot">
      <label class="switch-lg"><input id="scrape-force" type="checkbox"${inspect.overwrite ? ' checked' : ''}> 强制覆盖（不勾选 = 只补缺失）</label>
      <button id="scrape-cancel-btn" class="btn">取消</button>
      <button id="scrape-confirm" class="btn btn-accent">确认写入</button>
    </div>`;

  body.querySelectorAll('.scrape-candidate').forEach(button => button.addEventListener('click', async () => {
    const pick = preview.candidates[Number(button.dataset.idx)];
    try { await loadScrapeInspect(pick.provider, pick.id); }
    catch (e) { toast(e.message, 'error'); }
  }));
  body.querySelector('#scrape-cancel-btn').addEventListener('click', closeScrapePreview);
  body.querySelector('#scrape-confirm').addEventListener('click', async () => {
    const confirmBtn = body.querySelector('#scrape-confirm');
    confirmBtn.disabled = true;
    try {
      const result = await api(`/items/${state.movieId}/scrape`, {
        method: 'POST',
        body: JSON.stringify({ provider: state.provider, id: state.id, overwrite: body.querySelector('#scrape-force').checked })
      });
      toast(`已写入「${result.title}」（图片 ${result.images} 张）`);
      closeScrapePreview();
      closeDetail();
      pageItems();
    } catch (e) { toast(e.message, 'error'); confirmBtn.disabled = false; }
  });
}

/* ------------------------------------------------------------ 计划任务 */
// 常用预设；表达式用标准 5 段（分 时 日 月 周），按服务器本地时区执行。
const CRON_PRESETS = [
  ['每小时', '0 * * * *'],
  ['每 6 小时', '0 */6 * * *'],
  ['每天 03:00', '0 3 * * *'],
  ['每周一 04:00', '0 4 * * 1'],
  ['每月 1 日 05:00', '0 5 1 * *']
];
const TASK_TYPE_TEXT = { scan: '扫描媒体库', reindex: '重建索引', probe: '媒体信息探测' };
const RUN_STATUS_TEXT = { success: '成功', failed: '失败', skipped: '跳过', running: '进行中' };

let scheduledEditId = null; // 正在编辑的任务 id（null = 新建）

// 后端时间统一为 RFC3339（UTC），这里转成本地可读形式展示。
function fmtTime(value) {
  if (!value) return '—';
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString('zh-CN', { hour12: false });
}

function runBadge(status) {
  if (!status) return '<span class="hint" style="margin:0">—</span>';
  return `<span class="badge ${esc(status)}">${esc(RUN_STATUS_TEXT[status] || status)}</span>`;
}

async function pageScheduled() {
  const [data, libs] = await Promise.all([api('/scheduled'), api('/libraries')]);
  const items = data.items || [];
  const types = data.types || TASK_TYPE_TEXT;
  const libraries = libs.items || [];
  const editing = scheduledEditId ? items.find(item => item.id === scheduledEditId) : null;
  // 编辑时回填参数；参数在库里是 JSON 文本。
  let params = {};
  if (editing) { try { params = JSON.parse(editing.params || '{}') || {}; } catch { params = {}; } }

  const libOptions = libraries.map(lib => `<option value="${esc(lib.Id)}">${esc(lib.Name)}</option>`).join('');
  content.innerHTML = `
    <section class="panel">
      <div class="panel-head">
        <h2>${editing ? '编辑计划任务' : '新建计划任务'}</h2>
        ${editing ? '<div class="panel-actions"><button id="sched-cancel" class="btn btn-sm">取消编辑</button></div>' : ''}
      </div>
      <p class="hint" style="margin:0 0 14px">到点自动执行，保存后立即生效（无需重启）。同一任务上一轮未结束时跳过本次并记录。</p>
      <form id="sched-form" class="field-grid">
        <div class="field"><label for="s-name">名称 *</label>
          <input id="s-name" placeholder="如 夜间扫库" required value="${esc(editing ? editing.name : '')}"></div>
        <div class="field"><label for="s-type">任务类型 *</label>
          <select id="s-type">${Object.entries(types).map(([key, text]) =>
            `<option value="${esc(key)}"${editing && editing.type === key ? ' selected' : ''}>${esc(text)}</option>`).join('')}</select></div>
        <div class="field"><label for="s-cron">cron 表达式 *</label>
          <input id="s-cron" class="mono" placeholder="0 3 * * *" required value="${esc(editing ? editing.cron : '')}"></div>
        <div class="field" data-show="library"><label for="s-lib">媒体库</label>
          <select id="s-lib"><option value="0">全部媒体库</option>${libOptions}</select></div>
        <div class="field" data-show="probe"><label for="s-status">影片状态</label>
          <select id="s-status">
            <option value="success">可播放</option>
            <option value="manual">手动录入</option>
            <option value="">全部</option>
          </select></div>
        <div class="field" data-show="probe"><label>探测范围</label>
          <label class="check"><input id="s-only-missing" type="checkbox"> 仅未探测的条目</label></div>
        <div class="field"><label>启用</label>
          <label class="check"><input id="s-enabled" type="checkbox"${!editing || editing.enabled ? ' checked' : ''}> 保存后参与调度</label></div>
        <div class="field full"><label>常用预设</label>
          <div class="preset-row">${CRON_PRESETS.map(([text, expr]) =>
            `<button type="button" class="btn btn-sm preset" data-cron="${esc(expr)}">${esc(text)}</button>`).join('')}</div></div>
        <p class="hint full" id="s-preview" style="grid-column:1/-1">标准 5 段（分 时 日 月 周）或 @daily/@hourly 等描述符；留空无法保存。</p>
        <div class="form-foot" style="grid-column:1/-1">
          <button id="sched-submit" class="btn btn-accent" type="submit">${icon('plus')}<span>${editing ? '保存修改' : '新建任务'}</span></button>
        </div>
      </form>
    </section>

    <section class="panel">
      <div class="panel-head"><h2>已配置任务</h2>
        <div class="panel-actions"><button id="sched-refresh" class="btn btn-sm">${icon('refresh')}<span>刷新</span></button></div>
      </div>
      ${items.length ? `
        <div class="table-wrap"><table>
          <thead><tr><th>名称</th><th>类型</th><th>表达式</th><th>下次执行</th><th>上次结果</th><th>启用</th><th style="text-align:right">操作</th></tr></thead>
          <tbody>${items.map(item => `<tr>
            <td><strong class="title">${esc(item.name)}</strong></td>
            <td><span class="protocol">${esc(TASK_TYPE_TEXT[item.type] || item.type)}</span></td>
            <td class="mono">${esc(item.cron)}</td>
            <td class="mono">${esc(item.enabled ? fmtTime(item.next_run) : '已禁用')}</td>
            <td>${runBadge(item.last_status)}
              <small class="hint" style="margin:2px 0 0;display:block">${esc(fmtTime(item.last_run_at))}${item.last_message ? ' · ' + esc(item.last_message) : ''}</small></td>
            <td><label class="switch"><input type="checkbox" data-role="toggle" data-id="${esc(item.id)}"${item.enabled ? ' checked' : ''}><i></i></label></td>
            <td><div class="row-actions">
              <button class="btn btn-sm" data-role="run" data-id="${esc(item.id)}" title="立即执行一次">${icon('play')}<span>执行</span></button>
              <button class="btn btn-sm" data-role="edit" data-id="${esc(item.id)}">编辑</button>
              <button class="icon-btn danger" data-role="del" data-id="${esc(item.id)}" title="删除">${icon('trash')}</button>
            </div></td>
          </tr>`).join('')}</tbody>
        </table></div>` : empty('还没有计划任务', '在上方填写名称、类型与 cron 表达式后新建。')}
    </section>`;

  const typeEl = document.querySelector('#s-type');
  const cronEl = document.querySelector('#s-cron');
  const preview = document.querySelector('#s-preview');

  // 按任务类型显示相关参数：重建索引不需要参数，探测才有状态/范围。
  function syncParams() {
    const kind = typeEl.value;
    document.querySelectorAll('[data-show="library"]').forEach(el => { el.hidden = kind === 'reindex'; });
    document.querySelectorAll('[data-show="probe"]').forEach(el => { el.hidden = kind !== 'probe'; });
  }

  // 即时校验表达式并预览后续执行时间（后端解析，与调度用同一套规则）。
  const checkCron = debounce(async () => {
    const expr = cronEl.value.trim();
    if (!expr) { preview.textContent = '请输入 cron 表达式。'; preview.style.color = ''; return; }
    try {
      const result = await api('/scheduled/validate', { method: 'POST', body: JSON.stringify({ cron: expr }) });
      if (!result.valid) { preview.textContent = '表达式非法：' + result.error; preview.style.color = 'var(--danger)'; return; }
      preview.textContent = '接下来执行：' + (result.next_runs || []).map(fmtTime).join(' · ');
      preview.style.color = '';
    } catch (e) { preview.textContent = e.message; preview.style.color = 'var(--danger)'; }
  }, 300);

  // 回填编辑中的参数。
  if (editing) {
    document.querySelector('#s-lib').value = String(params.library_id || 0);
    if (params.status) document.querySelector('#s-status').value = params.status;
    document.querySelector('#s-only-missing').checked = params.only_missing !== false;
  } else {
    document.querySelector('#s-only-missing').checked = true;
  }
  syncParams();
  if (editing) checkCron();

  typeEl.addEventListener('change', syncParams);
  cronEl.addEventListener('input', checkCron);
  document.querySelectorAll('.preset').forEach(button => button.addEventListener('click', () => {
    cronEl.value = button.dataset.cron;
    checkCron();
  }));
  if (document.querySelector('#sched-cancel')) {
    document.querySelector('#sched-cancel').addEventListener('click', () => { scheduledEditId = null; pageScheduled(); });
  }
  document.querySelector('#sched-refresh').addEventListener('click', pageScheduled);

  document.querySelector('#sched-form').addEventListener('submit', async event => {
    event.preventDefault();
    const kind = typeEl.value;
    const params = { library_id: Number(document.querySelector('#s-lib').value) || 0 };
    if (kind === 'probe') {
      params.status = document.querySelector('#s-status').value;
      params.only_missing = document.querySelector('#s-only-missing').checked;
    }
    const body = {
      name: document.querySelector('#s-name').value.trim(),
      type: kind,
      cron: cronEl.value.trim(),
      enabled: document.querySelector('#s-enabled').checked,
      params
    };
    const submit = document.querySelector('#sched-submit');
    submit.disabled = true;
    try {
      if (editing) {
        await api('/scheduled/' + editing.id, { method: 'PUT', body: JSON.stringify(body) });
        toast('计划任务已保存');
        scheduledEditId = null;
      } else {
        await api('/scheduled', { method: 'POST', body: JSON.stringify(body) });
        toast(`计划任务「${body.name}」已创建`);
      }
      pageScheduled();
    } catch (e) { toast(e.message, 'error'); submit.disabled = false; }
  });

  document.querySelectorAll('[data-role]').forEach(el => {
    const id = el.dataset.id;
    const role = el.dataset.role;
    if (role === 'toggle') {
      el.addEventListener('change', async () => {
        try { await api(`/scheduled/${id}/toggle`, { method: 'POST' }); toast('状态已更新'); pageScheduled(); }
        catch (e) { toast(e.message, 'error'); pageScheduled(); }
      });
    } else if (role === 'run') {
      el.addEventListener('click', async () => {
        try { await api(`/scheduled/${id}/run`, { method: 'POST' }); toast('已触发执行，可在「任务」页查看结果'); }
        catch (e) { toast(e.message, 'error'); }
      });
    } else if (role === 'edit') {
      el.addEventListener('click', () => { scheduledEditId = Number(id); pageScheduled(); });
    } else if (role === 'del') {
      el.addEventListener('click', async () => {
        if (!confirm('删除该计划任务？已产生的影片数据不受影响。')) return;
        try {
          await api('/scheduled/' + id, { method: 'DELETE' });
          if (scheduledEditId === Number(id)) scheduledEditId = null;
          toast('计划任务已删除');
          pageScheduled();
        } catch (e) { toast(e.message, 'error'); }
      });
    }
  });
}

/* ---------------------------------------------------------------- 路由 */
const pages = {
  overview: pageOverview,
  libraries: pageLibraries,
  items: pageItems,
  manual: pageManual,
  settings: pageSettings,
  apikeys: pageAPIKeys,
  scrape: pageScrape,
  scheduled: pageScheduled,
  tasks: pageTasks,
  probe: pageProbe
};

async function page(name) {
  setMeta(name);
  content.innerHTML = skeletonPanel();
  const fn = pages[name];
  if (!fn) { content.innerHTML = `<div class="panel">${empty('模块即将开放', '请先使用上方导航。')}</div>`; return; }
  try {
    await fn();
  } catch (error) {
    content.innerHTML = `<div class="panel error" style="border-color:#a64952">${icon('alert')} ${esc(error.message || error)}</div>`;
  }
}

/* ------------------------------------------------------------ 媒体信息探测 */
// 与扫库相互独立：探测只读 .strm 直链、跑 ffprobe、把结果写回 NFO，不写索引库。
let probeTimer = null;

function probeEls() {
  return {
    panel: document.querySelector('#probe-progress'),
    title: document.querySelector('#probe-progress-title'),
    count: document.querySelector('#probe-progress-count'),
    fill: document.querySelector('#probe-progress-fill'),
    detail: document.querySelector('#probe-progress-detail')
  };
}

function renderProbeProgress(p) {
  const el = probeEls();
  if (!el.panel) return; // 不在媒体墙页面时只保持轮询，不渲染
  if (!p || (!p.running && !p.finished_at)) { el.panel.hidden = true; return; }
  el.panel.hidden = false;
  el.title.textContent = p.running ? '正在探测媒体信息' : (p.cancelled ? '探测已中止' : '探测完成');
  const total = p.total || 0;
  const done = p.done || 0;
  el.count.textContent = total ? `${done}/${total}` : String(done);
  el.fill.classList.toggle('is-indeterminate', !total && p.running);
  el.fill.style.width = total ? `${Math.min(100, Math.round(done / total * 100))}%` : '100%';
  const parts = [];
  if (p.success) parts.push(`成功 ${p.success}`);
  if (p.skipped) parts.push(`跳过 ${p.skipped}`);
  if (p.failed) parts.push(`失败 ${p.failed}`);
  if (p.running && p.current) parts.push(`当前 ${p.current}`);
  if (p.error) parts.push(`错误：${p.error}`);
  el.detail.textContent = parts.join(' · ') || (p.running ? '正在读取源信息…' : '');
}

function stopProbePolling() { if (probeTimer) { clearInterval(probeTimer); probeTimer = null; } }

function setProbeButton(busy) {
  const btn = document.querySelector('#probe-media');
  if (!btn) return;
  btn.disabled = busy;
  const span = btn.querySelector('span');
  if (span) span.textContent = busy ? '探测中…' : '探测媒体信息';
}

function startProbePolling() {
  if (probeTimer) return;
  probeTimer = setInterval(async () => {
    let p = null;
    try { p = await api('/probe/media/progress'); } catch { /* ignore */ }
    if (p) renderProbeProgress(p);
    if (!p || !p.running) {
      stopProbePolling();
      setProbeButton(false);
      if (p) {
        if (p.cancelled) toast(`探测已中止：成功 ${p.success} / 跳过 ${p.skipped} / 失败 ${p.failed}`);
        else toast(`探测完成：成功 ${p.success} / 跳过 ${p.skipped} / 失败 ${p.failed}`, p.failed ? 'error' : 'ok');
        if (p.failures && p.failures.length) toast(`失败示例：${p.failures[0]}`, 'error');
        // 自动收起，避免长期占位；有失败时保留，方便对照失败条数。
        if (!p.failed) {
          const el = probeEls();
          setTimeout(() => { if (el.panel && !probeTimer) el.panel.hidden = true; }, 8000);
        }
      }
    }
  }, 1000);
}

async function runProbeAll() {
  setProbeButton(true);
  const el = probeEls();
  if (el.panel) el.panel.hidden = false;
  if (el.title) el.title.textContent = '正在启动探测…';
  try {
    // 默认只补缺：已含 streamdetails 的条目跳过，避免每次全库重探（远程探测很慢）。
    const r = await api('/probe/media', { method: 'POST', body: JSON.stringify({ only_missing: true }) });
    if (!r.total) {
      toast(r.message || '没有可探测的影片');
      setProbeButton(false);
      if (el.panel) el.panel.hidden = true;
      return;
    }
    toast(`开始探测 ${r.total} 条影片`);
    startProbePolling();
  } catch (e) {
    toast(e.message, 'error');
    setProbeButton(false);
    if (el.panel) el.panel.hidden = true;
  }
}

async function probeItem(id) {
  toast('正在探测…');
  try {
    const r = await api('/items/' + id + '/probe', { method: 'POST' });
    toast(probeSummary(r.info), 'ok');
  } catch (e) { toast(e.message, 'error'); }
}

// probeSummary 把探测结果压成一行可读摘要，作为单条探测的即时反馈。
function probeSummary(info) {
  if (!info) return '探测完成';
  const video = info.video || {};
  const parts = [];
  if (video.width && video.height) parts.push(`${video.width}x${video.height}`);
  if (video.codec) parts.push(String(video.codec).toUpperCase());
  if (video.profile) parts.push(video.profile);
  if (video.framerate) parts.push(`${Number(video.framerate).toFixed(2)}fps`);
  const bitrate = info.bitrate || video.bitrate;
  if (bitrate) parts.push(`${(bitrate / 1000000).toFixed(2)} Mbps`);
  return parts.length ? '已写入 NFO：' + parts.join(' · ') : '已写入 NFO';
}

/* ---------------------------------------------------------------- 扫描进度 */
const scanPanel = document.querySelector('#scan-progress');
const scanTitle = document.querySelector('#scan-progress-title');
const scanCount = document.querySelector('#scan-progress-count');
const scanFill = document.querySelector('#scan-progress-fill');
const scanDetail = document.querySelector('#scan-progress-detail');
let scanTimer = null;

function baseName(path) {
  return String(path || '').split(/[\\/]/).filter(Boolean).pop() || '';
}

function renderScanProgress(p) {
  if (!p || (!p.running && !p.finished_at)) { scanPanel.hidden = true; return; }
  scanPanel.hidden = false;
  const lib = p.libraries > 1 ? `${p.library_name || '媒体库'}（${p.library_index}/${p.libraries}）` : (p.library_name || '媒体库');
  scanTitle.textContent = p.running ? `正在扫描 ${lib}` : `扫描完成 ${lib}`;
  const total = p.total || 0;
  const done = p.done || 0;
  scanCount.textContent = total ? `${done}/${total}` : String(done);
  scanFill.classList.toggle('is-indeterminate', !total && p.running);
  scanFill.style.width = total ? `${Math.min(100, Math.round(done / total * 100))}%` : '100%';
  const parts = [];
  if (p.success) parts.push(`可播放 ${p.success}`);
  if (p.pending) parts.push(`待补录 ${p.pending}`);
  if (p.incompatible) parts.push(`不兼容 ${p.incompatible}`);
  if (p.failed) parts.push(`失败 ${p.failed}`);
  if (p.running && p.current) parts.push(`当前 ${baseName(p.current)}`);
  if (p.error) parts.push(`错误：${p.error}`);
  scanDetail.textContent = parts.join(' · ') || (p.running ? '正在读取目录…' : '');
}

function stopScanPolling() { if (scanTimer) { clearInterval(scanTimer); scanTimer = null; } }

function startScanPolling() {
  if (scanTimer) return;
  scanTimer = setInterval(async () => {
    let p = null;
    try { p = await api('/scan/progress'); } catch { /* ignore */ }
    if (p) renderScanProgress(p);
    if (!p || !p.running) {
      stopScanPolling();
      const btn = document.querySelector('#scan');
      if (btn) { btn.disabled = false; btn.querySelector('span').textContent = '开始扫描'; }
      if (p) setTimeout(() => { scanPanel.hidden = true; }, 4000);
    }
  }, 600);
}

async function runScan(libraryId) {
  const scanBtn = document.querySelector('#scan');
  scanBtn.disabled = true;
  scanBtn.querySelector('span').textContent = '扫描中…';
  scanPanel.hidden = false;
  scanTitle.textContent = '正在扫描…';
  scanCount.textContent = '';
  scanDetail.textContent = '';
  startScanPolling();
  try {
    const url = libraryId ? `/scan?library_id=${encodeURIComponent(libraryId)}` : '/scan';
    const r = await api(url, { method: 'POST' });
    toast(`扫描完成：${r.success} 可播放 / ${r.pending} 待补录 / ${r.incompatible} 不兼容`);
  } catch (e) { toast(e.message, 'error'); }
  try { renderScanProgress(await api('/scan/progress')); } catch { /* ignore */ }
  startScanPolling();
  scanBtn.disabled = false;
  scanBtn.querySelector('span').textContent = '开始扫描';
  const name = location.hash.replace('#', '');
  if (pages[name]) page(name);
}

async function boot() {
  if (!token) { window.location.replace('/'); return; }
  try {
    const response = await fetch('/Users/Me', { headers: { 'X-Emby-Token': token } });
    if (!response.ok) throw new Error('unauthorized');
  } catch {
    localStorage.removeItem('emby_token');
    window.location.replace('/');
    return;
  }
  // 导航写 hash 并压入历史，配合 popstate 让浏览器前进/后退在页面间往返。
  document.querySelectorAll('.nav-item').forEach(button => button.addEventListener('click', () => {
    if (location.hash !== '#' + button.dataset.page) location.hash = button.dataset.page;
    page(button.dataset.page);
  }));
  // 前进/后退时按 URL（hash + query）重渲染当前页，筛选条件随 URL 一并恢复。
  window.addEventListener('popstate', () => {
    const name = location.hash.replace('#', '');
    page(pages[name] ? name : 'overview');
  });
  document.querySelector('#scan').addEventListener('click', () => runScan());
  window.addEventListener('scroll', wallMaybeLoadMore, { passive: true });
  document.querySelector('#player-close').addEventListener('click', closePlayer);
  document.querySelector('#player-overlay').addEventListener('click', event => {
    if (event.target.id === 'player-overlay') closePlayer();
  });
  // 详情抽屉：点遮罩关闭（抽屉本体不关，便于选中文本复制路径）。
  detailOverlay.addEventListener('click', event => {
    if (event.target.id === 'detail-overlay') closeDetail();
  });
  document.querySelector('#scrape-overlay').addEventListener('click', event => {
    if (event.target.id === 'scrape-overlay') closeScrapePreview();
  });
  document.querySelector('#scrape-close').addEventListener('click', closeScrapePreview);
  document.addEventListener('keydown', event => {
    if (event.key === 'Escape' && !document.querySelector('#scrape-overlay').hidden) { closeScrapePreview(); return; }
    if (event.key === 'Escape' && !detailOverlay.hidden) { closeDetail(); return; }
    if (event.key === 'Escape' && !document.querySelector('#player-overlay').hidden) closePlayer();
  });
  // 页面刷新/切换回来时，若扫描仍在进行则恢复进度显示。
  try {
    const p = await api('/scan/progress');
    if (p && p.running) {
      renderScanProgress(p);
      startScanPolling();
      const btn = document.querySelector('#scan');
      btn.disabled = true;
      btn.querySelector('span').textContent = '扫描中…';
    }
  } catch { /* ignore */ }
  // 探测任务在服务端异步执行，切页/刷新后同样要恢复进度显示。
  try {
    const p = await api('/probe/media/progress');
    if (p && p.running) {
      renderProbeProgress(p);
      startProbePolling();
      setProbeButton(true);
    }
  } catch { /* ignore */ }
  const initial = location.hash.replace('#', '');
  page(pages[initial] ? initial : 'overview');
}
boot();
