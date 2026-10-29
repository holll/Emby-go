'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { browser } = require('./web_browser_helper.cjs');

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
    return true;
  });
});
