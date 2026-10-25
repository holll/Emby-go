/* Emby-go 管理控制台前端 —— 单文件，无依赖 */
let token = localStorage.getItem('emby_token');
let me = null; // /api/auth/me：Web 端据此区分管理员与普通用户
// D03：新建密钥的原文只在创建后显示一次，刷新/离开页面即丢弃。
let createdAPIKey = null;
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
    play: '<path d="M8 5v14l11-7z"/>',
    'arrow-up': '<path d="M12 19V5"/><path d="m5 12 7-7 7 7"/>',
    'arrow-down': '<path d="M12 5v14"/><path d="m19 12-7 7-7-7"/>',
    image: '<rect x="3" y="3" width="18" height="18" rx="2"/><circle cx="8.5" cy="8.5" r="1.5"/><path d="m21 15-5-5L5 21"/>',
    edit: '<path d="M12 20h9"/><path d="M16.5 3.5a2.1 2.1 0 0 1 3 3L7 19l-4 1 1-4z"/>',
    x: '<path d="M18 6 6 18M6 6l12 12"/>'
  };
  return `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">${paths[name] || ''}</svg>`;
};

// 刮削写入的 NFO <title> 是「番号 标题」（见 internal/scraper/apply.go），
// 标题本身已含番号时，同行里不要再展示一遍番号。
// 判定与后端 metatube.SameNumber / leadingNumber 同构：取标题开头的番号样片段，
// 大写并去掉 - _ . 空白后比较，因此 ABF018 / abf_018 / ABF-018 视为同一个番号，
// 而 ABF-0182、ABF-018X 这类「番号只是前缀片段」的标题不算已带番号。
const compactNumber = value => String(value ?? '').toUpperCase().replace(/[-_.\s]/g, '');
function titleCarriesNumber(title, number) {
  const lead = String(title ?? '').trim().match(/^[A-Za-z0-9._-]+/);
  const normalized = compactNumber(number);
  return !!normalized && !!lead && compactNumber(lead[0]) === normalized;
}
// numberUnlessInTitle 返回需要单独展示的番号（标题已带则为空串）。
const numberUnlessInTitle = (title, number) => (titleCarriesNumber(title, number) ? '' : number);

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
  const requestToken = token;
  const currentIdentity = () => localStorage.getItem('emby_token') === requestToken;
  const identityError = () => Object.assign(new Error('登录身份已变更，请重新打开页面'), { status: 401, code: 'IDENTITY_CHANGED' });
  if (!currentIdentity()) throw identityError();
  const headers = { 'X-Emby-Token': requestToken, ...(options.headers || {}) };
  if (!(options.body instanceof FormData) && !headers['Content-Type']) headers['Content-Type'] = 'application/json';
  const response = await fetch('/api/admin' + path, { ...options, headers });
  if (!currentIdentity()) throw identityError();
  if (response.status === 401) {
    localStorage.removeItem('emby_token');
    window.location.replace('/');
    const error = new Error('登录已失效');
    error.status = 401;
    throw error;
  }
  if (!response.ok) {
    let message = '请求失败';
    let code;
    try { const body = await response.json(); message = body.error || message; code = body.code; } catch { /* ignore */ }
    if (!currentIdentity()) throw identityError();
    const error = new Error(message);
    error.status = response.status;
    error.code = code;
    throw error;
  }
  const webhookStatus = response.headers?.get?.('X-Webhook-Status');
  if (webhookStatus === 'partial') toast('部分通知未送达，业务操作已完成。', 'error');
  if (response.status === 204) {
    return webhookStatus ? { webhook_status: webhookStatus } : null;
  }
  const result = await response.json();
  if (!currentIdentity()) throw identityError();
  return result;
}

async function deleteIndexedItem(id) {
  const result = await api('/items/' + id, { method: 'DELETE' });
  if (result?.webhook_status === 'failed') {
    toast('索引已删除，但删除联动通知失败；请人工核对远端，不要重复删除。', 'error');
  } else if (result?.webhook_status === 'partial') {
    // api() 已统一提示“部分通知未送达，业务操作已完成”，这里不重复。
  } else if (result?.webhook_status === 'skipped') {
    toast('索引已删除，但源路径不符合通知条件，未发送删除联动。', 'error');
  } else if (result?.webhook_status === 'sent') {
    toast('索引已删除；接收端已确认通知，请以接收端日志核对远端结果。', 'ok');
  } else {
    toast('已删除索引', 'ok');
  }
}

const STATUS_TEXT = {
  success: '已刮削',
  manual: '手动录入',
  pending: '未刮削',
  needs_attention: '需处理',
  failed: '失败',
  skipped: '跳过'
};
// 「需处理」内部再按 last_scrape_error 前缀区分两种子情形：
// 带 store.ScrapeConfirmPrefix（[待人工确认] ）的是自动刮削主动跳过，
// 其余非空错误才是真失败。判定与后端 store.ScrapeConfirmPrefix 保持一致。
const SCRAPE_CONFIRM_PREFIX = '[待人工确认] ';
const needsAttentionBadge = error => {
  const text = String(error || '');
  return text.startsWith(SCRAPE_CONFIRM_PREFIX)
    ? '<span class="wall-badge confirm">待人工确认</span>'
    : '<span class="wall-badge failed">刮削失败</span>';
};
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
  settings: { title: '设置', crumb: 'Archive / Settings' },
  activity: { title: '活动记录', crumb: 'Archive / Activity' },
  logs: { title: '运行日志', crumb: 'Archive / Logs' },
  scheduled: { title: '计划任务', crumb: 'Archive / Scheduled' },
  scrape: { title: '刮削', crumb: 'Archive / Scrape' },
  translate: { title: '独立翻译', crumb: 'Archive / Translate' },
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

// 异步保存只刷新提交所属的导航，不能把用户拉回已经离开的页面。
function refreshPage(render, gen) {
  if (gen !== pageGen) return;
  return render();
}

/* ---------------------------------------------------------------- 总览 */
let dashboardStop = null;
function stopDashboardPolling() {
  if (typeof dashboardStop === 'function') dashboardStop();
  dashboardStop = null;
}
async function pageOverview() {
  const gen = pageGen, identity = token;
  stopDashboardPolling();
  if (typeof mountDashboard !== 'function') {
    content.innerHTML = '<section class="panel" role="alert"><h2>监控资源未加载</h2><p>请刷新页面重试；没有读取到的指标不会显示为零。</p></section>';
    return;
  }
  dashboardStop = mountDashboard(content, {
    apiRequest: api,
    isCurrent: () => gen === pageGen && token === identity && localStorage.getItem('emby_token') === identity
  });
}

