'use strict';

// Isolated settings panel: never receive, interpolate or persist the stored URL in the browser.
async function mountWebhookSettings(host, apiRequest, isCurrent = () => host.isConnected) {
  const events = [
    ['item.deleted', '明确删除影片（危险）'], ['item.updated', '人工更新影片'],
    ['item.rate', '收藏 / 评分'], ['item.unrate', '清除评分'],
    ['item.markplayed', '标为已看'], ['item.markunplayed', '取消已看'],
    ['playback.start', '开始播放'], ['playback.progress', '播放进度（高频）'],
    ['playback.pause', '暂停'], ['playback.unpause', '继续播放'], ['playback.stop', '停止播放'],
    ['user.authenticated', '登录成功'], ['user.logout', '退出登录']
  ];
  host.innerHTML = `
    <div class="panel-head"><h2 id="webhook-title">Webhook</h2><button type="button" id="wh-reload" class="btn btn-sm">重新加载</button></div>
    <p class="hint">保存即生效，重启保留。地址只写不回显，留空保留已有地址；不会试发通知。</p>
    <form id="wh-form">
      <fieldset id="wh-fields" disabled style="border:0;padding:0;margin:0;min-width:0">
        <legend class="hint">通知设置</legend>
        <div class="field-grid">
          <label class="check"><input id="wh-enabled" type="checkbox">启用通知</label>
          <label class="check"><input id="wh-private" type="checkbox">允许受信任的内网 / 本机接收端</label>
          <div class="field full"><label for="wh-url">接收地址（含 token，请妥善保密）</label>
            <input id="wh-url" type="password" maxlength="4096" autocomplete="new-password" spellcheck="false" aria-describedby="wh-url-state" placeholder="输入新的完整 HTTP(S) 地址；留空保留">
            <span id="wh-url-state" class="hint"></span>
            <label class="check"><input id="wh-clear" type="checkbox">清除已保存地址（必须关闭通知）</label>
          </div>
          <div class="field"><label for="wh-format">编码格式</label><select id="wh-format"><option value="json">JSON · SmartStrm</option><option value="form">Emby 表单 · data=&lt;JSON&gt;</option></select></div>
          <div class="field"><label for="wh-timeout">超时（秒，1–30）</label><input id="wh-timeout" type="number" min="1" max="30" step="1" required></div>
        </div>
        <fieldset style="margin:18px 0;padding:12px;min-width:0;border:1px solid var(--line)"><legend>事件订阅（至少一项）</legend>
          <div class="field-grid">${events.map(([name, label]) => `<label class="check"><input type="checkbox" data-wh-event="${name}"><span>${label}<br><small class="mono">${name}</small></span></label>`).join('')}</div>
        </fieldset>
        <p class="warning-block" role="note">item.deleted 可能让 SmartStrm 删除远端整个目录，包括同目录其它影片，且不可撤销。playback.progress 高频同步投递，会增加播放上报延迟。通知无自动重试或持久队列。</p>
        <label class="check"><input id="wh-confirm" type="checkbox">我已核对接收端路径映射，确认启用删除订阅可能删除远端整个目录。</label>
      </fieldset>
      <p id="wh-status" class="hint" role="status" aria-live="polite">正在加载通知设置…</p>
      <div class="form-foot"><button id="wh-save" class="btn btn-accent" disabled>保存 Webhook</button></div>
    </form>`;
  const find = selector => host.querySelector(selector);
  const form = find('#wh-form'), fields = find('#wh-fields'), save = find('#wh-save'), reload = find('#wh-reload');
  let config = null, busy = false, conflicted = false;
  const current = () => isCurrent() && host.isConnected;
  const status = (text, error = false) => {
    find('#wh-status').textContent = text;
    find('#wh-status').setAttribute('role', error ? 'alert' : 'status');
  };
  const setBusy = value => {
    busy = value;
    fields.disabled = value || !config;
    save.disabled = value || !config || conflicted;
    reload.disabled = value;
  };
  const valid = data => data && typeof data.config_etag === 'string' && data.config_etag.length > 0 &&
    typeof data.enabled === 'boolean' && typeof data.url_configured === 'boolean' && typeof data.allow_private === 'boolean' &&
    ['json', 'form'].includes(data.format) && Number.isInteger(data.timeout_seconds) && data.timeout_seconds >= 1 && data.timeout_seconds <= 30 &&
    Array.isArray(data.events) && data.events.length > 0 && data.events.every(name => events.some(([known]) => name === known));
  const apply = data => {
    config = data;
    conflicted = false;
    find('#wh-enabled').checked = data.enabled;
    find('#wh-private').checked = data.allow_private;
    find('#wh-format').value = data.format;
    find('#wh-timeout').value = String(data.timeout_seconds);
    find('#wh-url').value = '';
    find('#wh-clear').checked = false;
    find('#wh-confirm').checked = false;
    find('#wh-url-state').textContent = data.url_configured ? '已保存地址（不回显）；留空保留，输入新地址替换。' : '尚未配置接收地址。';
    for (const input of host.querySelectorAll('[data-wh-event]')) input.checked = data.events.includes(input.dataset.whEvent);
  };
  const load = async () => {
    if (busy || !current()) return;
    setBusy(true);
    status('正在加载通知设置…');
    try {
      const data = await apiRequest('/webhook/settings');
      if (!current()) return;
      if (!valid(data)) throw Error('invalid response');
      apply(data);
      status('设置已加载。保存不会发送测试通知。');
    } catch {
      if (current()) status('通知设置加载失败，未覆盖输入；请重新加载。', true);
    } finally { if (current()) setBusy(false); }
  };
  reload.addEventListener('click', () => {
    if (config && !confirm('重新加载将丢弃当前 Webhook 草稿。继续？')) return;
    return load();
  });
  form.addEventListener('submit', async event => {
    event.preventDefault();
    if (busy || !config || conflicted || !current()) return;
    const draft = {
      enabled: find('#wh-enabled').checked,
      url: find('#wh-url').value.trim(),
      clear_url: find('#wh-clear').checked,
      events: Array.from(host.querySelectorAll('[data-wh-event]')).filter(input => input.checked).map(input => input.dataset.whEvent),
      format: find('#wh-format').value,
      timeout_seconds: Number(find('#wh-timeout').value),
      allow_private: find('#wh-private').checked,
      confirm_delete: find('#wh-confirm').checked
    };
    if (!draft.events.length) { status('请至少选择一个事件；关闭通知请关闭启用开关。', true); return; }
    if (!Number.isInteger(draft.timeout_seconds) || draft.timeout_seconds < 1 || draft.timeout_seconds > 30) { status('超时必须是 1–30 秒的整数。', true); return; }
    if (draft.clear_url && (draft.enabled || draft.url)) { status('清除地址前请关闭通知，并清空新地址输入。', true); return; }
    if (draft.enabled && !draft.url && !(config.url_configured && !draft.clear_url)) { status('启用通知前请填写接收地址。', true); return; }
    if (draft.enabled && draft.events.includes('item.deleted') && !draft.confirm_delete) { status('请先明确确认远端整目录删除风险。', true); find('#wh-confirm').focus(); return; }
    setBusy(true);
    status('正在保存；不会发送测试通知…');
    try {
      const data = await apiRequest('/webhook/settings', { method: 'PUT', headers: { 'If-Match': config.config_etag }, body: JSON.stringify(draft) });
      if (!current()) return;
      if (!valid(data)) throw Error('invalid response');
      apply(data);
      status('Webhook 已保存并即时生效，重启保留。未发送测试通知。');
    } catch (error) {
      if (!current()) return;
      if (error.status === 412) {
        conflicted = true;
        status('配置已被其他管理员修改，草稿已保留。请先复制需保留的输入，再重新加载核对；不会自动覆盖。', true);
      } else {
        status('保存未确认，输入已保留。请检查后重试；若实际已保存，重试将提示冲突，不会覆盖新配置。', true);
      }
    } finally { if (current()) setBusy(false); }
  });
  await load();
}
