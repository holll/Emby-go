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
  const pendingCount = status.pending?.length || 0;
  const incompatibleCount = status.incompatible?.length || 0;
  const rows = [
    ['success', (status.success || []).length],
    ['manual', (status.manual || []).length],
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
  const a11y = playable ? ` tabindex="0" role="button" aria-label="播放 ${esc(label)}"` : '';
  return `
    <article class="wall-card ${playable ? 'is-playable' : ''}" data-play="${item.id}"${a11y}>
      <div class="wall-poster">
        ${body}
        <div class="wall-badges">${badges}</div>
        ${progress ? `<div class="wall-progress"><i style="width:${progress}%"></i></div>` : ''}
        <div class="wall-actions">
          <button class="icon-btn" data-reread="${item.id}" title="重读 .strm 与 NFO">${icon('refresh')}</button>
          <button class="icon-btn danger" data-delete="${item.id}" title="删除索引（不删文件）">${icon('trash')}</button>
        </div>
        ${playable ? `<div class="wall-play">${icon('play')}</div>` : ''}
      </div>
      <div class="wall-meta">
        <strong title="${esc(title || item.source_path)}">${esc(label)}</strong>
        <small>${esc(sub)}</small>
      </div>
    </article>`;
}

const WALL_PAGE_SIZE = 100;
let wallState = null;