/* ---------------------------------------------------------------- 媒体库 */
async function pageLibraries() {
  const gen = pageGen;
  const data = await api('/libraries');
  if (gen !== pageGen) return; // 等待期间切走了页面，这次结果作废
  const items = data.items || [];
  const total = data.total ?? items.length;
  content.innerHTML = `
    <section class="panel">
      <div class="panel-head"><h2>媒体库</h2><span class="hint" style="margin:0">共 ${esc(total)} 个 · 顺序即 Emby 客户端里的库顺序</span></div>
      ${items.length ? `
        <div class="table-wrap"><table>
          <thead><tr>
            <th style="width:56px">顺序</th>
            <th>媒体库</th>
            <th>路径</th>
            <th class="lib-id">ID</th>
            <th style="text-align:right">操作</th>
          </tr></thead>
          <tbody>${items.map((item, index) => `<tr>
            <td><div class="lib-order">
              <button class="icon-btn" data-lib-move="up" data-lib="${esc(item.Id)}" title="上移"${index === 0 ? ' disabled' : ''}>${icon('arrow-up')}</button>
              <button class="icon-btn" data-lib-move="down" data-lib="${esc(item.Id)}" title="下移"${index === items.length - 1 ? ' disabled' : ''}>${icon('arrow-down')}</button>
            </div></td>
            <td><div class="lib-cell">
              ${item.cover
                ? `<img class="lib-cover" loading="lazy" src="${esc(item.cover)}?maxWidth=120" alt="">`
                : `<span class="lib-cover-fallback">${icon('film')}</span>`}
              <div>
                <strong class="title">${esc(item.Name)}</strong>
                <span class="sub"><span class="lib-count">${esc(item.count ?? 0)}<small>部影片</small></span></span>
              </div>
            </div></td>
            <td class="mono"><span class="lib-path">${esc(item.Path)}</span></td>
            <td class="num lib-id">${esc(item.Id)}</td>
            <td><div class="row-actions">
              <button class="btn btn-sm" data-lib-cover="${esc(item.Id)}" title="上传封面图片">${icon('image')}<span>封面</span></button>
              ${item.cover ? `<button class="icon-btn" data-lib-cover-clear="${esc(item.Id)}" title="移除封面（回退自动封面）">${icon('x')}</button>` : ''}
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
    </section>
    <input type="file" id="lib-cover-file" accept="image/*" hidden>`;
  document.querySelector('#library-form').addEventListener('submit', async event => {
    event.preventDefault();
    const body = Object.fromEntries(new FormData(event.target));
    try {
      const lib = await api('/libraries', { method: 'POST', body: JSON.stringify(body) });
      toast(`已添加媒体库「${lib.Name}」`);
      refreshPage(pageLibraries, gen);
    } catch (e) { toast(e.message, 'error'); }
  });
  document.querySelectorAll('[data-lib-scan]').forEach(button => button.addEventListener('click', () => runScan(button.dataset.libScan)));
  document.querySelectorAll('[data-lib-move]').forEach(button => button.addEventListener('click', async () => {
    button.disabled = true;
    try {
      await api('/libraries/' + button.dataset.lib + '/move', { method: 'POST', body: JSON.stringify({ direction: button.dataset.libMove }) });
      refreshPage(pageLibraries, gen);
    } catch (e) { toast(e.message, 'error'); button.disabled = false; }
  }));
  // 上传封面：所有「封面」按钮共用同一个隐藏 file input，先记住目标库再打开选择器。
  const coverInput = document.querySelector('#lib-cover-file');
  let coverTarget = null;
  document.querySelectorAll('[data-lib-cover]').forEach(button => button.addEventListener('click', () => {
    coverTarget = button.dataset.libCover;
    coverInput.value = '';
    coverInput.click();
  }));
  coverInput.addEventListener('change', async () => {
    const file = coverInput.files && coverInput.files[0];
    if (!file || !coverTarget) return;
    const form = new FormData();
    form.append('file', file);
    try {
      await api('/libraries/' + coverTarget + '/cover', { method: 'POST', body: form });
      toast('封面已更新', 'ok');
      refreshPage(pageLibraries, gen);
    } catch (e) { toast(e.message, 'error'); }
  });
  document.querySelectorAll('[data-lib-cover-clear]').forEach(button => button.addEventListener('click', async () => {
    if (!confirm('移除上传的封面？将回退到目录自带图或影片代表图。')) return;
    try { await api('/libraries/' + button.dataset.libCoverClear + '/cover', { method: 'DELETE' }); toast('封面已移除', 'ok'); refreshPage(pageLibraries, gen); }
    catch (e) { toast(e.message, 'error'); }
  }));
  document.querySelectorAll('[data-lib-delete]').forEach(button => button.addEventListener('click', async () => {
    if (!confirm('删除该媒体库及其影片索引？不会删除磁盘文件，但该库影片的播放进度/收藏会一并清除。')) return;
    try { await api('/libraries/' + button.dataset.libDelete, { method: 'DELETE' }); toast('媒体库已删除', 'ok'); refreshPage(pageLibraries, gen); }
    catch (e) { toast(e.message, 'error'); }
  }));
}

/* ---------------------------------------------------------------- 媒体墙 */
function wallCard(item, ud) {
  const st = item.Status || item.status || '';
  const title = item.Title || '';
  const playable = st === 'success' || st === 'manual';
  // 海报格子最宽约 185px（.wall 的 minmax(148px,1fr)）；按 1x/2x 取 200/400，
  // 避免 1x 屏也拉 360 的缩略图。不带 maxWidth 会拉原图（约 56KB/张 × 每页 100 张），
  // 服务端虽支持按需缩略（LRU 缓存 + 并发限流 + 失败回退原图），前端不请求就等于白搭。
  const posterBase = item.PosterPath ? `/Items/${item.id}/Images/Primary`
    : (item.LandscapePath ? `/Items/${item.id}/Images/Thumb` : '');
  const poster = posterBase ? `${posterBase}?maxWidth=360` : '';
  const posterSrcset = posterBase ? `${posterBase}?maxWidth=200 1x, ${posterBase}?maxWidth=400 2x` : '';
  const sub = [numberUnlessInTitle(title, item.Number), item.Year, item.OriginalTitle].filter(Boolean).join(' · ')
    || (item.source_protocol || item.SourceProtocol || '');
  const progress = ud && item.RuntimeSeconds > 0 && ud.position_ticks > 0
    ? Math.min(100, Math.round(ud.position_ticks / (item.RuntimeSeconds * 10000000) * 100)) : 0;
  // 需处理的两种子情形（刮削失败 / 待人工确认）用 badge 区分，其余状态直接用状态名。
  const attention = st === 'pending' && item.last_scrape_error;
  const stateBadge = attention
    ? needsAttentionBadge(item.last_scrape_error)
    : (st !== 'success' ? `<span class="wall-badge ${esc(st)}">${esc(STATUS_TEXT[st] || st)}</span>` : '');
  const badges = [
    stateBadge,
    ud && ud.played ? '<span class="wall-badge played">已看</span>' : '',
    ud && ud.favorite ? '<span class="wall-badge fav">♥</span>' : '',
    (item.AdditionalParts || []).length ? `<span class="wall-badge multi">CD×${(item.AdditionalParts || []).length + 1}</span>` : ''
  ].join('');
  const body = poster
    ? `<img loading="lazy" src="${esc(poster)}"${posterSrcset ? ` srcset="${esc(posterSrcset)}"` : ''} alt="">`
    : `<span class="wall-path" title="${esc(item.source_path)}">${esc(item.source_path || title || '—')}</span>`;
  const label = title || String(item.source_path || '').split(/[\\/]/).pop() || '—';
  return `
    <article class="wall-card" data-play="${item.id}">
      <div class="wall-poster">
        ${body}
        <div class="wall-badges">${badges}</div>
        ${progress ? `<div class="wall-progress"><i style="width:${progress}%"></i></div>` : ''}
        ${typeof me !== 'undefined' && me?.is_admin ? `<label class="wall-select"><input type="checkbox" data-translate-select="${item.id}" aria-label="选择翻译：${esc(label)}">选择</label>` : ''}
        <div class="wall-actions">
          <button class="icon-btn" data-probe="${item.id}" title="探测媒体信息（ffprobe，写回 NFO）">${icon('probe')}</button>
          <button class="icon-btn" data-reread="${item.id}" title="重读 .strm 与 NFO">${icon('refresh')}</button>
          <button class="icon-btn danger" data-delete="${item.id}" title="删除索引（启用 Webhook 时可能联动删除远端目录）">${icon('trash')}</button>
        </div>
        ${playable ? `<button class="wall-play" data-quickplay="${item.id}" title="直接播放">${icon('play')}</button>` : ''}
      </div>
      <div class="wall-meta">
        <button type="button" class="wall-title" data-open="${item.id}" title="${esc(title || item.source_path)}">${esc(label)}</button>
        <small>${esc(sub)}</small>
      </div>
    </article>`;
}

const WALL_PAGE_SIZE = 100;
// 筛选后整页重绘会丢焦点；记录触发控件选择器，重绘后还原（P1-9）。
let pendingWallFocus = '';
let wallState = null;
let wallGen = 0; // 列表请求代号，单调递增；用于丢弃被新搜索/筛选作废的过期响应

async function pageItems() {
  const gen = pageGen;
  const requestGen = ++wallGen; // 必须在 /libraries 之前作废同导航内的旧筛选请求
  if (typeof stopTranslationPolling === 'function') stopTranslationPolling();
  stopProbePolling();
  stopLogsPolling();
  wallState = null;
  const params = new URLSearchParams(location.search);
  // 旧书签兼容：早期用 ?scrape=failed 表示「刮削失败」，现已并入 ?status=needs_attention。
  if (!params.get('status') && params.get('scrape') === 'failed') {
    params.set('status', 'needs_attention');
    history.replaceState({}, '', location.pathname + '?' + params.toString() + location.hash);
  }
  const status = params.get('status') || '';
  const search = params.get('search') || '';
  const sort = params.get('sort') || 'datecreated';
  const order = params.get('order') || (sort === 'title' ? 'asc' : 'desc');
  // 库筛选：URL 里的 library_id 若指向已删除的库，回退「全部」并同步清洗 URL，
  // 避免刷新后一直停在空列表（旧书签/前进后退同样适用）。
  const libraries = ((await api('/libraries')).items || []);
  if (gen !== pageGen || requestGen !== wallGen) return;
  const requestedLib = params.get('library_id') || '';
  const libraryID = libraries.some(lib => String(lib.Id) === requestedLib) ? requestedLib : '';
  if (requestedLib && !libraryID) {
    params.delete('library_id');
    history.replaceState({}, '', location.pathname + (params.toString() ? '?' + params.toString() : '') + location.hash);
  }
  const activeLib = libraries.find(lib => String(lib.Id) === libraryID);
  // 实体筛选（详情抽屉里点演员/类型/厂商/合集跳过来的），单值，URL 可见且可一键清除。
  const entity = ENTITY_PARAMS.map(([key, label]) => [key, label, params.get(key) || '']).find(item => item[2]) || null;
  wallState = {
    status, search, sort, order, libraryID, libraryName: activeLib ? activeLib.Name : '',
    entity, offset: 0, total: 0, loading: false, failed: false, done: false, items: [], userdata: {}
  };

  wallState.translateSelection = new Set();
  // 一排互斥的状态筛选：未刮削 / 需处理 互斥（合起来是全部 pending）；
  // 已刮削的片子重刮失败仍属「已刮削」，不会掉进需处理。
  const pills = [
    ['', '全部'],
    ['unscraped', '未刮削'],
    ['success', '已刮削'],
    ['manual', '手动录入'],
    ['needs_attention', '需处理']
  ];
  const sorts = [['datecreated', '最近入库'], ['title', '标题'], ['year', '年份'], ['communityrating', '评分']];
  // 库少时平铺成 Tab，库多（>8）转下拉，避免筛选行被撑爆。
  const libFilter = libraries.length <= 8
    ? `<button class="pill ${libraryID === '' ? 'is-active' : ''}" data-lib="" aria-pressed="${libraryID === ''}">全部库</button>` +
      libraries.map(lib => `<button class="pill ${libraryID === String(lib.Id) ? 'is-active' : ''}" data-lib="${esc(lib.Id)}" aria-pressed="${libraryID === String(lib.Id)}">${esc(lib.Name)}</button>`).join('')
    : `<select id="item-lib" aria-label="媒体库"><option value="">全部库</option>${libraries.map(lib =>
        `<option value="${esc(lib.Id)}" ${libraryID === String(lib.Id) ? 'selected' : ''}>${esc(lib.Name)}</option>`).join('')}</select>`;
  content.innerHTML = `
    <section class="panel">
      <div class="panel-head">
        <h2>媒体墙</h2>
        <div class="panel-actions">
          <span class="hint" id="wall-count" style="margin:0"></span>

        </div>
      </div>
      ${libraries.length ? `<div class="filters">${libFilter}</div>` : ''}
      ${entity ? `<div class="filters"><span class="filter-chip">${esc(entity[1])}：${esc(entity[2])}
        <button class="chip-x" id="entity-clear" title="清除该筛选" aria-label="清除筛选">✕</button></span></div>` : ''}
      <div class="filters wall-toolbar">
        <input id="item-search" aria-label="搜索媒体" placeholder="搜索标题 / 番号 / 原名" value="${esc(search)}">
        <select id="item-sort" aria-label="排序方式">${sorts.map(([value, label]) => `<option value="${value}" ${sort === value ? 'selected' : ''}>${esc(label)}</option>`).join('')}</select>
      </div>
      <div class="filters wall-status-filters">
        ${pills.map(([value, label]) => `<button class="pill ${status === value ? 'is-active' : ''}" data-status="${value}" aria-pressed="${status === value}">${esc(label)}</button>`).join('')}
      </div>
      <div id="probe-progress" class="probe-progress" hidden>
        <div class="scan-progress-head"><strong id="probe-progress-title">探测中…</strong><span id="probe-progress-count"></span></div>
        <div class="scan-progress-bar"><i id="probe-progress-fill"></i></div>
        <small id="probe-progress-detail"></small>
      </div>
      <details class="disclosure wall-maintenance"><summary class="disclosure-summary">批量维护与翻译</summary><div class="disclosure-body">
<button class="btn" id="probe-media" title="调用系统 ffprobe 读取码率/分辨率/编码等真实参数并写回 NFO（与扫库相互独立）">${icon('probe')}<span>探测媒体信息</span></button>
      ${typeof me !== 'undefined' && me?.is_admin ? `<div class="filters"><button type="button" class="btn" id="wall-translate-toggle" aria-expanded="false" aria-controls="wall-translate-panel">选择影片仅翻译</button><span id="wall-translate-count" role="status">已选 0 部（最多1000）</span></div><section id="wall-translate-panel" hidden>${translationWidget('wall-translate')}</section>` : ''}
      </div></details>
      <div class="wall" id="wall"></div>
      <div id="wall-empty"></div>
      <div id="wall-more" class="wall-more"></div>
      <p class="hint">点击海报打开详情抽屉（抽屉内可播放、刮削、探测、编辑元数据）；海报上的播放图标为快捷播放。非 http(s) 源的 .strm 在扫描时直接跳过，不会出现在这里。</p>
    </section>`;
  if (typeof pendingWallFocus !== 'undefined' && pendingWallFocus) {
    const target = document.querySelector(pendingWallFocus);
    if (target && typeof target.focus === 'function') target.focus();
    pendingWallFocus = '';
  }

  // 离散筛选（状态/库/排序）压入历史，浏览器前进/后退可逐步回退到上一个筛选组合。
  // 始终保留 hash，否则一次筛选就会让「刷新」离开媒体墙页。
  const queryURL = qs => location.pathname + (qs.toString() ? '?' + qs.toString() : '') + location.hash;
  const pushQuery = qs => history.pushState({}, '', queryURL(qs));
  const replaceQuery = qs => history.replaceState({}, '', queryURL(qs));
  // 状态筛选：一排互斥，点第二次取消回「全部」（与库筛选同款 toggle 语义）。
  document.querySelectorAll('.pill[data-status]').forEach(button => button.addEventListener('click', () => {
    const qs = new URLSearchParams(location.search);
    const value = button.dataset.status;
    if (!value || qs.get('status') === value) qs.delete('status'); else qs.set('status', value);
    qs.delete('search');
    // 选中状态时清掉旧的 scrape 参数（早期书签可能两个都在）。
    qs.delete('scrape');
    pendingWallFocus = '[data-status="' + (qs.get('status') || '') + '"]';
    pushQuery(qs);
    refreshPage(pageItems, gen);
  }));
  // 库筛选 Tab：与状态/搜索/排序叠加，只动 library_id 一个参数。
  document.querySelectorAll('.pill[data-lib]').forEach(button => button.addEventListener('click', () => {
    const qs = new URLSearchParams(location.search);
    const value = button.dataset.lib;
    if (value) qs.set('library_id', value); else qs.delete('library_id');
    pendingWallFocus = '[data-lib="' + (qs.get('library_id') || '') + '"]';
    pushQuery(qs);
    refreshPage(pageItems, gen);
  }));
  const libSelect = document.querySelector('#item-lib');
  if (libSelect) libSelect.addEventListener('change', () => {
    const qs = new URLSearchParams(location.search);
    if (libSelect.value) qs.set('library_id', libSelect.value); else qs.delete('library_id');
    pendingWallFocus = '#item-lib';
    pushQuery(qs);
    refreshPage(pageItems, gen);
  });
  const searchInput = document.querySelector('#item-search');
  searchInput.addEventListener('input', debounce(() => {
    // 切页或同页重建之后，旧搜索延时不能改URL或复活新观察器。
    if (gen !== pageGen || document.querySelector('#item-search') !== searchInput) return;
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
    pendingWallFocus = '#item-sort';
    pushQuery(q);
    refreshPage(pageItems, gen);
  });
  const entityClear = document.querySelector('#entity-clear');
  if (entityClear) entityClear.addEventListener('click', () => {
    const qs = new URLSearchParams(location.search);
    ENTITY_PARAMS.forEach(([key]) => qs.delete(key));
    pushQuery(qs);
    refreshPage(pageItems, gen);
  });
  const probeButton = document.querySelector('#probe-media');
  if (probeButton) probeButton.addEventListener('click', runProbeAll);
  // 页面恢复也走有超时/退避的观察器，不在启动阶段裸读全局进度。
  startProbePolling();
  const batchTranslate = document.querySelector('#wall-translate');
  if (batchTranslate && typeof me !== 'undefined' && me?.is_admin) {
    const sourceWall = wallState;
    let submittedWall = null;
    mountTranslation(batchTranslate, () => {
      submittedWall = wallGen;
      return { movie_ids: [...sourceWall.translateSelection] };
    }, () => wallState === sourceWall && document.querySelector('#wall-translate') === batchTranslate,
    () => { if (wallState === sourceWall && wallGen === submittedWall) reloadWall(); });
    document.querySelector('#wall-translate-toggle').addEventListener('click', event => {
      const panel = document.querySelector('#wall-translate-panel');
      panel.hidden = !panel.hidden;
      event.currentTarget.setAttribute('aria-expanded', String(!panel.hidden));
      document.querySelector('#wall').classList.toggle('is-selecting', !panel.hidden);
    });
  }
  // 事件委托：卡片按页追加，统一在容器上处理，避免每页重新绑定。
  document.querySelector('#wall').addEventListener('click', async event => {
    const selection = event.target.closest('[data-translate-select], .wall-select');
    if (selection) {
      event.stopPropagation();
      const input = selection.matches?.('input') ? selection : selection.querySelector?.('input');
      if (!input || !wallState?.translateSelection) return;
      if (input.checked) {
        if (wallState.translateSelection.size >= 1000) { input.checked = false; toast('每批最多选择1000部', 'error'); return; }
        wallState.translateSelection.add(input.dataset.translateSelect);
      } else wallState.translateSelection.delete(input.dataset.translateSelect);
      document.querySelector('#wall-translate-count').textContent = `已选 ${wallState.translateSelection.size} 部（最多1000）`;
      return;
    }
    const probe = event.target.closest('[data-probe]');
    if (probe) {
      event.stopPropagation();
      probeItem(probe.dataset.probe);
      return;
    }
    const reread = event.target.closest('[data-reread]');
    if (reread) {
      event.stopPropagation();
      try { const r = await api('/items/' + reread.dataset.reread + '/reread', { method: 'POST' }); toast(`状态已更新：${STATUS_TEXT[r.status] || r.status}`); refreshPage(pageItems, gen); }
      catch (e) { toast(e.message, 'error'); }
      return;
    }
    const remove = event.target.closest('[data-delete]');
    if (remove) {
      event.stopPropagation();
      if (!confirm('删除该影片索引？如已启用删除 Webhook，接收端可能同时删除远端整个目录，且无法撤销。继续？')) return;
      try { await deleteIndexedItem(remove.dataset.delete); refreshPage(pageItems, gen); }
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
    const open = event.target.closest('[data-open]');
    if (open) {
      event.stopPropagation();
      const target = wallState.items.find(m => String(m.id) === open.dataset.open);
      if (target) openDetail(target);
      return;
    }
    const card = event.target.closest('.wall-card');
    if (!card) return;
    const item = wallState.items.find(m => String(m.id) === card.dataset.play);
    if (item) openDetail(item);
  });
  await loadWallPage();
}

// reloadWall 只重置列表并重新拉取，保留筛选栏与搜索框焦点（搜索输入走此路径，避免整页重渲染打断输入）。
function reloadWall() {
  if (!wallState) return;
  wallGen += 1; // 作废尚未返回的旧请求
  // 搜索不重建DOM，但仍更换观察代次，避免旧回包污染或永久静默。
  startProbePolling();
  wallState.offset = 0;
  wallState.total = 0;
  wallState.items = [];
  wallState.translateSelection?.clear();
  const selectedCount = document.querySelector('#wall-translate-count');
  if (selectedCount) selectedCount.textContent = '已选 0 部（最多1000）';
  wallState.userdata = {};
  wallState.done = false;
  wallState.loading = false;
  wallState.failed = false;
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
  if (!wallState || wallState.loading || wallState.failed || wallState.done) return;
  const more = document.querySelector('#wall-more');
  if (!more) return;
  if (more.getBoundingClientRect().top <= window.innerHeight + 600) loadWallPage();
}

async function loadWallPage() {
  if (!wallState || wallState.loading || wallState.failed || wallState.done) return;
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
            wallState.search ? '试试其它关键词。' : '添加媒体库并扫描后再来看看。')
        : '';
    }
    if (more) more.textContent = wallState.done && data.total > 0 ? `已全部加载（共 ${data.total} 条）` : '';
  } catch (error) {
    if (!wallState || wallGen !== gen) return;
    wallState.failed = true;
    if (more) {
      more.innerHTML = `<span role="alert">加载失败：${esc(error.message || error)}</span> <button class="btn btn-sm" type="button" id="wall-retry">重试</button>`;
      more.querySelector('#wall-retry').addEventListener('click', () => {
        if (!wallState || wallGen !== gen) return;
        wallState.failed = false;
        loadWallPage();
      });
    }
  } finally {
    if (wallState && wallGen === gen) {
      wallState.loading = false;
      // 首屏未填满时继续加载，直到撑满视口。
      wallMaybeLoadMore();
    }
  }
}

/* ------------------------------------------------------------ 详情抽屉 */
// 实体筛选参数：抽屉里点演员/类型/厂商/合集/标签会带着其中一个参数回到媒体墙。
// 必须与后端 SearchAdmin 认识的查询参数逐一对应（admin.go 解析
// tag/genre/studio/person/collection）；少一个，那类链接点了就等于没筛，
// URL 里还会留下一个清不掉的参数。
const ENTITY_PARAMS = [['person', '演员'], ['genre', '类型'], ['studio', '厂商'], ['collection', '合集'], ['tag', '标签']];

const detailOverlay = document.querySelector('#detail-overlay');
const detailDrawer = document.querySelector('#detail-drawer');

let detailGen = 0;
let detailTranslationStop = null;

function closeDetail() {
  if (typeof detailTranslationStop !== 'undefined') { detailTranslationStop?.(); detailTranslationStop = null; }
  detailGen += 1;
  detailOverlay.hidden = true;
  detailDrawer.innerHTML = '';
  overlayPop('#detail-overlay');
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

const EDIT_LOCK_FIELDS = [['Name', '标题'], ['Number', '番号'], ['OriginalTitle', '原名'], ['Overview', '简介'], ['ProductionYear', '年份'], ['Genres', '类型'], ['Tags', '标签'], ['Studios', '制作商'], ['Maker', '厂商'], ['Label', '厂牌']];

function editLockFields(original, inputs) {
  const known = new Set(EDIT_LOCK_FIELDS.map(([name]) => name.toLowerCase()));
  return [...new Set([
    ...(original || []).filter(name => !known.has(name.toLowerCase())),
    ...Array.from(inputs).filter(input => input.checked).map(input => input.dataset.lockField)
  ])];
}

// Explicit R1 recovery: re-read current NFO, never submit the old edit body.
function renderEditRecovery(panel, id, result, onRecovered, isCurrent) {
  panel.hidden = false;
  const digest = result.recovery && result.recovery.nfo_sha256;
  panel.innerHTML = `<p role="alert">NFO 已保存，但索引未完成。${result.manual_status === 'pending' ? '手动录入标记也未保存，请暂停自动刮削。' : ''} 不要再次提交旧表单。</p>
    <p class="hint">恢复只重读当前 NFO，不覆盖文件。文件已变化时请重新打开条目核对；若无法恢复，请由管理员检查文件权限、媒体库及数据库后人工对账。此操作未排入后台队列。</p>
    ${digest ? '<button class="btn" type="button" data-recover-edit>重试当前 NFO 索引</button>' : ''}
    <p role="alert" data-recovery-error></p>`;
  const retry = panel.querySelector('[data-recover-edit]');
  if (!retry) return;
  retry.addEventListener('click', async () => {
    if (!isCurrent() || retry.disabled) return;
    retry.disabled = true;
    try {
      const next = await api(`/items/${id}/recover-edit`, { method: 'POST', body: JSON.stringify({ nfo_sha256: digest }) });
      if (!isCurrent()) return;
      if (next.status === 'partial_success') renderEditRecovery(panel, id, next, onRecovered, isCurrent);
      else onRecovered();
    } catch (error) {
      if (!isCurrent()) return;
      panel.querySelector('[data-recovery-error]').textContent = error.message;
      retry.disabled = false;
    }
  });
}

/* 遮罩焦点：z-index 顺序与 Esc/Tab 行为一致（FE-04）。 */
const overlayFocusStack = [];
const OVERLAY_LAYERS = ['#scrape-overlay', '#player-overlay', '#detail-overlay'];

function overlayVisible(selector) {
  const overlay = document.querySelector(selector);
  return !!overlay && !overlay.hidden;
}

function topOverlaySelector() {
  return OVERLAY_LAYERS.find(overlayVisible) || '';
}

// 路由切换时按层级从上到下收起浮层，避免播放器/详情跨页残留。
function closeOverlaysForNavigation() {
  if (overlayVisible('#scrape-overlay')) closeScrapePreview();
  if (overlayVisible('#player-overlay')) closePlayer();
  if (overlayVisible('#detail-overlay')) closeDetail();
}

function overlayFocusables(selector) {
  const overlay = document.querySelector(selector);
  if (!overlay || typeof overlay.querySelectorAll !== 'function') return [];
  return Array.from(overlay.querySelectorAll(
    'button:not([disabled]), summary, [href], input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])'
  )).filter(element => !element.hidden && (typeof element.getClientRects !== 'function' || element.getClientRects().length > 0));
}

function overlayContains(overlay, element) {
  if (!overlay || !element) return false;
  return typeof overlay.contains === 'function' ? overlay.contains(element) : false;
}

// overlayPush 记录触发元素并把焦点移入遮罩；重复打开同一遮罩不会重复入栈。
function overlayPush(selector) {
  const overlay = document.querySelector(selector);
  if (!overlay) return;
  const active = document.activeElement;
  const trigger = active && active !== document.body ? active : null;
  if (!overlayFocusStack.some(entry => entry.selector === selector)) overlayFocusStack.push({ selector, trigger });
  const target = overlayFocusables(selector)[0] || overlay;
  if (target && typeof target.focus === 'function') target.focus();
}

// overlayFocusFirst 在异步内容渲染后把焦点移到首个可操作元素；焦点已离开则不抢。
function overlayFocusFirst(selector) {
  if (topOverlaySelector() !== selector) return;
  const overlay = document.querySelector(selector);
  const active = document.activeElement;
  if (active && active !== document.body && active !== overlay && !overlayContains(overlay, active)) return;
  const target = overlayFocusables(selector)[0];
  if (target && typeof target.focus === 'function') target.focus();
}

function overlayPop(selector) {
  for (let i = overlayFocusStack.length - 1; i >= 0; i--) {
    if (overlayFocusStack[i].selector !== selector) continue;
    const { trigger } = overlayFocusStack.splice(i, 1)[0];
    if (trigger && typeof trigger.focus === 'function') {
      const stillThere = !document.contains || document.contains(trigger);
      if (stillThere) trigger.focus();
    }
    return;
  }
}

// overlayTrapTab 保持 Tab/Shift+Tab 在顶层遮罩内循环。
function overlayTrapTab(event) {
  if (event.key !== 'Tab') return;
  const selector = topOverlaySelector();
  if (!selector) return;
  const overlay = document.querySelector(selector);
  const items = overlayFocusables(selector);
  if (!items.length) {
    event.preventDefault();
    if (overlay && typeof overlay.focus === 'function') overlay.focus();
    return;
  }
  // 受控顺序循环：不依赖浏览器对「几乎不可聚焦」元素的自然 Tab 顺序，
  // 避免焦点落到 body 或播放器内部控件后逃出顶层遮罩（FE-07）。
  event.preventDefault();
  const active = document.activeElement;
  const index = items.indexOf(active);
  if (index === -1) {
    items[event.shiftKey ? items.length - 1 : 0].focus();
    return;
  }
  const next = (index + (event.shiftKey ? -1 : 1) + items.length) % items.length;
  items[next].focus();
}
/* 遮罩焦点结束 */

async function openDetail(item) {
  if (typeof detailTranslationStop !== 'undefined') { detailTranslationStop?.(); detailTranslationStop = null; }
  const gen = pageGen;
  const session = ++detailGen;
  const isCurrent = () => gen === pageGen && session === detailGen;
  detailDrawer.innerHTML = '<div class="detail-body"><p class="hint">加载中…</p></div>';
  detailOverlay.hidden = false;
  overlayPush('#detail-overlay');
  let data;
  try {
    data = await api(`/items/${item.id}/detail`);
  } catch (error) {
    if (!isCurrent()) return;
    detailDrawer.innerHTML = `<div class="detail-body"><p class="hint">${esc(error.message || error)}</p></div>`;
    return;
  }
  if (!isCurrent()) return;
  // 注意：store.Movie 的 JSON 字段名大小写并不统一——只有少数几个字段带小写下划线 tag
  // （id / collection / official_rating / source_path / source_protocol / created_at …），
  // 其余保持 Go 字段名。取值时按接口实际返回的键来写。
  const m = data.movie || {};
  const title = m.Title || String(item.id);
  const st = m.Status || '';
  const playable = st === 'success' || st === 'manual';
  // 详情海报渲染 104px 宽，背景铺满抽屉；按 1x/2x 取 120/240 与 540/1080，避免拉原图。
  const posterBase = m.PosterPath ? `/Items/${m.id}/Images/Primary` : '';
  const poster = posterBase ? `${posterBase}?maxWidth=240` : '';
  const posterSrcset = posterBase ? `${posterBase}?maxWidth=120 1x, ${posterBase}?maxWidth=240 2x` : '';
  const backdropBase = m.BackdropPath ? `/Items/${m.id}/Images/Backdrop`
    : (m.LandscapePath ? `/Items/${m.id}/Images/Thumb` : posterBase);
  const backdrop = backdropBase ? `${backdropBase}?maxWidth=1080` : '';
  const backdropSrcset = backdropBase ? `${backdropBase}?maxWidth=540 1x, ${backdropBase}?maxWidth=1080 2x` : '';
  const head = [numberUnlessInTitle(title, m.Number), m.Year, fmtDuration(m.RuntimeSeconds), m.Rating ? `★ ${m.Rating}` : ''].filter(Boolean).join(' · ');
  const actors = data.actors || [];
  const lockedFields = data.locked_fields || [];
  const lockedNames = new Set(lockedFields.map(name => name.toLowerCase()));

  detailDrawer.innerHTML = `
    <div class="detail-hero">
      ${backdrop ? `<img class="detail-backdrop" src="${esc(backdrop)}"${backdropSrcset ? ` srcset="${esc(backdropSrcset)}"` : ''} alt="">` : ''}
      <button id="detail-close" class="icon-btn detail-close" title="关闭 (Esc)">✕</button>
      <div class="detail-hero-body">
        ${poster ? `<img class="detail-poster" width="104" height="156" src="${esc(poster)}"${posterSrcset ? ` srcset="${esc(posterSrcset)}"` : ''} alt="">` : ''}
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
          : `<span class="hint" style="margin:0">该影片暂无可用源</span>`}
      </div>
      <details class="disclosure detail-maintenance"><summary class="disclosure-summary">维护与元数据编辑</summary><div class="disclosure-body">
      <div class="detail-actions">
        <button id="detail-probe" class="btn">${icon('probe')}<span>探测媒体信息</span></button>
        <button id="detail-scrape" class="btn">${icon('search')}<span>刮削</span></button>
        <button id="detail-reread" class="btn">${icon('refresh')}<span>重读源</span></button>
        <button id="detail-edit-toggle" class="btn" aria-expanded="false" aria-controls="detail-edit">${icon('edit')}<span>编辑元数据</span></button>
      </div>

      ${typeof me !== 'undefined' && me?.is_admin ? `<section class="detail-section"><h3>仅翻译</h3>${translationWidget('detail-translate')}</section>` : ''}
      <section class="detail-section" id="detail-edit" hidden>
        <h3>编辑元数据 <small>写入 NFO，保存后归入「手动录入」</small></h3>
        <form id="detail-edit-form" class="field-grid" aria-describedby="edit-clear-hint">
          <p id="edit-clear-hint" class="hint" style="grid-column:1/-1">留空的文本、列表和年份会清除对应元数据；未展示的字段保持不变。取消不会保存。</p>
          <div class="field"><label for="e-title">标题</label><input id="e-title" value="${esc(m.Title || '')}"></div>
          <div class="field"><label for="e-number">番号</label><input id="e-number" value="${esc(m.Number || '')}"></div>
          <div class="field"><label for="e-year">年份</label><input id="e-year" type="number" min="1900" max="2100" value="${esc(m.Year || '')}"></div>
          <div class="field"><label for="e-original">原名</label><input id="e-original" value="${esc(m.OriginalTitle || '')}"></div>
          <div class="field full"><label for="e-plot">简介</label><textarea id="e-plot">${esc(m.Plot || '')}</textarea></div>
          <div class="field"><label for="e-genres">类型（逗号分隔）</label><input id="e-genres" value="${esc((m.Genres || []).join(', '))}"></div>
          <div class="field"><label for="e-tags">标签（逗号分隔）</label><input id="e-tags" value="${esc((m.Tags || []).join(', '))}"></div>
          <div class="field"><label for="e-studios">制作商（逗号分隔）</label><input id="e-studios" value="${esc((m.Studios || []).join(', '))}"></div>
          <div class="field"><label for="e-maker">厂商</label><input id="e-maker" value="${esc(m.Maker || '')}"></div>
          <div class="field"><label for="e-label">厂牌</label><input id="e-label" value="${esc(m.Label || '')}"></div>
          <div class="field full"><label>源地址（只读，换源请用「重读源」）</label>
            <input value="${esc(m.source_path || '')}" readonly></div>
          <div class="field full">
            <label class="check"><input id="e-lock-data" type="checkbox"${data.lock_data ? ' checked' : ''}> 锁定全部元数据</label>
            <p class="hint">锁定阻止自动刮削覆盖（包括强制覆盖），仍允许人工编辑。全锁优先于以下单字段设置。</p>
            <div class="field-grid">${EDIT_LOCK_FIELDS.map(([name, label]) => `<label class="check"><input type="checkbox" data-lock-field="${esc(name)}"${lockedNames.has(name.toLowerCase()) ? ' checked' : ''}> 锁定${esc(label)}</label>`).join('')}</div>
          </div>
          <div class="form-foot" style="grid-column:1/-1">
            <button id="detail-edit-save" class="btn btn-accent" type="submit">${icon('plus')}<span>保存</span></button>
            <button id="detail-edit-cancel" class="btn" type="button">取消</button>
            <span class="hint" style="margin:0">走标签级更新，NFO 里未建模的标签不会被丢弃。</span>
          </div>
          <div id="detail-edit-recovery" class="field full" hidden></div>
        </form>
      </section>

      </div></details>

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
    if (isCurrent()) openDetail(item);
  });
  const translateRoot = document.querySelector('#detail-translate');
  if (translateRoot && me?.is_admin) {
    const originalWall = wallGen;
    detailTranslationStop?.();
    detailTranslationStop = mountTranslation(translateRoot, () => ({ movie_ids: [m.id] }),
      () => isCurrent() && document.querySelector('#detail-translate') === translateRoot,
      () => {
        if (!isCurrent()) return;
        if (wallGen === originalWall) reloadWall();
        if (document.querySelector('#detail-edit')?.hidden !== false) openDetail(item);
        else toast('翻译已完成；当前编辑草稿保持，请退出编辑后重新打开详情核对');
      });
  }
  // 单条刮削走「预览 → 人工确认」，取消则零写入零请求（需求 5j）。
  document.querySelector('#detail-scrape').addEventListener('click', () => openScrapePreview(item));
  document.querySelector('#detail-reread').addEventListener('click', async () => {
    try {
      const r = await api(`/items/${m.id}/reread`, { method: 'POST' });
      toast(`状态已更新：${STATUS_TEXT[r.status] || r.status}`);
      if (!isCurrent()) return;
      closeDetail();
      refreshPage(pageItems, gen);
    } catch (e) { toast(e.message, 'error'); }
  });
  // 元数据编辑：默认收起，点「编辑元数据」展开（表单已随抽屉渲染，取值即可）。
  const editSection = document.querySelector('#detail-edit');
  const editToggle = document.querySelector('#detail-edit-toggle');
  if (editToggle && editSection) {
    editToggle.addEventListener('click', () => {
      editSection.hidden = !editSection.hidden;
      editToggle.setAttribute('aria-expanded', String(!editSection.hidden));
    });
  }
  const editCancel = document.querySelector('#detail-edit-cancel');
  if (editCancel) editCancel.addEventListener('click', () => {
    editSection.hidden = true;
    editToggle?.setAttribute('aria-expanded', 'false');
  });
  const editForm = document.querySelector('#detail-edit-form');
  let editNeedsRecovery = false;
  if (editForm) editForm.addEventListener('submit', async event => {
    event.preventDefault();
    if (editNeedsRecovery) return; // Do not replay a stale edit after NFO was saved.
    const split = id => document.querySelector(id).value.split(/[,，]/).map(s => s.trim()).filter(Boolean);
    const year = Number(document.querySelector('#e-year').value) || 0;
    // 新人工 PATCH：空字符串/空数组/年份0是显式清除，false是显式解锁。
    // 不回退旧 PUT，否则清空会静默变成不修改。
    const body = {
      title: document.querySelector('#e-title').value.trim(),
      number: document.querySelector('#e-number').value.trim(),
      original_title: document.querySelector('#e-original').value.trim(),
      plot: document.querySelector('#e-plot').value,
      year,
      genres: split('#e-genres'),
      tags: split('#e-tags'),
      studios: split('#e-studios'),
      maker: document.querySelector('#e-maker').value.trim(),
      label: document.querySelector('#e-label').value.trim(),
      locked_fields: editLockFields(lockedFields, editForm.querySelectorAll('[data-lock-field]')),
      lock_data: document.querySelector('#e-lock-data').checked
    };
    const saveBtn = document.querySelector('#detail-edit-save');
    saveBtn.disabled = true;
    try {
      const result = await api(`/items/${m.id}`, { method: 'PATCH', body: JSON.stringify(body) });
      if (result.status === 'partial_success') {
        editNeedsRecovery = true;
        toast('NFO 已保存，索引尚未完成，请处理恢复提示', 'error');
        if (isCurrent()) renderEditRecovery(document.querySelector('#detail-edit-recovery'), m.id, result, () => {
          toast('索引恢复完成（手动录入）');
          closeDetail();
          refreshPage(pageItems, gen);
        }, isCurrent);
        return;
      }
      toast('元数据已保存（归类为手动录入）');
      if (!isCurrent()) return;
      closeDetail();
      refreshPage(pageItems, gen);
    } catch (e) {
      toast(e.message, 'error');
      saveBtn.disabled = false;
    }
  });
  // 实体跳转：统一委托，演员卡片与元数据里的类型/标签/厂商/合集共用一条路径。
  detailDrawer.querySelectorAll('[data-entity-key]').forEach(el => el.addEventListener('click', () => {
    gotoEntity(el.dataset.entityKey, el.dataset.entityValue);
  }));
  overlayFocusFirst('#detail-overlay');
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
  overlayPop('#player-overlay');
}

// streamURL 构造播放地址。播放器是 <video>/artplayer，浏览器不会给它带
// X-Emby-Token 这类自定义头，只能把凭据放进 query —— 后端 authOK 支持
// ?api_key=（与 Emby 客户端一致）。stream/proxy 两个端点都要求鉴权。
function streamURL(id, kind) {
  return '/Videos/' + id + '/' + kind + '?api_key=' + encodeURIComponent(token || '');
}

function openPlayer(item) {
  const overlay = document.querySelector('#player-overlay');
  const title = item.Title || String(item.source_path || '').split(/[\\/]/).pop() || '播放';
  document.querySelector('#player-title').textContent = title;
  overlay.hidden = false;
  overlayPush('#player-overlay');
  document.body.classList.add('player-open');
  const stage = document.querySelector('#player-stage');
  stage.innerHTML = '';
  const container = String(item.source_container || item.SourceContainer || '').toLowerCase();
  const type = container === 'webm' ? 'webm' : 'mp4';
  let usedProxy = false;
  artInstance = new Artplayer({
    container: stage,
    url: streamURL(item.id, 'stream'),
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
    artInstance.url = streamURL(item.id, 'proxy');
    // 回退发生在 error 回调里（非用户手势），移动端需静音才允许自动播放。
    artInstance.muted = true;
    artInstance.play().catch(() => { /* 用户可手动点播放 */ });
  });
}

/* ---------------------------------------------------------------- 设置 */
// 活动记录页 = 登录/播放审计（只读，管理员可见；保留 90 天且最多 5 万条）。
async function pageActivity() {
  const gen = pageGen;
  if (!me || !me.is_admin) {
    content.innerHTML = `<section class="panel">${empty('仅管理员可用', '活动记录只对管理员开放。')}</section>`;
    return;
  }
  const [logins, plays] = await Promise.all([
    api('/login-events?limit=50'), api('/playback-events?limit=50')
  ]);
  if (gen !== pageGen) return;
  content.innerHTML = `
    <section class="panel" aria-labelledby="audit-title">
      <div class="panel-head"><h2 id="audit-title">活动记录</h2><span class="hint" style="margin:0">只读 · 保留 90 天且最多 5 万条</span><button id="audit-refresh" class="btn btn-sm">刷新</button></div>
      <h3 style="margin:12px 0 6px">登录记录</h3>
      ${(logins.events || []).length ? `<div class="table-wrap"><table>
        <thead><tr><th>时间</th><th>账户</th><th>来源 IP</th><th>结果</th></tr></thead>
        <tbody>${logins.events.map(e => `<tr><td class="mono">${esc(e.created_at)}</td><td>${esc(e.name || '—')}</td><td class="mono">${esc(e.ip || '—')}</td><td>${e.success ? '成功' : '失败'}</td></tr>`).join('')}</tbody>
      </table></div>` : empty('暂无登录记录', '登录成功与失败都会记录。')}
      <h3 style="margin:18px 0 6px">播放记录</h3>
      ${(plays.events || []).length ? `<div class="table-wrap"><table>
        <thead><tr><th>时间</th><th>账户</th><th>影片</th><th>事件</th><th>位置</th></tr></thead>
        <tbody>${plays.events.map(e => `<tr><td class="mono">${esc(e.created_at)}</td><td>${esc(e.user_name || '—')}</td><td>${esc(e.movie_title || '#' + e.movie_id)}</td><td>${esc(e.kind)}</td><td class="mono">${esc(String(e.position_ticks))}</td></tr>`).join('')}</tbody>
      </table></div>` : empty('暂无播放记录', '播放/进度/停止事件会记录在这里。')}
    </section>`;
  document.querySelector('#audit-refresh')?.addEventListener('click', () => refreshPage(pageActivity, gen));
}

// 设置页 = 只读服务设置 + API 密钥管理（原独立的「API 密钥」页已并入这里）。
let settingsCategory = 'account';
function mountSettingsCategories(root) {
  const buttons = root.querySelectorAll('[data-settings-category]');
  const panels = root.querySelectorAll('[data-settings-panel]');
  const select = category => {
    settingsCategory = category;
    buttons.forEach(button => button.setAttribute('aria-pressed', String(button.dataset.settingsCategory === category)));
    panels.forEach(panel => { panel.hidden = panel.dataset.settingsPanel !== category; });
  };
  buttons.forEach(button => button.addEventListener('click', () => select(button.dataset.settingsCategory)));
  select(settingsCategory);
}
async function pageSettings() {
  const gen = pageGen;
  if (!me || !me.is_admin) {
    renderSelfPassword();
    return;
  }
  const [s, keyData, recovery, users, libs] = await Promise.all([
    api('/settings'), api('/apikeys'), api('/recovery'), api('/users'), api('/libraries')
  ]);
  if (gen !== pageGen) return; // 等待期间切走了页面，这次结果作废
  const fields = [
    ['监听地址', s.listen],
    ['数据库', s.db_path],
    ['缓存后端', s.cache + (s.redis_addr ? ` · ${s.redis_addr}/${s.redis_db}` : '')],
    ['Redis 在线', s.redis_online ? '是' : '否'],
    ['登录会话有效期', s.session_ttl_days > 0 ? `${s.session_ttl_days} 天（使用中自动续期）` : '永不过期'],
    ['媒体信息探测', s.ffprobe_error ? `不可用：${s.ffprobe_error}` : (s.ffprobe_path ? `就绪：${s.ffprobe_path}` : '未检测（需要 ffprobe）')]
  ];
  const keys = keyData.items || [];
  content.innerHTML = `
    <nav class="settings-nav" aria-label="设置分类">${[['account', '账户与安全'], ['users', '用户与授权'], ['notifications', '通知'], ['logs', '日志与保留'], ['cache', '图片缓存'], ['recovery', '索引恢复' + ((recovery.items || []).length ? '（' + recovery.items.length + '）' : '')], ['service', '服务信息']].map(([key, label]) => `<button type="button" class="btn" data-settings-category="${key}" aria-pressed="${key === 'account'}">${esc(label)}</button>`).join('')}</nav>
    <div class="settings-category" data-settings-panel="account">
    <section class="panel">
      <div class="panel-head"><h2>登录会话</h2></div>
      <p class="hint" style="margin:0 0 12px">退出全部会话会让本机与其它设备上的登录令牌立即失效（API 密钥不受影响）；当前页面也会回到登录页。</p>
      <button id="logout-all" class="btn btn-danger">${icon('trash')}<span>退出全部会话</span></button>
    </section>

    <section class="panel" aria-labelledby="pw-title">
      <div class="panel-head"><h2 id="pw-title">修改我的密码</h2></div>
      <form id="pw-form" class="field-grid">
        <div class="field"><label for="pw-current">当前密码</label><input id="pw-current" type="password" required autocomplete="current-password"></div>
        <div class="field"><label for="pw-new">新密码</label><input id="pw-new" type="password" minlength="9" required autocomplete="new-password"></div>
        <div class="form-foot" style="grid-column:1/-1;margin:2px 0 0"><button class="btn btn-accent">保存并退出其它会话</button></div>
      </form>
      <p class="hint">修改后当前与其它设备的登录令牌都会失效，需要重新登录。</p>
    </section>
    </div>

    <div class="settings-category" data-settings-panel="users" hidden>
    <section class="panel" aria-labelledby="users-title">
      <div class="panel-head"><h2 id="users-title">用户管理</h2><span class="hint" style="margin:0">共 ${esc(String((users.users || []).length))} 个账户 · 媒体库默认无授权</span></div>
      <div class="table-wrap"><table class="user-table">
        <thead><tr><th>账户</th><th>角色</th><th>最后登录</th><th>媒体库授权</th><th style="text-align:right">操作</th></tr></thead>
        <tbody>${(users.users || []).map(u => `<tr data-user-row="${u.id}">
          <td data-label="账户"><strong class="title">${esc(u.name)}</strong>${u.id === 1 ? ' <span class="badge">owner</span>' : ''}</td>
          <td data-label="角色">${u.is_admin ? '管理员' : '普通用户'}</td>
          <td data-label="最后登录" class="mono">${esc(u.last_login_at || '—')}</td>
          <td data-label="媒体库授权"><div class="chip-row">${(libs.items || []).map(l => `<label class="check"><input type="checkbox" data-user-lib="${u.id}" value="${l.Id}"${(u.library_ids || []).includes(l.Id) ? ' checked' : ''}> ${esc(l.Name)}</label>`).join('') || '—'}</div></td>
          <td data-label="操作"><div class="row-actions">
            <button class="btn btn-sm" data-user-save="${u.id}">保存授权</button>
            <button class="btn btn-sm" data-user-reset="${u.id}" data-user-name="${esc(u.name)}">重置密码</button>
            <button class="icon-btn danger" data-user-del="${u.id}" title="删除用户"${u.id === 1 ? ' disabled' : ''}>${icon('trash')}</button>
          </div></td>
        </tr>`).join('')}</tbody>
      </table></div>
      <form id="user-create-form" class="field-grid" style="margin-top:18px">
        <div class="field"><label for="user-name">新用户名</label><input id="user-name" name="name" minlength="3" required placeholder="至少 3 个字符"></div>
        <div class="field"><label for="user-pass">初始密码</label><input id="user-pass" name="password" type="password" minlength="9" required placeholder="至少 9 个字符"></div>
        <div class="field"><label class="check"><input id="user-admin" name="is_admin" type="checkbox"> 管理员</label></div>
        <div class="form-foot" style="grid-column:1/-1;margin:2px 0 0"><button class="btn btn-accent">${icon('plus')}<span>创建用户</span></button></div>
      </form>
      <p class="hint">普通用户默认看不到任何媒体库；在行内勾选后点「保存授权」。删除用户会保留其播放与登录历史。</p>
    </section>

    <section class="panel">
      <div class="panel-head"><h2>管理员 API 密钥</h2><span class="hint" style="margin:0">共 ${esc(keyData.total ?? keys.length)} 个 · 全权凭据，可调用全部管理与 Emby 接口</span></div>
      <p class="warning-block" role="note">D02 已确认：当前单用户版本中 API 密钥等同管理员权限，泄露即等于交出管理面；请仅发给可信脚本。未来多用户版本中，非管理员用户不得创建或管理 API 密钥。</p>
      ${createdAPIKey ? `<div class="warning-block" role="note">新密钥只显示这一次，请立即复制并妥善保存：
        <code id="created-key" class="mono">${esc(createdAPIKey)}</code>
        <div class="row-actions" style="margin-top:8px">
          <button class="btn btn-sm" data-copy-created="${esc(createdAPIKey)}">复制新密钥</button>
          <button class="btn btn-sm" id="dismiss-created-key">我已保存</button>
        </div></div>` : ''}
      ${keys.length ? `
        <div class="table-wrap"><table>
          <thead><tr><th>名称</th><th>密钥</th><th>创建时间</th><th>版本</th><th>最近轮转</th><th style="text-align:right">操作</th></tr></thead>
          <tbody>${keys.map(item => `<tr>
            <td><strong class="title">${esc(item.name || '—')}</strong></td>
            <td class="mono">${esc(item.key_prefix || '—')}…</td>
            <td class="mono">${esc(item.created_at || '—')}</td>
            <td class="mono">v${esc(item.version || 1)}</td>
            <td class="mono">${esc(item.rotated_at || '—')}</td>
            <td><div class="row-actions">
              <button class="icon-btn" data-key-rotate="${esc(item.id)}" data-version="${esc(item.version || 1)}" data-name="${esc(item.name || '')}" title="重设/轮转密钥">${icon('refresh')}</button>
              <button class="icon-btn danger" data-key-delete="${esc(item.id)}" title="删除密钥">${icon('trash')}</button>
            </div></td>
          </tr>`).join('')}</tbody>
        </table></div>` : empty('还没有管理员 API 密钥', '在下方创建后即可用 X-Emby-Token 调用全部接口。')}
      <form id="key-rotate-form" class="field-grid" style="margin-top:18px" hidden>
        <p class="warning-block" style="grid-column:1/-1" role="note">轮转 <strong id="key-rotate-name">该密钥</strong> 会让所有使用旧密钥的脚本/客户端立即断开；旧密钥不可找回，只能重新轮转。需要当前密码再认证。</p>
        <div class="field"><label for="key-rotate-password">当前密码</label><input id="key-rotate-password" type="password" required autocomplete="current-password"></div>
        <div class="field"><label for="key-rotate-reason">原因</label><input id="key-rotate-reason" type="text" maxlength="200" placeholder="如 forgotten / 泄露处置" required></div>
        <div class="form-foot" style="grid-column:1/-1;margin:2px 0 0">
          <button class="btn btn-accent" type="submit">确认轮转（旧密钥立即失效）</button>
          <button class="btn btn-sm" id="key-rotate-cancel" type="button">取消</button>
        </div>
      </form>
      <form id="apikey-form" class="field-grid" style="margin-top:18px">
        <div class="field"><label for="key-name">新建管理员密钥名称</label><input id="key-name" name="name" placeholder="如 yamby / 脚本" required></div>
        <div class="form-foot" style="grid-column:1/-1;margin:2px 0 0"><button class="btn btn-accent">${icon('plus')}<span>创建</span></button></div>
      </form>
      <p class="hint">调用示例：<code>curl -H "X-Emby-Token: &lt;密钥&gt;" http://host:18080/Users/1/Views</code>，也可用 <code>?api_key=&lt;密钥&gt;</code>。密钥即管理员凭据，请勿外泄；删除前先确认使用方。</p>
    </section>
    </div>

    <div class="settings-category" data-settings-panel="notifications" hidden>
    <section id="webhook-settings" class="panel" aria-labelledby="webhook-title"></section>
    </div>

    <div class="settings-category" data-settings-panel="logs" hidden>
    <section id="log-settings" class="panel" aria-labelledby="log-settings-title"></section>

    <section class="panel" aria-labelledby="logs-link-title">
      <div class="panel-head"><h2 id="logs-link-title">运行日志</h2><a class="btn btn-sm" href="#logs">打开日志页</a></div>
      <p class="hint" style="margin:0">运行日志已迁到独立的「运行日志」页，按磁盘文件增量读取；设置页只保留日志保存期限与活动记录，不与运行日志混用。</p>
    </section>
    </div>

    <div class="settings-category" data-settings-panel="cache" hidden>
    <section id="image-cache-settings" class="panel" aria-labelledby="image-cache-title"></section>
    </div>

    <div class="settings-category" data-settings-panel="recovery" hidden>
    <section class="panel">
      <div class="panel-head"><h2>索引恢复</h2><span class="hint" style="margin:0">NFO 已保存但索引未完成的条目</span></div>
      ${recovery.stale_over_7d > 0 ? `<p class="warning-block" role="alert">有 ${esc(String(recovery.stale_over_7d))} 个条目索引未完成已超过 7 天，请尽快打开对应条目处理恢复提示。</p>` : ''}
      ${(recovery.items || []).length ? `
        <div class="table-wrap"><table>
          <thead><tr><th>条目</th><th>首次失败</th><th>已持续</th><th>原因</th></tr></thead>
          <tbody>${recovery.items.map(item => `<tr>
            <td><strong class="title">${esc(item.title || '#' + item.movie_id)}</strong></td>
            <td class="mono">${esc(item.first_failed_at)}</td>
            <td class="mono">${esc(String(item.age_days))} 天</td>
            <td class="mono">${esc(item.code)}</td>
          </tr>`).join('')}</tbody>
        </table></div>` : empty('没有待恢复条目', '索引失败时会在这里显示；成功重新索引后自动清除。')}
    </section>
    </div>

    <div class="settings-category" data-settings-panel="service" hidden>
    <section class="panel">
      <div class="panel-head"><h2>服务设置</h2></div>
      <div class="table-wrap"><table>
        <tbody>${fields.map(([k, v]) => `<tr><th style="width:160px">${esc(k)}</th><td class="mono">${esc(v ?? '—')}</td></tr>`).join('')}</tbody>
      </table></div>
      <p class="hint">上述启动配置修改后需重启服务；通知、日志期限与图片缓存分类中的设置保存即时生效。浏览海报墙请通过 Emby 客户端连接本服务（System/Info/Public 的 ServerId 用于标识实例）。</p>
    </section>
    </div>`;
  mountSettingsCategories(content);
  const logHost = document.querySelector('#log-settings');
  void mountLogSettings(logHost, api, () => gen === pageGen && logHost.isConnected);
  const webhookHost = document.querySelector('#webhook-settings');
  void mountWebhookSettings(webhookHost, api, () => gen === pageGen && webhookHost.isConnected);
  const imageCacheHost = document.querySelector('#image-cache-settings');
  void mountImageCacheSettings(imageCacheHost, api, () => gen === pageGen && imageCacheHost.isConnected);
  document.querySelector('#apikey-form').addEventListener('submit', async event => {
    event.preventDefault();
    const name = String(new FormData(event.target).get('name') || '').trim();
    const requestIdentity = token;
    try {
      const created = await api('/apikeys', { method: 'POST', body: JSON.stringify({ name }) });
      if (gen !== pageGen || localStorage.getItem('emby_token') !== requestIdentity) return;
      createdAPIKey = created.key || null;
      settingsCategory = 'users';
      toast('密钥已创建');
      refreshPage(pageSettings, gen);
    }
    catch (e) { toast(e.message, 'error'); }
  });
  const logoutAll = document.querySelector('#logout-all');
  if (logoutAll) logoutAll.addEventListener('click', async () => {
    if (!confirm('退出全部登录会话？其它设备需要重新登录，API 密钥不受影响。')) return;
    try {
      const result = await api('/access-tokens', { method: 'DELETE' });
      toast(`已退出 ${result.revoked} 个会话`, 'ok');
      clearSessionView();
      localStorage.removeItem('emby_token');
      token = null;
      window.location.replace('/');
    } catch (e) { toast(e.message, 'error'); }
  });
  document.querySelectorAll('[data-copy-created]').forEach(button => button.addEventListener('click', async () => {
    try { await navigator.clipboard.writeText(button.dataset.copyCreated); toast('新密钥已复制', 'ok'); }
    catch { toast('复制失败，请手动选择', 'error'); }
  }));
  const dismissKey = document.querySelector('#dismiss-created-key');
  if (dismissKey) dismissKey.addEventListener('click', () => { createdAPIKey = null; refreshPage(pageSettings, gen); });
  document.querySelectorAll('[data-copy]').forEach(button => button.addEventListener('click', async () => {
    try { await navigator.clipboard.writeText(button.dataset.copy); toast('已复制密钥', 'ok'); }
    catch { toast('复制失败，请手动选择', 'error'); }
  }));
  const rotateForm = document.querySelector('#key-rotate-form');
  const rotatePassword = document.querySelector('#key-rotate-password');
  let rotateTarget = null;
  document.querySelectorAll('[data-key-rotate]').forEach(button => button.addEventListener('click', () => {
    rotateTarget = { id: button.dataset.keyRotate, version: Number(button.dataset.version) || 1 };
    document.querySelector('#key-rotate-name').textContent = button.dataset.name || '该密钥';
    rotateForm.hidden = false;
    rotatePassword.value = '';
    rotatePassword.focus();
  }));
  document.querySelector('#key-rotate-cancel').addEventListener('click', () => {
    rotateTarget = null;
    rotateForm.hidden = true;
    rotatePassword.value = '';
  });
  rotateForm.addEventListener('submit', async event => {
    event.preventDefault();
    if (!rotateTarget) return;
    const stable = String(rotateTarget.id).replace(/^ak_/, '');
    const ifMatch = `"ak-${stable}-${rotateTarget.version}"`;
    const body = JSON.stringify({
      current_password: rotatePassword.value,
      reason: String(document.querySelector('#key-rotate-reason').value || '').trim()
    });
    const requestIdentity = token;
    const requestGen = gen;
    try {
      const rotated = await api('/apikeys/' + encodeURIComponent(rotateTarget.id) + '/rotate', {
        method: 'POST', body, headers: { 'If-Match': ifMatch }
      });
      if (requestGen !== pageGen || localStorage.getItem('emby_token') !== requestIdentity) return;
      createdAPIKey = rotated.key || null;
      toast('密钥已轮转，旧密钥已立即失效', 'ok');
      refreshPage(pageSettings, gen);
    } catch (e) {
      if (e.code === 'API_KEY_VERSION_STALE') {
        toast('密钥版本已变化，已刷新列表；请确认后重新操作', 'error');
        refreshPage(pageSettings, gen);
        return;
      }
      toast(e.message, 'error');
    }
  });
  document.querySelectorAll('[data-key-delete]').forEach(button => button.addEventListener('click', async () => {
    if (!confirm('删除该 API 密钥？使用它的客户端将立即失效。')) return;
    try { await api('/apikeys/' + encodeURIComponent(button.dataset.keyDelete), { method: 'DELETE' }); toast('密钥已删除', 'ok'); refreshPage(pageSettings, gen); }
    catch (e) { toast(e.message, 'error'); }
  }));
  document.querySelector('#user-create-form')?.addEventListener('submit', async event => {
    event.preventDefault();
    const data = new FormData(event.target);
    const body = { name: String(data.get('name') || '').trim(), password: String(data.get('password') || ''), is_admin: data.get('is_admin') === 'on' };
    try { await api('/users', { method: 'POST', body: JSON.stringify(body) }); toast('用户已创建'); refreshPage(pageSettings, gen); }
    catch (e) { toast(e.message, 'error'); }
  });
  document.querySelectorAll('[data-user-save]').forEach(button => button.addEventListener('click', async () => {
    const id = button.dataset.userSave;
    const checked = Array.from(document.querySelectorAll(`[data-user-lib="${id}"]:checked`)).map(input => Number(input.value));
    try { await api('/users/' + id + '/libraries', { method: 'PUT', body: JSON.stringify({ library_ids: checked }) }); toast('媒体库授权已保存', 'ok'); }
    catch (e) { toast(e.message, 'error'); }
  }));
  document.querySelectorAll('[data-user-reset]').forEach(button => button.addEventListener('click', async () => {
    const value = prompt(`为 ${button.dataset.userName} 设置新密码（至少 9 个字符）：`);
    if (!value) return;
    try { await api('/users/' + button.dataset.userReset, { method: 'PUT', body: JSON.stringify({ password: value }) }); toast('密码已重置，该用户的登录会话已失效', 'ok'); }
    catch (e) { toast(e.message, 'error'); }
  }));
  document.querySelectorAll('[data-user-del]').forEach(button => button.addEventListener('click', async () => {
    if (!confirm('删除该用户？其播放与登录历史会保留并标注已删除，令牌立即失效。')) return;
    try { await api('/users/' + button.dataset.userDel, { method: 'DELETE' }); toast('用户已删除'); refreshPage(pageSettings, gen); }
    catch (e) { toast(e.message, 'error'); }
  }));
  document.querySelector('#pw-form')?.addEventListener('submit', async event => {
    event.preventDefault();
    try {
      const response = await fetch('/api/auth/password', { method: 'POST', headers: { 'Content-Type': 'application/json', 'X-Emby-Token': token }, body: JSON.stringify({ current_password: document.querySelector('#pw-current').value, new_password: document.querySelector('#pw-new').value }) });
      const body = response.status === 204 ? {} : await response.json().catch(() => ({}));
      if (!response.ok) throw new Error(body.error || '修改失败');
      toast('密码已修改，请重新登录', 'ok');
      localStorage.removeItem('emby_token');
      setTimeout(() => window.location.replace('/'), 800);
    } catch (e) { toast(e.message, 'error'); }
  });
}

// renderSelfPassword 是普通用户的设置页：没有服务配置/密钥/用户管理，只有改密。
function renderSelfPassword() {
  content.innerHTML = `
    <section class="panel">
      <div class="panel-head"><h2>账户</h2><span class="hint" style="margin:0">${esc(me ? me.name : '')}</span></div>
      <p class="hint">普通用户通过 Emby 客户端观看；服务配置与用户管理仅管理员可见。</p>
    </section>
    <section class="panel">
      <div class="panel-head"><h2>修改我的密码</h2></div>
      <form id="pw-form" class="field-grid">
        <div class="field"><label for="pw-current">当前密码</label><input id="pw-current" type="password" required autocomplete="current-password"></div>
        <div class="field"><label for="pw-new">新密码</label><input id="pw-new" type="password" minlength="9" required autocomplete="new-password"></div>
        <div class="form-foot" style="grid-column:1/-1;margin:2px 0 0"><button class="btn btn-accent">保存并退出其它会话</button></div>
      </form>
    </section>`;
  document.querySelector('#pw-form').addEventListener('submit', async event => {
    event.preventDefault();
    try {
      const response = await fetch('/api/auth/password', { method: 'POST', headers: { 'Content-Type': 'application/json', 'X-Emby-Token': token }, body: JSON.stringify({ current_password: document.querySelector('#pw-current').value, new_password: document.querySelector('#pw-new').value }) });
      const body = response.status === 204 ? {} : await response.json().catch(() => ({}));
      if (!response.ok) throw new Error(body.error || '修改失败');
      toast('密码已修改，请重新登录', 'ok');
      localStorage.removeItem('emby_token');
      setTimeout(() => window.location.replace('/'), 800);
    } catch (e) { toast(e.message, 'error'); }
  });
}

/* ---------------------------------------------------------------- 任务日志 */
// 原独立的「任务」页已并入计划任务页下半部分：这里只负责渲染日志区块 + 绑定刷新/立即扫描。
async function renderTaskLog() {
  const box = document.querySelector('#task-log');
  if (!box) return;
  let data;
  try { data = await api('/tasks'); }
  catch (e) { box.innerHTML = `<p class="hint" style="color:var(--danger)">${esc(e.message)}</p>`; return; }
  const running = data.running;
  const items = data.items || [];
  const runLabel = s => RUN_STATUS_TEXT[s] || s;
  box.innerHTML = `
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
      </table></div>` : empty('暂无任务', '点击「立即扫描」开始索引。')}`;
}

/* ---------------------------------------------------------------- 探针 */
async function pageProbe() {
  const gen = pageGen;
  const data = await api('/probe');
  if (gen !== pageGen) return; // 等待期间切走了页面，这次结果作废
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
    try { await api('/probe', { method: 'DELETE' }); toast('探针记录已清空'); refreshPage(pageProbe, gen); }
    catch (e) { toast(e.message, 'error'); }
  });
}

