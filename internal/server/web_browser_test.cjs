'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { available, browser } = require('./web_browser_helper.cjs');

const environmentKeys = ['HOME', 'XDG_CACHE_HOME', 'XDG_CONFIG_HOME', 'XDG_DATA_HOME', 'XDG_RUNTIME_DIR', 'DBUS_SESSION_BUS_ADDRESS', 'DBUS_SYSTEM_BUS_ADDRESS', 'FONTCONFIG_FILE', 'FONTCONFIG_PATH'];

function poisonEnvironment(t, dir) {
  const saved = Object.fromEntries(environmentKeys.map(key => [key, process.env[key]]));
  const blocked = path.join(dir, 'not-a-directory');
  fs.writeFileSync(blocked, 'cannot contain a cache or config directory');
  const config = path.join(dir, 'unwritable-fonts.conf');
  fs.writeFileSync(config, '<?xml version="1.0"?><!DOCTYPE fontconfig SYSTEM "fonts.dtd"><fontconfig><dir>/usr/share/fonts</dir><cachedir>' + blocked + '/fontconfig</cachedir></fontconfig>');
  for (const key of environmentKeys) process.env[key] = path.join(blocked, key);
  process.env.DBUS_SESSION_BUS_ADDRESS = 'disabled:';
  process.env.DBUS_SYSTEM_BUS_ADDRESS = 'disabled:';
  process.env.FONTCONFIG_FILE = config;
  t.after(() => {
    for (const [key, value] of Object.entries(saved)) {
      if (value === undefined) delete process.env[key]; else process.env[key] = value;
    }
  });
  return Object.fromEntries(environmentKeys.map(key => [key, process.env[key]]));
}

function fixtureDirectory(t) {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'emby-browser-environment-'));
  t.after(() => {
    assert.equal(path.dirname(dir), os.tmpdir());
    assert.ok(path.basename(dir).startsWith('emby-browser-environment-'));
    fs.rmSync(dir, { recursive: true, force: true });
  });
  return dir;
}

test('browser launch failures retain executable and OS error diagnostics', async t => {
  await assert.rejects(browser(t, '/nonexistent/emby-test-chromium'), error => {
    assert.match(error.message, /Chromium spawn failed/);
    assert.match(error.message, /executable=\/nonexistent\/emby-test-chromium/);
    assert.match(error.message, /ENOENT/);
    return true;
  });
});

test('zero-exit browser without a CDP endpoint is a failure with stderr, not a successful probe', async t => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'emby-fake-browser-'));
  const executable = path.join(dir, 'chrome');
  fs.writeFileSync(executable, '#!/bin/sh\necho "browser fixture failed before DevTools" >&2\nexit 0\n', { mode: 0o700 });
  t.after(() => {
    assert.equal(path.dirname(dir), os.tmpdir());
    assert.ok(path.basename(dir).startsWith('emby-fake-browser-'));
    fs.rmSync(dir, { recursive: true, force: true });
  });
  await assert.rejects(browser(t, executable), error => {
    assert.match(error.message, /Chromium exited before CDP startup/);
    assert.match(error.message, /exit=0/);
    assert.match(error.message, /browser fixture failed before DevTools/);
    assert.match(error.message, /elapsed_ms=\d+; first_stderr_ms=\d+/);
    return true;
  });
});

test('each browser receives independent writable caches, local disabled buses and no inherited font overrides', async t => {
  const dir = fixtureDirectory(t);
  const poisoned = poisonEnvironment(t, dir);
  const report = path.join(dir, 'children.jsonl');
  const executable = path.join(dir, 'chrome');
  const source = `#!/usr/bin/env node
const fs = require('node:fs'), path = require('node:path');
const keys = ${JSON.stringify(environmentKeys)};
const env = Object.fromEntries(keys.map(key => [key, process.env[key] ?? null]));
const writable = ['HOME','XDG_CACHE_HOME','XDG_CONFIG_HOME','XDG_DATA_HOME','XDG_RUNTIME_DIR'].every(key => {
  fs.writeFileSync(path.join(env[key], 'write-probe'), 'ok');
  return (fs.statSync(env[key]).mode & 0o777) === 0o700;
});
fs.writeFileSync(path.join(env.XDG_CACHE_HOME, 'fontconfig', 'write-probe'), 'ok');
fs.appendFileSync(${JSON.stringify(report)}, JSON.stringify({ env, writable }) + '\\n');
process.stderr.write('fixture exits before DevTools\\n');
`;
  fs.writeFileSync(executable, source, { mode: 0o700 });
  for (let index = 0; index < 2; index++) {
    await assert.rejects(browser(t, executable), /Chromium exited before CDP startup/);
  }
  assert.deepEqual(Object.fromEntries(environmentKeys.map(key => [key, process.env[key]])), poisoned, 'child isolation never mutates parent environment');
  const children = fs.readFileSync(report, 'utf8').trim().split('\n').map(line => JSON.parse(line));
  assert.equal(children.length, 2);
  assert.notEqual(children[0].env.XDG_CACHE_HOME, children[1].env.XDG_CACHE_HOME, 'no cross-browser shared font cache');
  for (const child of children) {
    assert.equal(child.writable, true);
    const root = path.dirname(child.env.XDG_CACHE_HOME);
    assert.equal(path.dirname(root), os.tmpdir());
    assert.ok(path.basename(root).startsWith('emby-browser-'));
    for (const key of ['HOME', 'XDG_CACHE_HOME', 'XDG_CONFIG_HOME', 'XDG_DATA_HOME', 'XDG_RUNTIME_DIR']) {
      assert.equal(path.dirname(child.env[key]), root);
    }
    for (const key of ['DBUS_SESSION_BUS_ADDRESS', 'DBUS_SYSTEM_BUS_ADDRESS']) {
      assert.ok(child.env[key].startsWith('unix:path=' + root + path.sep));
      assert.equal(fs.existsSync(child.env[key].slice('unix:path='.length)), false);
    }
    assert.equal(child.env.FONTCONFIG_FILE, null);
    assert.equal(child.env.FONTCONFIG_PATH, null);
  }
});

test('real Chromium renders system fonts and evaluates a page despite poisoned host cache and bus environment', async t => {
  if (!available(t)) return;
  const dir = fixtureDirectory(t);
  const poisoned = poisonEnvironment(t, dir);
  const started = performance.now();
  const { navigate, evaluate } = await browser(t);
  t.diagnostic('isolated Chromium ready in ' + Math.round(performance.now() - started) + 'ms; startup budget remains 15000ms');
  await navigate('data:text/html;base64,' + Buffer.from('<!doctype html><html><head><meta charset="utf-8"></head><body><button id="probe" style="min-width:44px;min-height:44px;font:16px sans-serif">真实字体 Cache probe</button></body></html>').toString('base64'));
  const result = await evaluate(`(async () => {
    await document.fonts.ready;
    const button = document.querySelector('#probe');
    button.focus();
    const rect = button.getBoundingClientRect();
    const canvas = document.createElement('canvas').getContext('2d');
    canvas.font = '16px sans-serif';
    return { text: button.textContent, width: rect.width, height: rect.height, focused: document.activeElement === button, measured: canvas.measureText('Cache probe').width };
  })()`);
  assert.equal(result.text, '真实字体 Cache probe');
  assert.ok(result.width >= 44 && result.height >= 44);
  assert.equal(result.focused, true);
  assert.ok(result.measured > 0, 'system font shaping produced real text metrics');
  assert.deepEqual(Object.fromEntries(environmentKeys.map(key => [key, process.env[key]])), poisoned);
});
