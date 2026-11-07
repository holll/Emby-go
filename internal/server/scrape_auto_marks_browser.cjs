'use strict';
// Run with node directly: the Go fixture owns the App and the stdin handshake.
const test = require('node:test');
const assert = require('node:assert/strict');
const { browser } = require('./web_browser_helper.cjs');

async function waitFor(b, expression) {
  await b.evaluate(`(async () => {
    const deadline = performance.now() + 10000;
    while (!(${expression})) {
      if (performance.now() > deadline) throw Error('Timed out waiting for ' + ${JSON.stringify(expression)} + '\\n' + document.body.innerText);
      await new Promise(resolve => setTimeout(resolve, 20));
    }
  })()`);
}

async function click(b, selector) {
  const point = await b.evaluate(`(() => {
    const e = document.querySelector(${JSON.stringify(selector)});
    if (!e || e.disabled) throw Error('Missing/disabled control: ' + ${JSON.stringify(selector)});
    e.scrollIntoView({block:'center', inline:'nearest', behavior:'instant'});
    const r = e.getBoundingClientRect(), x = r.x + r.width / 2, y = r.y + r.height / 2;
    if (!r.width || !r.height || !e.contains(document.elementFromPoint(x, y))) throw Error('Control not hit-testable: ' + ${JSON.stringify(selector)});
    return {x, y};
  })()`);
  await b.call('Input.dispatchMouseEvent', { type: 'mousePressed', button: 'left', clickCount: 1, ...point });
  await b.call('Input.dispatchMouseEvent', { type: 'mouseReleased', button: 'left', clickCount: 1, ...point });
}