/* ---------------------------------------------------------------- 刮削 */
// 配置存 DB（config.yaml 不设 scrape 段）；密钥只写不回显，读取时返回掩码。
async function pageScrape() {
  stopScrapePolling();
  const gen = pageGen;
  const [settings, libs] = await Promise.all([api('/scrape/settings'), api('/libraries')]);
  const libraries = libs.items || [];
  const tr = settings.translate || {};
  if (gen !== pageGen) return; // 等待期间切走了页面，这次结果作废
  const libOptions = libraries.map(lib => `<option value="${esc(lib.Id)}">${esc(lib.Name)}</option>`).join('');

  content.innerHTML = `
    <section class="panel">
      <div class="panel-head"><h2>立即刮削</h2>
        <div class="panel-actions"><span class="hint" id="scrape-count" style="margin:0"></span></div>
      </div>
      <div class="filters">
        <select id="sc-lib" aria-label="媒体库"><option value="0">全部媒体库</option>${libOptions}</select>
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
    </section>

    <details class="panel disclosure" id="scrape-configuration">
      <summary class="disclosure-summary">服务连接与刮削策略（含共享翻译服务）</summary>
      <div class="disclosure-body">
      <div class="panel-head"><h2>服务连接与刮削策略</h2>
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

        <div class="field full"><label style="color:var(--accent)">共享翻译服务（独立翻译与刮削共用连接）</label></div>
        <p class="hint field full">以下连接也供独立翻译使用；标题/简介开关仅控制刮削内嵌翻译，不限制独立翻译任务。</p>
        <div class="field"><label>刮削时翻译标题</label>
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
    </div></details>`;

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
    try { await api('/scrape/settings', { method: 'PUT', body: JSON.stringify(body) }); toast('配置已保存并即时生效'); refreshPage(pageScrape, gen); }
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
      if (gen === pageGen) startScrapePolling();
    } catch (e) { toast(e.message, 'error'); }
  });
  document.querySelector('#avatar-start').addEventListener('click', async () => {
    try {
      const result = await api('/scrape/avatars', { method: 'POST', body: JSON.stringify({ library_id: Number(document.querySelector('#sc-lib').value) || 0 }) });
      toast(`头像任务已启动（${result.total} 个演员）`);
      if (gen === pageGen) startScrapePolling();
    } catch (e) { toast(e.message, 'error'); }
  });
  document.querySelector('#scrape-cancel').addEventListener('click', async () => {
    try { await api('/scrape/cancel', { method: 'POST' }); toast('已请求中止'); }
    catch (e) { toast(e.message, 'error'); }
  });

  // 首次查询也走有超时/鉴权分类的观察器，不能在页面初始化外单独悬挂。
  const progressEls = scrapeEls();
  progressEls.panel.hidden = false;
  progressEls.detail.setAttribute('role', 'status');
  progressEls.detail.textContent = '正在读取进度，任务状态未确认…';
  startScrapePolling();
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

