'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const source = fs.readFileSync(path.join(__dirname, 'web/dashboard.js'), 'utf8');
const css = fs.readFileSync(path.join(__dirname, 'web/dashboard.css'), 'utf8');
const app = fs.readFileSync(path.join(__dirname, 'web/app.js'), 'utf8');
const flush = async () => { for (let i = 0; i < 12; i++) await Promise.resolve(); };
function snapshot(minutes = 15, overrides = {}) {
  const start = Date.parse('2026-10-24T16:00:00Z');
  return {
    instance_id: 'test-instance', started_at: new Date(start).toISOString(), generated_at: new Date(start + minutes * 60000).toISOString(),
    retention_minutes: 60, window_minutes: minutes, window_start: new Date(start).toISOString(), window_end: new Date(start + minutes * 60000).toISOString(),
    requests: { total: minutes * 3, errors_4xx: 0, errors_5xx: 0, rate_per_second: .05, p50_ms: 10, p95_ms: 50, p50_overflow: false, p95_overflow: false, sampled_seconds: minutes * 60 },
    latency: { unit: 'ms', method: 'histogram_upper_bound', max_finite_ms: 60000, scope: 'json_control_plane' },
    series: Array.from({ length: minutes }, (_, index) => ({ start: new Date(start + index * 60000).toISOString(), observed: true, requests: 3, errors_4xx: 0, errors_5xx: 0, p50_ms: 10, p95_ms: 50, p50_overflow: false, p95_overflow: false })),
    runtime: { uptime_seconds: 120, goroutines: 13, heap_alloc_bytes: 2097152, heap_inuse_bytes: 4194304, go_sys_bytes: 8388608, num_gc: 7 },
    media: { success: 20, manual: 2, pending: 3, total: 25, libraries: 2, updated_at: new Date(start).toISOString() },
    tasks: { running: 1, recent: [{ id: 'task-1', kind: 'scan', status: 'running', started_at: new Date(start).toISOString(), ended_at: '' }] },
    ...overrides
  };
}
function fixture(api, { hidden = false, requestFullscreen } = {}) {
  let now = 0, nextTimer = 1, identity = true;
  const timers = new Map(), calls = [];
  const classList = () => { const values = new Set(); return { toggle(name, on) { if (on === undefined ? !values.has(name) : on) values.add(name); else values.delete(name); }, contains: name => values.has(name) }; };
  const makeNode = () => ({ textContent: '', innerHTML: '', disabled: false, listeners: new Map(), classList: classList(),
    addEventListener(type, fn) { this.listeners.set(type, fn); }, setAttribute(name, value) { this[name] = value; }, focus() { this.focused = true; } });
  const nodes = new Map();
  const node = name => { if (!nodes.has(name)) nodes.set(name, makeNode()); return nodes.get(name); };
  const windows = [5, 15, 60].map(value => ({ ...makeNode(), dataset: { window: String(value) } }));
  const root = { ...makeNode(), querySelector: selector => node(selector.match(/data-dash="([^"]+)"/)[1]), querySelectorAll: () => windows };
  if (requestFullscreen) root.requestFullscreen = requestFullscreen;
  const events = new Map();
  const doc = { hidden, body: { classList: classList() }, fullscreenElement: null,
    addEventListener(type, fn) { events.set(type, fn); }, removeEventListener(type, fn) { if (events.get(type) === fn) events.delete(type); },
    exitFullscreen() { doc.fullscreenElement = null; return Promise.resolve(); } };
  const host = { isConnected: true, ownerDocument: doc, innerHTML: '', querySelector: () => root };
  const box = vm.createContext({ document: doc, AbortController, Intl, Date, Math, console,
    setTimeout(fn, ms) { const id = nextTimer++; timers.set(id, { at: now + ms, fn }); return id; }, clearTimeout(id) { timers.delete(id); } });
  vm.runInContext(source, box);
  const stop = box.mountDashboard(host, { isCurrent: () => identity, apiRequest: (...args) => { calls.push(args); return api(...args); } });
  return { box, root, host, doc, node, windows, stop, calls, timers, events,
    click: name => node(name).listeners.get('click')(),
    select: value => windows.find(button => button.dataset.window === String(value)).listeners.get('click')(),
    async visible(value) { doc.hidden = !value; events.get('visibilitychange')?.(); await flush(); },
    identity(value) { identity = value; },
    async tick(ms) { const target = now + ms; let guard = 0; while (true) { const ready = [...timers.entries()].filter(([, timer]) => timer.at <= target).sort((a, b) => a[1].at - b[1].at)[0]; if (!ready) break; assert.ok(++guard < 100); now = ready[1].at; timers.delete(ready[0]); ready[1].fn(); await flush(); } now = target; await flush(); }
  };
}

test('dashboard格式验证、无样本/溢出及断线图不把未知值绘制为零', () => {
  const env = fixture(async () => snapshot()), tools = env.box.EmbyDashboard;
  assert.equal(tools.validSnapshot(snapshot(), 15), true);
  for (const bad of [null, {}, snapshot(5), snapshot(15, { series: [] }), snapshot(15, { requests: { total: NaN } })]) assert.equal(tools.validSnapshot(bad, 15), false);
  assert.equal(tools.latency(null, false), '—'); assert.equal(tools.latency(null, true), '>60,000'); assert.equal(tools.count(undefined), '—');
  const points = [snapshot().series[0], { ...snapshot().series[1], observed: false, p95_ms: null }, snapshot().series[2]];
  const html = tools.chart(points, [{ key: 'p95_ms', tone: 'cyan' }], '毫秒', '<script>bad</script>');
  assert.match(html, /role="img"/); assert.equal((html.match(/M\d/g) || []).length, 2); assert.doesNotMatch(html, /<script>/); assert.match(html, /&lt;script&gt;/);
  assert.match(tools.chart(points.map(point => ({ ...point, p95_ms: null })), [{ key: 'p95_ms', tone: 'cyan' }], '毫秒', '无样本'), /暂无延迟样本/);
  env.stop();
});

test('首页初始不伪造零，成功局部更新数值/图/表而不重建外壳，保留指标口径', async () => {
  let resolve; const env = fixture(() => new Promise(done => { resolve = done; }));
  assert.equal(env.calls.length, 1); assert.match(env.host.innerHTML, /id="overview-dashboard"/); assert.match(env.host.innerHTML, /aria-live="off"/);
  assert.match(env.host.innerHTML, /data-dash="p95">—/); assert.equal(env.node('refresh').disabled, true);
  const shell = env.host.innerHTML; resolve(snapshot()); await flush();
  assert.equal(env.node('p95').textContent, '50'); assert.equal(env.node('heap').textContent, '2'); assert.equal(env.node('requests').textContent, '45');
  assert.match(env.node('traffic').innerHTML, /<svg/); assert.match(env.node('samples').innerHTML, /<tr>/); assert.equal(env.host.innerHTML, shell);
  assert.match(env.host.innerHTML, /JSON 控制面/); assert.match(env.host.innerHTML, /非进程 RSS/); assert.match(env.host.innerHTML, /桶上界/);
  assert.equal(env.root.classList.contains('is-stale'), false); env.stop();
});

test('5秒串行读取与手工刷新单飞；失败标stale保留快照并退避', async () => {
  let attempt = 0, finish;
  const env = fixture(async () => { attempt++; if (attempt === 2) return new Promise(resolve => { finish = resolve; }); if (attempt >= 3) throw Object.assign(Error('server error'), { status: 503 }); return snapshot(); });
  await flush(); await env.tick(5000); assert.equal(env.calls.length, 2);
  env.click('refresh'); env.click('refresh'); await flush(); assert.equal(env.calls.length, 2);
  finish(snapshot()); await flush(); await env.tick(5000); assert.equal(env.calls.length, 3);
  assert.equal(env.node('requests').textContent, '45'); assert.equal(env.root.classList.contains('is-stale'), true); assert.match(env.node('state').textContent, /过期/);
  await env.tick(9999); assert.equal(env.calls.length, 3); await env.tick(1); assert.equal(env.calls.length, 4); env.stop();
});

test('暂停与hidden中止请求且无自动轮询，手工单次更新保持暂停，恢复重新读取', async () => {
  let pending; const env = fixture(() => new Promise(resolve => { pending = resolve; }));
  const first = env.calls[0][1].signal; env.click('pause'); await flush(); assert.equal(first.aborted, true); assert.equal(env.timers.size, 0);
  pending(snapshot()); await flush(); assert.equal(env.node('p95').textContent, ''); await env.tick(60000); assert.equal(env.calls.length, 1);
  env.click('refresh'); await flush(); assert.equal(env.calls.length, 2); pending(snapshot()); await flush(); await env.tick(60000); assert.equal(env.calls.length, 2);
  assert.match(env.node('state').textContent, /已暂停/); env.click('pause'); await flush(); assert.equal(env.calls.length, 3);
  await env.visible(false); assert.equal(env.calls[2][1].signal.aborted, true); assert.equal(env.timers.size, 0);
  await env.visible(true); assert.equal(env.calls.length, 4); env.stop(); await flush(); assert.equal(env.timers.size, 0); assert.equal(env.events.size, 0);
});

test('软超时10秒中止并退避；窗口切换忽略旧回包，离页/换身份不能写旧数据', async () => {
  const resolvers = []; const env = fixture(() => new Promise(resolve => resolvers.push(resolve)));
  await env.tick(10000); assert.equal(env.calls[0][1].signal.aborted, true); assert.match(env.node('state').textContent, /超时/);
  resolvers[0](snapshot()); await flush(); assert.equal(env.node('p95').textContent, '');
  await env.tick(10000); assert.equal(env.calls.length, 2);
  env.select(5); await flush(); assert.equal(env.calls[1][1].signal.aborted, true); assert.match(env.calls[2][0], /window=5m$/);
  resolvers[1](snapshot()); resolvers[2](snapshot(5)); await flush(); assert.equal(env.node('requests').textContent, '15');
  await env.tick(5000); env.identity(false); resolvers[3](snapshot(5, { requests: { ...snapshot(5).requests, total: 999 } })); await flush(); assert.equal(env.node('requests').textContent, '15');
  env.stop(); assert.equal(env.events.size, 0); await flush(); assert.equal(env.timers.size, 0);
});

test('权限拒绝停止自动读取；畸形响应不是成功零快照', async () => {
  for (const status of [401, 403]) {
    const env = fixture(async () => { throw Object.assign(Error('denied'), { status }); }); await flush();
    assert.match(env.node('state').textContent, /读取受限/); await env.tick(60000); assert.equal(env.calls.length, 1); env.stop();
  }
  const env = fixture(async () => ({})); await flush(); assert.match(env.node('state').textContent, /尚无数据/); assert.equal(env.node('p95').textContent, ''); env.stop();
});

test('溢出/启动前/无请求零桶如实展示，任务与实例文本不可注入DOM', async () => {
  const data = snapshot(); data.requests.p95_ms = null; data.requests.p95_overflow = true;
  Object.assign(data.series[0], { observed: false, requests: null, errors_4xx: null, errors_5xx: null, p50_ms: null, p95_ms: null });
  Object.assign(data.series[1], { requests: 0, errors_4xx: 0, errors_5xx: 0, p50_ms: null, p95_ms: null });
  data.tasks.recent[0].kind = '<img src=x onerror=alert(1)>'; data.tasks.recent[0].status = '<svg onload=alert(1)>';
  const env = fixture(async () => data); await flush();
  assert.equal(env.node('p95').textContent, '>60,000'); assert.match(env.node('samples').innerHTML, /无观测<\/th><td>—<\/td>/);
  assert.match(env.node('samples').innerHTML, /<td>0<\/td><td>0<\/td><td>0<\/td><td>—/);
  assert.doesNotMatch(env.node('tasks').innerHTML, /<img|<svg/); assert.match(env.node('tasks').innerHTML, /&lt;img/); env.stop();
});

test('大屏显式进入，Fullscreen拒绝有CSS fallback；Esc/销毁清理，迟到授权不复活', async () => {
  const env = fixture(async () => snapshot(), { requestFullscreen: async () => { throw Error('not allowed'); } }); await flush();
  assert.equal(env.doc.body.classList.contains('dashboard-wide'), false); await env.click('wide');
  assert.equal(env.doc.body.classList.contains('dashboard-wide'), true); assert.match(env.node('feedback').textContent, /未允许全屏/);
  env.events.get('keydown')({ key: 'Escape' }); assert.equal(env.doc.body.classList.contains('dashboard-wide'), false);
  await env.click('wide'); env.stop(); assert.equal(env.doc.body.classList.contains('dashboard-wide'), false); assert.equal(env.events.size, 0);
  let granted; const late = fixture(async () => snapshot(), { requestFullscreen: () => new Promise(resolve => { granted = resolve; }) }); await flush();
  const pending = late.click('wide'); late.stop(); late.doc.fullscreenElement = late.root; granted(); await pending;
  assert.equal(late.doc.fullscreenElement, null); assert.equal(late.doc.body.classList.contains('dashboard-wide'), false);
});

test('媒体摘要局部失败不阻断HTTP/runtime，保留缓存标过期且跨实例清空', async () => {
  const unavailable = { available: false, stale: true, error: 'MEDIA_UNAVAILABLE', updated_at: '', total: null, success: null, manual: null, pending: null, libraries: null };
  let attempt = 0;
  const env = fixture(async () => {
    attempt++;
    const data = snapshot(15, { media: attempt === 2 ? snapshot().media : unavailable });
    data.requests.total = attempt * 100;
    if (attempt >= 4) data.instance_id = 'new-instance';
    return data;
  });
  await flush(); assert.equal(env.node('requests').textContent, '100'); assert.match(env.node('media').innerHTML, /暂无可用/); assert.doesNotMatch(env.node('media').innerHTML, /<strong>0/);
  await env.tick(5000); const cached = env.node('media').innerHTML; assert.match(cached, /<strong>20/); assert.doesNotMatch(env.node('media-updated').textContent, /过期/);
  await env.tick(5000); assert.equal(env.node('requests').textContent, '300'); assert.equal(env.node('media').innerHTML, cached); assert.match(env.node('media-updated').textContent, /已过期/); assert.equal(env.root.classList.contains('is-stale'), false);
  await env.tick(5000); assert.match(env.node('media').innerHTML, /暂无可用/); assert.equal(env.node('requests').textContent, '400'); env.stop();
});

test('服务端available=true/stale=true保留旧媒体时间，恢复成功才去掉过期提示', async () => {
  let stale = true;
  const env = fixture(async () => snapshot(15, { media: { ...snapshot().media, available: true, stale, error_code: stale ? 'SUMMARY_UNAVAILABLE' : '' } }));
  await flush(); assert.match(env.node('media').innerHTML, /<strong>20/); assert.match(env.node('media-updated').textContent, /已过期/); assert.equal(env.root.classList.contains('is-stale'), false);
  const oldTime = env.node('media-updated').textContent.split('摘要 ')[1];
  stale = false; await env.tick(5000); assert.doesNotMatch(env.node('media-updated').textContent, /已过期/); assert.equal(env.node('media-updated').textContent.split('摘要 ')[1], oldTime); env.stop();
});

test('任务摘要完整中文映射及头像兼容别名，不把unknown伪装成功', async () => {
  const entries = [['scrape_avatars', '补演员头像'], ['avatars', '补演员头像'], ['analyze', '媒体分析'], ['webhook', '通知投递'], ['edit', '元数据编辑'], ['other', '其他任务']];
  const data = snapshot();
  data.tasks.recent = entries.map(([kind], index) => ({ id: 'task-' + index, kind, status: 'unknown', started_at: data.started_at, ended_at: '' }));
  const env = fixture(async () => data); await flush();
  for (const [, label] of entries) assert.ok(env.node('tasks').innerHTML.includes(label));
  assert.equal((env.node('tasks').innerHTML.match(/状态未知/g) || []).length, entries.length);
  assert.doesNotMatch(env.node('tasks').innerHTML, /已完成/); env.stop();
});

test('首页最小生命周期整合保留图片缓存与其它功能；CSS无CDN且有小屏与减动态', () => {
  assert.match(app, /dashboardStop = mountDashboard\(content/); assert.match(app, /typeof stopDashboardPolling === 'function'/);
  assert.match(app, /mountImageCacheSettings/); assert.match(app, /NFO 不可读/);
  const overview = app.slice(app.indexOf('async function pageOverview()'), app.indexOf('async function pageLibraries()'));
  assert.doesNotMatch(overview, /reindex|method: 'POST'/);
  assert.doesNotMatch(css, /@import|https?:\/\/|overflow-x:\s*hidden/); assert.match(css, /prefers-reduced-motion/); assert.match(css, /max-width: 650px/); assert.match(css, /min-height: 44px/);
});