test('production App keeps auto-marks partial success visible until keyboard completion', { timeout: 60000 }, async t => {
  const target = process.env.SCRAPE_BROWSER_TARGET;
  const mode = process.env.SCRAPE_BROWSER_MODE;
  const width = Number(process.env.SCRAPE_BROWSER_WIDTH);
  const title = 'Browser saved <img src=x onerror="window.__autoMarksInjected=1"> &';
  assert.ok(target, 'Go fixture target required');
  assert.ok(['ownership', 'all'].includes(mode));
  assert.ok([390, 1440].includes(width));
  const b = await browser(t);
  await b.call('Emulation.setDeviceMetricsOverride', { width, height: 900, deviceScaleFactor: 1, mobile: width === 390 });
  await b.call('Page.addScriptToEvaluateOnNewDocument', { source: `
    window.__browserErrors = [];
    addEventListener('error', e => { if (e.message) __browserErrors.push(e.message); });
    addEventListener('unhandledrejection', e => __browserErrors.push(String(e.reason)));
  ` });
  await b.navigate(target + '/');
  await waitFor(b, '!!document.querySelector("#auth-submit")');
  await click(b, '#auth-user');
  await b.call('Input.insertText', { text: 'admin' });
  await click(b, '#auth-pw');
  await b.call('Input.insertText', { text: 'password-1234' });
  await click(b, '#auth-submit');
  const loginDeadline = performance.now() + 10000;
  while (new URL((await b.call('Page.getFrameTree')).frameTree.frame.url).pathname !== '/admin') {
    assert.ok(performance.now() < loginDeadline, 'real login did not navigate to admin');
    await new Promise(resolve => setTimeout(resolve, 20));
  }
  await b.navigate(target + '/admin#items');
  assert.equal(await b.evaluate('!!localStorage.getItem("emby_token")'), true);
  await waitFor(b, '!!document.querySelector(".wall-card")');
  await click(b, '.wall-card');
  await waitFor(b, '!!document.querySelector("#detail-scrape")');
  await click(b, '.detail-maintenance > summary');
  await click(b, '#detail-scrape');
  await waitFor(b, '!!document.querySelector("#scrape-confirm") && !document.querySelector("#scrape-confirm").disabled');
  // Keep a read-only remote reference to the existing wall card so the saved
  // result is checked after a genuine wall replacement, not against stale DOM.
  const oldCard = await b.call('Runtime.evaluate', { expression: 'document.querySelector(".wall-card")', returnByValue: false });
  assert.ok(oldCard.result.objectId);
  if (!(await b.evaluate('document.querySelector("#scrape-force").checked'))) await click(b, '#scrape-force');
  await click(b, '#scrape-confirm');
  await waitFor(b, '!!document.querySelector("#scrape-done") && document.querySelector("#detail-overlay").hidden');
  const refreshDeadline = performance.now() + 10000;
  while (!(await b.call('Runtime.callFunctionOn', {
    objectId: oldCard.result.objectId,
    functionDeclaration: 'function () { return !this.isConnected; }', returnByValue: true
  })).result.value) {
    assert.ok(performance.now() < refreshDeadline, 'actual wall card was not replaced after save');
    await new Promise(resolve => setTimeout(resolve, 20));
  }
  await b.call('Runtime.releaseObject', { objectId: oldCard.result.objectId });
  await waitFor(b, '!!document.querySelector(".wall-card")');
  // Go passively records the actual response status/body/count and checks them
  // together with SQLite/NFO at the milestone. No extra CDP session or request.

  const saved = await b.evaluate(`(() => {
    const body = document.querySelector('#scrape-body'), status = body.querySelector('[role="status"]');
    return {
      title: body.querySelector('#scrape-result-title')?.textContent,
      text: body.textContent, html: body.innerHTML,
      live: status?.getAttribute('aria-live'), atomic: status?.getAttribute('aria-atomic'),
      labelledBy: status?.getAttribute('aria-labelledby'),
      headings: Array.from(body.querySelectorAll('h4')).map(e => e.textContent),
      focus: document.activeElement?.id, overlayHidden: document.querySelector('#scrape-overlay').hidden,
      staleControls: !!body.querySelector('#scrape-confirm, #scrape-manual-search, #scrape-query, #scrape-force, .scrape-candidate'),
      injectedElement: !!body.querySelector('img[onerror], script'),
      injected: window.__autoMarksInjected,
      errors: window.__browserErrors,
      geometry: (() => {
        const modal = document.querySelector('.scrape-modal').getBoundingClientRect();
        return { width: innerWidth, scroll: document.documentElement.scrollWidth, left: modal.left, right: modal.right,
          bodyWidth: body.clientWidth, bodyScroll: body.scrollWidth, statusWidth: status.clientWidth, statusScroll: status.scrollWidth };
      })()
    };
  })()`);
  assert.match(saved.title, /NFO 已保存/);
  assert.ok(saved.text.includes(title), 'untrusted title rendered as literal text');
  assert.match(saved.text, /标记归属需人工核对/);
  assert.match(saved.text, /不要直接重新刮削/);
  assert.match(saved.text, /重扫不能恢复归属记录/);
  assert.doesNotMatch(saved.text, /PRIVATE_AUTO_MARKS_(?:SQL|INDEX)_CANARY/);
  assert.ok(saved.html.includes('&lt;img'), 'untrusted title is HTML escaped');
  assert.equal(saved.live, 'polite');
  assert.equal(saved.atomic, 'true');
  assert.equal(saved.labelledBy, 'scrape-result-title');
  assert.equal(saved.focus, 'scrape-done');
  assert.equal(saved.overlayHidden, false, 'partial success remains open after page refresh');
  assert.equal(saved.staleControls, false);
  assert.equal(saved.injectedElement, false);
  assert.equal(saved.injected, undefined);
  assert.deepEqual(saved.errors, []);
  assert.deepEqual(saved.headings, mode === 'all' ? ['标记归属需人工核对', '索引需重扫', '图片警告'] : ['标记归属需人工核对']);
  const g = saved.geometry;
  assert.ok(g.width <= width + 1 && g.scroll <= width + 1 && g.left >= 0 && g.right <= width + 1 &&
    g.bodyScroll <= g.bodyWidth + 1 && g.statusScroll <= g.statusWidth + 1, JSON.stringify(g));

  const acknowledged = new Promise(resolve => process.stdin.once('data', resolve));
  process.stdin.resume();
  console.log('SCRAPE_AUTO_MARKS_SAVED');
  assert.equal(String(await acknowledged).trim(), 'continue');
  process.stdin.pause();
  assert.equal(await b.evaluate('document.activeElement?.id'), 'scrape-done');
  assert.equal(await b.evaluate('document.querySelector("#scrape-overlay").hidden'), false);
  await b.call('Input.dispatchKeyEvent', { type: 'keyDown', key: 'Enter', code: 'Enter', windowsVirtualKeyCode: 13, text: '\r', unmodifiedText: '\r' });
  await b.call('Input.dispatchKeyEvent', { type: 'keyUp', key: 'Enter', code: 'Enter', windowsVirtualKeyCode: 13 });
  await waitFor(b, 'document.querySelector("#scrape-overlay").hidden');
  assert.deepEqual(await b.evaluate('window.__browserErrors'), []);
  assert.equal(await b.evaluate('window.__autoMarksInjected'), undefined);
  console.log(JSON.stringify({ mode, width, realLogin: true, productionUI: true, persistentSavedResult: true, keyboardDone: true, geometry: saved.geometry }));
});