// 保留原有真值归一化：负 total 仍参与计算，百分比仅限制上界。
function progressCounts(p) {
  const total = p.total || 0;
  const done = p.done || 0;
  const percent = total ? Math.min(100, Math.round(done / total * 100)) : 100;
  return { total, done, percent };
}

function renderScrapeProgress(p) {
  const el = scrapeEls();
  if (!el.panel) return;
  if (!p || (!p.running && !p.finished_at)) { el.panel.hidden = true; return; }
  el.panel.hidden = false;
  el.title.textContent = p.running ? (p.kind === 'scrape_avatars' ? '正在补演员头像' : '正在刮削') : '刮削结束';
  const { total, done, percent } = progressCounts(p);
  el.count.textContent = total ? `${done}/${total}` : String(done);
  el.fill.classList.toggle('is-indeterminate', !total && p.running);
  el.fill.style.width = `${percent}%`;
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

// 每次请求完成后才安排下一次；stop仅停止观察，不取消后台任务。
function startProgressPolling(path, onProgress, onUnavailable, keepObserving = () => false, readProgress = api) {
  let stopped = false, timer = null, request = null, deadline = null, delay = 1500;
  function stop() {
    stopped = true;
    if (timer !== null) clearTimeout(timer);
    if (deadline !== null) clearTimeout(deadline);
    if (request) request.abort();
  }
  async function tick() {
    if (stopped) return;
    timer = null;
    const controller = new AbortController();
    request = controller;
    deadline = setTimeout(() => controller.abort(), 10000);
    try {
      const p = await readProgress(path, { signal: controller.signal });
      if (stopped) return;
      if (controller.signal.aborted) throw new Error('progress timeout');
      if (!p || typeof p.running !== 'boolean') throw new Error('invalid progress');
      onProgress(p);
      delay = 1500;
      if (!p.running && !keepObserving(p)) { stop(); return; }
    } catch (error) {
      if (stopped) return;
      const denied = error.status === 401 || error.status === 403;
      delay = Math.min(delay * 2, 15000);
      onUnavailable(denied ? null : delay, error);
      if (denied) stopped = true;
    } finally {
      if (deadline !== null) clearTimeout(deadline);
      deadline = null;
      request = null;
    }
    if (!stopped) timer = setTimeout(tick, delay);
  }
  timer = setTimeout(tick, 0);
  return stop;
}

function stopScrapePolling() { if (scrapeTimer) { scrapeTimer(); scrapeTimer = null; } }

function startScrapePolling() {
  stopScrapePolling();
  const gen = pageGen;
  scrapeTimer = startProgressPolling('/scrape/progress', p => {
    if (gen === pageGen) renderScrapeProgress(p);
  }, delay => {
    if (gen !== pageGen) return;
    const el = scrapeEls();
    if (!el.panel) return;
    el.panel.hidden = false;
    el.detail.setAttribute('role', 'status');
    el.detail.textContent = delay === null
      ? '进度读取受限，任务状态未确认。请检查登录或权限后重新进入此页。'
      : `进度暂不可用，任务状态未确认；${delay / 1000}秒后自动重试。`;
  });
}

/* ------------------------------------------------------------ 单条刮削预览 */
let scrapeState = null;

function closeScrapePreview() {
  const overlay = document.querySelector('#scrape-overlay');
  if (overlay) overlay.hidden = true;
  scrapeState = null;
  overlayPop('#scrape-overlay');
}

// openScrapePreview 打开单条刮削预览：先取候选，再自动 inspect 推荐项。
async function openScrapePreview(item) {
  const overlay = document.querySelector('#scrape-overlay');
  const body = document.querySelector('#scrape-body');
  overlay.hidden = false;
  overlayPush('#scrape-overlay');
  body.innerHTML = '<p class="hint">正在搜索候选…</p>';
  // 捕获本次会话的状态对象：await 期间浮层可能被关掉（scrapeState 置 null）
  // 或换成另一条影片，回写时必须认这个对象、而不是认全局变量。
  // inspectSeq 只增不减：慢回包必须让位给更晚的点击（P1-10）。
  const state = { movieId: item.id, preview: null, inspect: null, provider: '', id: '', inspectSeq: 0 };
  scrapeState = state;
  try {
    const preview = await api(`/items/${item.id}/scrape/preview`);
    if (scrapeState !== state) return; // 已关闭或已切到另一条
    state.preview = preview;
    if (!preview.candidates || !preview.candidates.length) {
      body.innerHTML = `<p class="hint">没有搜到候选（搜索词：${esc(preview.query || '—')}）。可先在详情里补番号，或在 NFO 里填好番号后重试。</p>`;
      return;
    }
    const pick = preview.candidates[preview.recommended >= 0 ? preview.recommended : 0];
    await loadScrapeInspect(pick.provider, pick.id);
  } catch (e) {
    if (scrapeState !== state) return;
    body.innerHTML = `<p class="hint" style="color:var(--danger)">${esc(e.message)}</p>`;
  }
}

async function loadScrapeInspect(provider, id) {
  const state = scrapeState;
  if (!state) return; // 浮层已关闭，不必也无法渲染
  // 同一会话内每次点击取号；只有最后一次点击的回包可以写状态与重绘。
  // 慢候选先发出、后返回时不得覆盖用户已经选中的新候选。
  const seq = ++state.inspectSeq;
  const body = document.querySelector('#scrape-body');
  body.innerHTML = '<p class="hint">正在读取详情…</p>';
  let inspect;
  try {
    inspect = await api(`/items/${state.movieId}/scrape/inspect`, {
      method: 'POST', body: JSON.stringify({ provider, id })
    });
  } catch (error) {
    if (scrapeState !== state || seq !== state.inspectSeq) return;
    // 保留已选详情和候选；首次自动 inspect 失败也提供明确的原位重试。
    if (state.inspect) renderScrapePreview();
    else body.innerHTML = '';
    body.insertAdjacentHTML('afterbegin', `<div role="alert"><p class="hint">读取详情失败：${esc(error.message)}</p><button id="scrape-inspect-retry" type="button" class="btn btn-sm">重试读取详情</button></div>`);
    body.querySelector('#scrape-inspect-retry').addEventListener('click', () => { void loadScrapeInspect(provider, id); });
    return;
  }
  if (scrapeState !== state || seq !== state.inspectSeq) return; // 已关闭、已换片或已被更新的点击取代
  state.inspect = inspect;
  state.provider = provider;
  state.id = id;
  renderScrapePreview();
}

function renderScrapePreview() {
  const gen = pageGen;
  const detailSession = detailGen;
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
        <small>${esc([numberUnlessInTitle(item.title, item.number), item.provider, item.score ? '★ ' + item.score : '', item.exact ? '番号命中' : ''].filter(Boolean).join(' · '))}</small>
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
      if (gen !== pageGen || scrapeState !== state) return;
      closeScrapePreview();
      if (detailSession === detailGen) closeDetail();
      refreshPage(pageItems, gen);
    } catch (e) { toast(e.message, 'error'); confirmBtn.disabled = false; }
  });
}

