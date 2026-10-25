const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const source = fs.readFileSync(path.join(__dirname, 'web/image-cache-settings.js'), 'utf8');

function load(extra = {}) {
  const sandbox = { Math, Number, JSON, String, Error, Promise, console, confirm: () => true, ...extra };
  vm.createContext(sandbox);
  vm.runInContext(source, sandbox);
  return sandbox;
}

function makeHost() {
  const nodes = new Map();
  const node = sel => {
    if (!nodes.has(sel)) {
      nodes.set(sel, {
        textContent: '', value: '', disabled: false, html: '', role: '',
        setAttribute(key, value) { this[key] = value; },
        onsumbit: null,
      });
    }
    return nodes.get(sel);
  };
  const host = {
    isConnected: true,
    nodes,
    node,
    querySelector: sel => node(sel),
  };
  Object.defineProperty(host, 'innerHTML', {
    get() { return this.html || ''; },
    set(value) { this.html = value; },
  });
  return host;
}

test('图片缓存设置：体积格式化与 GiB 往返', () => {
  const lib = load();
  assert.equal(lib.imageCacheSize(0), '0 B');
  assert.equal(lib.imageCacheSize(512), '512 B');
  assert.equal(lib.imageCacheSize(1536), '1.5 KiB');
  assert.equal(lib.imageCacheSize(2 * 1024 ** 3), '2 GiB');
  assert.equal(lib.imageCacheGiB(2 * 1024 ** 3), 2);
  assert.equal(lib.imageCacheGiB(1073741824), 1);
  assert.equal(lib.imageCacheGiB(536870912), 0.5);
  assert.equal(lib.imageCacheGiB(0), 0);
});

test('图片缓存设置：渲染占用、保存换算成字节、冲突保留草稿', async () => {
  const host = makeHost();
  const calls = [];
  const settings = {
    dir: '/data/image-cache', enabled: true, max_bytes: 2 * 1024 ** 3,
    used_bytes: 1536, files: 2, max_allowed_bytes: 64 * 1024 ** 3, config_etag: '"ic1-2147483648"',
  };
  const api = async (p, options = {}) => {
    calls.push({ path: p, options });
    if (p === '/image-cache/settings' && !options.method) return { ...settings };
    if (p === '/image-cache/settings' && options.method === 'PUT') {
      const body = JSON.parse(options.body);
      assert.equal(options.headers['If-Match'], settings.config_etag);
      return { ...settings, max_bytes: body.max_bytes, config_etag: `"ic1-${body.max_bytes}"` };
    }
    throw new Error(`unexpected ${p}`);
  };
  const lib = load();
  await lib.mountImageCacheSettings(host, api, () => true);

  assert.match(host.innerHTML, /图片缓存/);
  assert.equal(host.node('#ic-dir').textContent, '/data/image-cache');
  assert.equal(host.node('#ic-enabled').textContent, '已启用');
  assert.match(host.node('#ic-used').textContent, /1\.5 KiB · 2 个变体/);
  assert.equal(host.node('#ic-limit').textContent, '2 GiB');
  assert.equal(host.node('#ic-gib').value, '2');
  assert.equal(host.node('#ic-fields').disabled, false);
  assert.equal(host.node('#ic-save').disabled, false);

  // 保存：0.5 GiB → 536870912 字节。
  host.node('#ic-gib').value = '0.5';
  const submit = host.node('#ic-form').onsubmit;
  let prevented = false;
  await submit({ preventDefault() { prevented = true; } });
  assert.ok(prevented);
  const put = calls.find(c => c.options.method === 'PUT');
  assert.equal(JSON.parse(put.options.body).max_bytes, 536870912);
  assert.match(host.node('#ic-status').textContent, /512 MiB/);

  // 412：保留草稿、提示重新加载，而不是静默重提。
  const conflictHost = makeHost();
  const conflictApi = async (p, options = {}) => {
    if (options.method === 'PUT') {
      const error = new Error('setting changed');
      error.status = 412;
      throw error;
    }
    return { ...settings };
  };
  await lib.mountImageCacheSettings(conflictHost, conflictApi, () => true);
  conflictHost.node('#ic-gib').value = '1';
  await conflictHost.node('#ic-form').onsubmit({ preventDefault() {} });
  assert.match(conflictHost.node('#ic-status').textContent, /已被其它管理员修改/);
  assert.equal(conflictHost.node('#ic-gib').value, '1');
  assert.equal(conflictHost.node('#ic-save').disabled, true);
});

test('图片缓存设置：禁用状态不可保存、清理按钮走 POST', async () => {
  const host = makeHost();
  const calls = [];
  const api = async (p, options = {}) => {
    calls.push({ path: p, options });
    if (p === '/image-cache/trim') return { removed: 3, freed_bytes: 4096, files: 0, used_bytes: 0 };
    return {
      dir: '', enabled: true, max_bytes: 0, used_bytes: 0, files: 0,
      max_allowed_bytes: 64 * 1024 ** 3, config_etag: '"ic1-0"',
    };
  };
  const lib = load();
  await lib.mountImageCacheSettings(host, api, () => true);
  assert.equal(host.node('#ic-limit').textContent, '不限制');
  assert.equal(host.node('#ic-gib').value, '0');

  // 上限 0 表示不限制：允许清理，且清理成功后回读设置。
  await host.node('#ic-trim').onclick();
  const trim = calls.find(c => c.path === '/image-cache/trim');
  assert.equal(trim.options.method, 'POST');
  assert.match(host.node('#ic-status').textContent, /已清理 3 个变体，释放 4 KiB/);

  // 配置禁用时表单与按钮全部锁死。
  const disabledHost = makeHost();
  await lib.mountImageCacheSettings(disabledHost, async () => ({
    dir: '', enabled: false, max_bytes: 0, used_bytes: 0, files: 0,
    max_allowed_bytes: 64 * 1024 ** 3, config_etag: '"ic1-0"',
  }), () => true);
  assert.equal(disabledHost.node('#ic-fields').disabled, true);
  assert.equal(disabledHost.node('#ic-save').disabled, true);
  assert.equal(disabledHost.node('#ic-trim').disabled, true);
});
