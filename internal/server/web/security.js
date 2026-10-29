/* Administrator IP blocklist: persisted rules, process-local counters, no automatic mutations. */
(function (global) {
  'use strict';
  const esc = value => String(value ?? '').replace(/[&<>'"]/g, char => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', "'": '&#39;', '"': '&quot;' }[char]));
  function mountIPBlocklist(host, options) {
    const { apiRequest, isCurrent = () => true, confirmAction = message => global.confirm(message) } = options;
    let disposed = false, busy = false, timer = null, controller = null, serial = 0;
    const listeners = [], current = () => !disposed && host.isConnected !== false && isCurrent();
    host.innerHTML = `<section class="panel security-panel" aria-labelledby="security-title"><div class="panel-head"><h2 id="security-title">IP 黑名单</h2><button class="btn" type="button" data-security="refresh">刷新规则与计数</button></div>
      <p class="hint">规则持久保存并立即生效，适用于请求来源 IP。拒绝当前访问 IP、回环地址和全部地址，避免误封自己；反向代理环境请先核对可信代理配置。</p>
      <p data-security="summary" role="status" aria-live="polite">正在读取规则…</p><p data-security="client" class="hint"></p>
      <form class="field-grid" data-security="form"><div class="field"><label for="security-network">IP 或 CIDR</label><input id="security-network" data-security="network" required maxlength="80" autocomplete="off" placeholder="如 203.0.113.5 或 203.0.113.0/24"></div><div class="field"><label for="security-note">备注（可选，最多 200 字节）</label><input id="security-note" data-security="note" maxlength="200" autocomplete="off"></div><div class="form-foot"><button class="btn btn-danger" type="submit" data-security="add">确认后添加规则</button></div></form>
      <p data-security="error" role="alert" hidden></p><div data-security="rules"></div><p class="hint">拦截计数为本次服务运行期间的实时统计，重启归零；每 5 秒刷新，后台标签页暂停。规则数量不代表被拦截请求数量。</p></section>`;
    const el = key => host.querySelector('[data-security="' + key + '"]');
    const listen = (node, type, fn) => { node.addEventListener(type, fn); listeners.push(() => node.removeEventListener(type, fn)); };
    const fail = text => { if (!current()) return; el('error').textContent = text; el('error').hidden = false; };
    const controls = disabled => { el('add').disabled = el('network').disabled = el('note').disabled = disabled; el('rules').querySelectorAll('button').forEach(button => { button.disabled = disabled; }); };
    const schedule = () => { clearTimeout(timer); if (current() && !document.hidden && !busy) timer = setTimeout(() => { void load(); }, 5000); };
    async function request(method, body) {
      controller?.abort(); const active = new AbortController(), sequence = ++serial; controller = active;
      let timedOut = false;
      const deadline = setTimeout(() => { timedOut = true; active.abort(); }, 10000);
      try {
        const value = await apiRequest('/security/ip-blocklist', { method, ...(body ? { body: JSON.stringify(body) } : {}), signal: active.signal });
        if (sequence !== serial) throw Object.assign(new Error('cancelled'), { name: 'AbortError' });
        return value;
      } catch (error) { if (timedOut) throw new Error('request timed out'); throw error; }
      finally { clearTimeout(deadline); if (controller === active) controller = null; }
    }
    async function load() {
      clearTimeout(timer); if (!current() || busy || document.hidden) return;
      try {
        const data = await request('GET'); if (!current() || busy) return;
        if (!Array.isArray(data.items)) throw new Error('invalid response');
        el('summary').textContent = `${data.items.length} / ${data.max_rules || 256} 条规则 · 本次运行已拦截 ${Number(data.total_blocked) || 0} 次`;
        el('client').textContent = `当前访问 IP：${data.client_ip || '未提供'} · 计数开始：${data.stats_since || '未提供'}`;
        el('rules').innerHTML = data.items.length ? `<div class="table-wrap security-table"><table><thead><tr><th>IP / CIDR</th><th>备注</th><th>添加时间</th><th>本次拦截</th><th>操作</th></tr></thead><tbody>${data.items.map(item => `<tr><td data-label="IP / CIDR" class="mono">${esc(item.network)}</td><td data-label="备注">${esc(item.note || '—')}</td><td data-label="添加时间">${esc(item.created_at || '—')}</td><td data-label="本次拦截">${esc(item.blocked ?? 0)}</td><td data-label="操作"><button type="button" class="btn" data-network="${esc(item.network)}">移除</button></td></tr>`).join('')}</tbody></table></div>` : '<p class="hint">暂无规则，当前没有通过此黑名单拦截的地址。</p>';
      } catch (error) { if (current() && error.name !== 'AbortError') fail('规则或计数读取失败，显示的旧数据可能已过期；请刷新重试。'); }
      finally { schedule(); }
    }
    async function mutate(method, body) {
      if (!current() || busy) return;
      busy = true; clearTimeout(timer); controller?.abort(); serial++; controls(true); el('error').hidden = true;
      try {
        await request(method, body); if (!current()) return;
        if (method === 'POST') { el('network').value = ''; el('note').value = ''; }
        el('summary').textContent = method === 'POST' ? '规则已保存，正在重新核对列表…' : '规则已移除，正在重新核对列表…';
      } catch (error) {
        if (!current()) return;
        const known = error.status >= 400 && error.status < 500;
        fail(known ? ({ 400: 'IP / CIDR 或备注格式不正确，请核对输入。', 403: '权限不足，请使用管理员账户。', 409: '规则与已有范围重叠，或可能误封当前 IP / 回环 / 全部地址，未添加。' }[error.status] || '操作被服务器拒绝，请刷新规则后核对。') : '保存或移除结果未确认，输入已保留。请先刷新核对规则，勿重复提交；不会自动重试。');
      } finally { busy = false; if (current()) { controls(false); await load(); } }
    }
    listen(el('form'), 'submit', event => {
      event.preventDefault(); if (!current() || busy) return;
      const network = el('network').value.trim(), note = el('note').value.trim();
      if (!network || network.length > 80 || new TextEncoder().encode(note).length > 200 || /[\u0000-\u001f\u007f]/.test(note)) { fail('请输入 IP 或 CIDR，备注最多 200 UTF-8 字节，且不能含控制字符。'); return; }
      if (!confirmAction(`确认封禁 ${network}？\n规则持久保存并立即生效，匹配地址将无法访问服务。请确认不是当前访问 IP、回环或全部地址。`)) return;
      void mutate('POST', { network, note });
    });
    listen(el('rules'), 'click', event => {
      const button = event.target.closest('button[data-network]'); if (!button || !current() || busy) return;
      const network = button.dataset.network;
      if (!confirmAction(`确认移除黑名单规则 ${network}？\n匹配来源将不再被此规则拦截。`)) return;
      void mutate('DELETE', { network });
    });
    listen(el('refresh'), 'click', () => { if (!busy) { el('error').hidden = true; void load(); } });
    const visibility = () => { clearTimeout(timer); if (!document.hidden) void load(); };
    listen(document, 'visibilitychange', visibility);
    void load();
    return () => { disposed = true; serial++; clearTimeout(timer); controller?.abort(); listeners.forEach(remove => remove()); };
  }
  global.mountIPBlocklist = mountIPBlocklist;
})(typeof window === 'undefined' ? globalThis : window);
