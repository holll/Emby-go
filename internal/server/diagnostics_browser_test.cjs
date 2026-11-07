'use strict';
// N-05 真实浏览器回归：诊断页「备份」面板能显示固定目录与最近结果，
// 点击「立即备份」后由服务端创建快照，面板回到可用状态并显示该次结果。
const test = require('node:test');
const assert = require('node:assert/strict');
const { browser } = require('./web_browser_helper.cjs');

async function waitFor(b, expression, label) {
  await b.evaluate(`(async () => {
    const deadline = performance.now() + 30000;
    while (!(${expression})) {
      if (performance.now() > deadline) throw Error('Timed out waiting for ' + ${JSON.stringify(label || expression)} + '\\n' + document.body.innerText);
      await new Promise(resolve => setTimeout(resolve, 50));
    }
  })()`);
}

test('admin runs a backup from the diagnostics page and sees the result', { timeout: 120000 }, async t => {
  const target = process.env.DIAG_BROWSER_TARGET;
  const token = process.env.DIAG_BROWSER_TOKEN;
  assert.ok(target && token, 'Go fixture target/token required');
  const b = await browser(t);
  await b.call('Emulation.setDeviceMetricsOverride', { width: 1440, height: 900, deviceScaleFactor: 1, mobile: false });
  await b.call('Page.addScriptToEvaluateOnNewDocument', { source: `
    window.__browserErrors = [];
    addEventListener('error', e => { if (e.message) __browserErrors.push(e.message); });
    addEventListener('unhandledrejection', e => __browserErrors.push(String(e.reason)));
    try { localStorage.setItem('emby_token', ${JSON.stringify(token)}); } catch {}
  ` });
  await b.navigate(target + '/admin#diagnostics');
  await waitFor(b, 'document.querySelector("#diag-backup-run") && !document.querySelector("#diag-backup-run").disabled', '备份面板加载完成');
  const body = await b.evaluate('document.body.innerText');
  assert.match(body, /备份/);
  assert.match(body, /最近一次/);

  await b.evaluate(`(() => {
    const btn = document.querySelector('#diag-backup-run');
    if (!btn || btn.disabled) throw Error('备份按钮不可用');
    btn.click();
  })()`);
  await waitFor(b, 'document.querySelector("#diag-backup-run") && !document.querySelector("#diag-backup-run").disabled', '备份任务完成');
  const after = await b.evaluate('document.body.innerText');
  assert.match(after, /NFO \d+/);
  assert.match(after, /已成功|成功/);
  const errors = await b.evaluate('window.__browserErrors');
  assert.deepEqual(errors, []);
});
