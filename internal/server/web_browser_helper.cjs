'use strict';
// One real-browser transport for CSS probes and full-page tests. Never treat a
// successful CLI exit as evidence that Chromium actually evaluated the page.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { spawn } = require('node:child_process');
const chrome = process.env.CHROME_BIN || [
  '/root/.cache/ms-playwright/chromium-1248/chrome-linux64/chrome',
  '/usr/bin/google-chrome', '/usr/bin/chromium', '/usr/bin/chromium-browser',
].find(file => fs.existsSync(file));

function available(t) {
  if (chrome && fs.existsSync(chrome) && typeof WebSocket === 'function') return true;
  const message = `Real Chromium tests require an existing browser and Node WebSocket; CHROME_BIN=${chrome || 'not found'}`;
  // Optional local browser installation remains optional, never CI coverage.
  if (process.env.CI || process.env.CHROME_BIN) assert.fail(message);
  t.skip(message);
  return false;
}

async function browser(t, executable = chrome) {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'emby-browser-'));
  // Fresh profiles otherwise start first-run/background services. These services
  // are irrelevant to local fixtures and can block cold CI browser startup.
  const proc = spawn(executable, [
    '--headless', '--no-sandbox', '--disable-gpu', '--disable-dev-shm-usage',
    '--no-first-run', '--no-default-browser-check', '--disable-background-networking',
    '--disable-component-update', '--disable-default-apps', '--disable-sync',
    '--disable-extensions', '--metrics-recording-only', '--password-store=basic',
    '--use-mock-keychain', '--remote-debugging-port=0',
    '--user-data-dir=' + path.join(dir, 'profile'), 'about:blank',
  ], { stdio: ['ignore', 'ignore', 'pipe'] });
  let stderr = '', ws, exitCode, exitSignal, spawnError;
  const exited = new Promise(resolve => {
    proc.once('exit', (code, signal) => { exitCode = code; exitSignal = signal; resolve(); });
    proc.once('error', error => { spawnError = error; resolve(); });
  });
  proc.stderr.on('data', chunk => { stderr = (stderr + chunk).slice(-65536); });
  const diagnostic = message => Error(`${message}; executable=${executable}; pid=${proc.pid}; exit=${exitCode}; signal=${exitSignal}; spawn=${spawnError?.message || 'none'}\n${stderr}`);
  t.after(async () => {
    if (ws && ws.readyState === WebSocket.OPEN) {
      try { ws.send(JSON.stringify({ id: 0, method: 'Browser.close' })); } catch { /* bounded termination below */ }
    }
    const terminate = setTimeout(() => proc.kill(), 1000);
    const forceKill = setTimeout(() => proc.kill('SIGKILL'), 2500);
    try { await exited; } finally { clearTimeout(terminate); clearTimeout(forceKill); if (ws) ws.close(); }
    assert.equal(path.dirname(dir), os.tmpdir());
    assert.ok(path.basename(dir).startsWith('emby-browser-'));
    fs.rmSync(dir, { recursive: true, force: true, maxRetries: 8, retryDelay: 100 });
  });
  const url = await new Promise((resolve, reject) => {
    const timeout = setTimeout(() => finish(diagnostic('Chromium CDP startup timeout (15000ms)')), 15000);
    const onData = () => {
      const match = stderr.match(/DevTools listening on (ws:\/\/[^\s]+)/);
      if (match) finish(null, match[1]);
    };
    const onError = () => finish(diagnostic('Chromium spawn failed'));
    const onExit = () => finish(diagnostic('Chromium exited before CDP startup'));
    function finish(error, value) {
      clearTimeout(timeout); proc.stderr.off('data', onData); proc.off('error', onError); proc.off('exit', onExit);
      if (error) reject(error); else resolve(value);
    }
    proc.stderr.on('data', onData); proc.once('error', onError); proc.once('exit', onExit);
  });
  ws = new WebSocket(url);
  await new Promise((resolve, reject) => {
    const timeout = setTimeout(() => reject(diagnostic('Chromium WebSocket open timeout')), 15000);
    ws.addEventListener('open', () => { clearTimeout(timeout); resolve(); }, { once: true });
    ws.addEventListener('error', () => { clearTimeout(timeout); reject(diagnostic('Chromium WebSocket open failed')); }, { once: true });
  });
  let serial = 0;
  const pending = new Map();
  const rejectPending = () => {
    for (const entry of pending.values()) { clearTimeout(entry.timeout); entry.reject(diagnostic('Chromium CDP connection closed')); }
    pending.clear();
  };
  ws.addEventListener('close', rejectPending);
  ws.addEventListener('error', rejectPending);
  ws.addEventListener('message', event => {
    const message = JSON.parse(event.data), entry = pending.get(message.id);
    if (!entry) return;
    pending.delete(message.id); clearTimeout(entry.timeout);
    if (message.error) entry.reject(diagnostic(JSON.stringify(message.error))); else entry.resolve(message.result);
  });
  const send = (method, params = {}, sessionId) => new Promise((resolve, reject) => {
    const id = ++serial;
    const timeout = setTimeout(() => { pending.delete(id); reject(diagnostic('CDP timeout ' + method)); }, 15000);
    pending.set(id, { resolve, reject, timeout });
    try { ws.send(JSON.stringify({ id, method, params, ...(sessionId ? { sessionId } : {}) })); }
    catch (error) { pending.delete(id); clearTimeout(timeout); reject(error); }
  });
  const { targetId } = await send('Target.createTarget', { url: 'about:blank' });
  const { sessionId } = await send('Target.attachToTarget', { targetId, flatten: true });
  const call = (method, params) => send(method, params, sessionId);
  await call('Page.enable');
  const evaluate = async expression => {
    const result = await call('Runtime.evaluate', { expression, awaitPromise: true, returnByValue: true });
    if (result.exceptionDetails) throw diagnostic(JSON.stringify(result.exceptionDetails));
    return result.result.value;
  };
  const navigate = async url => {
    let onMessage, timer;
    const loaded = new Promise((resolve, reject) => {
      timer = setTimeout(() => reject(diagnostic('Chromium page load timeout: ' + url.slice(0, 200))), 15000);
      onMessage = event => {
        const message = JSON.parse(event.data);
        if (message.sessionId === sessionId && message.method === 'Page.loadEventFired') resolve();
      };
      ws.addEventListener('message', onMessage);
    });
    // Attach a handler immediately, including when navigation itself fails.
    loaded.catch(() => {});
    try {
      const { errorText } = await call('Page.navigate', { url });
      assert.equal(errorText, undefined, 'Chromium fixture navigation failed: ' + errorText);
      await loaded;
    } finally { clearTimeout(timer); ws.removeEventListener('message', onMessage); }
  };
  return { call, evaluate, navigate, dir };
}

async function probe(t, html, widths) {
  const { call, evaluate, navigate } = await browser(t);
  const results = [];
  for (const width of widths) {
    await call('Emulation.setDeviceMetricsOverride', { width, height: 900, deviceScaleFactor: 1, mobile: false });
    await navigate('data:text/html;base64,' + Buffer.from(html).toString('base64'));
    results.push(await evaluate(`(async()=>{
      while(document.readyState !== 'complete') await new Promise(resolve=>setTimeout(resolve,10));
      // Let the fixture's scheduled measurement finish; the CDP command timeout
      // remains fatal if the fixture never publishes a result.
      while(!document.querySelector('#result')?.textContent) await new Promise(resolve=>setTimeout(resolve,10));
      return JSON.parse(document.querySelector('#result').textContent);
    })()`));
  }
  return results;
}
module.exports = { available, browser, probe };
