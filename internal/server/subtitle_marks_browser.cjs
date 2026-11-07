'use strict';
// The Go test owns the real App, SQLite/NFO snapshots and stdin milestones.
const test = require('node:test');
const assert = require('node:assert/strict');
const { browser } = require('./web_browser_helper.cjs');

async function waitFor(b, expression) {
  await b.evaluate(`(async () => {
    const deadline = performance.now() + 10000;
    while (!(${expression})) {
      if (performance.now() > deadline) throw Error('Timed out: ' + ${JSON.stringify(expression)} + '\\n' + document.body.innerText);
      await new Promise(resolve => setTimeout(resolve, 20));
    }
  })()`);
}

async function click(b, selector) {
  const point = await b.evaluate(`(() => {
    const e = document.querySelector(${JSON.stringify(selector)});
    if (!e || e.disabled) throw Error('Missing/disabled: ' + ${JSON.stringify(selector)});
    e.scrollIntoView({block:'center', inline:'nearest', behavior:'instant'});
    const r = e.getBoundingClientRect(), x = r.x + r.width / 2, y = r.y + r.height / 2;
    if (!r.width || !r.height || !e.contains(document.elementFromPoint(x, y))) throw Error('Not hit-testable: ' + ${JSON.stringify(selector)});
    return {x, y};
  })()`);
  await b.call('Input.dispatchMouseEvent', { type: 'mousePressed', button: 'left', clickCount: 1, ...point });
  await b.call('Input.dispatchMouseEvent', { type: 'mouseReleased', button: 'left', clickCount: 1, ...point });
}

async function milestone(name) {
  const acknowledged = new Promise(resolve => process.stdin.once('data', resolve));
  process.stdin.resume();
  console.log(name);
  assert.equal(String(await acknowledged).trim(), 'continue');
  process.stdin.pause();
}

async function geometry(b) {
  const g = await b.evaluate(`(() => {
    const panel = document.querySelector('#detail-subtitle-marks'), r = panel.getBoundingClientRect();
    return { width: innerWidth, pageScroll: document.documentElement.scrollWidth,
      left: r.left, right: r.right, client: panel.clientWidth, scroll: panel.scrollWidth };
  })()`);
  assert.ok(g.pageScroll <= g.width + 1 && g.left >= 0 && g.right <= g.width + 1 && g.scroll <= g.client + 1, JSON.stringify(g));
  return g;
}