/* ------------------------------------------------------------ 独立翻译 */
const TRANSLATE_HINT = '不重新刮削、不取图。标题来自 NFO originaltitle（缺失则跳过）；简介来自当前内容，可能已经是译文，请谨慎重复翻译。保留字段锁；失败保留原文。复用刮削设置中的翻译服务地址、密钥和目标语言，不受内嵌翻译字段开关影响。';
const translationObservers = new Set();
function stopTranslationPolling() {
  translationObservers.forEach(stop => stop());
  translationObservers.clear();
}
function translationFields(root) {
  const fields = ['title', 'plot'].filter(field => root.querySelector(`[data-translate-field="${field}"]`)?.checked);
  if (!fields.length) throw new Error('请至少选择标题或简介');
  return fields;
}
function translationPayload(scope, fields, limit = 0) {
  if (!fields.length || fields.some(field => !['title', 'plot'].includes(field))) throw new Error('翻译字段无效');
  if (!Number.isInteger(limit) || limit < 0 || limit > 100000) throw new Error('数量上限须为 0–100000 的整数');
  const body = { fields, limit };
  if (scope.movie_ids !== undefined) {
    const ids = [...new Set(scope.movie_ids.map(Number))];
    if (!ids.length || ids.length > 1000 || ids.some(id => !Number.isSafeInteger(id) || id <= 0)) throw new Error('请选择 1–1000 部影片');
    body.movie_ids = ids;
  } else if (scope.library_id) {
    const id = Number(scope.library_id);
    if (!Number.isSafeInteger(id) || id <= 0) throw new Error('媒体库无效');
    body.library_id = id;
  }
  return body;
}
function translationWidget(id) {
  return `<div id="${id}" class="translate-widget">
    <p class="hint">${esc(TRANSLATE_HINT)}</p>
    <div class="filters"><label class="check"><input type="checkbox" data-translate-field="title" checked> 翻译标题</label>
      <label class="check"><input type="checkbox" data-translate-field="plot"> 翻译简介（当前内容）</label>
      <button type="button" class="btn" data-translate-start>仅翻译</button>
      <button type="button" class="btn" data-translate-cancel disabled>取消本任务</button>
      <button type="button" class="btn" data-translate-refresh>重读状态</button></div>
    <p class="hint" data-translate-progress role="status">正在确认翻译状态…</p></div>`;
}
// 每个挂载点独立持有任务ID/身份/页面代次；离开仅停止观察，不假定后台已取消。
function mountTranslation(root, getScope, isScopeCurrent, onComplete = () => {}) {
  const gen = pageGen, identity = token;
  const current = () => gen === pageGen && token === identity && localStorage.getItem('emby_token') === identity && isScopeCurrent();
  const start = root.querySelector('[data-translate-start]');
  const cancel = root.querySelector('[data-translate-cancel]');
  const progress = root.querySelector('[data-translate-progress]');
  let stop = null, taskId = null, submitted = false, busy = false, cancelling = false, observedRunning = false;
  const halt = () => { stop?.(); stop = null; translationObservers.delete(halt); };
  const observe = () => {
    halt(); if (!current()) return;
    start.disabled = true; cancel.disabled = true;
    translationObservers.add(halt);
    stop = startProgressPolling('/translate/status' + (taskId ? '?task_id=' + encodeURIComponent(taskId) : ''), p => {
      if (!current()) { halt(); return; }
      if (taskId && String(p.task_id) !== String(taskId)) { halt(); observedRunning = false; start.disabled = true; cancel.disabled = true; progress.textContent = '任务状态已被替换，请重读状态；不会取消或刷新其它任务。'; return; }
      if (p.task_id) taskId = p.task_id;
      observedRunning = p.running;
      start.disabled = busy || p.running;
      cancel.disabled = cancelling || !p.running || !taskId;
      progress.textContent = `${p.running ? '翻译中' : p.cancelled ? '已取消' : p.finished_at ? '翻译结束' : '尚无任务'}${taskId ? ' · 任务 ' + taskId : ''} · ${p.done || 0}/${p.total || 0} · 成功 ${p.success || 0} · 失败 ${p.failed || 0} · 跳过 ${p.skipped || 0}${p.error ? ' · ' + p.error : ''}${p.index_refresh_failed ? ' · 索引刷新失败，请手动重读' : ''}${p.failures?.length ? ' · ' + p.failures.map(f => f.error || f.reason || JSON.stringify(f)).join('；') : ''}`;
      if (!p.running && submitted) {
        submitted = false;
        // nochange/skipped 也可能已修复索引；仅刷新本次原scope，不宣称每条成功。
        if (Number(p.done) > 0 || Number(p.success) > 0) onComplete(p);
      }
    }, (delay, error) => {
      if (!current()) { halt(); return; }
      start.disabled = true; cancel.disabled = true;
      progress.textContent = error.status === 404 ? '本任务快照已被更新任务替换。请重读状态；不会刷新或取消其它任务。' : delay === null ? '状态读取受限，请重新登录或核对权限；不会重放翻译。' : `状态未知：${error.message}。只重试读取，不自动重放翻译。`;
      if (error.status === 404) halt();
    });
  };
  start.addEventListener('click', async () => {
    if (!current() || busy || start.disabled) return;
    let body, label;
    try {
      const scope = getScope();
      body = translationPayload(scope, translationFields(root), Number(scope.limit || 0));
      label = body.movie_ids ? `所选 ${body.movie_ids.length} 部影片` : body.library_id ? `媒体库 ${body.library_id}` : '全部媒体库';
    } catch (error) { progress.textContent = error.message; return; }
    if (!confirm(`仅翻译${label}的${body.fields.map(f => f === 'title' ? '标题' : '简介').join('和')}，最多 ${body.limit || '不限'} 部。\n${TRANSLATE_HINT}\n确认开始？`)) return;
    halt(); busy = true; start.disabled = true; cancel.disabled = true;
    try {
      const result = await api('/translate/run', { method: 'POST', body: JSON.stringify(body) });
      if (!current()) return;
      if (!result?.task_id) throw new Error('服务器未提供任务ID，请重读状态核对，不要重复提交');
      taskId = result.task_id; submitted = true;
      progress.textContent = '翻译任务已接受，正在确认本次进度…';
    } catch (error) {
      if (!current()) return;
      taskId = null; submitted = false;
      progress.textContent = `提交结果需核对：${error.message}。不会自动重放。`;
    } finally { busy = false; if (current()) observe(); }
  });
  cancel.addEventListener('click', async () => {
    if (!current() || cancelling || cancel.disabled || !observedRunning || !taskId) return;
    const cancellingTask = taskId;
    cancelling = true; cancel.disabled = true;
    try {
      await api('/translate/cancel', { method: 'POST', body: JSON.stringify({ task_id: cancellingTask }) });
      if (!current() || taskId !== cancellingTask) return;
      progress.textContent = '已请求取消，等待后台确认；已成功翻译的内容保留。';
    } catch (error) { if (current()) progress.textContent = `取消未确认：${error.message}；不会取消更新的任务。`; }
    finally { cancelling = false; if (current()) observe(); }
  });
  root.querySelector('[data-translate-refresh]').addEventListener('click', () => {
    if (busy || cancelling || !current()) return;
    taskId = null; submitted = false; observe();
  });
  observe();
  return halt;
}
async function pageTranslate() {
  const gen = pageGen;
  if (!me?.is_admin) { content.innerHTML = empty('仅管理员可运行独立翻译'); return; }
  const libs = await api('/libraries');
  if (gen !== pageGen) return;
  stopTranslationPolling();
  content.innerHTML = `<section class="panel"><h2>独立翻译任务</h2><p class="hint">以下按钮只手动执行一次，不创建自动调度。也可在<a href="#scheduled">计划任务</a>保存独立翻译配置：不勾选参与调度，随后随时点击“执行”；需要定时再明确启用。翻译服务在<a href="#scrape">刮削设置</a>配置。</p><div class="field-grid">
    <div class="field"><label for="translate-library">范围</label><select id="translate-library"><option value="0">全部媒体库（启动前需确认）</option>${(libs.items || []).map(lib => `<option value="${esc(lib.Id)}">${esc(lib.Name)}</option>`).join('')}</select></div>
    <div class="field"><label for="translate-limit">数量上限（0 为不限）</label><input id="translate-limit" type="number" min="0" max="100000" step="1" value="0"></div></div>${translationWidget('translate-task')}</section>`;
  const root = document.querySelector('#translate-task');
  mountTranslation(root, () => ({ library_id: Number(document.querySelector('#translate-library').value), limit: Number(document.querySelector('#translate-limit').value) }), () => document.querySelector('#translate-task') === root);
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
const TASK_TYPE_TEXT = { scan: '扫描媒体库', reindex: '重建索引', probe: '媒体信息探测', translate: '独立翻译' };
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

// Merge only on same-type edits: hidden supported settings (limit/overwrite)
// must survive a name/cron change. A type change starts a fresh parameter set.
function scheduledFormParams(kind, editing, stored, visible) {
  const params = editing && editing.type === kind ? { ...stored } : {};
  params.library_id = visible.library_id;
  if (kind === 'scan') {
    const mode = visible.mode ?? params.mode ?? 'full';
    if (!['full', 'incremental'].includes(mode)) throw new Error('请选择有效的扫描模式');
    params.mode = mode;
  }
  if (kind === 'probe') {
    params.probe_status_contract = 'explicit-v1';
    params.status = visible.status;
    params.only_missing = visible.only_missing;
  }
  if (kind === 'translate') {
    params.fields = visible.fields;
    params.limit = visible.limit;
    delete params.movie_ids; // 计划任务不支持固定影片ID列表。
  }
  return params;
}

async function pageScheduled() {
  const gen = pageGen;
  const [data, libs] = await Promise.all([api('/scheduled'), api('/libraries')]);
  if (gen !== pageGen) return; // 等待期间切走了页面，这次结果作废
  const items = data.items || [];
  const types = { ...TASK_TYPE_TEXT, ...(data.types || {}) };
  const libraries = libs.items || [];
  const editing = scheduledEditId ? items.find(item => item.id === scheduledEditId) : null;
  // 编辑时回填参数；参数在库里是 JSON 文本。
  let params = {}, paramsReadable = true;
  if (editing) {
    try {
      params = JSON.parse(editing.params || '{}');
      if (!params || typeof params !== 'object' || Array.isArray(params)) throw new Error('invalid params');
    } catch { params = {}; paramsReadable = false; }
  }

  const libOptions = libraries.map(lib => `<option value="${esc(lib.Id)}">${esc(lib.Name)}</option>`).join('');
  content.innerHTML = `
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
            <td><label class="switch"><input type="checkbox" data-role="toggle" data-id="${esc(item.id)}" aria-label="计划任务：${esc(TASK_TYPE_TEXT[item.type] || item.type)}"${item.enabled ? ' checked' : ''}><i></i></label></td>
            <td><div class="row-actions">
              <button class="btn btn-sm" data-role="run" data-id="${esc(item.id)}" title="立即执行一次">${icon('play')}<span>执行</span></button>
              <button class="btn btn-sm" data-role="edit" data-id="${esc(item.id)}">编辑</button>
              <button class="icon-btn danger" data-role="del" data-id="${esc(item.id)}" title="删除">${icon('trash')}</button>
            </div></td>
          </tr>`).join('')}</tbody>
        </table></div>` : empty('还没有计划任务', '展开下方“新建计划任务”配置；立即执行已有任务无需新建。')}
    </section>


    <details class="panel disclosure" id="scheduled-editor"${editing ? ' open' : ''}>
      <summary class="disclosure-summary">${editing ? '编辑计划任务' : '新建计划任务'}</summary>
      <div class="disclosure-body">
      <div class="panel-head">
        <h2>${editing ? '编辑计划任务' : '新建计划任务'}</h2>
        ${editing ? '<div class="panel-actions"><button id="sched-cancel" class="btn btn-sm">取消编辑</button></div>' : ''}
      </div>
      <p class="hint" style="margin:0 0 14px">启用后到点自动执行；也可取消“保存后参与调度”，仅保存配置后点击“执行”手动运行。新建独立翻译默认不启用自动调度。同一任务上一轮未结束时跳过本次并记录。</p>
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
        <div class="field" data-show="scan"><label for="s-scan-mode">扫描模式</label>
          <select id="s-scan-mode"><option value="full">全量重建（兼容旧计划）</option><option value="incremental">增量扫描（只重建变化组）</option></select>
          <p class="hint">两种模式都会完整枚举目录。无模式的旧计划按全量执行；重建索引不使用此参数。</p></div>
        <div class="field" data-show="probe"><label for="s-status">影片状态</label>
          <select id="s-status">
            <option value="success">可播放</option>
            <option value="manual">手动录入</option>
            <option value="all">全部已有 NFO 的影片</option>
          </select></div>
        <div class="field" data-show="probe"><label>探测范围</label>
          <label class="check"><input id="s-only-missing" type="checkbox"> 仅未探测的条目</label></div>
        <div class="field full" data-show="translate"><label>翻译字段</label>
          <label class="check"><input id="s-translate-title" type="checkbox" checked> 标题（NFO originaltitle，缺失跳过）</label>
          <label class="check"><input id="s-translate-plot" type="checkbox"> 简介（当前内容，可能已翻译）</label>
          <p class="hint">${esc(TRANSLATE_HINT)}</p></div>
        <div class="field" data-show="translate"><label for="s-translate-limit">数量上限（0 为不限）</label><input id="s-translate-limit" type="number" min="0" max="100000" step="1" value="0"></div>
        <div class="field"><label>启用</label>
          <label class="check"><input id="s-enabled" type="checkbox"${!editing || editing.enabled ? ' checked' : ''}> 保存后参与调度</label></div>
        <div class="field full"><label>常用预设</label>
          <div class="preset-row">${CRON_PRESETS.map(([text, expr]) =>
            `<button type="button" class="btn btn-sm preset" data-cron="${esc(expr)}">${esc(text)}</button>`).join('')}</div></div>
        <p class="hint full" id="s-preview" style="grid-column:1/-1">标准 5 段（分 时 日 月 周）或 @daily/@hourly 等描述符；按服务器本地时区执行，浏览器时区只影响展示；留空无法保存。</p>
        <div class="form-foot" style="grid-column:1/-1">
          ${paramsReadable ? '' : '<p role="alert">原任务参数无法读取，已禁止保存。请先由管理员核对配置，不会用空参数覆盖。</p>'}
          ${editing && !editing.config_etag ? '<p role="alert">服务器未提供配置校验令牌，已禁止覆盖保存。请更新服务后刷新。</p>' : ''}
          <p id="sched-conflict" role="alert" hidden></p>
          <button id="sched-submit" class="btn btn-accent" type="submit"${paramsReadable && (!editing || editing.config_etag) ? '' : ' disabled'}>${icon('plus')}<span>${editing ? '保存修改' : '新建任务'}</span></button>
        </div>
      </form>
    </div></details>

    <section class="panel">
      <div class="panel-head"><h2>执行记录</h2>
        <div class="panel-actions">
          <button id="task-refresh" class="btn btn-sm">刷新</button>
          <button id="task-scan" class="btn btn-accent btn-sm">${icon('film')}<span>立即扫描</span></button>
        </div>
      </div>
      <div id="task-log"><p class="hint">加载中…</p></div>
    </section>`;

  const typeEl = document.querySelector('#s-type');
  const cronEl = document.querySelector('#s-cron');
  const preview = document.querySelector('#s-preview');

  // 按任务类型显示相关参数：重建索引不需要参数，探测才有状态/范围。
  function syncParams() {
    const kind = typeEl.value;
    document.querySelectorAll('[data-show="library"]').forEach(el => { el.hidden = kind === 'reindex'; });
    document.querySelectorAll('[data-show="scan"]').forEach(el => { el.hidden = kind !== 'scan'; });
    document.querySelectorAll('[data-show="probe"]').forEach(el => { el.hidden = kind !== 'probe'; });
    document.querySelectorAll('[data-show="translate"]').forEach(el => { el.hidden = kind !== 'translate'; });
    if (!editing && kind === 'translate') document.querySelector('#s-enabled').checked = false;
  }

  // 即时校验表达式并预览后续执行时间（后端解析，与调度用同一套规则）。
  // cronSeq 实现 latest-wins：慢请求返回时若表达式已变，旧结果作废，不覆盖新预览。
  let cronSeq = 0;
  const checkCron = debounce(async () => {
    const expr = cronEl.value.trim();
    const seq = ++cronSeq;
    if (!expr) { preview.textContent = '请输入 cron 表达式。'; preview.style.color = ''; return; }
    try {
      const result = await api('/scheduled/validate', { method: 'POST', body: JSON.stringify({ cron: expr }) });
      if (seq !== cronSeq || cronEl.value.trim() !== expr) return;
      if (!result.valid) { preview.textContent = '表达式非法：' + result.error; preview.style.color = 'var(--danger)'; return; }
      preview.textContent = `接下来执行（服务器时区 ${result.time_zone || '本地'}）：` + (result.next_runs || []).map(fmtTime).join(' · ');
      preview.style.color = '';
    } catch (e) {
      if (seq !== cronSeq || cronEl.value.trim() !== expr) return;
      preview.textContent = e.message; preview.style.color = 'var(--danger)';
    }
  }, 300);

  // 回填编辑中的参数。
  if (editing) {
    document.querySelector('#s-lib').value = String(params.library_id || 0);
    // Legacy omitted/null/empty status means success, never infer all.
    document.querySelector('#s-status').value = params.status || 'success';
    document.querySelector('#s-only-missing').checked = params.only_missing !== false;
  } else {
    document.querySelector('#s-only-missing').checked = true;
  }
  if (editing?.type === 'scan') {
    document.querySelector('#s-scan-mode').value = params.mode === undefined ? 'full' : params.mode;
    if (!['full', 'incremental'].includes(params.mode === undefined ? 'full' : params.mode)) {
      paramsReadable = false;
      document.querySelector('#sched-submit').disabled = true;
      const warning = document.querySelector('#sched-conflict');
      warning.hidden = false; warning.textContent = '扫描模式参数无法读取，禁止用默认值覆盖。请核对原配置。';
    }
  }
  if (editing?.type === 'translate') {
    const fields = params.fields === undefined ? ['title'] : params.fields;
    if (!Array.isArray(fields) || !fields.length || fields.some(field => !['title', 'plot'].includes(field))) paramsReadable = false;
    document.querySelector('#s-translate-title').checked = Array.isArray(fields) && fields.includes('title');
    document.querySelector('#s-translate-plot').checked = Array.isArray(fields) && fields.includes('plot');
    document.querySelector('#s-translate-limit').value = String(params.limit ?? 0);
    if (!paramsReadable) {
      document.querySelector('#sched-submit').disabled = true;
      const warning = document.querySelector('#sched-conflict');
      warning.hidden = false; warning.textContent = '翻译字段参数无法读取，禁止用默认值覆盖。请核对原配置。';
    }
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
    document.querySelector('#sched-cancel').addEventListener('click', () => { scheduledEditId = null; refreshPage(pageScheduled, gen); });
  }
  document.querySelector('#sched-refresh').addEventListener('click', pageScheduled);
  // 任务日志区块（原「任务」页并入这里）：立即扫描用顶部的全局扫描链路，刷新只重绘日志。
  document.querySelector('#task-scan').addEventListener('click', () => runScan());
  document.querySelector('#task-refresh').addEventListener('click', renderTaskLog);
  renderTaskLog();

  document.querySelector('#sched-form').addEventListener('submit', async event => {
    event.preventDefault();
    // 单次提交：按钮在请求期间保持 disabled，重复提交（含双击/回车连发）直接忽略。
    const submit = document.querySelector('#sched-submit');
    if (submit.disabled) return;
    if (!paramsReadable) { toast('原任务参数无法读取，请先核对配置', 'error'); return; }
    if (editing && !editing.config_etag) { toast('缺少配置校验令牌，请更新服务后刷新', 'error'); return; }
    const conflict = document.querySelector('#sched-conflict');
    if (conflict && conflict.hidden === false) return;
    const kind = typeEl.value;
    const libraryValue = document.querySelector('#s-lib').value;
    if (kind !== 'reindex' && libraryValue === '') { toast('原媒体库不可用，请明确选择媒体库', 'error'); return; }
    let translateParams = {};
    if (kind === 'translate') {
      const fields = [];
      if (document.querySelector('#s-translate-title').checked) fields.push('title');
      if (document.querySelector('#s-translate-plot').checked) fields.push('plot');
      try { translateParams = translationPayload({ library_id: Number(libraryValue) }, fields, Number(document.querySelector('#s-translate-limit').value)); }
      catch (error) { toast(error.message, 'error'); return; }
    }
    if (kind === 'scan' && !['full', 'incremental'].includes(document.querySelector('#s-scan-mode').value)) {
      toast('请选择有效的扫描模式', 'error'); return;
    }
    const taskParams = scheduledFormParams(kind, editing, params, {
      library_id: Number(libraryValue) || 0,
      mode: kind === 'scan' ? document.querySelector('#s-scan-mode').value : undefined,
      status: document.querySelector('#s-status').value,
      only_missing: document.querySelector('#s-only-missing').checked,
      ...translateParams
    });
    const body = {
      name: document.querySelector('#s-name').value.trim(),
      type: kind,
      cron: cronEl.value.trim(),
      enabled: document.querySelector('#s-enabled').checked,
      params: taskParams
    };
    submit.disabled = true;
    try {
      if (editing) {
        await api('/scheduled/' + editing.id, { method: 'PATCH', headers: { 'If-Match': editing.config_etag }, body: JSON.stringify(body) });
        toast('计划任务已保存');
        scheduledEditId = null;
      } else {
        await api('/scheduled', { method: 'POST', body: JSON.stringify(body) });
        toast(`计划任务「${body.name}」已创建`);
      }
      refreshPage(pageScheduled, gen);
    } catch (e) {
      toast(e.message, 'error');
      if (e.status === 412) {
        if (conflict) { conflict.hidden = false; conflict.textContent = '配置已被其他操作修改。当前输入未保存，请先复制需要保留的内容，再刷新核对；不会自动覆盖。'; }
        submit.disabled = true;
      } else { submit.disabled = false; }
    }
  });

  document.querySelectorAll('[data-role]').forEach(el => {
    const id = el.dataset.id;
    const role = el.dataset.role;
    if (role === 'toggle') {
      el.addEventListener('change', async () => {
        try { await api(`/scheduled/${id}/toggle`, { method: 'POST' }); toast(editing ? '状态已更新，当前编辑输入保持；时间信息请保存或取消编辑后刷新' : '状态已更新'); if (!editing) refreshPage(pageScheduled, gen); }
        catch (e) { toast(e.message, 'error'); if (editing) el.checked = !el.checked; else refreshPage(pageScheduled, gen); }
      });
    } else if (role === 'run') {
      el.addEventListener('click', async () => {
        if (el.disabled) return;
        const task = items.find(item => String(item.id) === String(id));
        if (task?.type === 'translate') {
          let p;
          try {
            p = JSON.parse(task.params || '{}');
            if (!p || typeof p !== 'object' || Array.isArray(p)) throw new Error('invalid params');
            translationPayload({ library_id: p.library_id }, p.fields === undefined ? ['title'] : p.fields, p.limit ?? 0);
          } catch { toast('翻译计划参数无法确认，请先编辑核对', 'error'); return; }
          const range = p.library_id ? `媒体库 ${p.library_id}` : '全部媒体库';
          if (!confirm(`手动执行翻译计划「${task.name}」：${range}，字段 ${(p.fields || ['title']).join('/')}，数量 ${p.limit || '不限'}。\n${TRANSLATE_HINT}\n本次执行不改变自动调度开关。确认？`)) return;
        }
        el.disabled = true;
        try { await api(`/scheduled/${id}/run`, { method: 'POST' }); toast('已触发执行，结果见下方任务日志；翻译详情可在独立翻译页查看'); }
        catch (e) { toast(e.message, 'error'); }
        finally { el.disabled = false; }
      });
    } else if (role === 'edit') {
      el.addEventListener('click', () => { scheduledEditId = Number(id); refreshPage(pageScheduled, gen); });
    } else if (role === 'del') {
      el.addEventListener('click', async () => {
        if (!confirm('删除该计划任务？已产生的影片数据不受影响。')) return;
        try {
          await api('/scheduled/' + id, { method: 'DELETE' });
          toast('计划任务已删除');
          if (gen !== pageGen) return;
          if (editing) {
            el.closest('tr')?.remove();
            if (editing.id === Number(id)) {
              const panel = document.querySelector('#sched-conflict');
              panel.hidden = false; panel.textContent = '此任务已删除。当前输入未保存，请先复制需要保留的内容，再刷新。';
              document.querySelector('#sched-submit').disabled = true;
            }
          } else {
            if (scheduledEditId === Number(id)) scheduledEditId = null;
            refreshPage(pageScheduled, gen);
          }
        } catch (e) { toast(e.message, 'error'); }
      });
    }
  });
}

