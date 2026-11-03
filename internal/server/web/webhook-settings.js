'use strict';

// 管理员多目标 Webhook 面板。地址明文回显，仅保存在当前页面内存。
// 保存时空白 URL 仍表示保留，clear_url 才清除。整集合替换必须带 ETag。
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
    <p class="hint wh-intro">每个目标可单独开关并订阅事件。接收地址明文显示，可能包含 token，请勿向他人分享页面截图。</p>
    <p class="hint wh-delivery" role="note"><strong>投递机制：</strong>同步投递，无自动重试或持久队列；部分目标失败不影响业务结果。</p>
    <div id="wh-targets"></div>
    <p id="wh-empty" class="hint" hidden>当前没有通知目标；保存空集合会停止所有 Webhook 投递。</p>
    <div id="wh-remove-preview" class="warning-block" role="alert" hidden></div>
    <label class="check" id="wh-confirm-remove-row" hidden><input id="wh-confirm-remove" type="checkbox">我确认上述目标将被删除（已有地址一并移除）。</label>
    <label class="check" id="wh-confirm-empty-row" hidden><input id="wh-confirm-empty" type="checkbox">我确认清空全部 Webhook 目标，停止所有通知。</label>
    <div class="wh-delete-risk">
      <p id="wh-delete-risk" class="warning-block" role="note"><strong>删除风险：</strong>item.deleted 可能让 SmartStrm 删除远端整个目录，包括同目录其它影片，且不可撤销。</p>
      <label class="check"><input id="wh-confirm-delete" type="checkbox" aria-describedby="wh-delete-risk">对新增的删除订阅，我已理解并确认上述风险。</label>
    </div>
    <p id="wh-save-impact" class="hint">保存影响：整集合原子替换，即时生效、重启保留；只保存配置，不会发送测试通知。</p>
    <p id="wh-status" class="hint" role="status" aria-live="polite">正在加载通知目标…</p>
    <div class="form-foot"><button id="wh-add" class="btn btn-sm" type="button">+ 添加目标</button><button id="wh-save" class="btn btn-accent" type="button" disabled>保存全部目标</button></div>
    <dialog id="wh-dialog" class="wh-dialog" aria-labelledby="wh-dialog-title" aria-describedby="wh-dialog-hint">
      <form id="wh-dialog-form" novalidate>
        <div class="wh-dialog-head"><h2 id="wh-dialog-title">添加 Webhook 目标</h2><button id="wh-dialog-close" type="button" class="btn btn-sm" aria-label="关闭添加 Webhook 目标">关闭</button></div>
        <div class="wh-dialog-scroll"><p id="wh-dialog-hint" class="hint">地址明文显示。可先点击“发送测试消息”验证，再添加到草稿并保存。添加和保存不会自动发送通知。</p><div id="wh-dialog-body"></div><p id="wh-dialog-status" class="hint" role="alert" aria-live="polite"></p></div>
        <div class="wh-dialog-foot"><button id="wh-dialog-cancel" type="button" class="btn">取消</button><button id="wh-dialog-submit" type="submit" class="btn btn-accent">添加到草稿</button></div>
      </form>
    </dialog>`;

  const find = selector => host.querySelector(selector);
  const targetsHost = find('#wh-targets');
  let config = null, busy = false, conflicted = false, activeTest = null;
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
    find('#wh-add').disabled = value || !config || conflicted || targets.filter(t => !t.removed).length >= 10;
    find('#wh-save').disabled = value || !config || conflicted;
    find('#wh-dialog-submit').disabled = value;
    for (const input of targetsHost.querySelectorAll('input, select, button')) input.disabled = value;
    for (const card of targetsHost.querySelectorAll('[data-target-index]')) {
      const t = targets[Number(card.dataset.targetIndex)];
      card.querySelector('[data-field="clear-url"]').disabled = value || !t.urlConfigured;
      card.querySelector('[data-field="url"]').disabled = value || t.clearUrl;
    }
  };

  const readTarget = (card, t) => {
    const val = selector => card.querySelector(selector);
    t.expanded = card.open;
    t.name = val('[data-field="name"]').value.trim();
    t.enabled = val('[data-field="enabled"]').checked;
    t.allowPrivate = val('[data-field="allow-private"]').checked;
    t.format = val('[data-field="format"]').value;
    t.timeoutSeconds = Number(val('[data-field="timeout"]').value);
    t.clearUrl = val('[data-field="clear-url"]').checked;
    t.url = t.clearUrl ? '' : val('[data-field="url"]').value.trim();
    t.events = Array.from(card.querySelectorAll('[data-event]')).filter(input => input.checked).map(input => input.dataset.event);
  };
  const capture = () => {
    for (const card of targetsHost.querySelectorAll('[data-target-index]')) {
      const t = targets[Number(card.dataset.targetIndex)];
      if (t) readTarget(card, t);
    }
  };
  const targetError = (t, requireURL = false) => {
    if (!t.name || [...t.name].length > 80) return '每个目标都需要 1–80 个字符的名称。';
    if (!t.events.length) return `目标「${t.name}」至少要选择一个事件。`;
    if (!Number.isInteger(t.timeoutSeconds) || t.timeoutSeconds < 1 || t.timeoutSeconds > 30) return `目标「${t.name}」的超时必须是 1–30 秒的整数。`;
    if (requireURL && !t.url) return '请输入完整的 HTTP(S) 接收地址。';
    if (t.url) {
      try {
        const url = new URL(t.url);
        if (!['http:', 'https:'].includes(url.protocol) || !url.hostname || t.url.length > 4096) throw Error('invalid URL');
      } catch { return `目标「${t.name}」的接收地址必须是完整的 HTTP(S) 地址。`; }
    }
    if (t.enabled && !t.url && !(t.urlConfigured && !t.clearUrl)) return `目标「${t.name}」启用前需要接收地址。`;
    if (t.clearUrl && t.enabled) return `目标「${t.name}」清除地址前必须关闭启用。`;
    return '';
  };

  const runTest = async (card, target) => {
    if (busy || activeTest || !current()) return;
    const button = card.querySelector('[data-test]');
    const feedback = card.querySelector('[data-test-status]');
    const report = (text, error = false) => {
      feedback.textContent = text;
      feedback.setAttribute('role', error ? 'alert' : 'status');
    };
    const field = name => card.querySelector(`[data-field="${name}"]`);
    const saved = config?.targets.find(t => t.id === target.id);
    const draft = { url: field('url').value.trim() || (!target.isNew ? saved?.url || '' : ''),
      format: field('format').value, timeout_seconds: Number(field('timeout').value), allow_private: field('allow-private').checked };
    if (field('clear-url').checked) { report('已选择清除地址，请取消清除并填写接收地址后再测试。', true); return; }
    try {
      const url = new URL(draft.url);
      if (!['http:', 'https:'].includes(url.protocol) || !url.hostname || draft.url.length > 4096) throw Error('invalid URL');
    } catch { report('测试需要完整的 HTTP(S) 接收地址。', true); return; }
    if (!['json', 'form'].includes(draft.format) || !Number.isInteger(draft.timeout_seconds) || draft.timeout_seconds < 1 || draft.timeout_seconds > 30) {
      report('请选择有效的编码格式，超时须为 1–30 秒的整数。', true); return;
    }
    capture(); // Preserve other targets' current clear/retain state when controls are restored.
    const controller = new AbortController();
    const controls = Array.from(card.querySelectorAll('input, select, button'));
    const disabled = controls.map(el => el.disabled);
    let observer;
    const restore = () => {
      observer?.disconnect();
      globalThis.removeEventListener?.('pagehide', request.cancel);
      globalThis.removeEventListener?.('hashchange', request.cancel);
      controls.forEach((el, i) => { el.disabled = disabled[i]; });
      button.textContent = '发送测试消息';
      if (current()) setBusy(false);
    };
    const request = { card, cancel() {
      if (activeTest !== request) return;
      activeTest = null;
      controller.abort();
      restore();
      if (current()) status('测试请求已取消；接收端可能已收到消息，不会自动重发。');
    } };
    activeTest = request;
    globalThis.addEventListener?.('pagehide', request.cancel);
    globalThis.addEventListener?.('hashchange', request.cancel);
    if (typeof MutationObserver !== 'undefined' && document.documentElement) {
      observer = new MutationObserver(() => { if (!current()) request.cancel(); });
      observer.observe(document.documentElement, { childList: true, subtree: true });
    }
    setBusy(true);
    controls.forEach(el => { el.disabled = true; });
    if (card === draftCard && dialog.open) find('#wh-dialog-close').focus({ preventScroll: true });
    button.textContent = '正在发送…';
    report('正在发送 webhook.test 测试消息；仅本次发送，不保存配置、不自动重试。');
    try {
      const result = await apiRequest('/webhook/test', { method: 'POST', body: JSON.stringify(draft), signal: controller.signal });
      if (!current() || activeTest !== request) return;
      if (!result || !['sent', 'failed'].includes(result.status) || !Number.isFinite(result.duration_ms) || result.duration_ms < 0) throw Error('invalid response');
      const elapsed = `${Math.round(result.duration_ms)} ms`;
      if (result.status === 'sent') {
        report(`测试消息已获接收端确认（${elapsed}）。未保存配置；仅验证本次接收，不代表业务事件订阅或远端操作成功。`);
      } else {
        const reasons = { timeout: '发送超时', canceled: '请求已取消', ignored: '接收端忽略了测试消息，可能不支持 webhook.test 事件',
          read_response: '读取接收确认失败', response_too_large: '接收确认超过大小限制', request_failed: '连接失败，请核对地址、网络或内网访问权限', busy: '已有测试正在执行，请稍后手动重试' };
        const code = typeof result.error_code === 'string' ? result.error_code : '';
        const reason = /^status_[1-5]\d\d$/.test(code) ? `接收端返回 HTTP ${code.slice(7)}` : reasons[code] || '未能确认接收结果';
        report(`测试未通过（${elapsed}）：${reason}。未保存配置，不会自动重发。`, true);
      }
    } catch (error) {
      if (!current() || activeTest !== request) return;
      const reason = error.status === 401 || error.status === 403 ? '登录失效或无管理员权限' : error.status === 400 ? '测试参数无效，请核对地址和发送设置' : '请求结果未知，请核对网络和接收端记录';
      report(`测试未确认：${reason}。接收端可能已收到消息，不会自动重发。`, true);
    } finally {
      if (activeTest === request) { activeTest = null; restore(); }
    }
  };

  const cardFor = (target, index, editor = false) => {
    const section = document.createElement(editor ? 'div' : 'details');
    section.className = editor ? 'wh-target-editor' : 'panel disclosure webhook-target';
    section.open = target.expanded ?? target.isNew;
    if (!editor) section.dataset.targetIndex = String(index);
    section.innerHTML = `
      ${editor ? '' : '<summary class="disclosure-summary"><span data-target-summary></span></summary>'}
      <div class="disclosure-body">
      ${editor ? '' : `<div class="wh-target-actions"><button class="btn btn-sm" type="button" data-remove>删除目标 ${index + 1}</button></div>`}
      <div class="field-grid">
        <div class="field full"><label for="wh-${index}-name">名称</label><input id="wh-${index}-name" data-field="name" maxlength="80" autocomplete="off" placeholder="例如：家庭通知机器人"></div>
        <label class="check" for="wh-${index}-enabled"><input id="wh-${index}-enabled" data-field="enabled" type="checkbox">启用该目标</label>
        <label class="check" for="wh-${index}-allow-private"><input id="wh-${index}-allow-private" data-field="allow-private" type="checkbox">允许受信任的内网 / 本机接收端</label>
        <div class="field"><label for="wh-${index}-format">编码格式</label><select id="wh-${index}-format" data-field="format" aria-describedby="wh-${index}-format-hint"><option value="json">JSON 请求体</option><option value="form">表单 data（兼容模式）</option></select><span id="wh-${index}-format-hint" class="hint">两种方式发送相同的事件 JSON，仅 HTTP 包装方式不同：JSON 直接作为请求体；表单将 JSON 放入 data 字段。请按接收端要求选择。</span></div>
        <div class="field"><label for="wh-${index}-timeout">超时（秒，1–30）</label><input id="wh-${index}-timeout" data-field="timeout" type="number" min="1" max="30" step="1" required></div>
        <div class="field full"><label for="wh-${index}-url">接收地址（含 token，请妥善保密）</label>
          <input id="wh-${index}-url" data-field="url" type="text" inputmode="url" maxlength="4096" autocomplete="off" autocapitalize="none" spellcheck="false" aria-describedby="wh-${index}-url-hint" placeholder="https://example.com/webhook?token=…">
          <span id="wh-${index}-url-hint" class="hint">${target.urlConfigured ? '已保存地址明文显示；可直接修改，留空仍保留旧地址。要删除地址，请勾选下方“清除已保存地址”。' : '请输入完整的 HTTP(S) 地址；地址及 token 将明文显示。'}</span>
          <label class="check" for="wh-${index}-clear-url"><input id="wh-${index}-clear-url" data-field="clear-url" type="checkbox" ${target.urlConfigured ? '' : 'disabled'}>清除已保存地址</label>
        </div>
      </div>
      <div class="wh-test-actions"><button type="button" class="btn btn-sm" data-test aria-describedby="wh-${index}-test-status">发送测试消息</button><p id="wh-${index}-test-status" data-test-status class="hint" role="status" aria-live="polite">按当前地址和编码发送独立的 webhook.test 消息，无需保存、启用或选择事件。不模拟删除等业务操作；接收端可能不支持测试事件。</p></div>
      <fieldset class="wh-events"><legend>事件订阅（至少一项）</legend>
        ${editor ? '<p id="wh-draft-delete-risk" class="warning-block">删除风险：item.deleted 可能让 SmartStrm 删除远端整个目录，且不可撤销。启用此订阅后，仍须在主页面确认删除风险才能保存。</p>' : ''}
        <p id="wh-${index}-progress-hint" class="hint wh-progress-hint">性能提示：playback.progress 高频同步投递，会增加播放上报延迟；仅在需要时订阅。</p>
        <div class="field-grid">${events.map(([name, label]) => `<label class="check" for="wh-${index}-${name}"><input id="wh-${index}-${name}" type="checkbox" data-event="${name}"${name === 'playback.progress' ? ` aria-describedby="wh-${index}-progress-hint"` : name === 'item.deleted' ? ` aria-describedby="${editor ? 'wh-draft-delete-risk' : 'wh-delete-risk'}"` : ''}><span>${label}<br><small class="mono">${name}</small></span></label>`).join('')}</div>
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
    section.querySelector('[data-test]').addEventListener('click', () => runTest(section, target));
    section.querySelector('[data-field="clear-url"]').addEventListener('change', () => {
      section.querySelector('[data-field="url"]').disabled = section.querySelector('[data-field="clear-url"]').checked;
    });
    if (editor) return section;
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
    find('#wh-add').disabled = busy || !config || conflicted || shown >= 10;
  };

  function valid(data) {
    return data && typeof data.config_etag === 'string' && data.config_etag.length > 0 &&
      Array.isArray(data.targets) && data.targets.length <= 10 &&
      data.targets.every(t => t && typeof t.id === 'string' && t.id.length > 0 && typeof t.name === 'string' &&
        t.name.length > 0 && t.name.length <= 80 && typeof t.url === 'string' && typeof t.url_configured === 'boolean' &&
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
        id: t.id, name: t.name, enabled: !!t.enabled, url: t.url, urlConfigured: !!t.url_configured,
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
  const dialog = find('#wh-dialog');
  const dialogBody = find('#wh-dialog-body');
  let draftTarget = null, draftCard = null;
  const closeDialog = () => dialog.close();
  dialog.addEventListener('keydown', event => {
    if (!dialog.open) return;
    if (event.key === 'Escape') { event.preventDefault(); event.stopPropagation(); closeDialog(); return; }
    if (event.key !== 'Tab') return;
    const controls = Array.from(dialog.querySelectorAll('button, input, select, textarea, a[href], [tabindex]:not([tabindex="-1"])'))
      .filter(el => !el.disabled && el.getClientRects().length > 0);
    const first = controls[0], last = controls[controls.length - 1];
    if (!first) { event.preventDefault(); return; }
    if (event.shiftKey && document.activeElement === first) {
      event.preventDefault(); last.focus();
    } else if (!event.shiftKey && document.activeElement === last) {
      event.preventDefault(); first.focus();
    }
    event.stopPropagation();
  });
  find('#wh-dialog-close').addEventListener('click', closeDialog);
  find('#wh-dialog-cancel').addEventListener('click', closeDialog);
  dialog.addEventListener('close', () => {
    if (activeTest?.card === draftCard) activeTest.cancel();
    draftTarget = null;
    draftCard = null;
    dialogBody.innerHTML = '';
    if (current()) find('#wh-add').focus({ preventScroll: true });
  });
  find('#wh-add').addEventListener('click', () => {
    if (busy || !config || conflicted || !current() || dialog.open) return;
    capture();
    if (targets.filter(t => !t.removed).length >= 10) { status('最多只能配置 10 个目标。', true); return; }
    draftTarget = { id: '', name: '', enabled: false, url: '', urlConfigured: false, clearUrl: false,
      events: [], format: 'json', timeoutSeconds: 5, allowPrivate: false, isNew: true, removed: false };
    draftCard = cardFor(draftTarget, 'draft', true);
    dialogBody.innerHTML = '';
    dialogBody.appendChild(draftCard);
    find('#wh-dialog-status').textContent = '';
    dialog.showModal();
    draftCard.querySelector('[data-field="name"]').focus();
  });
  find('#wh-dialog-form').addEventListener('submit', event => {
    event.preventDefault();
    if (busy || !config || conflicted || !current() || !dialog.open || !draftTarget || !draftCard) return;
    readTarget(draftCard, draftTarget);
    const error = targetError(draftTarget, true);
    if (error) { find('#wh-dialog-status').textContent = error; return; }
    if (targets.filter(t => !t.removed).length >= 10) { find('#wh-dialog-status').textContent = '最多只能配置 10 个目标。'; return; }
    capture();
    targets.push(draftTarget);
    render();
    status('新目标已加入草稿；请点击“保存全部目标”生效，尚未发送通知。');
    closeDialog();
  });
  find('#wh-save').addEventListener('click', async () => {
    if (busy || !config || conflicted || !current()) return;
    capture();
    const active = targets.filter(t => !t.removed);
    if (active.length === 0 && !find('#wh-confirm-empty').checked) {
      status('清空全部目标需要先勾选确认。', true); return;
    }
    for (const t of active) {
      const error = targetError(t);
      if (error) { status(error, true); return; }
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
        id: t.id, name: t.name, enabled: !!t.enabled, url: t.url, urlConfigured: !!t.url_configured,
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
