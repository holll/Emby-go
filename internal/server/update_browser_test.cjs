'use strict';
// 真实浏览器里的 U-01 入口：管理员页面加载后自动检查更新，左下角出现徽标，
// 点击后弹出面板；升级失败（下载 404/500）必须在面板内显示原因且可重试。
// 第二个用例把 apply/status/healthz/version 的 fetch 换成脚本桩，覆盖「服务重启
// 恢复后，实际版本与目标一致才显示成功」这一段前端逻辑（真实重启由 systemd 回归覆盖）。
// Go 夹具提供 App、假 Release 与管理员 token，Node 侧不接触 GitHub。
const test = require('node:test');
const assert = require('node:assert/strict');
const { browser } = require('./web_browser_helper.cjs');

async function waitFor(b, expression, label) {
  await b.evaluate(`(async () => {
    const deadline = performance.now() + 15000;
    while (!(${expression})) {
      if (performance.now() > deadline) throw Error('Timed out waiting for ' + ${JSON.stringify(label || expression)} + '\\n' + document.body.innerText);
      await new Promise(resolve => setTimeout(resolve, 25));
    }
  })()`);
}

// waitForRedirect 用于「打开 /admin 后立刻被 replace 到 /」的场景：导航会销毁旧执行
// 上下文，Runtime.evaluate 可能以 "Inspected target navigated or closed" 失败；重试到
// 新页面稳定为止。
async function waitForRedirect(b, expression, label) {
  const deadline = Date.now() + 15000;
  while (Date.now() < deadline) {
    try {
      if (await b.evaluate(expression)) return;
    } catch { /* navigation in flight */ }
    await new Promise(resolve => setTimeout(resolve, 100));
  }
  throw new Error('Timed out waiting for ' + label + '\n' + await b.evaluate('document.body.innerText').catch(() => ''));
}

async function click(b, selector) {
  const point = await b.evaluate(`(() => {
    const e = document.querySelector(${JSON.stringify(selector)});
    if (!e || e.disabled || e.hidden) throw Error('Missing/disabled control: ' + ${JSON.stringify(selector)});
    e.scrollIntoView({block:'center', inline:'nearest', behavior:'instant'});
    const r = e.getBoundingClientRect(), x = r.x + r.width / 2, y = r.y + r.height / 2;
    if (!r.width || !r.height || !e.contains(document.elementFromPoint(x, y))) throw Error('Control not hit-testable: ' + ${JSON.stringify(selector)});
    return {x, y};
  })()`);
  await b.call('Input.dispatchMouseEvent', { type: 'mousePressed', button: 'left', clickCount: 1, ...point });
  await b.call('Input.dispatchMouseEvent', { type: 'mouseReleased', button: 'left', clickCount: 1, ...point });
}

async function openAsAdmin(t, tag) {
  const target = process.env.UPDATE_BROWSER_TARGET;
  const token = process.env.UPDATE_BROWSER_TOKEN;
  assert.ok(target && token && tag, 'Go fixture target/token/tag required');
  const b = await browser(t);
  await b.call('Emulation.setDeviceMetricsOverride', { width: 1440, height: 900, deviceScaleFactor: 1, mobile: false });
  await b.call('Page.addScriptToEvaluateOnNewDocument', { source: `
    window.__browserErrors = [];
    addEventListener('error', e => { if (e.message) __browserErrors.push(e.message); });
    addEventListener('unhandledrejection', e => __browserErrors.push(String(e.reason)));
    try { localStorage.setItem('emby_token', ${JSON.stringify(token)}); } catch {}
  ` });
  await b.navigate(target + '/admin');
  await waitFor(b, 'document.querySelector("#version-btn") && !document.querySelector("#version-btn").disabled', '版本号按钮可用（管理员已登录）');
  await waitFor(b, 'document.querySelector("#version-badge") && !document.querySelector("#version-badge").hidden', '自动检查后出现新版本徽标');
  assert.equal(await b.evaluate('document.querySelector("#version-badge").title'), '有新版本 ' + tag);
  await click(b, '#version-btn');
  await waitFor(b, 'document.querySelector("#update-overlay") && document.querySelector("#update-overlay").open', '更新面板打开');
  return b;
}

test('admin sees new-version badge, opens upgrade dialog, and reads a failed install in place', { timeout: 90000 }, async t => {
  const tag = process.env.UPDATE_BROWSER_TAG;
  const b = await openAsAdmin(t, tag);
  assert.equal(await b.evaluate('document.querySelector("#update-latest").textContent'), tag);
  assert.match(await b.evaluate('document.querySelector("#update-summary").textContent'), /发现新版本/);
  assert.equal(await b.evaluate('document.querySelector("#update-apply").hidden'), false);

  await click(b, '#update-apply');
  await waitFor(b, 'document.querySelector("#update-status").classList.contains("is-error")', '下载失败在面板内显示');
  const status = await b.evaluate('document.querySelector("#update-status").textContent');
  assert.match(status, /升级失败/);
  assert.equal(await b.evaluate('document.querySelector("#update-apply").hidden'), false);
  assert.equal(await b.evaluate('document.querySelector("#update-reload").hidden'), true);

  await b.call('Input.dispatchKeyEvent', { type: 'keyDown', key: 'Escape', code: 'Escape', windowsVirtualKeyCode: 27 });
  await b.call('Input.dispatchKeyEvent', { type: 'keyUp', key: 'Escape', code: 'Escape', windowsVirtualKeyCode: 27 });
  await waitFor(b, '!document.querySelector("#update-overlay").open', 'Escape 关闭更新面板');
  assert.deepEqual(await b.evaluate('window.__browserErrors'), []);
});

