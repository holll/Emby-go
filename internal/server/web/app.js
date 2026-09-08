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
    search: '<circle cx="11" cy="11" r="7"/><path d="m21 21-4.3-4.3"/>'
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
  items: { title: '影片记录', crumb: 'Archive / Items' },
  manual: { title: '手动补录', crumb: 'Archive / Manual Ingest' },
  settings: { title: '设置', crumb: 'Archive / Settings' },
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
        ['媒体库', libraries.items?.length || 0, 'Collection Folder'],
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
  content.innerHTML = `
    <section class="panel">
      <div class="panel-head"><h2>媒体库</h2><span class="hint" style="margin:0">扫描/浏览均以库为单位</span></div>
      ${items.length ? `
        <div class="table-wrap"><table>
          <thead><tr><th>名称</th><th>路径</th><th>ID</th></tr></thead>
          <tbody>${items.map(item => `<tr><td><strong class="title">${esc(item.Name)}</strong></td><td class="mono">${esc(item.Path)}</td><td class="num">${esc(item.Id)}</td></tr>`).join('')}</tbody>
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
}

/* ---------------------------------------------------------------- 影片记录 */
async function pageItems() {
  const params = new URLSearchParams(location.search);
  const active = params.get('status') || '';
  const search = params.get('search') || '';
  const query = new URLSearchParams();
  if (active) query.set('status', active);
  if (search) query.set('search', search);
  const data = await api('/items?limit=1000&' + query.toString());
  const items = data.items || [];

  const pills = [['', '全部'], ['success', '可播放'], ['manual', '手动'], ['pending', '待补录'], ['incompatible', '不兼容']];
  content.innerHTML = `
    <section class="panel">
      <div class="panel-head"><h2>影片记录</h2><span class="hint" style="margin:0">共 ${esc(data.total)} 条${active ? ' · ' + esc(STATUS_TEXT[active] || active) : ''}</span></div>
      <div class="filters">
        ${pills.map(([value, label]) => `<button class="pill ${active === value ? 'is-active' : ''}" data-status="${value}">${esc(label)}</button>`).join('')}
        <input id="item-search" placeholder="搜索标题 / 番号 / 原名" value="${esc(search)}" style="min-width:220px">
      </div>
      ${items.length ? `
        <div class="table-wrap"><table>
          <thead><tr><th>影片</th><th>状态</th><th>源协议</th><th style="text-align:right">操作</th></tr></thead>
          <tbody>${items.map(item => `
            <tr>
              <td>
                <strong class="title">${esc(item.Title || item.title || '—')}</strong>
                ${(item.Number || item.year || item.OriginalTitle) ? `<span class="sub">${[item.Number, item.year, item.OriginalTitle].filter(Boolean).join(' · ')}</span>` : ''}
              </td>
              <td>${statusBadge(item.Status || item.status)}</td>
              <td>${protoBadge(item.source_protocol)} ${item.source_container ? `<span class="protocol">${esc(item.source_container)}</span>` : ''}</td>
              <td><div class="row-actions">
                <button class="btn btn-sm" data-reread="${item.id}" title="重读 .strm 与 NFO">${icon('refresh')}<span>重读源</span></button>
                <button class="icon-btn danger" data-delete="${item.id}" title="删除索引（不删文件）">${icon('trash')}</button>
              </div></td>
            </tr>`).join('')}</tbody>
        </table></div>` : empty(active ? `没有 ${STATUS_TEXT[active] || active} 的影片` : '没有影片', search ? '试试其它关键词。' : '开始扫描或手动补录后再来看看。')}
      <p class="hint">不兼容源不会进入 Emby；更换为 http(s) .strm 后点击「重读源」即可重新判定。删除索引不会触碰磁盘文件。</p>
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
  document.querySelectorAll('[data-reread]').forEach(button => button.addEventListener('click', async () => {
    try { const r = await api('/items/' + button.dataset.reread + '/reread', { method: 'POST' }); toast(`状态已更新：${STATUS_TEXT[r.status] || r.status}`); pageItems(); }
    catch (e) { toast(e.message, 'error'); }
  }));
  document.querySelectorAll('[data-delete]').forEach(button => button.addEventListener('click', async () => {
    if (!confirm('仅删除数据库索引，不删除源文件。继续？')) return;
    try { await api('/items/' + button.dataset.delete, { method: 'DELETE' }); toast('已删除索引', 'ok'); pageItems(); }
    catch (e) { toast(e.message, 'error'); }
  }));
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
  document.querySelector('#task-scan').addEventListener('click', async () => {
    try { const r = await api('/scan', { method: 'POST' }); toast(`扫描完成：${r.success} 成功 / ${r.incompatible} 不兼容`); pageTasks(); }
    catch (e) { toast(e.message, 'error'); }
  });
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

async function runScan() {
  const scanBtn = document.querySelector('#scan');
  scanBtn.disabled = true;
  scanBtn.querySelector('span').textContent = '扫描中…';
  try {
    const r = await api('/scan', { method: 'POST' });
    toast(`扫描完成：${r.success} 可播放 / ${r.pending} 待补录 / ${r.incompatible} 不兼容`);
  } catch (e) { toast(e.message, 'error'); }
  scanBtn.disabled = false;
  scanBtn.querySelector('span').textContent = '开始扫描';
  if (pages[location.hash.replace('#', '')]) page(location.hash.replace('#', ''));
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
  document.querySelector('#scan').addEventListener('click', runScan);
  const initial = location.hash.replace('#', '');
  page(pages[initial] ? initial : 'overview');
}
boot();