/* ---------------------------------------------------------------- 运行日志 */
// R4：独立磁盘日志页。数据来自 /api/admin/logs/disk（受限根 + HMAC 游标），
// 不再混用设置页的 /logs ring 接口；所有内容走 textContent/esc，不渲染 HTML。
let logsTimer = null;
let logsEpoch = 0;
let logsVisibilityHandler = null;

function stopLogsPolling() {
  if (logsTimer) { clearTimeout(logsTimer); logsTimer = null; }
  if (logsVisibilityHandler) {
    document.removeEventListener('visibilitychange', logsVisibilityHandler);
    logsVisibilityHandler = null;
  }
  logsEpoch++;
}

async function pageLogs() {
  const gen = pageGen;
  if (!me || !me.is_admin) {
    content.innerHTML = `<section class="panel">${empty('仅管理员可用', '运行日志只对管理员开放。')}</section>`;
    return;
  }
  stopLogsPolling();
  const epoch = ++logsEpoch;
  const identity = token;
  const state = {
    items: [], ids: new Set(), nextAfter: '', nextBefore: '', hasMoreBefore: false,
    level: '', paused: false, backoff: 1000, loadingEarlier: false
  };

  content.innerHTML = `
    <section class="panel" aria-labelledby="disk-logs-title">
      <div class="panel-head"><h2 id="disk-logs-title">运行日志</h2>
        <div class="row-actions">
          <select id="dl-level" aria-label="日志级别筛选">
            <option value="">全部级别</option><option value="DEBUG">DEBUG</option><option value="INFO">INFO</option><option value="WARN">WARN</option><option value="ERROR">ERROR</option>
          </select>
          <button id="dl-pause" class="btn btn-sm" type="button">暂停</button>
          <button id="dl-refresh" class="btn btn-sm" type="button">刷新</button>
        </div></div>
      <p class="hint" style="margin:0 0 8px">来源：服务器磁盘 <code>app-YYYY-MM-DD.log</code>（最新在上）· 约 1 秒增量刷新，非 fsync 保证 · 无法确认安全的旧格式行只显示隐藏占位符</p>
      <p id="dl-status" class="hint" role="status" aria-live="polite">正在连接磁盘日志…</p>
      <div id="dl-newbar" class="warning-block" role="status" hidden><button id="dl-newjump" class="btn btn-sm" type="button">有新日志，点击查看</button></div>
      <div id="dl-scroll" class="table-wrap" style="max-height:58vh;overflow:auto">
        <table class="log-table">
          <thead><tr><th style="width:190px">时间</th><th style="width:74px">级别</th><th>内容</th></tr></thead>
          <tbody id="dl-body"><tr><td colspan="3">正在加载…</td></tr></tbody>
        </table>
      </div>
      <div class="form-foot"><button id="dl-earlier" class="btn btn-sm" type="button" hidden>加载更早</button></div>
    </section>`;

  const find = selector => content.querySelector(selector);
  const scroll = find('#dl-scroll');
  const body = find('#dl-body');
  const statusEl = find('#dl-status');
  const newbar = find('#dl-newbar');
  const current = () => epoch === logsEpoch && gen === pageGen && identity === token
    && localStorage.getItem('emby_token') === identity && statusEl.isConnected;
  const setStatus = (text, error = false) => {
    if (!current()) return;
    statusEl.textContent = text;
    statusEl.setAttribute('role', error ? 'alert' : 'status');
  };

  const render = () => {
    if (!current()) return;
    const filtered = state.items.filter(item => !state.level || String(item.level).toUpperCase() === state.level);
    body.innerHTML = filtered.length
      ? filtered.map(item => `<tr><td class="mono">${esc(item.time || '—')}</td><td class="mono">${esc(item.level || '—')}</td><td class="mono">${esc(item.message || '')}</td></tr>`).join('')
      : `<tr><td colspan="3">${state.level ? '当前已加载范围内没有该级别的日志' : '暂无日志'}</td></tr>`;
    const earlier = find('#dl-earlier');
    if (earlier) earlier.hidden = !state.hasMoreBefore || state.loadingEarlier;
  };

  // 按行 ID 去重；最多渲染 1000 行；始终 newest-first，追加后重新排序。
  const addItems = (items) => {
    let added = 0;
    for (const item of items || []) {
      if (!item || !item.id || state.ids.has(item.id)) continue;
      state.ids.add(item.id);
      state.items.push(item);
      added++;
    }
    if (!added) return 0;
    if (state.items.length > 1000) {
      state.items.splice(1000).forEach(item => state.ids.delete(item.id));
    }
    state.items.sort((a, b) => String(b.time || '').localeCompare(String(a.time || '')));
    return added;
  };

  let controller = null;
  const request = async (query) => {
    controller?.abort();
    controller = new AbortController();
    const timer = setTimeout(() => controller.abort(), 10000);
    try {
      return await api('/logs/disk' + query, { signal: controller.signal });
    } finally {
      clearTimeout(timer);
    }
  };

  const replaceWithTail = (data, note) => {
    state.items = [];
    state.ids.clear();
    addItems(data.items);
    state.nextAfter = data.next_after || '';
    state.nextBefore = data.next_before || '';
    state.hasMoreBefore = !!data.next_before;
    state.backoff = 1000;
    newbar.hidden = true;
    render();
    setStatus(note, true);
  };

  const schedule = (delay) => {
    if (!current()) return;
    if (logsTimer) clearTimeout(logsTimer);
    logsTimer = setTimeout(poll, delay);
  };

  const initial = async () => {
    try {
      const data = await request('?kind=app&limit=200');
      if (!current()) return;
      state.items = [];
      state.ids.clear();
      addItems(data.items || []);
      state.nextAfter = data.next_after || '';
      state.nextBefore = data.next_before || '';
      state.hasMoreBefore = !!data.has_more;
      state.backoff = 1000;
      render();
      if (data.reset || data.gap) setStatus('日志已轮转或清理，已从当前尾部重新加载；更早的缺口无法补读。', true);
      else setStatus('已连接 · 每秒增量刷新');
      schedule(1000);
    } catch (e) {
      if (!current()) return;
      state.backoff = Math.min(Math.max(state.backoff * 2, 1000), 30000);
      setStatus(`连接中断：${e.message || '读取失败'}，${Math.round(state.backoff / 1000)} 秒后重试`, true);
      schedule(state.backoff);
    }
  };

  const poll = async () => {
    logsTimer = null;
    if (!current()) return;
    if (document.hidden) return; // 可见性恢复时由 visibilitychange 重新排程
    if (state.paused) { schedule(2000); return; }
    try {
      const query = state.nextAfter
        ? `?kind=app&after=${encodeURIComponent(state.nextAfter)}&limit=200`
        : '?kind=app&limit=200';
      const data = await request(query);
      if (!current()) return;
      if (data.reset || data.gap) {
        replaceWithTail(data, '日志文件已轮转或被清理，已从当前尾部重新加载；缺口无法补读。');
      } else {
        const added = addItems(data.items || []);
        state.nextAfter = data.next_after || state.nextAfter;
        if (added) {
          // 用户离开顶部阅读时暂停插入，只提示有新日志，避免抢滚动位置。
          if (scroll.scrollTop > 8) newbar.hidden = false;
          else render();
        }
        state.backoff = 1000;
        if (!state.paused) setStatus('已连接 · 每秒增量刷新');
      }
      schedule(1000);
    } catch (e) {
      if (!current() || e.status === 401) return;
      state.backoff = Math.min(Math.max(state.backoff * 2, 1000), 30000);
      setStatus(`连接中断：${e.message || '读取失败'}，${Math.round(state.backoff / 1000)} 秒后重试`, true);
      schedule(state.backoff);
    }
  };

  logsVisibilityHandler = () => {
    if (!current()) return;
    if (document.hidden) {
      if (logsTimer) { clearTimeout(logsTimer); logsTimer = null; }
      return;
    }
    state.backoff = 1000;
    schedule(0);
  };
  document.addEventListener('visibilitychange', logsVisibilityHandler);

  find('#dl-level').addEventListener('change', event => {
    state.level = String(event.target.value || '').toUpperCase();
    render();
  });
  find('#dl-pause').addEventListener('click', () => {
    state.paused = !state.paused;
    find('#dl-pause').textContent = state.paused ? '继续' : '暂停';
    if (state.paused) {
      if (logsTimer) { clearTimeout(logsTimer); logsTimer = null; }
      setStatus('已暂停自动刷新；点击「继续」恢复。');
    } else {
      setStatus('已恢复增量刷新');
      schedule(0);
    }
  });
  find('#dl-refresh').addEventListener('click', () => {
    if (logsTimer) { clearTimeout(logsTimer); logsTimer = null; }
    setStatus('正在重新加载…');
    void initial();
  });
  find('#dl-earlier').addEventListener('click', async () => {
    if (!current() || state.loadingEarlier || !state.nextBefore) return;
    state.loadingEarlier = true;
    render();
    try {
      const data = await request(`?kind=app&before=${encodeURIComponent(state.nextBefore)}&limit=200`);
      if (!current()) return;
      if (data.reset || data.gap) { replaceWithTail(data, '日志已轮转或清理，无法继续向前读取；已回到当前尾部。'); return; }
      const before = scroll.scrollTop;
      addItems(data.items || []);
      state.nextBefore = data.next_before || state.nextBefore;
      state.hasMoreBefore = !!data.has_more;
      render();
      // 追加在底部，保持用户当前阅读位置。
      scroll.scrollTop = Math.min(before, scroll.scrollHeight - scroll.clientHeight);
      setStatus(`已加载更早日志 · 当前已加载 ${state.items.length} 条（仅本页范围，不代表全部磁盘历史）`);
    } catch (e) {
      if (current()) setStatus(`加载更早日志失败：${e.message || '读取失败'}`, true);
    } finally {
      state.loadingEarlier = false;
      if (current()) render();
    }
  });
  find('#dl-newjump').addEventListener('click', () => {
    render();
    scroll.scrollTop = 0;
    newbar.hidden = true;
  });

  await initial();
}

