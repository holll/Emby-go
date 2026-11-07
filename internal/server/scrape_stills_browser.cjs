'use strict';
// Deliberately not web*_test.cjs: this requires the Go-owned live App fixture.
// Run in-process (node script, not node --test) so Go's stdin ack reaches us.
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

// Real CDP pointer input also verifies controls are hit-testable in the complete
// modal/drawer stack, rather than calling production functions or .click().
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

async function fill(b, selector, value) {
  await click(b, selector);
  await b.call('Input.insertText', { text: value });
}

test('production App scrape stills preview/cancel/ordered save', { timeout: 60000 }, async t => {
  const target = process.env.SCRAPE_BROWSER_TARGET;
  const mode = process.env.SCRAPE_BROWSER_MODE;
  const width = Number(process.env.SCRAPE_BROWSER_WIDTH);
  assert.ok(target, 'Go fixture target required');
  assert.ok(['cancel', 'save'].includes(mode));
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
  await fill(b, '#auth-user', 'admin');
  await fill(b, '#auth-pw', 'password-1234');
  await click(b, '#auth-submit');
  // Login replaces the document. Observe navigation outside its JS context;
  // an awaited Runtime.evaluate promise would be destroyed on that real redirect.
  const deadline = performance.now() + 10000;
  while (new URL((await b.call('Page.getFrameTree')).frameTree.frame.url).pathname !== '/admin') {
    assert.ok(performance.now() < deadline, 'real login did not navigate to admin');
    await new Promise(resolve => setTimeout(resolve, 20));
  }
  await b.navigate(target + '/admin#items');
  assert.equal(await b.evaluate('!!localStorage.getItem("emby_token")'), true, 'real login stored the real issued token');
  await waitFor(b, '!!document.querySelector(".wall-card")');
  await click(b, '.wall-card');
  await waitFor(b, '!!document.querySelector("#detail-scrape")');
  await click(b, '.detail-maintenance > summary');
  await click(b, '#detail-scrape');
  await waitFor(b, '!!document.querySelector("#scrape-still-1")');
  assert.equal(await b.evaluate('document.querySelectorAll(".scrape-still-check:checked").length'), 0, 'automatic search/inspect defaults to none');

  // Exercise the actual manual search form too (not a direct API request).
  await fill(b, '#scrape-query', 'ABF-018');
  await click(b, '#scrape-manual-search button[type="submit"]');
  await waitFor(b, '!!document.querySelector("#scrape-still-1")');
  assert.equal(await b.evaluate('document.querySelectorAll(".scrape-still-check").length'), 2);
  assert.equal(await b.evaluate('document.querySelectorAll(".scrape-still-check:checked").length'), 0, 'manual search/inspect defaults to none');
  for (const index of [1, 0]) {
    await click(b, '#scrape-still-' + index);
    await waitFor(b, `document.querySelector('#scrape-still-${index}').checked`);
    await waitFor(b, `document.querySelector('#scrape-still-${index}').closest('label').querySelector('img').naturalWidth > 0`);
  }
  const preview = await b.evaluate(`Array.from(document.querySelectorAll('.scrape-still')).map(e => ({
    src: e.querySelector('img').getAttribute('src'), width: e.querySelector('img').naturalWidth,
    order: e.querySelector('strong').textContent
  }))`);
  assert.deepEqual(preview.map(p => p.width), [40, 80], 'real authenticated preview proxy decoded both upstream JPEGs');
  assert.match(preview[1].order, /fanart1\.jpg/);
  assert.match(preview[0].order, /fanart2\.jpg/);
  for (const p of preview) {
    const url = new URL(p.src, target);
    assert.equal(url.pathname, '/api/admin/scrape/image');
    assert.equal(url.searchParams.get('kind'), 'still');
    assert.match(url.searchParams.get('version'), /^[a-f0-9]{64}$/);
  }
  const geometry = await b.evaluate(`(() => {
    const modal = document.querySelector('.scrape-modal').getBoundingClientRect();
    return { inner: innerWidth, scroll: document.documentElement.scrollWidth, left: modal.left, right: modal.right };
  })()`);
  assert.ok(geometry.inner <= width + 1 && geometry.scroll <= width + 1 && geometry.left >= 0 && geometry.right <= width + 1, JSON.stringify(geometry));

  // Go snapshots real movie DB + every media path/byte while these selections
  // remain live. No fixture endpoint or production control hook is introduced.
  const acknowledged = new Promise(resolve => process.stdin.once('data', resolve));
  process.stdin.resume();
  console.log('SCRAPE_STILLS_PRECONFIRM');
  assert.equal(String(await acknowledged).trim(), 'continue');
  process.stdin.pause();
  if (mode === 'cancel') {
    await click(b, '#scrape-cancel-btn');
    await waitFor(b, 'document.querySelector("#scrape-overlay").hidden');
  } else {
    await click(b, '#scrape-confirm');
    await waitFor(b, 'document.querySelector("#scrape-overlay").hidden');
    await waitFor(b, 'document.querySelector("#detail-overlay").hidden && !!document.querySelector(".wall-card")');
    assert.match(await b.evaluate('document.querySelector("#toasts").textContent'), /已写入/);
  }
  assert.deepEqual(await b.evaluate('__browserErrors'), [], 'no page errors or unhandled rejections');
  console.log(JSON.stringify({ mode, width, realLogin: true, productionUI: true, previewWidths: preview.map(p => p.width), selected: [1, 0], geometry }));
});