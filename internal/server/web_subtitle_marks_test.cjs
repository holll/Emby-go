'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const source = fs.readFileSync(path.join(__dirname, 'web/app.js'), 'utf8');
const code = source.slice(source.indexOf('function bindSubtitleMarks('), source.indexOf('/* 遮罩焦点：'));
const escCode = source.slice(source.indexOf('const esc = '), source.indexOf('const icon = '));
const rowCode = source.slice(source.indexOf('function detailMetaRow('), source.indexOf('const EDIT_LOCK_FIELDS'));
const plan = (overrides = {}) => ({ movie_id: 7, title: 'Movie', library_name: 'Library', main_file: '/fixture/movie.strm', version: 'v1', overwrite: false,
  evidence: { code: 'C', tag: '中文字幕', source: 'subtitle', matches: ['movie.zh.srt'] }, before: ['manual'], after: ['manual', '中文字幕'], previous_owned: ['old owned'], owned: [], protected: false, changed: true,
  image_note: '未重下载海报，旧图角标可能未同步', ...overrides });
const saved = overrides => ({ status: 'success', saved: true, indexed: true, ownership_saved: true, warnings: [], ...overrides });
const flush = () => new Promise(resolve => setImmediate(resolve));
function fixture(respond) {
  const calls = [];
  let active = null, current = true;
  function element() {
    const children = new Map();
    return {
      html: '', hidden: true, disabled: false, checked: false, handlers: {}, attrs: {},
      set innerHTML(html) {
        this.html = html; children.clear();
        for (const match of html.matchAll(/<(?:button|input)[^>]*id="([^"]+)"[^>]*>/g)) {
          const child = element(); child.disabled = / disabled/.test(match[0]); child.checked = / checked/.test(match[0]); children.set('#' + match[1], child);
        }
      },
      get innerHTML() { return this.html; },
      querySelector(selector) { return children.get(selector) || null; },
      setAttribute(name, value) { this.attrs[name] = value; },
      addEventListener(name, callback) { this.handlers[name] = callback; },
      focus() { active = this; },
      click() { if (!this.disabled) return this.handlers.click(); },
      change(checked) { this.checked = checked; return this.handlers.change({ target: this }); }
    };
  }
  const panel = element(), toggle = element();
  const box = vm.createContext({ api: async (url, options) => { const call = { url, method: options.method, body: JSON.parse(options.body) }; calls.push(call); return respond(call); } });
  vm.runInContext(escCode + rowCode + code, box);
  box.bindSubtitleMarks(panel, toggle, 7, () => current);
  return { panel, toggle, calls, get active() { return active; }, stale() { current = false; },
    async open() { toggle.click(); await flush(); },
    async confirm() { await panel.querySelector('#subtitle-marks-confirm').click(); },
    async overwrite(value) { panel.querySelector('#subtitle-marks-overwrite').change(value); await flush(); } };
}