/* ---------------------------------------------------------------- 路由 */
const pages = {
  overview: pageOverview,
  libraries: pageLibraries,
  items: pageItems,
  settings: pageSettings,
  activity: pageActivity,
  logs: pageLogs,
  scrape: pageScrape,
  translate: pageTranslate,
  scheduled: pageScheduled,
  probe: pageProbe
};

// pageGen 是本轮导航的代号。页面函数在 await 之后拿它比对：快速连点导航时，
// 先发出的慢请求可能后返回，把已经渲染好的新页面整页覆盖掉（导航高亮还停在新的那个）。
let pageGen = 0;

async function page(name) {
  // 浮层属于来源页上下文，切页/前进后退时先收起（P1-7）。
  createdAPIKey = null; // 导航销毁仅在创建页暂存的管理员密钥原文。
  if (typeof stopDashboardPolling === 'function') stopDashboardPolling();
  if (typeof closeOverlaysForNavigation === 'function') closeOverlaysForNavigation();
  stopScrapePolling();
  stopLogsPolling();
  if (typeof stopTranslationPolling === 'function') stopTranslationPolling();
  stopProbePolling();
  // 切页即换 DOM：旧页面挂着的延时句柄（探测面板/扫描面板自动收起）必须作废，
  // 否则它会在新页面上命中同名元素，把不该关的面板关掉。
  cancelProbeHide();
  cancelScanHide();
  const gen = ++pageGen;
  setMeta(name);
  content.innerHTML = skeletonPanel();
  const fn = pages[name];
  if (!fn) { content.innerHTML = `<div class="panel">${empty('模块即将开放', '请先使用上方导航。')}</div>`; return; }
  try {
    await fn();
  } catch (error) {
    // 已经切到别的页面了，这个错误属于上一次导航，别把新页面替换成错误页。
    if (gen !== pageGen) return;
    content.innerHTML = `<div class="panel error" style="border-color:#a64952">${icon('alert')} ${esc(error.message || error)}</div>`;
  }
}

/* ------------------------------------------------------------ 媒体信息探测 */
// 与扫库相互独立：探测只读 .strm 直链、跑 ffprobe、把结果写回 NFO，不写索引库。
let probeTimer = null;
// 探测完成后自动收面板的延时句柄。切页会重建 DOM，旧句柄若不取消，
// 8 秒后会隐藏新页面上的同名面板（表现为「进媒体场面板自己弹出来又消失」）。
let probeHideTimer = null;

function probeEls() {
  return {
    panel: document.querySelector('#probe-progress'),
    title: document.querySelector('#probe-progress-title'),
    count: document.querySelector('#probe-progress-count'),
    fill: document.querySelector('#probe-progress-fill'),
    detail: document.querySelector('#probe-progress-detail')
  };
}

function cancelProbeHide() {
  if (probeHideTimer) { clearTimeout(probeHideTimer); probeHideTimer = null; }
}

// hideProbePanelLater 8 秒后收起探测面板（有失败时保留，方便对照失败条数）。
// 只隐藏当前这一次渲染持有的元素，且在探测重新开始或切页时作废。
function hideProbePanelLater() {
  cancelProbeHide();
  probeHideTimer = setTimeout(() => {
    probeHideTimer = null;
    if (probeTimer) return; // 又开始了新的一轮，交给它自己收
    const el = probeEls();
    if (el.panel) el.panel.hidden = true;
  }, 8000);
}

function renderProbeProgress(p) {
  const el = probeEls();
  if (!el.panel) return; // 不在媒体墙页面时只保持轮询，不渲染
  if (!p || (!p.running && !p.finished_at)) { el.panel.hidden = true; return; }
  el.panel.hidden = false;
  el.title.textContent = p.running ? '正在探测媒体信息' : (p.cancelled ? '探测已中止' : (p.error ? '探测结束（存在错误）' : '探测完成'));
  const { total, done, percent } = progressCounts(p);
  el.count.textContent = total ? `${done}/${total}` : String(done);
  el.fill.classList.toggle('is-indeterminate', !total && p.running);
  el.fill.style.width = `${percent}%`;
  const parts = [];
  if (p.success) parts.push(`成功 ${p.success}`);
  if (p.skipped) parts.push(`跳过 ${p.skipped}`);
  if (p.failed) parts.push(`失败 ${p.failed}`);
  if (p.running && p.current) parts.push(`当前 ${p.current}`);
  if (p.error) parts.push(`错误：${p.error}`);
  el.detail.textContent = parts.join(' · ') || (p.running ? '正在读取源信息…' : '');
}

function stopProbePolling() {
  if (probeTimer) { probeTimer(); probeTimer = null; }
  cancelProbeHide();
}

function setProbeButton(busy) {
  const btn = document.querySelector('#probe-media');
  if (!btn) return;
  btn.disabled = busy;
  const mode = document.querySelector('#scan-mode');
  if (mode) mode.disabled = busy;
  const span = btn.querySelector('span');
  if (span) span.textContent = busy ? '探测中…' : '探测媒体信息';
}

function probeProgressUnavailable(delay) {
  const el = probeEls();
  if (!el.panel) return;
  el.panel.hidden = false;
  el.title.textContent = '探测状态未确认';
  el.detail.setAttribute('role', 'status');
  el.detail.textContent = delay === null
    ? '进度读取受限，任务状态未确认。请检查登录或权限后重新进入媒体墙。'
    : `进度暂不可用，任务状态未确认；${delay / 1000}秒后自动重试。`;
  setProbeButton(true); // 未确认空闲前，不允许盲目重复启动。
}

