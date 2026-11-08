'use strict';
// 单独任务中心页：管理端从导航进入 #tasks，表格列出任务摘要与结果列，
// 类型/状态筛选与刷新按钮可用，且页面无浏览器错误。Go 夹具提供 App 与管理员 token。
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

test('tasks page lists records with filters and a result column', { timeout: 90000 }, async t => {
  const target = process.env.TASKS_BROWSER_TARGET;
  const token = process.env.TASKS_BROWSER_TOKEN;
  assert.ok(target && token, 'Go fixture target/token required');
  const b = await browser(t);
  await b.call('Emulation.setDeviceMetricsOverride', { width: 1440, height: 900, deviceScaleFactor: 1, mobile: false });
  await b.call('Page.addScriptToEvaluateOnNewDocument', { source: `
    window.__browserErrors = [];
    addEventListener('error', e => { if (e.message) __browserErrors.push(e.message); });
    addEventListener('unhandledrejection', e => __browserErrors.push(String(e.reason)));
    try { localStorage.setItem('emby_token', ${JSON.stringify(token)}); } catch {}
  ` });
  await b.navigate(target + '/admin#tasks');
  await waitFor(b, 'document.querySelectorAll("#task-table tbody tr").length > 0', '任务表格出现记录');
  assert.equal(await b.evaluate('!!document.querySelector(".nav-item[data-page=tasks]")'), true);
  const headers = await b.evaluate('[...document.querySelectorAll("#task-table thead th")].map(th => th.textContent)');
  assert.deepEqual(headers, ['ID', '类型', '状态', '开始时间', '结束时间', '结果', '错误']);
  assert.equal(await b.evaluate('!!document.querySelector("#task-type") && !!document.querySelector("#task-status")'), true);
  assert.equal(await b.evaluate('!!document.querySelector("#task-refresh")'), true);
  // 刷新后仍保留表格内容；运行中的任务会自行轮询，这里只验证请求可用。
  await b.evaluate('document.querySelector("#task-refresh").click()');
  await waitFor(b, 'document.querySelectorAll("#task-table tbody tr").length > 0', '刷新后仍有记录');
  assert.deepEqual(await b.evaluate('window.__browserErrors'), []);
});
