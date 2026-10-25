'use strict';

// 首页独立控制器。只持有当前挂载的快照，不持久化监控数据或身份。
(function (global) {
  const escape = value => String(value ?? '').replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
  const finite = value => typeof value === 'number' && Number.isFinite(value) && value >= 0;
  const count = value => finite(value) ? new Intl.NumberFormat('zh-CN', { maximumFractionDigits: 0 }).format(value) : '—';
  const decimal = value => finite(value) ? new Intl.NumberFormat('zh-CN', { maximumFractionDigits: 2 }).format(value) : '—';
  const latency = (value, overflow) => overflow === true ? '>60,000' : decimal(value);
  const clock = value => {
    if (typeof value !== 'string' || !value) return '—';
    const date = new Date(value);
    return Number.isFinite(date.getTime()) ? date.toLocaleTimeString('zh-CN', { hour12: false }) : '—';
  };
  const duration = value => {
    if (!finite(value)) return '—';
    const seconds = Math.floor(value);
    return seconds >= 86400 ? `${Math.floor(seconds / 86400)} 天 ${Math.floor(seconds % 86400 / 3600)} 小时` : seconds >= 3600 ? `${Math.floor(seconds / 3600)} 小时 ${Math.floor(seconds % 3600 / 60)} 分` : `${Math.floor(seconds / 60)} 分 ${seconds % 60} 秒`;
  };
  function validSnapshot(data, minutes) {
    if (!data || typeof data !== 'object' || data.window_minutes !== minutes || typeof data.instance_id !== 'string' || !data.instance_id || !Number.isFinite(Date.parse(data.generated_at))) return false;
    if (!data.requests || !data.runtime || !data.tasks || !Array.isArray(data.tasks.recent) || !Array.isArray(data.series) || data.series.length !== minutes) return false;
    if (data.tasks.recent.some(task => !task || typeof task !== 'object')) return false;
    for (const key of ['total', 'errors_4xx', 'errors_5xx', 'sampled_seconds']) if (!finite(data.requests[key])) return false;
    for (const key of ['p50_ms', 'p95_ms', 'rate_per_second']) if (data.requests[key] !== null && !finite(data.requests[key])) return false;
    for (const key of ['p50_overflow', 'p95_overflow']) if (typeof data.requests[key] !== 'boolean') return false;
    for (const key of ['uptime_seconds', 'goroutines', 'heap_alloc_bytes', 'heap_inuse_bytes', 'go_sys_bytes', 'num_gc']) if (!finite(data.runtime[key])) return false;
    if (!finite(data.tasks.running)) return false;
    return data.series.every(point => point && Number.isFinite(Date.parse(point.start)) && typeof point.observed === 'boolean' &&
      ['requests', 'errors_4xx', 'errors_5xx', 'p50_ms', 'p95_ms'].every(key => point[key] === null || finite(point[key])) &&
      typeof point.p50_overflow === 'boolean' && typeof point.p95_overflow === 'boolean');
  }
  function chart(points, lines, unit, label) {
    const values = points.flatMap(point => lines.map(line => point.observed && finite(point[line.key]) ? point[line.key] : null)).filter(finite);
    if (!values.length) return `<div class="dash-chart-empty">${unit === '次' ? '暂无请求观测' : '暂无延迟样本'}<span>无观测或超出有限桶的数据不绘制为零；详见数据表。</span></div>`;
    const max = Math.max(1, ...values), left = 46, top = 20, width = 580, height = 154;
    const x = index => left + index / Math.max(1, points.length - 1) * width;
    const y = value => top + height - value / max * height;
    const grid = [0, 0.5, 1].map(ratio => `<line class="dash-gridline" x1="${left}" x2="${left + width}" y1="${y(max * ratio)}" y2="${y(max * ratio)}"/><text class="dash-axis" x="${left - 8}" y="${y(max * ratio) + 4}" text-anchor="end">${escape(new Intl.NumberFormat('zh-CN', { notation: 'compact', maximumFractionDigits: 1 }).format(max * ratio))}</text>`).join('');
    const paths = lines.map(line => {
      let drawing = false;
      const path = points.map((point, index) => {
        const value = point[line.key];
        if (!point.observed || !finite(value)) { drawing = false; return ''; }
        const command = `${drawing ? 'L' : 'M'}${x(index).toFixed(2)},${y(value).toFixed(2)}`;
        drawing = true; return command;
      }).join(' ');
      const dots = points.map((point, index) => point.observed && finite(point[line.key]) ? `<circle class="dash-line-${line.tone}" cx="${x(index).toFixed(2)}" cy="${y(point[line.key]).toFixed(2)}" r="2.5"/>` : '').join('');
      return `<path class="dash-line dash-line-${line.tone}${line.dashed ? ' dash-dashed' : ''}" d="${path}"/>${dots}`;
    }).join('');
    return `<svg viewBox="0 0 646 210" role="img" aria-label="${escape(label)}"><title>${escape(label)}；横轴为分钟，纵轴单位${escape(unit)}。完整数值见下方数据表。</title>${grid}${paths}<text class="dash-axis" x="${left}" y="199">${escape(clock(points[0]?.start))}</text><text class="dash-axis" x="${left + width}" y="199" text-anchor="end">${escape(clock(points[points.length - 1]?.start))}</text></svg>`;
  }
  const kinds = { scan: '媒体扫描', reindex: '重建索引', probe: '媒体探测', scrape: '元数据刮削', scrape_avatars: '补演员头像', avatars: '补演员头像', translate: '独立翻译', analyze: '媒体分析', webhook: '通知投递', edit: '元数据编辑', other: '其他任务' };
  const states = { running: '执行中', queued: '排队中', succeeded: '已完成', success: '已完成', failed: '失败', skipped: '已跳过', cancelled: '已中止', canceled: '已中止', unknown: '状态未知' };
  function mountDashboard(host, options) {
    const { apiRequest, isCurrent } = options;
    let active = true, paused = false, wide = false, fullscreenPending = false, reading = false, timer = null, request = null, cancelDeadline = null, serial = 0, minutes = 15, snapshot = null, failures = 0;
    let mediaSnapshot = null, mediaInstance = null;
    const doc = host.ownerDocument || global.document;
    host.innerHTML = `<section id="overview-dashboard" class="dashboard" aria-label="运行监控首页" aria-live="off">
      <header class="dash-heading"><div><p class="dash-kicker">EMBY-GO / CONTROL PLANE</p><h2>运行观测<span>每一帧数据，都有据可查。</span></h2></div><div class="dash-update"><span class="dash-state" data-dash="state" role="status" aria-live="polite">正在获取运行快照…</span><span data-dash="updated">尚未收到数据</span></div></header>
      <div class="dash-toolbar"><div class="dash-windows" role="group" aria-label="统计窗口">${[5, 15, 60].map(value => `<button type="button" class="btn" data-window="${value}" aria-pressed="${value === 15}">${value} 分钟</button>`).join('')}</div><div class="dash-controls"><button type="button" class="btn" data-dash="pause" aria-pressed="false">暂停更新</button><button type="button" class="btn" data-dash="refresh">立即刷新</button><button type="button" class="btn" data-dash="wide" aria-pressed="false">大屏视图</button></div></div>
      <p class="dash-feedback" data-dash="feedback" role="status" aria-live="polite"></p>
      <div class="dash-kpis" aria-label="核心运行指标">${[['p95', 'P95 请求延迟', 'ms', '直方图桶上界估计'], ['requests', '窗口请求量', '次', 'JSON 控制面请求'], ['errors', '服务端错误', '次 / 5xx', '不含媒体流与图片'], ['heap', 'Go 堆已分配', 'MiB', '非进程 RSS / 内存上限']].map(([key, name, unit, note]) => `<article class="dash-kpi dash-kpi-${key}"><h3>${name}</h3><p><strong data-dash="${key}">—</strong><span>${unit}</span></p><small data-dash="${key}-note">${note}</small></article>`).join('')}</div>
      <div class="dash-charts"><section class="dash-panel" aria-labelledby="dash-traffic-title"><div class="dash-panel-head"><div><p class="dash-kicker">TRAFFIC</p><h3 id="dash-traffic-title">请求与服务端错误</h3></div><span>次 / 分钟</span></div><div class="dash-legend"><span class="dash-key dash-key-blue">请求</span><span class="dash-key dash-key-amber">5xx 错误</span></div><div class="dash-chart" data-dash="traffic">尚未收到数据</div></section>
      <section class="dash-panel" aria-labelledby="dash-latency-title"><div class="dash-panel-head"><div><p class="dash-kicker">LATENCY</p><h3 id="dash-latency-title">请求耗时分位数</h3></div><span>毫秒 / 桶上界</span></div><div class="dash-legend"><span class="dash-key dash-key-cyan">P95</span><span class="dash-key dash-key-muted">P50 · 虚线</span></div><div class="dash-chart" data-dash="latency">尚未收到数据</div></section></div>
      <details class="dash-data disclosure"><summary class="disclosure-summary">查看逐分钟数据表与统计口径</summary><div class="disclosure-body"><p class="dash-note">统计仅覆盖 JSON 控制面；流媒体、图片、静态资源、健康检查与监控自身请求不参与统计。P95 表示约 95% 请求落在该桶上界内，不是平均耗时。启动前无观测、无延迟样本显示“—”；超过 60 秒的分位数显示 &gt;60,000 ms。进程重启后历史清空，最多保留 60 分钟。</p><p data-dash="window-info" class="dash-note">等待服务端窗口信息。</p><div class="table-wrap"><table class="dash-table"><caption class="sr-only">所选窗口逐分钟监控样本</caption><thead><tr><th>分钟</th><th>请求</th><th>4xx</th><th>5xx</th><th>P50 ms</th><th>P95 ms</th></tr></thead><tbody data-dash="samples"><tr><td colspan="6">尚未收到数据</td></tr></tbody></table></div></div></details>
      <div class="dash-bottom"><section class="dash-panel"><div class="dash-panel-head"><div><p class="dash-kicker">ARCHIVE</p><h3>媒体档案</h3></div><a href="#items">查看媒体墙 →</a></div><div class="dash-media" data-dash="media">尚未收到数据</div><p class="dash-note" data-dash="media-updated">媒体摘要最多缓存 15 秒；NFO 是元数据真源。</p></section>
      <section class="dash-panel"><div class="dash-panel-head"><div><p class="dash-kicker">RUNTIME</p><h3>Go 运行时</h3></div><span data-dash="uptime">—</span></div><dl class="dash-runtime" data-dash="runtime"><dt>运行指标</dt><dd>—</dd></dl></section>
      <section class="dash-panel dash-tasks"><div class="dash-panel-head"><div><p class="dash-kicker">JOBS</p><h3>任务摘要 <span data-dash="running">—</span></h3></div><a href="#scheduled">计划与执行记录 →</a></div><div data-dash="tasks">尚未收到数据</div><p class="dash-note">仅展示最近最多 10 项；任务失败不代表整个服务不可用。</p></section></div>
      <footer class="dash-footer"><nav aria-label="监控快捷入口"><a href="#libraries">媒体库</a><a href="#scrape">刮削</a><a href="#translate">独立翻译</a><a href="#logs">运行日志</a><a href="#settings">服务设置</a><a href="#scheduled">计划与执行记录 →</a></nav></footer>
    </section>`;
    const root = host.querySelector('.dashboard');
    const node = name => root.querySelector(`[data-dash="${name}"]`);
    const text = (name, value) => { const target = node(name); if (target.textContent !== String(value)) target.textContent = String(value); };
    const html = (name, value) => { const target = node(name); if (target.innerHTML !== value) target.innerHTML = value; };
    const current = () => active && host.isConnected && isCurrent();
    const status = (value, stale = true) => { text('state', value); root.classList.toggle('is-stale', stale); };
    function patch(data) {
      snapshot = data;
      text('p95', latency(data.requests.p95_ms, data.requests.p95_overflow));
      text('p95-note', data.requests.total === 0 ? '窗口内暂无延迟样本' : '直方图桶上界估计 · 非平均值');
      text('requests', count(data.requests.total)); text('requests-note', `${decimal(data.requests.rate_per_second)} 次 / 秒 · 实际采样 ${decimal(data.requests.sampled_seconds)} 秒`);
      text('errors', count(data.requests.errors_5xx)); text('errors-note', data.requests.total > 0 ? `${decimal(data.requests.errors_5xx / data.requests.total * 100)}% 请求 · 4xx ${count(data.requests.errors_4xx)}` : '窗口内无请求 · 不推断健康状态');
      text('heap', decimal(data.runtime.heap_alloc_bytes / 1048576));
      text('updated', `快照 ${clock(data.generated_at)}`);
      text('window-info', `窗口 ${clock(data.window_start)} — ${clock(data.window_end)} · ${data.window_minutes} 分钟 · 浏览器本地时间 · 实例 ${data.instance_id}`);
      html('traffic', chart(data.series, [{ key: 'requests', tone: 'blue' }, { key: 'errors_5xx', tone: 'amber', dashed: true }], '次', '逐分钟请求与 5xx 错误趋势'));
      html('latency', chart(data.series, [{ key: 'p95_ms', tone: 'cyan' }, { key: 'p50_ms', tone: 'muted', dashed: true }], '毫秒', '逐分钟 P95 与 P50 延迟趋势'));
      html('samples', data.series.map(point => `<tr><th scope="row">${escape(clock(point.start))}${point.observed ? '' : ' · 无观测'}</th><td>${count(point.requests)}</td><td>${count(point.errors_4xx)}</td><td>${count(point.errors_5xx)}</td><td>${latency(point.p50_ms, point.p50_overflow)}</td><td>${latency(point.p95_ms, point.p95_overflow)}</td></tr>`).join(''));
      if (mediaInstance !== data.instance_id) { mediaSnapshot = null; mediaInstance = data.instance_id; }
      const media = data.media;
      const completeMedia = media && ['success', 'manual', 'pending', 'total', 'libraries'].every(key => finite(media[key]));
      const staleMedia = !completeMedia || media.available === false || media.stale === true;
      if (completeMedia) mediaSnapshot = media;
      if (mediaSnapshot) {
        html('media', [['success', '已刮削'], ['manual', '手动录入'], ['pending', '待处理'], ['libraries', '媒体库']].map(([key, label]) => `<div><strong>${count(mediaSnapshot[key])}</strong><span>${label}</span></div>`).join(''));
        text('media-updated', `${staleMedia ? '摘要已过期 · 保留上次数据 · ' : ''}共 ${count(mediaSnapshot.total)} 部 · 摘要 ${clock(mediaSnapshot.updated_at)} · 最多缓存 15 秒`);
      } else {
        html('media', '<p class="dash-note">暂无可用媒体摘要</p>');
        text('media-updated', '媒体摘要读取失败；请求统计与 Go 运行时仍独立更新，不将未知计数显示为零。');
      }
      node('media-updated').classList.toggle('dash-warning', staleMedia);
      text('uptime', `运行 ${duration(data.runtime.uptime_seconds)}`);
      html('runtime', [['Goroutines', count(data.runtime.goroutines)], ['Go 堆使用', `${decimal(data.runtime.heap_inuse_bytes / 1048576)} MiB`], ['Go 向系统申请', `${decimal(data.runtime.go_sys_bytes / 1048576)} MiB`], ['GC 次数', count(data.runtime.num_gc)]].map(([key, value]) => `<dt>${key}</dt><dd>${value}</dd>`).join(''));
      text('running', `· ${count(data.tasks.running)} 项执行中`);
      html('tasks', data.tasks.recent.length ? `<ul class="dash-task-list">${data.tasks.recent.slice(0, 10).map(task => `<li><div><strong>${escape(kinds[task.kind] || task.kind || '任务')}</strong><small>${escape(clock(task.started_at))} · ${task.ended_at ? escape(clock(task.ended_at)) : '尚未结束'}</small></div><span class="dash-task-state">${escape(states[task.status] || task.status || '未知')}</span></li>`).join('')}</ul>` : '<p class="dash-note">暂无任务记录</p>');
    }
    function cancelRead() {
      serial++; reading = false;
      if (timer !== null) global.clearTimeout(timer); timer = null;
      if (request) { request.abort(); request = null; }
      if (cancelDeadline) { cancelDeadline(); cancelDeadline = null; }
      node('refresh').disabled = false;
    }
    function schedule() {
      if (current() && !paused && !doc.hidden) timer = global.setTimeout(() => read(false), Math.min(30000, 5000 * Math.pow(2, Math.min(failures, 3))));
    }
    async function read(manual) {
      if (!current()) { stop(); return; }
      if (reading || doc.hidden || (!manual && paused)) return;
      if (timer !== null) global.clearTimeout(timer); timer = null;
      reading = true; node('refresh').disabled = true;
      const id = ++serial, selected = minutes, controller = new global.AbortController(); request = controller;
      let timeout;
      const deadline = new Promise((_, reject) => {
        cancelDeadline = () => { global.clearTimeout(timeout); reject(new Error('读取已取消')); };
        timeout = global.setTimeout(() => { controller.abort(); reject(new Error('读取超时')); }, 10000);
      });
      try {
        const data = await Promise.race([apiRequest(`/dashboard?window=${selected}m`, { signal: controller.signal }), deadline]);
        if (!current() || id !== serial) return;
        if (!validSnapshot(data, selected)) throw new Error('监控响应格式不完整');
        patch(data); failures = 0;
        status(paused ? '已暂停 · 显示当前快照' : '快照已更新 · 每 5 秒读取', paused);
      } catch (error) {
        if (!current() || id !== serial) return;
        failures++;
        if (error.status === 401 || error.status === 403) { paused = true; node('pause').setAttribute('aria-pressed', 'true'); text('pause', '恢复更新'); status('读取受限 · 自动更新已停止'); }
        else status(`${snapshot ? '数据已过期 · 保留上次快照' : '监控暂不可用 · 尚无数据'} · ${error.message === '读取超时' ? '读取超时' : '读取失败'}${paused ? '' : '，将退避重试'}`);
      } finally {
        global.clearTimeout(timeout);
        if (id === serial) { request = null; cancelDeadline = null; reading = false; node('refresh').disabled = false; schedule(); }
      }
    }
    const onVisibility = () => {
      if (!current()) { stop(); return; }
      cancelRead();
      if (doc.hidden) status('页面在后台 · 已停止读取');
      else if (!paused) void read(false);
    };
    function leaveFullscreen() {
      if (doc.fullscreenElement === root && typeof doc.exitFullscreen === 'function') Promise.resolve(doc.exitFullscreen()).catch(() => {});
    }
    function setWide(value) {
      wide = value;
      root.classList.toggle('is-wide', value); doc.body.classList.toggle('dashboard-wide', value);
      node('wide').setAttribute('aria-pressed', String(value)); text('wide', value ? '退出大屏' : '大屏视图');
      if (!value) leaveFullscreen();
    }
    const onFullscreen = () => { if (wide && !doc.fullscreenElement && !fullscreenPending) setWide(false); };
    const onKey = event => { if (event.key === 'Escape' && wide) { setWide(false); if (current()) node('wide').focus(); } };
    node('wide').addEventListener('click', async () => {
      if (!current()) return;
      if (wide) { setWide(false); return; }
      setWide(true);
      if (typeof root.requestFullscreen !== 'function') { text('feedback', '已切换页面大屏布局；浏览器不支持全屏。'); return; }
      fullscreenPending = true;
      try { await root.requestFullscreen(); if (!current() || !wide) leaveFullscreen(); else text('feedback', '已进入大屏；按 Esc 或“退出大屏”返回。'); }
      catch { if (current() && wide) text('feedback', '浏览器未允许全屏，已使用页面大屏布局；可随时退出。'); }
      finally { fullscreenPending = false; }
    });
    node('pause').addEventListener('click', () => {
      if (!current()) return;
      paused = !paused; cancelRead(); node('pause').setAttribute('aria-pressed', String(paused)); text('pause', paused ? '恢复更新' : '暂停更新');
      if (paused) status('已暂停 · 保留当前快照'); else void read(false);
    });
    node('refresh').addEventListener('click', () => { if (current()) void read(true); });
    root.querySelectorAll('[data-window]').forEach(button => button.addEventListener('click', () => {
      if (!current() || minutes === Number(button.dataset.window)) return;
      minutes = Number(button.dataset.window); cancelRead();
      root.querySelectorAll('[data-window]').forEach(item => item.setAttribute('aria-pressed', String(Number(item.dataset.window) === minutes)));
      status(snapshot ? '统计窗口已切换 · 暂留上一窗口快照' : '正在读取所选统计窗口…');
      void read(true);
    }));
    function stop() {
      if (!active) return;
      active = false; cancelRead(); setWide(false);
      doc.removeEventListener('visibilitychange', onVisibility); doc.removeEventListener('fullscreenchange', onFullscreen); doc.removeEventListener('keydown', onKey);
    }
    doc.addEventListener('visibilitychange', onVisibility); doc.addEventListener('fullscreenchange', onFullscreen); doc.addEventListener('keydown', onKey);
    if (doc.hidden) status('页面在后台 · 尚未读取'); else void read(false);
    return stop;
  }
  global.mountDashboard = mountDashboard;
  global.EmbyDashboard = Object.freeze({ validSnapshot, chart, count, latency, duration });
})(globalThis);