test('real local subtitle organizer previews without writes and explicitly commits through App', { timeout: 60000 }, async t => {
  const target = process.env.SUBTITLE_BROWSER_TARGET;
  const mode = process.env.SUBTITLE_BROWSER_MODE;
  const width = Number(process.env.SUBTITLE_BROWSER_WIDTH);
  assert.ok(target, 'Go App fixture required');
  assert.ok(['success', 'ownership'].includes(mode));
  assert.ok([375, 1280].includes(width));
  const b = await browser(t);
  await b.call('Emulation.setDeviceMetricsOverride', { width, height: 900, deviceScaleFactor: 1, mobile: width === 375 });
  await b.call('Page.addScriptToEvaluateOnNewDocument', { source: `
    window.__subtitleErrors = [];
    addEventListener('error', e => { if (e.message) __subtitleErrors.push(e.message); });
    addEventListener('unhandledrejection', e => __subtitleErrors.push(String(e.reason)));
  ` });
  await b.navigate(target + '/');
  await waitFor(b, '!!document.querySelector("#auth-submit")');
  await click(b, '#auth-user');
  await b.call('Input.insertText', { text: 'admin' });
  await click(b, '#auth-pw');
  await b.call('Input.insertText', { text: 'password-1234' });
  await click(b, '#auth-submit');
  const deadline = performance.now() + 10000;
  while (new URL((await b.call('Page.getFrameTree')).frameTree.frame.url).pathname !== '/admin') {
    assert.ok(performance.now() < deadline, 'real login did not navigate to admin');
    await new Promise(resolve => setTimeout(resolve, 20));
  }
  await b.navigate(target + '/admin#items');
  await waitFor(b, '!!document.querySelector(".wall-card")');
  await click(b, '.wall-card');
  await waitFor(b, '!!document.querySelector("#detail-subtitle-marks-toggle")');
  // The actual maintenance disclosure must expose an operable control even
  // though the unrelated MetaTube scrape control has no configured upstream.
  await click(b, '.detail-maintenance > summary');
  await click(b, '#detail-subtitle-marks-toggle');
  await waitFor(b, '!!document.querySelector("#subtitle-marks-confirm") && !document.querySelector("#subtitle-marks-confirm").disabled');
  const previewText = await b.evaluate('document.querySelector("#detail-subtitle-marks").textContent');
  for (const text of ['不需要 MetaTube 配置', 'Local browser movie', 'Subtitle fixture', 'ABF-018.strm', '人工保留', '中文字幕', 'ABF-018.zh.srt', '当前程序归属', '拟提交归属', '未重下载海报']) {
    assert.ok(previewText.includes(text), text + ': ' + previewText);
  }
  assert.equal(await b.evaluate('document.querySelector("#subtitle-marks-overwrite").checked'), false);
  const previewGeometry = await geometry(b);
  // Closing an untouched preview and reopening it must not implicitly confirm.
  await click(b, '#subtitle-marks-close');
  assert.equal(await b.evaluate('document.querySelector("#detail-subtitle-marks").hidden'), true);
  await click(b, '#detail-subtitle-marks-toggle');
  await waitFor(b, '!!document.querySelector("#subtitle-marks-confirm") && !document.querySelector("#subtitle-marks-confirm").disabled');
  // Existing human tags protect default fill-only mode. Changing the actual
  // checkbox requests a new, versioned overwrite preview before confirmation.
  assert.match(await b.evaluate('document.querySelector("#subtitle-marks-preview").textContent'), /受保护/);
  await click(b, '#subtitle-marks-overwrite');
  await waitFor(b, '!!document.querySelector("#subtitle-marks-confirm") && !document.querySelector("#subtitle-marks-confirm").disabled && document.querySelector("#subtitle-marks-overwrite").checked');
  assert.match(await b.evaluate('document.querySelector("#subtitle-marks-preview").textContent'), /将按以上预览整理标签/);
  await milestone('SUBTITLE_PREVIEW_READY');
  assert.equal(await b.evaluate('document.activeElement?.id'), 'subtitle-marks-confirm');
  await click(b, '#subtitle-marks-confirm');
  await waitFor(b, '!!document.querySelector("#subtitle-marks-done")');
  const saved = await b.evaluate(`(() => {
    const panel = document.querySelector('#detail-subtitle-marks'), status = panel.querySelector('[role="status"]');
    return { title: panel.querySelector('h3').textContent, text: panel.textContent,
      live: status.getAttribute('aria-live'), atomic: status.getAttribute('aria-atomic'),
      focus: document.activeElement?.id, hidden: panel.hidden,
      retry: !!panel.querySelector('#subtitle-marks-confirm, #subtitle-marks-preview-button') };
  })()`);
  assert.equal(saved.title, mode === 'success' ? '字幕标记整理完成' : '整理有事项需核对');
  assert.match(saved.text, /NFO 已保存/);
  assert.match(saved.text, /不会自动重试写入/);
  assert.match(saved.text, /未重下载海报，旧图角标可能未同步/);
  if (mode === 'ownership') {
    assert.match(saved.text, /标记归属需人工核对/);
    assert.match(saved.text, /不要直接重新整理/);
    assert.match(saved.text, /重扫不能恢复归属记录/);
  } else {
    assert.doesNotMatch(saved.text, /标记归属需人工核对/);
  }
  assert.doesNotMatch(saved.text, /PRIVATE_SUBTITLE_BROWSER_SQL/);
  assert.equal(saved.live, 'polite');
  assert.equal(saved.atomic, 'true');
  assert.equal(saved.focus, 'subtitle-marks-done');
  assert.equal(saved.hidden, false);
  assert.equal(saved.retry, false);
  const savedGeometry = await geometry(b);
  await milestone('SUBTITLE_SAVED_READY');
  assert.equal(await b.evaluate('document.querySelector("#detail-subtitle-marks").hidden'), false);
  await b.call('Input.dispatchKeyEvent', { type: 'keyDown', key: 'Enter', code: 'Enter', windowsVirtualKeyCode: 13, text: '\r', unmodifiedText: '\r' });
  await b.call('Input.dispatchKeyEvent', { type: 'keyUp', key: 'Enter', code: 'Enter', windowsVirtualKeyCode: 13 });
  await waitFor(b, 'document.querySelector("#detail-subtitle-marks").hidden');
  assert.equal(await b.evaluate('document.activeElement?.id'), 'detail-subtitle-marks-toggle');
  assert.deepEqual(await b.evaluate('window.__subtitleErrors'), []);
  console.log(JSON.stringify({ width, mode, realApp: true, realLogin: true, explicitConfirm: true, previewGeometry, savedGeometry }));
});
