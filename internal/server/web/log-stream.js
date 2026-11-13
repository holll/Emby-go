/* Raw disk logs over authenticated fetch SSE. Writes remain regular HTTP operations. */
(function (global) {
  'use strict';

  function createSSEParser(receive) {
    let pending = '', event = '', data = [];
    function line(value) {
      if (!value) {
        if (data.length) receive(event || 'message', data.join('\n'));
        event = ''; data = [];
        return;
      }
      if (value[0] === ':') return;
      const colon = value.indexOf(':');
      const name = colon < 0 ? value : value.slice(0, colon);
      let content = colon < 0 ? '' : value.slice(colon + 1);
      if (content[0] === ' ') content = content.slice(1);
      if (name === 'event') event = content;
      if (name === 'data') data.push(content);
    }
    return chunk => {
      pending += chunk;
      let start = 0;
      for (let i = 0; i < pending.length; i++) {
        if (pending[i] !== '\n' && pending[i] !== '\r') continue;
        if (pending[i] === '\r' && i === pending.length - 1) break;
        line(pending.slice(start, i));
        if (pending[i] === '\r' && pending[i + 1] === '\n') i++;
        start = i + 1;
      }
      pending = pending.slice(start);
    };
  }

  function mountLogStream(host, options) {
    const doc = host.ownerDocument, identity = options.token;
    let disposed = false, paused = false, terminal = false, generation = 0;
    let controller = null, timer = null, failures = 0, kind = 'app', cursor = '';
    let text = '', gapNote = '', clipped = false;
    const limit = 512 * 1024;
    host.innerHTML = `<section class="panel" aria-labelledby="dl-title"><div class="panel-head"><h2 id="dl-title">实时日志</h2><div class="row-actions"><label for="dl-kind">日志文件</label><select id="dl-kind"><option value="app">app.log</option><option value="request">requests.log</option></select><button id="dl-pause" class="btn" type="button" aria-pressed="false">暂停</button><button id="dl-refresh" class="btn" type="button">重新连接</button></div></div><p class="hint" id="dl-source"></p><p class="hint">直接显示磁盘原文，不解析或脱敏。requests.log 可能包含敏感 URL，请勿公开分享。</p><p id="dl-status" role="status" aria-atomic="true">正在连接…</p><p id="dl-gap" class="hint"></p><div id="dl-scroll" class="disk-log-scroll" tabindex="0" role="region" aria-label="原始日志"><pre id="dl-body" class="dl-raw-log"></pre></div></section>`;
    const el = name => host.querySelector('#dl-' + name);
    const body = el('body'), scroll = el('scroll'), status = el('status');
    const identityMatches = () => global.localStorage.getItem('emby_token') === identity;
    const current = () => !disposed && options.current() && identityMatches();
    const message = value => { status.textContent = value; };
    const source = () => { el('source').textContent = kind === 'app' ? '来源：app-YYYY-MM-DD.log（按日期轮转）' : '来源：request-YYYY-MM-DD.log（按日期轮转）'; };
    function disconnect() {
      generation++;
      global.clearTimeout(timer); timer = null;
      if (controller) { controller.abort(); controller = null; }
    }
    function identityChanged() {
      if (identityMatches()) return;
      disconnect(); terminal = true; text = ''; body.textContent = '';
      message('登录身份已变更，日志连接已关闭；请重新打开页面。');
    }
    function denied() {
      terminal = true; message('登录失效或权限不足，日志连接已停止。');
      options.onUnauthorized();
    }
    function render(data) {
      if (data.kind !== kind || typeof data.text !== 'string' || typeof data.cursor !== 'string' || typeof data.reset !== 'boolean' || typeof data.gap !== 'boolean') throw new Error('日志响应格式无效');
      const atEnd = scroll.scrollHeight - scroll.scrollTop - scroll.clientHeight < 40;
      if (data.reset) { text = ''; gapNote = '日志已轮转或清理，已重新读取当前尾部；缺口无法补读。'; clipped = false; }
      else if (data.gap) gapNote = '仅加载当前日志尾部，较早内容未包含在本次显示中。';
      text += data.text;
      if (text.length > limit) {
        let offset = text.length - limit;
        // Never split a UTF-16 surrogate pair while bounding the visible tail.
        if (text.charCodeAt(offset) >= 0xdc00 && text.charCodeAt(offset) <= 0xdfff) offset++;
        text = text.slice(offset); clipped = true;
      }
      body.textContent = text;
      cursor = data.cursor;
      el('gap').textContent = [gapNote, clipped ? '显示长度已达上限，仅保留最近内容；磁盘文件不受影响。' : ''].filter(Boolean).join(' ');
      if (atEnd || data.reset) scroll.scrollTop = scroll.scrollHeight;
      failures = 0; message('已连接 · 原文实时更新');
    }
    async function connect() {
      if (!current()) { identityChanged(); return; }
      if (paused || doc.hidden || terminal) return;
      disconnect(); const ownGeneration = generation;
      const active = new global.AbortController(); controller = active;
      message('正在连接…');
      let reader;
      try {
        const after = cursor ? '&after=' + encodeURIComponent(cursor) : '';
        const response = await global.fetch('/api/admin/logs/stream?kind=' + kind + after, { headers: { 'X-Emby-Token': identity }, signal: active.signal, cache: 'no-store' });
        if (!current() || ownGeneration !== generation) return;
        if (response.status === 401 || response.status === 403) { denied(); return; }
        if (!response.ok) {
          if (response.status === 400) {
            const error = await response.json();
            if (!current() || ownGeneration !== generation) return;
            if (error.code === 'LOGS_CURSOR_INVALID' && cursor) { cursor = ''; gapNote = '日志游标已失效，将重新读取当前尾部。'; text = ''; body.textContent = ''; el('gap').textContent = gapNote; }
            else { terminal = true; message('日志请求被拒绝，请重新打开页面。'); throw new Error('日志请求被拒绝'); }
          } else throw new Error('日志服务暂不可用');
        } else {
          if (!response.headers.get('Content-Type')?.toLowerCase().startsWith('text/event-stream')) throw new Error('日志响应格式无效');
          const parse = createSSEParser((event, raw) => {
            if (!current() || ownGeneration !== generation) return;
            if (event === 'logs') render(JSON.parse(raw));
            else if (event === 'error') {
              const error = JSON.parse(raw);
              if (['AUTH_INVALID', 'ADMIN_REQUIRED', 'UNAUTHORIZED', 'FORBIDDEN'].includes(error.code)) denied();
              throw new Error('日志流已中断');
            }
          });
          reader = response.body.getReader();
          const decoder = new global.TextDecoder();
          while (current() && ownGeneration === generation) {
            const part = await reader.read();
            if (!current() || ownGeneration !== generation) break;
            if (part.done) break;
            parse(decoder.decode(part.value, { stream: true }));
          }
        }
      } catch (_) {
        if (current() && ownGeneration === generation && !terminal) message('连接中断，保留已显示原文，正在重试…');
      } finally {
        if (reader) { try { await reader.cancel(); } catch (_) { /* A failed network stream may already be closed. */ } }
        active.abort();
        if (controller === active) controller = null;
        if (!identityMatches()) identityChanged();
        if (current() && ownGeneration === generation && !paused && !doc.hidden && !terminal) {
          failures++; timer = global.setTimeout(connect, Math.min(30000, 1000 * 2 ** Math.min(failures - 1, 5)));
        }
      }
    }
    const onKind = () => {
      disconnect(); kind = el('kind').value; cursor = ''; text = ''; gapNote = ''; clipped = false;
      body.textContent = ''; el('gap').textContent = ''; source();
      if (!terminal && !paused) void connect();
    };
    const onPause = () => {
      paused = !paused; disconnect();
      el('pause').textContent = paused ? '继续' : '暂停'; el('pause').setAttribute('aria-pressed', String(paused));
      if (paused) message('已暂停 · 保留当前原文'); else void connect();
    };
    const onRefresh = () => { disconnect(); failures = 0; if (!terminal && !paused) void connect(); };
    const onVisibility = () => {
      disconnect();
      if (!current()) { identityChanged(); return; }
      if (doc.hidden) message('页面在后台 · 已暂停日志连接');
      else if (!paused && !terminal) void connect();
    };
    el('kind').addEventListener('change', onKind);
    el('pause').addEventListener('click', onPause);
    el('refresh').addEventListener('click', onRefresh);
    doc.addEventListener('visibilitychange', onVisibility);
    global.addEventListener('storage', identityChanged);
    source(); void connect();
    return () => {
      disposed = true; disconnect();
      el('kind').removeEventListener('change', onKind);
      el('pause').removeEventListener('click', onPause);
      el('refresh').removeEventListener('click', onRefresh);
      doc.removeEventListener('visibilitychange', onVisibility);
      global.removeEventListener('storage', identityChanged);
    };
  }
  global.mountLogStream = mountLogStream;
  if (typeof module !== 'undefined' && module.exports) module.exports = { createSSEParser, mountLogStream };
})(typeof window === 'undefined' ? globalThis : window);