async function pageItems() {
  const params = new URLSearchParams(location.search);
  const status = params.get('status') || '';
  const search = params.get('search') || '';
  const sort = params.get('sort') || 'datecreated';
  const order = params.get('order') || (sort === 'title' ? 'asc' : 'desc');
  wallState = { status, search, sort, order, offset: 0, total: 0, loading: false, done: false, items: [], userdata: {} };

  const pills = [['', '全部'], ['success', '可播放'], ['manual', '手动'], ['pending', '待补录'], ['incompatible', '不兼容']];
  const sorts = [['datecreated', '最近入库'], ['title', '标题'], ['year', '年份'], ['communityrating', '评分']];
  content.innerHTML = `
    <section class="panel">
      <div class="panel-head"><h2>媒体墙</h2><span class="hint" id="wall-count" style="margin:0"></span></div>
      <div class="filters">
        ${pills.map(([value, label]) => `<button class="pill ${status === value ? 'is-active' : ''}" data-status="${value}">${esc(label)}</button>`).join('')}
        <input id="item-search" placeholder="搜索标题 / 番号 / 原名" value="${esc(search)}" style="min-width:200px">
        <select id="item-sort">${sorts.map(([value, label]) => `<option value="${value}" ${sort === value ? 'selected' : ''}>${esc(label)}</option>`).join('')}</select>
      </div>
      <div class="wall" id="wall"></div>
      <div id="wall-empty"></div>
      <div id="wall-more" class="wall-more"></div>
      <p class="hint">点击海报在线播放（浏览器不支持的编码如 H.265/MKV 可能无法播放）；不兼容源不会进入 Emby，更换为 http(s) .strm 后「重读源」可重新判定。</p>
    </section>`;

  document.querySelectorAll('.pill').forEach(button => button.addEventListener('click', () => {
    const q = new URLSearchParams(location.search);
    const value = button.dataset.status;
    if (value) q.set('status', value); else q.delete('status');
    q.delete('search');
    history.replaceState({}, '', '?' + q.toString());
    pageItems();
  }));
  const searchInput = document.querySelector('#item-search');
  let timer;
  searchInput.addEventListener('input', () => {
    clearTimeout(timer);
    timer = setTimeout(() => {
      const q = new URLSearchParams(location.search);
      const value = searchInput.value.trim();
      if (value) q.set('search', value); else q.delete('search');
      history.replaceState({}, '', '?' + q.toString());
      pageItems();
    }, 260);
  });
  const sortSelect = document.querySelector('#item-sort');
  if (sortSelect) sortSelect.addEventListener('change', () => {
    const q = new URLSearchParams(location.search);
    q.set('sort', sortSelect.value);
    q.delete('order');
    history.replaceState({}, '', '?' + q.toString());
    pageItems();
  });
  // 事件委托：卡片按页追加，统一在容器上处理，避免每页重新绑定。
  document.querySelector('#wall').addEventListener('click', async event => {
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
    const card = event.target.closest('.wall-card');
    if (!card) return;
    const item = wallState.items.find(m => String(m.id) === card.dataset.play);
    if (!item) return;
    const st = item.Status || item.status;
    if (st !== 'success' && st !== 'manual') { toast('该影片不可播放（待补录 / 协议不兼容）', 'error'); return; }
    openPlayer(item);
  });
  // 键盘可达：卡片获得焦点后回车/空格播放（桌面端无障碍）。
  document.querySelector('#wall').addEventListener('keydown', event => {
    if (event.key !== 'Enter' && event.key !== ' ') return;
    const card = event.target.closest('.wall-card.is-playable');
    if (!card) return;
    event.preventDefault();
    const item = wallState.items.find(m => String(m.id) === card.dataset.play);
    if (item) openPlayer(item);
  });
  await loadWallPage();
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
  const more = document.querySelector('#wall-more');
  if (more) more.textContent = '加载中…';
  try {
    const query = new URLSearchParams({ limit: String(WALL_PAGE_SIZE), offset: String(wallState.offset), sort: wallState.sort, order: wallState.order });
    if (wallState.status) query.set('status', wallState.status);
    if (wallState.search) query.set('search', wallState.search);
    const data = await api('/items?' + query.toString());
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
    if (count) count.textContent = `共 ${data.total} 条${wallState.status ? ' · ' + (STATUS_TEXT[wallState.status] || wallState.status) : ''} · 已加载 ${wallState.items.length}`;
    const emptyBox = document.querySelector('#wall-empty');
    if (emptyBox) {
      emptyBox.innerHTML = data.total === 0
        ? empty(wallState.status ? `没有 ${STATUS_TEXT[wallState.status] || wallState.status} 的影片` : '还没有影片', wallState.search ? '试试其它关键词。' : '开始扫描或手动补录后再来看看。')
        : '';
    }
    if (more) more.textContent = wallState.done && data.total > 0 ? `已全部加载（共 ${data.total} 条）` : '';
  } catch (error) {
    if (more) more.textContent = '加载失败：' + (error.message || error);
  } finally {
    wallState.loading = false;
    // 首屏未填满时继续加载，直到撑满视口。
    wallMaybeLoadMore();
  }
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
  const runLabel = s => ({ running: '进行中', success: '成功', failed: '失败' }[s] || s);
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
              <td>${esc(item.type)}</td>
              <td><span class="badge ${esc(item.status)}">${esc(runLabel(item.status))}</span></td>
              <td class="mono">${esc(item.started_at || '—')}</td>
              <td class="mono">${esc(item.ended_at || '—')}</td>
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

/* ---------------------------------------------------------------- 路由 */
const pages = {
  overview: pageOverview,
  libraries: pageLibraries,
  items: pageItems,
  manual: pageManual,
  settings: pageSettings,
  apikeys: pageAPIKeys,
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
  document.querySelectorAll('.nav-item').forEach(button => button.addEventListener('click', () => page(button.dataset.page)));
  document.querySelector('#scan').addEventListener('click', () => runScan());
  window.addEventListener('scroll', wallMaybeLoadMore, { passive: true });
  document.querySelector('#player-close').addEventListener('click', closePlayer);
  document.querySelector('#player-overlay').addEventListener('click', event => {
    if (event.target.id === 'player-overlay') closePlayer();
  });
  document.addEventListener('keydown', event => {
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
  const initial = location.hash.replace('#', '');
  page(pages[initial] ? initial : 'overview');
}
boot();