function startProbePolling(announced = false) {
  stopProbePolling();
  const gen = pageGen, requestGen = wallGen;
  let observedRunning = announced;
  setProbeButton(true);
  probeTimer = startProgressPolling('/probe/media/progress', p => {
    if (gen !== pageGen || requestGen !== wallGen) return;
    renderProbeProgress(p);
    if (p.running) { observedRunning = true; setProbeButton(true); return; }
    stopProbePolling();
    setProbeButton(false);
    // 初读的历史终态不是本次任务完成通知；未知状态也不生成终态。
    if (p.finished_at && observedRunning) {
      if (p.cancelled) toast(`探测已中止：成功 ${p.success} / 跳过 ${p.skipped} / 失败 ${p.failed}`);
      else if (p.error) toast(`探测结束：${p.error}`, 'error');
      else toast(`探测完成：成功 ${p.success} / 跳过 ${p.skipped} / 失败 ${p.failed}`, p.failed ? 'error' : 'ok');
      if (p.failures && p.failures.length) toast(`失败示例：${p.failures[0]}`, 'error');
    }
    if (p.finished_at && !p.failed && !p.error) hideProbePanelLater();
  }, delay => {
    if (gen !== pageGen || requestGen !== wallGen) return;
    probeProgressUnavailable(delay);
    if (delay === null) stopProbePolling();
  });
}

async function runProbeAll() {
  const gen = pageGen, requestGen = wallGen;
  stopProbePolling();
  setProbeButton(true);
  const el = probeEls();
  if (el.panel) el.panel.hidden = false;
  if (el.title) el.title.textContent = '正在启动探测…';
  try {
    // 默认只补缺：已含 streamdetails 的条目跳过，避免每次全库重探（远程探测很慢）。
    const r = await api('/probe/media', { method: 'POST', body: JSON.stringify({ only_missing: true }) });
    if (gen !== pageGen || requestGen !== wallGen) return;
    if (!r.total) {
      toast(r.message || '没有可探测的影片');
      setProbeButton(false);
      if (el.panel) el.panel.hidden = true;
      return;
    }
    toast(`开始探测 ${r.total} 条影片`);
    startProbePolling(true);
  } catch (e) {
    if (gen !== pageGen || requestGen !== wallGen) return;
    toast(e.message, 'error');
    if (e.status === 401 || e.status === 403) probeProgressUnavailable(null);
    else { probeProgressUnavailable(3000); startProbePolling(); }
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
const scanClose = document.querySelector('#scan-progress-close');
let scanTimer = null;
let scanSubmitting = false;
let scanObservation = 0;
// R2：首读历史终态不是通知；运行身份用 instance_id+run_id 识别，
// 不比较秒级时间戳，也不在重启后把重新编号的 task_id 当同一轮运行。
let scanFirstRead = true;
const scanObservedRuns = new Set();
const scanNotifiedRuns = new Set();
let scanWallRefresh = null; // 只刷新本次提交来源的媒体墙，不跟随后来的导航/筛选。

function scanRunKey(p) {
  if (p && p.instance_id && p.run_id) return p.instance_id + ':' + p.run_id;
  if (p && Number.isSafeInteger(p.task_id) && p.task_id > 0) return 'task:' + p.task_id;
  return '';
}

async function readScanProgress(path, options) {
  const p = await api(path, options);
  const pending = scanWallRefresh;
  if (!p || typeof p.running !== 'boolean' || !pending?.accepted || !pending.taskId) return p;
  if (p.running && Number(p.task_id) === pending.taskId) return p;
  // 202 后首读可能仍是旧任务（含 running）；始终按本次 task_id 确认。
  const task = await api('/tasks/' + pending.taskId, options);
  if (scanWallRefresh !== pending) return p;
  if (task.task_id !== undefined && Number(task.task_id) !== pending.taskId) throw new Error('scan task identity mismatch');
  if (task.status === 'queued' || task.status === 'running') return { running: true, task_id: pending.taskId, mode: task.mode, finished_at: null };
  if (!['succeeded', 'failed', 'skipped'].includes(task.status)) throw new Error('invalid scan task status');
  // 极快完成时服务器进度可能还停在上一轮；task 是当前提交的权威身份，
  // 此时清空 instance/run，让完成通知按 task:<id> 归并，而不是冒充旧 run。
  const stale = Number(p.task_id) !== pending.taskId;
  const result = task.result && typeof task.result === 'object' && !Array.isArray(task.result) ? task.result : null;
  const current = result ? { ...result } : (stale ? {} : p);
  if (result && Number.isFinite(result.groups_total) && Number.isFinite(result.groups_done)) {
    current.total = result.groups_total; current.done = result.groups_done;
  }
  return { ...current, mode: task.mode || current.mode,
    instance_id: stale ? '' : p.instance_id, run_id: stale ? '' : p.run_id,
    task_id: pending.taskId, running: false, finished_at: task.finished_at || (!stale ? p.finished_at : null),
    error: task.error || (task.status === 'succeeded' ? current.error || '' : '扫描任务未成功完成'), task_confirmed: true };
}
// 扫描完成后面板自动收起的延时句柄。与 probeHideTimer 同理：
// 句柄不托管时，上一轮排下的 4 秒定时器会关掉下一轮刚显示出来的面板
//（而那时轮询已停，面板再也不会被显示回来）。
let scanHideTimer = null;

function cancelScanHide() {
  if (scanHideTimer) { clearTimeout(scanHideTimer); scanHideTimer = null; }
}

function baseName(path) {
  return String(path || '').split(/[\\/]/).filter(Boolean).pop() || '';
}

function renderScanProgress(p) {
  if (!p || (!p.running && !p.finished_at)) { scanPanel.hidden = true; scanClose.hidden = true; return; }
  scanPanel.hidden = false;
  const hasFailure = p.error || p.failed || p.incomplete;
  scanClose.hidden = p.running || !hasFailure;
  const lib = p.libraries > 1 ? `${p.library_name || '媒体库'}（${p.library_index}/${p.libraries}）` : (p.library_name || '媒体库');
  scanTitle.textContent = p.running ? `正在扫描 ${lib}`
    : `${hasFailure ? '扫描结束（存在错误）' : '扫描完成'} ${lib}`;
  scanDetail.setAttribute('role', 'status');
  const groupMode = p.mode === 'incremental' || p.mode === 'full';
  const { total, done, percent } = progressCounts(groupMode && Number.isFinite(p.groups_total) && Number.isFinite(p.groups_done)
    ? { total: p.groups_total, done: p.groups_done } : p);
  scanCount.textContent = total ? `${done}/${total}${groupMode ? ' 组' : ''}` : String(done);
  scanFill.classList.toggle('is-indeterminate', !total && p.running);
  scanFill.style.width = `${percent}%`;
  const parts = [];
  if (groupMode) {
    parts.push(p.mode === 'incremental' ? '增量扫描' : '全量重建');
    parts.push(`未变化 ${p.unchanged_groups || 0}`, `新增 ${p.added || 0}`, `更新 ${p.updated || 0}`, `删除 ${p.deleted || 0}`);
    if (p.cleanup_performed) parts.push('已完成缺失索引清理检查');
    else if (!p.running) parts.push('未执行缺失索引清理（保留原索引）');
    if (p.incomplete) parts.push('未完成：部分成果已保留，请核对错误后重试');
  }
  if (p.success) parts.push(`已刮削 ${p.success}`);
  if (p.pending) parts.push(`未刮削 ${p.pending}`);
  if (p.unreadable_nfo) parts.push(`NFO 不可读 ${p.unreadable_nfo}（已保留原元数据）`);
  if (p.skipped) parts.push(`跳过 ${p.skipped}`);
  if (p.failed) parts.push(`失败 ${p.failed}`);
  if (p.running && p.current) parts.push(`当前 ${baseName(p.current)}`);
  if (p.error) parts.push(`错误：${p.error}`);
  scanDetail.textContent = parts.join(' · ') || (p.running ? '正在读取目录…' : '');
}

function stopScanPolling() {
  if (scanTimer) { scanTimer(); scanTimer = null; }
  cancelScanHide();
  scanObservation++;
}

function setScanButton(busy, label) {
  const btn = document.querySelector('#scan');
  if (!btn) return;
  btn.disabled = busy;
  const mode = document.querySelector('#scan-mode');
  if (mode) mode.disabled = busy;
  const span = btn.querySelector('span');
  if (span) span.textContent = label || (busy ? '扫描中…' : '开始扫描');
}

function scanProgressUnavailable(delay) {
  scanPanel.hidden = false;
  scanTitle.textContent = '扫描状态未确认';
  scanDetail.setAttribute('role', 'status');
  scanDetail.textContent = delay === null
    ? '进度读取受限，任务状态未确认。请检查登录或权限后刷新页面。'
    : `进度暂不可用，任务状态未确认；${delay / 1000}秒后自动重试。`;
  setScanButton(true, '确认扫描状态…');
}

function startScanPolling() {
  stopScanPolling();
  setScanButton(true, '确认扫描状态…');
  scanTimer = startProgressPolling('/scan/progress', p => {
    if (p.running && scanWallRefresh?.accepted && Number(p.task_id) === scanWallRefresh.taskId) scanWallRefresh.sawRunning = true;
    if (!p.running && scanSubmitting) {
      // 异步提交在途：首个GET可能早于 worker 启动，或读到上一轮终态。
      // 不把在途提交误判为空闲，也不停止后续观察。
      scanPanel.hidden = false;
      scanTitle.textContent = '扫描请求进行中';
      scanDetail.setAttribute('role', 'status');
      scanDetail.textContent = '扫描请求仍在等待响应，当前任务状态尚未确认。';
      setScanButton(true, '等待扫描响应…');
      return;
    }
    if (scanFirstRead && !p.running && !scanWallRefresh) {
      // 初次打开就先读到空闲/历史终态：不展示、不安排收起动画，按钮恢复可用。
      scanFirstRead = false;
      scanPanel.hidden = true;
      scanClose.hidden = true;
      setScanButton(false);
      stopScanPolling();
      return;
    }
    scanFirstRead = false;
    const runKey = scanRunKey(p);
    if (p.running) {
      if (runKey) scanObservedRuns.add(runKey);
      renderScanProgress(p);
      setScanButton(true);
      return;
    }
    if (runKey && scanNotifiedRuns.has(runKey)) {
      // 同一轮终态只通知一次；换页/回到首页不重复弹窗。
      scanPanel.hidden = true;
      scanClose.hidden = true;
      setScanButton(false);
      stopScanPolling();
      return;
    }
    if (runKey) scanNotifiedRuns.add(runKey);
    renderScanProgress(p);
    setScanButton(false);
    if (!p.running) {
      const pending = scanWallRefresh;
      scanWallRefresh = null;
      if (pending?.accepted && (p.task_confirmed || pending.sawRunning) &&
          pending.gen === pageGen && pending.wall === wallGen && pending.identity === token &&
          localStorage.getItem('emby_token') === pending.identity &&
          pages[location.hash.replace('#', '')] === pageItems) reloadWall();
      stopScanPolling();
      if (p.finished_at && !p.failed && !p.error && !p.incomplete) {
        const observation = scanObservation;
        scanHideTimer = setTimeout(() => {
          if (observation !== scanObservation || scanSubmitting || scanTimer) return;
          scanHideTimer = null;
          scanPanel.hidden = true;
        }, 4000);
      }
    }
  }, delay => {
    scanProgressUnavailable(delay);
    if (delay === null) stopScanPolling();
  }, () => scanSubmitting, readScanProgress);
}

async function runScan(libraryId) {
  // 行内入口也必须服从全局状态：提交在途、状态未知或已确认运行均不可再次POST。
  if (scanSubmitting || document.querySelector('#scan')?.disabled || localStorage.getItem('emby_token') !== token) return;
  const gen = pageGen, requestGen = wallGen;
  const pending = { gen, wall: requestGen, identity: token, accepted: false, taskId: null, sawRunning: false };
  scanWallRefresh = pending;
  scanSubmitting = true;
  let completed = false, denied = false;
  scanPanel.hidden = false;
  scanTitle.textContent = '正在启动扫描…';
  scanCount.textContent = '';
  scanDetail.textContent = '';
  startScanPolling();
  try {
    // 异步入口：202 只代表任务已排队，进度与计数由 /scan/progress 观察器呈现。
    const mode = document.querySelector('#scan-mode')?.value === 'full' ? 'full' : 'incremental';
    const url = libraryId ? `/tasks/scan?library_id=${encodeURIComponent(libraryId)}&mode=${mode}` : `/tasks/scan?mode=${mode}`;
    const accepted = await api(url, { method: 'POST' });
    if (scanWallRefresh !== pending || pending.identity !== token || localStorage.getItem('emby_token') !== pending.identity) return;
    pending.accepted = true;
    pending.taskId = Number.isSafeInteger(accepted?.task_id) && accepted.task_id > 0 ? accepted.task_id : null;
    completed = true;
    toast('扫描任务已启动，进度见上方面板');
  } catch (e) {
    toast(e.message, 'error');
    denied = e.status === 401 || e.status === 403;
  } finally {
    scanSubmitting = false;
  }
  // 提交失败不重放POST；响应结束后只通过同一观察器重新确认全局状态。
  if (scanWallRefresh !== pending || pending.identity !== token || localStorage.getItem('emby_token') !== pending.identity) return;
  if (denied) { scanWallRefresh = null; stopScanPolling(); scanProgressUnavailable(null); }
  else startScanPolling();
  // 全局工具栏跨页面继续观察；提交回包不得刷新用户已切走/重新筛选的页面。
  const name = location.hash.replace('#', '');
  if (completed && gen === pageGen && requestGen === wallGen && pages[name] === pageItems) {
    reloadWall();
    pending.wall = wallGen; // 接受时的兼容刷新不使完成刷新误认作用户换筛选。
  }
}

function clearSessionView() {
  if (typeof stopDashboardPolling === 'function') stopDashboardPolling();
  createdAPIKey = null;
  settingsCategory = 'account';
  me = null;
  ++pageGen;
  ++wallGen;
  scanWallRefresh = null;
  scanFirstRead = true;
  scanObservedRuns.clear();
  scanNotifiedRuns.clear();
  stopScrapePolling();
  if (typeof stopTranslationPolling === 'function') stopTranslationPolling();
  stopProbePolling();
  stopScanPolling();
  cancelProbeHide();
  cancelScanHide();
  closeOverlaysForNavigation();
  content.innerHTML = '';
  scanPanel.hidden = true;
}

async function logoutCurrentSession(button) {
  const requestToken = token;
  if (localStorage.getItem('emby_token') !== requestToken || button.disabled) return;
  button.disabled = true;
  try {
    const response = await fetch('/api/auth/logout', { method: 'POST', headers: { 'X-Emby-Token': requestToken } });
    if (localStorage.getItem('emby_token') !== requestToken) return;
    if (!response.ok && response.status !== 401) throw new Error('退出失败，请重试');
    clearSessionView();
    localStorage.removeItem('emby_token');
    token = null;
    window.location.replace('/');
  } catch (error) {
    if (localStorage.getItem('emby_token') === requestToken) toast(error.message || '退出失败，请重试', 'error');
  } finally {
    button.disabled = false;
  }
}

function identityStorageChanged(event) {
  if ((event.key === 'emby_token' || event.key === null) && localStorage.getItem('emby_token') !== token) {
    clearSessionView();
    window.location.replace('/');
  }
}

async function boot() {
  if (!token) { window.location.replace('/'); return; }
  const requestToken = token;
  try {
    const response = await fetch('/Users/Me', { headers: { 'X-Emby-Token': requestToken } });
    if (localStorage.getItem('emby_token') !== requestToken) return;
    if (response.status === 401) {
      localStorage.removeItem('emby_token');
      window.location.replace('/');
      return;
    }
    if (!response.ok) throw new Error('authentication unavailable');
    const dto = typeof response.json === 'function' ? await response.json().catch(() => null) : null;
    if (localStorage.getItem('emby_token') !== requestToken) return;
    me = dto ? { name: dto.Name || '', is_admin: !!(dto.Policy && dto.Policy.IsAdministrator) } : null;
  } catch {
    if (localStorage.getItem('emby_token') !== requestToken) return;
    // A failed validation is not evidence that the stored credential is invalid.
    content.innerHTML = `<section class="empty"><p role="alert">暂时无法验证登录状态，请检查服务或网络后重试。已保留登录凭据。</p><button id="auth-retry" class="btn btn-accent" type="button">重试验证</button></section>`;
    document.querySelector('#auth-retry').addEventListener('click', event => {
      event.currentTarget.disabled = true;
      boot();
    });
    return;
  }
  // 当前账户（普通用户只保留「设置」里的改密条目，管理页一律隐藏；观看走 Emby 客户端）。
  // 管理员标记直接取 /Users/Me 的 Policy.IsAdministrator，避免额外请求。
  if (me && !me.is_admin) {
    document.querySelectorAll('.nav-item').forEach(item => {
      if (item.dataset.page !== 'settings' && typeof item.remove === 'function') item.remove();
    });
    if (location.hash !== '#settings') location.hash = '#settings';
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
  document.querySelector('#logout').addEventListener('click', event => { void logoutCurrentSession(event.currentTarget); });
  document.querySelector('#scan').addEventListener('click', () => runScan());
  scanClose.addEventListener('click', () => {
    cancelScanHide();
    stopScanPolling();
    scanPanel.hidden = true;
    scanClose.hidden = true;
    setScanButton(false);
  });
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
    if (event.key === 'Escape') {
      const top = topOverlaySelector();
      if (top === '#scrape-overlay') closeScrapePreview();
      else if (top === '#player-overlay') closePlayer();
      else if (top === '#detail-overlay') closeDetail();
      return;
    }
    overlayTrapTab(event);
  });
  // 全局扫描观察不阻塞初始页面；首读也经过同一超时/恢复链路。
  startScanPolling();
  // 媒体墙渲染后由同一探测观察器恢复，boot不再裸读探测进度。
  const initial = location.hash.replace('#', '');
  page(pages[initial] ? initial : 'overview');
}
window.addEventListener('storage', event => identityStorageChanged(event));
boot();
