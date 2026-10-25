'use strict';

// 图片缓存（缩略图变体目录）：容量上限 + 当前占用 + 手动清理。
// 变体是纯派生数据，删掉只会让下次请求重新解码，不影响接口语义，
// 所以这里的每个操作都是「可随时反悔」的；真正的默认值仍在配置文件里。
function imageCacheSize(bytes) {
  if (!Number.isFinite(bytes) || bytes <= 0) return '0 B';
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB'];
  let value = bytes, unit = 0;
  while (value >= 1024 && unit < units.length - 1) { value /= 1024; unit++; }
  const text = value >= 100 || unit === 0 ? String(Math.round(value)) : value.toFixed(1).replace(/\.0$/, '');
  return `${text} ${units[unit]}`;
}

function imageCacheGiB(bytes) {
  if (!Number.isFinite(bytes) || bytes <= 0) return 0;
  return Math.round((bytes / (1024 * 1024 * 1024)) * 1000) / 1000;
}

async function mountImageCacheSettings(host, apiRequest, isCurrent = () => host.isConnected) {
  host.innerHTML = `
    <div class="panel-head"><h2 id="image-cache-title">图片缓存</h2><button type="button" id="ic-reload" class="btn btn-sm">重新加载</button></div>
    <p class="hint">客户端按 <code>maxWidth</code>/<code>maxHeight</code> 请求的封面会实时缩放（webp 解码 + 缩放 + 编码，单张 45~130ms），生成过的尺寸落盘复用，重启后仍然有效。目录里的文件都是派生数据，随时可以删除。</p>
    <div class="table-wrap"><table>
      <tbody>
        <tr><th style="width:160px">目录</th><td class="mono" id="ic-dir">—</td></tr>
        <tr><th>状态</th><td id="ic-enabled">—</td></tr>
        <tr><th>当前占用</th><td id="ic-used">—</td></tr>
        <tr><th>容量上限</th><td id="ic-limit">—</td></tr>
      </tbody>
    </table></div>
    <form id="ic-form">
      <fieldset id="ic-fields" disabled style="border:0;padding:0;margin:0;min-width:0">
        <legend class="hint">容量上限</legend>
        <div class="field"><label for="ic-gib">上限（GiB，0 = 不限制）</label><input id="ic-gib" type="number" min="0" max="64" step="0.5" required aria-describedby="ic-help"></div>
        <p id="ic-help" class="hint">超过上限时按最近使用时间从旧到新淘汰到 80% 水位；上限调小会立即清理。0 表示不限制容量，只受目录所在磁盘的剩余空间约束。</p>
      </fieldset>
      <p id="ic-status" class="hint" role="status" aria-live="polite">正在加载图片缓存设置…</p>
      <div class="form-foot">
        <button id="ic-save" class="btn btn-accent" type="submit" disabled>保存上限</button>
        <button id="ic-trim" class="btn" type="button" disabled>立即清理</button>
      </div>
    </form>`;
  const find = s => host.querySelector(s);
  let config = null, busy = false, conflicted = false;
  const current = () => isCurrent() && host.isConnected;
  const status = (text, error = false) => { find('#ic-status').textContent = text; find('#ic-status').setAttribute('role', error ? 'alert' : 'status'); };
  const setBusy = value => {
    busy = value;
    find('#ic-fields').disabled = value || !config || !config.enabled;
    find('#ic-save').disabled = value || !config || !config.enabled || conflicted;
    find('#ic-trim').disabled = value || !config || !config.enabled;
    find('#ic-reload').disabled = value;
  };
  const valid = d => d && typeof d.config_etag === 'string' && d.config_etag.length > 0
    && Number.isInteger(d.max_bytes) && d.max_bytes >= 0
    && Number.isFinite(d.used_bytes) && d.used_bytes >= 0;
  function render(data) {
    config = data;
    find('#ic-dir').textContent = data.dir || '（未启用）';
    find('#ic-enabled').textContent = data.enabled ? '已启用' : '已关闭（配置文件 image_cache.enabled=false）';
    find('#ic-used').textContent = data.enabled ? `${imageCacheSize(data.used_bytes)} · ${data.files} 个变体` : '—';
    find('#ic-limit').textContent = data.max_bytes > 0 ? imageCacheSize(data.max_bytes) : '不限制';
    find('#ic-gib').value = String(imageCacheGiB(data.max_bytes));
    conflicted = false;
    setBusy(false);
  }
  async function load() {
    if (busy || !current()) return;
    config = null; conflicted = false; setBusy(true);
    try {
      const data = await apiRequest('/image-cache/settings');
      if (!current()) return;
      if (!valid(data)) throw new Error('图片缓存设置响应无效，请重新加载。');
      render(data);
      status(data.enabled ? '设置保存后立即生效，无需重启。' : '配置文件已关闭图片缓存，页面不能在此开启。');
    } catch (e) { if (current()) status(e.message || '图片缓存设置读取失败。', true); }
    finally { if (current()) setBusy(false); }
  }
  find('#ic-reload').onclick = () => { if (busy || !current()) return; return load(); };
  find('#ic-trim').onclick = async () => {
    if (busy || !config || !current()) return;
    if (!confirm('清理会删除所有缓存的缩略图，之后请求会重新生成（仅影响性能）。继续？')) return;
    setBusy(true);
    try {
      const data = await apiRequest('/image-cache/trim', { method: 'POST' });
      if (!current()) return;
      const message = `已清理 ${data.removed || 0} 个变体，释放 ${imageCacheSize(data.freed_bytes || 0)}。`;
      await load();
      status(message);
    } catch (e) { if (current()) status(e.message || '清理失败。', true); }
    finally { if (current()) setBusy(false); }
  };
  find('#ic-form').onsubmit = async event => {
    event.preventDefault();
    if (busy || !config || conflicted || !current()) return;
    const raw = find('#ic-gib').value.trim(), gib = Number(raw);
    if (!raw || !Number.isFinite(gib) || gib < 0 || gib > 64) { status('上限必须是 0–64 之间的数字（GiB）。', true); return; }
    const bytes = Math.round(gib * 1024 * 1024 * 1024);
    setBusy(true);
    try {
      const data = await apiRequest('/image-cache/settings', { method: 'PUT', headers: { 'If-Match': config.config_etag }, body: JSON.stringify({ max_bytes: bytes }) });
      if (!current()) return;
      if (!valid(data)) { conflicted = true; throw new Error('保存结果未确认，请重新加载核对，勿盲目重提。'); }
      render(data);
      status(bytes > 0 ? `上限已保存为 ${imageCacheSize(bytes)}；超出部分已清理。` : '已取消容量上限：只受磁盘剩余空间约束。');
    } catch (e) {
      if (!current()) return;
      if (e.status === 412) { conflicted = true; setBusy(false); status('设置已被其它管理员修改，已保留草稿；请显式重新加载后核对。', true); return; }
      status(e.message || '保存失败，已保留输入；请先核对当前设置。', true);
    } finally { if (current()) setBusy(false); }
  };
  await load();
}