test('administrator-only detail entry, default fill preview and explicit versioned confirmation', async () => {
  const env = fixture(call => call.url.endsWith('/preview') ? plan() : saved());
  await env.open();
  assert.deepEqual(env.calls, [{ url: '/items/7/subtitle-marks/preview', method: 'POST', body: { overwrite: false } }]);
  assert.match(env.panel.innerHTML, /Movie|Library|当前主片|程序归属|拟新标签|未重下载海报/);
  await env.confirm();
  assert.deepEqual(env.calls[1], { url: '/items/7/subtitle-marks', method: 'POST', body: { overwrite: false, version: 'v1' } });
  assert.match(source, /me\?\.is_admin \? `<button id="detail-subtitle-marks-toggle"/);
  assert.equal(env.panel.hidden, false);
  env.panel.querySelector('#subtitle-marks-done').click();
  assert.equal(env.panel.hidden, true);
  assert.equal(env.active, env.toggle);
  await env.open();
  assert.equal(env.calls.length, 3, 'Done permits a new explicit preview, never auto-rewrites');
});

test('explicit overwrite invalidates preview and requires a fresh version, switching back restores fill', async () => {
  let resolve;
  const env = fixture(call => call.url.endsWith('/preview') && call.body.overwrite
    ? new Promise(yes => { resolve = yes; }) : call.url.endsWith('/preview') ? plan() : saved());
  await env.open();
  const oldConfirm = env.panel.querySelector('#subtitle-marks-confirm');
  const pending = env.overwrite(true);
  assert.equal(env.panel.querySelector('#subtitle-marks-confirm'), null);
  await oldConfirm.handlers.click();
  assert.equal(env.calls.length, 2);
  resolve(plan({ overwrite: true, version: 'v2' })); await pending;
  assert.equal(env.panel.querySelector('#subtitle-marks-overwrite').checked, true);
  await env.confirm();
  assert.deepEqual(env.calls[2].body, { overwrite: true, version: 'v2' });
  env.panel.querySelector('#subtitle-marks-done').click(); await env.open();
  assert.equal(env.calls.at(-1).body.overwrite, false);
});

test('partial results persist with independent ownership/index problems and Done never writes', async () => {
  const env = fixture(call => call.url.endsWith('/preview') ? plan() : saved({ status: 'partial_success', ownership_saved: false, indexed: false, auto_marks_error: 'ownership failure', refresh_error: 'index failure', warnings: ['warning'], canceled: true }));
  await env.open();
  const oldConfirm = env.panel.querySelector('#subtitle-marks-confirm');
  await env.confirm();
  assert.match(env.panel.innerHTML, /NFO 已保存|标记归属需人工核对|重扫不能恢复归属记录|索引需重扫|ownership failure|index failure|warning|请求已中止/);
  assert.doesNotMatch(env.panel.innerHTML, /id="subtitle-marks-(confirm|preview-button|overwrite)"/);
  assert.equal(env.panel.hidden, false);
  assert.equal(env.toggle.disabled, true);
  await oldConfirm.handlers.click();
  env.panel.querySelector('#subtitle-marks-done').click();
  assert.equal(env.calls.length, 2);
});

test('all dynamic preview and result text is escaped with the existing escaping function', async () => {
  const unsafe = '<img src=x onerror="boom(\'x\')"> &';
  const env = fixture(call => call.url.endsWith('/preview') ? plan({ title: unsafe, library_name: unsafe, main_file: unsafe, before: [unsafe], after: [unsafe], owned: [unsafe], image_note: unsafe, evidence: { code: unsafe, tag: unsafe, source: unsafe, matches: [unsafe] } }) : saved({ status: 'partial_success', auto_marks_error: unsafe, refresh_error: unsafe, warnings: [unsafe] }));
  await env.open();
  assert.doesNotMatch(env.panel.innerHTML, /<img|onerror="/);
  assert.match(env.panel.innerHTML, /&lt;img src=x onerror=&quot;boom\(&#39;x&#39;\)&quot;&gt; &amp;/);
  await env.confirm();
  assert.doesNotMatch(env.panel.innerHTML, /<img|onerror="/);
  assert.match(env.panel.innerHTML, /&lt;img/);
});

for (const status of [undefined, 500, 409, 422]) test(`confirmation failure ${status}: clears old preview and never assumes zero writes`, async () => {
  const env = fixture(call => {
    if (call.url.endsWith('/preview')) return plan();
    throw Object.assign(Error('<failed>'), { status });
  });
  await env.open();
  const oldConfirm = env.panel.querySelector('#subtitle-marks-confirm');
  await env.confirm();
  assert.doesNotMatch(env.panel.innerHTML, /id="subtitle-marks-confirm"/);
  assert.match(env.panel.innerHTML, /&lt;failed&gt;/);
  if (status === undefined || status === 500) assert.match(env.panel.innerHTML, /结果未知，先核对再重试/);
  if (status === 409) assert.match(env.panel.innerHTML, /预览已失效，请重新预览并再次明确确认/);
  else assert.match(env.panel.innerHTML, /不能据 HTTP 失败判断零写入/);
  await oldConfirm.handlers.click();
  assert.equal(env.calls.length, 2);
  await env.panel.querySelector('#subtitle-marks-preview-button').click();
  assert.equal(env.calls.length, 3);
  assert.ok(env.panel.querySelector('#subtitle-marks-confirm'));
});

for (const status of [400, 404, 409, 422, 503]) test(`preview error ${status} is escaped and has no write action`, async () => {
  const env = fixture(() => { throw Object.assign(Error('<preview-error>'), { status }); });
  await env.open();
  assert.match(env.panel.innerHTML, /&lt;preview-error&gt;/);
  assert.equal(env.panel.querySelector('#subtitle-marks-confirm'), null);
  assert.equal(env.calls.length, 1);
});

test('real Chromium: keyboard overwrite/confirm, persistent partial result, 44px targets and responsive long paths', async t => {
  const { available, browser } = require('./web_browser_helper.cjs');
  if (!available(t)) return;
  const { call, navigate, evaluate } = await browser(t);
  const css = fs.readFileSync(path.join(__dirname, 'web/style.css'), 'utf8');
  for (const width of [375, 667, 1280]) {
    await call('Emulation.setDeviceMetricsOverride', { width, height: width === 667 ? 375 : 900, deviceScaleFactor: 1, mobile: false });
    await call('Emulation.setEmulatedMedia', { features: [{ name: 'prefers-reduced-motion', value: 'reduce' }] });
    await navigate('data:text/html;charset=utf-8,' + encodeURIComponent(`<!doctype html><meta name="viewport" content="width=device-width, initial-scale=1"><style>${css}body{padding:16px}main{max-width:528px}</style><main><button id="toggle" class="btn">字幕标记整理</button><section id="panel" class="detail-section subtitle-marks" hidden></section></main>`));
    await evaluate(`${escCode}\n${rowCode}\n${code}\nwindow.calls=[];window.api=async(url,options)=>{const body=JSON.parse(options.body);calls.push({url,body});return url.endsWith('/preview')?${JSON.stringify(plan({ main_file: '/fixture/' + 'very-long-main-movie-name-'.repeat(30) + '.strm' }))}:{status:'partial_success',saved:true,indexed:false,ownership_saved:false,warnings:['fixture warning'],auto_marks_error:'ownership fixture',refresh_error:'index fixture'};};bindSubtitleMarks(document.querySelector('#panel'),document.querySelector('#toggle'),7,()=>true);document.querySelector('#toggle').click();`);
    const initial = await evaluate(`(()=>{const panel=document.querySelector('#panel'),confirm=panel.querySelector('#subtitle-marks-confirm');return {overflow:document.documentElement.scrollWidth>innerWidth,focus:document.activeElement===confirm,height:confirm.getBoundingClientRect().height,busy:panel.getAttribute('aria-busy'),mode:calls[0].body.overwrite};})()`);
    assert.equal(initial.overflow, false); assert.equal(initial.focus, true); assert.ok(initial.height >= 44); assert.equal(initial.busy, 'false'); assert.equal(initial.mode, false);
    await evaluate(`document.querySelector('#subtitle-marks-overwrite').focus()`);
    await call('Input.dispatchKeyEvent', { type: 'keyDown', key: ' ', code: 'Space', windowsVirtualKeyCode: 32 });
    await call('Input.dispatchKeyEvent', { type: 'keyUp', key: ' ', code: 'Space', windowsVirtualKeyCode: 32 });
    assert.equal(await evaluate('calls[1].body.overwrite'), true);
    assert.equal(await evaluate(`document.activeElement.id`), 'subtitle-marks-confirm');
    await call('Input.dispatchKeyEvent', { type: 'keyDown', key: 'Enter', code: 'Enter', windowsVirtualKeyCode: 13, text: '\r' });
    await call('Input.dispatchKeyEvent', { type: 'keyUp', key: 'Enter', code: 'Enter', windowsVirtualKeyCode: 13 });
    const result = await evaluate(`(()=>{const panel=document.querySelector('#panel'),done=panel.querySelector('#subtitle-marks-done');return {calls:calls.length,focus:document.activeElement===done,hidden:panel.hidden,confirm:!!panel.querySelector('#subtitle-marks-confirm'),text:panel.textContent,overflow:document.documentElement.scrollWidth>innerWidth,label:!!document.getElementById(panel.getAttribute('aria-labelledby'))};})()`);
    assert.equal(result.calls, 3); assert.equal(result.focus, true); assert.equal(result.hidden, false); assert.equal(result.confirm, false); assert.equal(result.overflow, false); assert.equal(result.label, true);
    assert.match(result.text, /标记归属需人工核对/); assert.match(result.text, /索引需重扫/);
    await call('Input.dispatchKeyEvent', { type: 'keyDown', key: 'Enter', code: 'Enter', windowsVirtualKeyCode: 13, text: '\r' });
    await call('Input.dispatchKeyEvent', { type: 'keyUp', key: 'Enter', code: 'Enter', windowsVirtualKeyCode: 13 });
    assert.deepEqual(await evaluate(`({hidden:document.querySelector('#panel').hidden,focus:document.activeElement.id,calls:calls.length})`), { hidden: true, focus: 'toggle', calls: 3 });
  }
});

test('closed or replaced details ignore an in-flight local preview', async () => {
  let resolve;
  const env = fixture(() => new Promise(yes => { resolve = yes; }));
  env.toggle.click(); env.stale();
  const html = env.panel.innerHTML;
  resolve(plan()); await flush();
  assert.equal(env.panel.innerHTML, html);
});
