'use strict';

// 多目标 Webhook 面板（R3）。地址只写不回显：GET 只给出 url_configured，
// 保存时空白 URL 表示保留旧秘密，clear_url 才清除。整集合替换必须带 ETag。
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
    <div class="panel-head"><h2 id="webhook-title">Webhook 多目标</h2><button type="button" id="wh-reload" class="btn btn-sm">重新加载</button></div>
    <p class="hint">每个目标可单独开关并订阅事件。地址只写不回显：留空保留已保存地址，填写新地址即替换。保存为整集合原子替换，重启保留；不会试发通知。</p>
    <div id="wh-targets"></div>
    <p id="wh-empty" class="hint" hidden>当前没有通知目标；保存空集合会停止所有 Webhook 投递。</p>
    <div id="wh-remove-preview" class="warning-block" role="alert" hidden></div>
    <label class="check" id="wh-confirm-remove-row" hidden><input id="wh-confirm-remove" type="checkbox">我确认上述目标将被删除（已有地址一并移除）。</label>
    <label class="check" id="wh-confirm-empty-row" hidden><input id="wh-confirm-empty" type="checkbox">我确认清空全部 Webhook 目标，停止所有通知。</label>
    <label class="check"><input id="wh-confirm-delete">对新增的删除订阅，我确认 SmartStrm 可能删除远端整个目录且不可撤销。</label>
    <p class="warning-block" role="note">item.deleted 可能让 SmartStrm 删除远端整个目录，包括同目录其它影片。playback.progress 高频同步投递，会增加播放上报延迟。通知无自动重试或持久队列；部分目标失败不影响业务结果。</p>
    <p id="wh-status" class="hint" role="status" aria-live="polite">正在加载通知目标…</p>
    <div class="form-foot"><button id="wh-add" class="btn btn-sm" type="button">+ 添加目标</button><button id="wh-save" class="btn btn-accent" type="button" disabled>保存全部目标</button></div>`;

  const find = selector => host.querySelector(selector);
  const targetsHost = find('#wh-targets');
  let config = null, busy = false, conflicted = false;
  let targets = []; // {id,name,enabled,url,urlConfigured,clearUrl,events,format,timeoutSeconds,allowPrivate,isNew,removed}
  const current = () => isCurrent() && host.isConnected;
  const status = (text, error = false) => {
    const el = find('#wh-status');
    if (!el) return;
    el.textContent = text;
    el.setAttribute('role', error ? 'alert' : 'status');
  };
  const setBusy = value => {
    busy = value;
    find('#wh-reload').disabled = value;
    find('#wh-add').disabled = value || targets.filter(t => !t.removed).length >= 10;
    find('#wh-save').disabled = value || !config || conflicted;
    for (const input of targetsHost.querySelectorAll('input, select, button')) input.disabled = value;
  };

  const capture = () => {
    for (const card of targetsHost.querySelectorAll('[data-target-index]')) {
      const index = Number(card.dataset.targetIndex);
      const t = targets[index];
      if (!t) continue;
      const val = selector => card.querySelector(selector);
      t.expanded = card.open;
      t.name = val('[data-field="name"]').value.trim();
      t.enabled = val('[data-field="enabled"]').checked;
      t.allowPrivate = val('[data-field="allow-private"]').checked;
      t.format = val('[data-field="format"]').value;
      t.timeoutSeconds = Number(val('[data-field="timeout"]').value);
      t.url = val('[data-field="url"]').value.trim();
      t.clearUrl = val('[data-field="clear-url"]').checked;
      t.events = Array.from(card.querySelectorAll('[data-event]')).filter(input => input.checked).map(input => input.dataset.event);
    }
  };

  const cardFor = (target, index) => {
    const section = document.createElement('details');
    section.className = 'panel disclosure webhook-target';
    section.open = target.expanded ?? target.isNew;
    section.dataset.targetIndex = String(index);
    section.innerHTML = `
      <summary class="disclosure-summary"><span data-target-summary></span></summary>
      <div class="disclosure-body">
      <div class="panel-head"><h3>目标 ${index + 1}${target.isNew ? '（新建）' : ''}</h3><button class="btn btn-sm" type="button" data-remove>删除目标</button></div>
      <div class="field-grid">
        <div class="field full"><label>名称</label><input data-field="name" maxlength="80" autocomplete="off" placeholder="例如：家庭通知机器人"></div>
        <label class="check"><input data-field="enabled" type="checkbox">启用该目标</label>
        <label class="check"><input data-field="allow-private" type="checkbox">允许受信任的内网 / 本机接收端</label>
        <div class="field"><label>编码格式</label><select data-field="format"><option value="json">JSON · SmartStrm</option><option value="form">Emby 表单 · data=&lt;JSON&gt;</option></select></div>
        <div class="field"><label>超时（秒，1–30）</label><input data-field="timeout" type="number" min="1" max="30" step="1" required></div>
        <div class="field full"><label>接收地址（含 token，请妥善保密）</label>
          <input data-field="url" type="password" maxlength="4096" autocomplete="new-password" spellcheck="false" placeholder="输入新的完整 HTTP(S) 地址；留空保留">
          <span class="hint">${target.urlConfigured ? '已保存地址（不回显）；留空保留，输入新地址替换。' : '尚未配置接收地址。'}</span>
          <label class="check"><input data-field="clear-url" type="checkbox" ${target.urlConfigured ? '' : 'disabled'}>清除已保存地址</label>
        </div>
      </div>
      <fieldset style="margin:14px 0 0;padding:12px;min-width:0;border:1px solid var(--line)"><legend>事件订阅（至少一项）</legend>
        <div class="field-grid">${events.map(([name, label]) => `<label class="check"><input type="checkbox" data-event="${name}"><span>${label}<br><small class="mono">${name}</small></span></label>`).join('')}</div>
      </fieldset></div>`;
    const set = (selector, value) => { section.querySelector(selector).value = value; };
    const check = (selector, value) => { section.querySelector(selector).checked = value; };
    set('[data-field="name"]', target.name);
    set('[data-field="url"]', target.url || '');
    check('[data-field="enabled"]', target.enabled);
    check('[data-field="allow-private"]', target.allowPrivate);
    set('[data-field="format"]', target.format);
    set('[data-field="timeout"]', String(target.timeoutSeconds));
    check('[data-field="clear-url"]', target.clearUrl);
    for (const input of section.querySelectorAll('[data-event]')) input.checked = target.events.includes(input.dataset.event);
    const updateSummary = () => {
      const name = section.querySelector('[data-field="name"]').value.trim() || `目标 ${index + 1}`;
      const enabled = section.querySelector('[data-field="enabled"]').checked;
      const count = Array.from(section.querySelectorAll('[data-event]')).filter(input => input.checked).length;
      section.querySelector('[data-target-summary]').textContent = `${name} · ${enabled ? '已启用' : '未启用'} · ${target.urlConfigured ? '地址已保存' : '地址未保存'} · ${count} 项订阅${target.isNew ? ' · 新建' : ''}`;
    };
    section.addEventListener('input', updateSummary);
    section.addEventListener('change', updateSummary);
    updateSummary();
    section.querySelector('[data-remove]').addEventListener('click', () => {
      if (busy) return;
      capture();
      target.removed = true;
      render();
    });
    return section;
  };

  const updateDeletePreview = () => {
    const removed = targets.filter(t => t.removed && !t.isNew);
    const preview = find('#wh-remove-preview');
    preview.hidden = removed.length === 0;
    preview.textContent = removed.length ? `保存后将删除：${removed.map(t => t.name || t.id).join('、')}` : '';
    find('#wh-confirm-remove-row').hidden = removed.length === 0;
    find('#wh-confirm-empty-row').hidden = targets.filter(t => !t.removed).length !== 0;
    find('#wh-empty').hidden = targets.filter(t => !t.removed).length !== 0;
    if (!removed.length) find('#wh-confirm-remove').checked = false;
    if (targets.filter(t => !t.removed).length !== 0) find('#wh-confirm-empty').checked = false;
  };

  const render = () => {
    targetsHost.innerHTML = '';
    let shown = 0;
    targets.forEach((target, index) => {
      if (target.removed) return;
      targetsHost.appendChild(cardFor(target, index));
      shown++;
    });
    updateDeletePreview();
    find('#wh-add').disabled = busy || shown >= 10;
  };

  function valid(data) {
    return data && typeof data.config_etag === 'string' && data.config_etag.length > 0 &&
      Array.isArray(data.targets) && data.targets.length <= 10 &&
      data.targets.every(t => t && typeof t.id === 'string' && t.id.length > 0 && typeof t.name === 'string' &&
        t.name.length > 0 && t.name.length <= 80 && typeof t.url_configured === 'boolean' &&
        Array.isArray(t.events) && t.events.length > 0 && t.events.every(name => events.some(([known]) => name === known)) &&
        ['json', 'form'].includes(t.format) && Number.isInteger(t.timeout_seconds) &&
        t.timeout_seconds >= 1 && t.timeout_seconds <= 30);
  }

  const load = async () => {
    if (busy || !current()) return;
    setBusy(true);
    status('正在加载通知目标…');
    try {
      const data = await apiRequest('/webhook/targets');
      if (!current()) return;
      if (!valid(data)) throw Error('invalid response');
      config = data;
      conflicted = false;
      targets = (data.targets || []).map(t => ({
        id: t.id, name: t.name, enabled: !!t.enabled, url: '', urlConfigured: !!t.url_configured,
        clearUrl: false, events: Array.isArray(t.events) ? t.events.slice() : [],
        format: t.format, timeoutSeconds: Number(t.timeout_seconds), allowPrivate: !!t.allow_private,
        isNew: false, removed: false
      }));
      render();
      find('#wh-confirm-delete').checked = false;
      status(data.multi_target ? '多目标配置已加载。保存不会发送测试通知。' : '旧单目标配置已加载；首次保存后升级为多目标，旧单目标保存接口将停用。');
    } catch {
      if (current()) status('通知目标加载失败，未覆盖当前输入；请重新加载。', true);
    } finally { if (current()) setBusy(false); }
  };

  find('#wh-reload').addEventListener('click', () => {
    if (busy) return;
    capture();
    if (config && !confirm('重新加载将丢弃当前 Webhook 草稿。继续？')) return;
    return load();
  });
  find('#wh-add').addEventListener('click', () => {
    if (busy) return;
    capture();
    if (targets.filter(t => !t.removed).length >= 10) { status('最多只能配置 10 个目标。', true); return; }
    targets.push({ id: '', name: '', enabled: false, url: '', urlConfigured: false, clearUrl: false,
      events: [], format: 'json', timeoutSeconds: 5, allowPrivate: false, isNew: true, removed: false });
    render();
  });
  find('#wh-save').addEventListener('click', async () => {
    if (busy || !config || conflicted || !current()) return;
    capture();
    const active = targets.filter(t => !t.removed);
    if (active.length === 0 && !find('#wh-confirm-empty').checked) {
      status('清空全部目标需要先勾选确认。', true); return;
    }
    for (const t of active) {
      if (!t.name) { status('每个目标都需要名称。', true); return; }
      if (!t.events.length) { status(`目标「${t.name}」至少要选择一个事件。`, true); return; }
      if (!Number.isInteger(t.timeoutSeconds) || t.timeoutSeconds < 1 || t.timeoutSeconds > 30) {
        status(`目标「${t.name}」的超时必须是 1–30 秒的整数。`, true); return;
      }
      if (t.enabled && !t.url && !(t.urlConfigured && !t.clearUrl)) {
        status(`目标「${t.name}」启用前需要接收地址。`, true); return;
      }
      if (t.clearUrl && t.url) { status(`目标「${t.name}」不能同时清除与替换地址。`, true); return; }
      if (t.clearUrl && t.enabled) { status(`目标「${t.name}」清除地址前必须关闭启用。`, true); return; }
    }
    const wasDeleteSubscribed = target => {
      const loaded = config && Array.isArray(config.targets)
        ? config.targets.find(item => item.id === target.id) : null;
      return !!(loaded && loaded.enabled && Array.isArray(loaded.events) && loaded.events.includes('item.deleted'));
    };
    if (active.some(t => t.enabled && t.events.includes('item.deleted') && !wasDeleteSubscribed(t))
      && !find('#wh-confirm-delete').checked) {
      // 服务端仍会独立校验，这里先拦截以免无谓的 400 往返。
      status('启用 item.deleted 订阅前，请勾选删除风险确认。', true); return;
    }
    if (targets.some(t => t.removed && !t.isNew) && !find('#wh-confirm-remove').checked) {
      status('请先确认删除清单。', true); return;
    }
    const draft = {
      targets: active.map(t => {
        const out = { name: t.name, enabled: t.enabled, events: t.events, format: t.format,
          timeout_seconds: t.timeoutSeconds, allow_private: t.allowPrivate, clear_url: t.clearUrl };
        if (!t.isNew) out.id = t.id;
        if (t.url) out.url = t.url;
        return out;
      }),
      confirm_delete: find('#wh-confirm-delete').checked,
      confirm_empty_targets: find('#wh-confirm-empty').checked
    };
    setBusy(true);
    status('正在保存；不会发送测试通知…');
    try {
      const data = await apiRequest('/webhook/targets', { method: 'PUT', headers: { 'If-Match': config.config_etag }, body: JSON.stringify(draft) });
      if (!current()) return;
      if (!valid(data)) throw Error('invalid response');
      config = data;
      conflicted = false;
      targets = (data.targets || []).map(t => ({
        id: t.id, name: t.name, enabled: !!t.enabled, url: '', urlConfigured: !!t.url_configured,
        clearUrl: false, events: t.events.slice(), format: t.format,
        timeoutSeconds: Number(t.timeout_seconds), allowPrivate: !!t.allow_private, isNew: false, removed: false
      }));
      render();
      find('#wh-confirm-delete').checked = false;
      find('#wh-confirm-remove').checked = false;
      find('#wh-confirm-empty').checked = false;
      status('Webhook 目标已保存并即时生效，重启保留。未发送测试通知。');
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
