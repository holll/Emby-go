'use strict';

async function mountLogSettings(host, apiRequest, isCurrent = () => host.isConnected) {
  host.innerHTML = `
    <div class="panel-head"><h2 id="log-settings-title">日志保存期限</h2><button type="button" id="lr-reload" class="btn btn-sm">重新加载</button></div>
    <p class="hint">设置 app / request 磁盘日志的最大保存天数；保存即生效，重启保留。0 表示不自动清理，建议 7–30 天。</p>
    <form id="lr-form">
      <fieldset id="lr-fields" disabled style="border:0;padding:0;margin:0;min-width:0">
        <legend class="hint">历史日志自动清理</legend>
        <div class="field"><label for="lr-days">最大保存天数（0–3650）</label><input id="lr-days" type="number" min="0" max="3650" step="1" required aria-describedby="lr-help"></div>
        <p id="lr-help" class="hint">按服务器日志文件日期计算，包含今天。例如 7 天保留今天及前 6 天。仅清理 app-YYYY-MM-DD.log 与 request-YYYY-MM-DD.log；不清理子目录、符号链接或其它文件，也不改变后台内存日志面板。</p>
        <p class="warning-block">保存非零期限会立即清理过期历史日志，无法撤销；之后跨日写日志时自动检查。没有新日志写入时不会后台定时扫描。</p>
        <label class="check"><input id="lr-confirm" type="checkbox">我确认永久删除超过上述期限的历史日志。</label>
      </fieldset>
      <p id="lr-status" class="hint" role="status" aria-live="polite">正在加载日志设置…</p>
      <div class="form-foot"><button id="lr-save" class="btn btn-accent" disabled>保存日志期限</button></div>
    </form>`;
  const find = s => host.querySelector(s);
  let config = null, busy = false, conflicted = false;
  const current = () => isCurrent() && host.isConnected;
  const status = (text, error = false) => { find('#lr-status').textContent = text; find('#lr-status').setAttribute('role', error ? 'alert' : 'status'); };
  const setBusy = value => { busy = value; find('#lr-fields').disabled = value || !config; find('#lr-save').disabled = value || !config || conflicted; find('#lr-reload').disabled = value; };
  const valid = d => d && Number.isInteger(d.retention_days) && d.retention_days >= 0 && d.retention_days <= 3650 && typeof d.config_etag === 'string' && d.config_etag.length > 0;
  async function load() {
    if (busy || !current()) return;
    config = null; conflicted = false; setBusy(true);
    try {
      const data = await apiRequest('/logs/settings');
      if (!current()) return;
      if (!valid(data)) throw new Error('日志设置响应无效，请重新加载。');
      config = data; find('#lr-days').value = String(data.retention_days); find('#lr-confirm').checked = false;
      status(data.retention_days ? `当前保留 ${data.retention_days} 天（含今天）。` : '当前未启用自动清理。');
    } catch (e) { if (current()) status(e.message || '日志设置读取失败。', true); }
    finally { if (current()) setBusy(false); }
  }
  find('#lr-days').oninput = () => { find('#lr-confirm').checked = false; };
  find('#lr-reload').onclick = () => { if (busy || !current()) return; if (config && !confirm('重新加载会丢弃当前日志期限草稿，继续？')) return; return load(); };
  find('#lr-form').onsubmit = async event => {
    event.preventDefault(); if (busy || !config || conflicted || !current()) return;
    const raw = find('#lr-days').value.trim(), days = Number(raw);
    if (!raw || !Number.isInteger(days) || days < 0 || days > 3650) { status('保存天数必须是 0–3650 的整数。', true); return; }
    const confirmed = find('#lr-confirm').checked;
    if (days > 0 && !confirmed) { status('请确认永久清理过期历史日志。', true); return; }
    setBusy(true);
    try {
      const data = await apiRequest('/logs/settings', { method: 'PUT', headers: { 'If-Match': config.config_etag }, body: JSON.stringify({ retention_days: days, confirm_cleanup: confirmed }) });
      if (!current()) return;
      if (!valid(data)) { conflicted = true; throw new Error('保存结果未确认，请重新加载核对，勿盲目重提。'); }
      config = data; find('#lr-days').value = String(data.retention_days); find('#lr-confirm').checked = false;
      if (data.cleanup_warning) status('期限已保存，但部分历史日志未能清理；请检查目录权限，修复后再次保存或等待下次跨日写入。', true);
      else status(days ? '期限已保存并执行历史清理。' : '已关闭自动清理；之前删除的文件不会恢复。');
    } catch (e) {
      if (!current()) return;
      if (e.status === 412) { conflicted = true; status('设置已被其它管理员修改，已保留草稿；请显式重新加载后核对。', true); }
      else status(e.message || '保存失败，已保留输入；请先核对当前设置。', true);
    } finally { if (current()) setBusy(false); }
  };
  await load();
}