test('upgrade success is shown only after the restarted service reports the target version', { timeout: 90000 }, async t => {
  const tag = process.env.UPDATE_BROWSER_TAG;
  const b = await openAsAdmin(t, tag);
  const installed = tag + '-deadbee';
  await b.evaluate(`(() => {
    const original = window.fetch.bind(window);
    const json = (status, body) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
    window.__upgradeStub = { applied: 0, status: 0 };
    window.fetch = async (input, init) => {
      const raw = typeof input === 'string' ? input : input.url;
      const path = new URL(raw, location.origin).pathname;
      if (path === '/api/admin/update/apply') { window.__upgradeStub.applied += 1; return json(202, { version: ${JSON.stringify(tag)} }); }
      if (path === '/api/admin/update/status') {
        window.__upgradeStub.status += 1;
        if (window.__upgradeStub.status === 1) return json(200, { phase: 'downloading', target: ${JSON.stringify(tag)}, downloaded_bytes: 5242880, total_bytes: 20971520 });
        if (window.__upgradeStub.status === 2) return json(200, { phase: 'downloading', target: ${JSON.stringify(tag)}, downloaded_bytes: 15728640, total_bytes: 20971520 });
        return json(200, { phase: 'restarting', target: ${JSON.stringify(tag)} });
      }
      if (path === '/healthz') return new Response('ok', { status: 200 });
      if (path === '/api/version') return json(200, { product: 'Emby-go', version: ${JSON.stringify(installed)} });
      return original(input, init);
    };
  })()`);

  await click(b, '#update-apply');
  await waitFor(b, 'document.querySelector("#update-progress") && !document.querySelector("#update-progress").hidden && /已下载 75%/.test(document.querySelector("#update-progress-label").textContent)', '下载进度推进到 75%');
  assert.equal(await b.evaluate('document.querySelector("#update-progress-fill").style.width'), '75%');
  assert.match(await b.evaluate('document.querySelector("#update-progress-label").textContent'), /已下载 75% · 15 MB \/ 20 MB/);
  await waitFor(b, 'document.querySelector("#update-status").classList.contains("is-ok")', '重启后核对版本并显示成功');
  assert.equal(await b.evaluate('document.querySelector("#update-progress").hidden'), true);
  assert.equal(await b.evaluate('window.__upgradeStub.applied'), 1);
  assert.match(await b.evaluate('document.querySelector("#update-status").textContent'), /已升级到/);
  assert.equal(await b.evaluate('document.querySelector("#version-text").textContent'), installed);
  assert.equal(await b.evaluate('document.querySelector("#version-badge").hidden'), true);
  assert.equal(await b.evaluate('document.querySelector("#update-apply").hidden'), true);
  assert.equal(await b.evaluate('document.querySelector("#update-reload").hidden'), false);
  assert.equal(await b.evaluate('document.querySelector("#update-check").disabled'), false);
  assert.deepEqual(await b.evaluate('window.__browserErrors'), []);
});

test('non-admin is redirected to login and blocked from the admin console', { timeout: 90000 }, async t => {
  const target = process.env.UPDATE_BROWSER_TARGET;
  const viewer = process.env.UPDATE_BROWSER_VIEWER_TOKEN;
  assert.ok(target && viewer, 'Go fixture viewer token required');
  const b = await browser(t);
  await b.call('Emulation.setDeviceMetricsOverride', { width: 1440, height: 900, deviceScaleFactor: 1, mobile: false });
  await b.call('Page.addScriptToEvaluateOnNewDocument', { source: `
    window.__browserErrors = [];
    window.__updateCalls = 0;
    addEventListener('error', e => { if (e.message) __browserErrors.push(e.message); });
    addEventListener('unhandledrejection', e => __browserErrors.push(String(e.reason)));
    const originalFetch = window.fetch.bind(window);
    window.fetch = (input, init) => {
      const raw = typeof input === 'string' ? input : input.url;
      const path = new URL(raw, location.origin).pathname;
      if (path.startsWith('/api/admin/update')) window.__updateCalls += 1;
      return originalFetch(input, init);
    };
    try { localStorage.setItem('emby_token', ${JSON.stringify(viewer)}); } catch {}
  ` });
  // /admin 会立刻 replace 到 /，等待 loadEventFired 的 navigate() 可能在跳转中报
  // “Inspected target navigated or closed”；这里只发起导航，由 waitForRedirect 观察结果。
  await b.call('Page.navigate', { url: target + '/admin' });
  await waitForRedirect(b, 'location.pathname === "/" && document.querySelector(".auth-error") && /仅对管理员开放/.test(document.querySelector(".auth-error").textContent)', '非管理员回到登录页并提示仅管理员开放');
  assert.equal(await b.evaluate('!!document.querySelector("#version-btn")'), false);
  assert.equal(await b.evaluate('localStorage.getItem("emby_token")'), null);
  await b.evaluate('new Promise(resolve => setTimeout(resolve, 500))');
  assert.equal(await b.evaluate('window.__updateCalls'), 0);
  assert.deepEqual(await b.evaluate('window.__browserErrors'), []);
});
