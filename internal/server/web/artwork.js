/* 独立封面维护：只发送候选身份，不接受自定义图片 URL。 */
(function (global) {
  'use strict';
  const escape = value => String(value ?? '').replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
  function candidateImage(c) {
    return '/api/admin/scrape/image?kind=' + (c.thumb ? 'thumb' : 'primary') + '&provider=' + encodeURIComponent(c.provider) + '&id=' + encodeURIComponent(c.id);
  }
  function mountArtwork(host, options) {
    const { apiRequest, movie, onComplete = () => {}, isCurrent = () => true } = options;
    const confirmAction = options.confirm || (message => global.confirm(message));
    let disposed = false, capabilities = {}, searchGeneration = 0, searching = false;
    const movieKey = String(movie.id);
    const listeners = [], imageControllers = new Set(), imageURLs = new Set();
    function clearImages() {
      for (const controller of imageControllers) { clearTimeout(controller.timeout); controller.abort(); }
      imageControllers.clear();
      for (const url of imageURLs) global.URL.revokeObjectURL(url);
      imageURLs.clear();
    }
    async function loadImage(node, candidate, generation) {
      if (!options.imageRequest) return;
      const image = node.querySelector('img'), controller = new AbortController();
      imageControllers.add(controller);
      const timeout = setTimeout(() => controller.abort(), 10000); controller.timeout = timeout;
      try {
        const blob = await options.imageRequest(candidateImage(candidate), { signal: controller.signal });
        if (!current() || generation !== searchGeneration || controller.signal.aborted) return;
        if (!/^image\/(?:png|jpeg|webp|gif|avif)$/i.test(blob.type)) throw new Error('预览格式不可用');
        const url = global.URL.createObjectURL(blob); imageURLs.add(url); image.src = url;
      } catch (_) { if (current() && generation === searchGeneration) image.alt = '候选封面预览不可用'; }
      finally { clearTimeout(timeout); imageControllers.delete(controller); }
    }
    const current = () => !disposed && host.isConnected !== false && isCurrent();
    host.innerHTML = `<section class="artwork-panel" aria-label="独立封面维护">
      <header><h3>封面维护 <small>不修改元数据</small></h3><p class="hint">截图补图或从候选换源，仅处理本片封面；不会触发刮削、翻译或改写标题。</p></header>
      <details class="artwork-section" open><summary>视频截图补封面</summary><div class="artwork-fields">
        <label>定位方式<select data-art="position-mode"><option value="percent">按百分比</option><option value="seconds">按秒数</option></select></label>
        <label><span data-art="position-label">影片位置（%）</span><input data-art="position" type="number" min="0" max="100" step="any" value="10"></label>
        <label class="artwork-check"><input data-art="overwrite" type="checkbox">覆盖已有封面（默认只补缺图）</label>
        <p class="hint">仅对可信 STRM 源执行截图。网络协议限制不能完全阻止私网访问或上游重定向。</p>
        <button type="button" class="btn" data-art="screenshot" disabled>截图补封面</button>
      </div></details>
      <details class="artwork-section"><summary>从来源候选更换封面</summary><div class="artwork-fields">
        <label>搜索标题 / 番号<input data-art="query" maxlength="200" value="${escape(movie.Number || movie.Title || '')}"></label>
        <label>来源筛选（可选）<input data-art="provider" maxlength="100" placeholder="留空搜索全部来源"></label>
        <button type="button" class="btn" data-art="search" disabled>搜索封面候选</button>
        <p class="hint">只能选择服务端候选，不支持自定义 URL。确认后只替换封面，演员、标题、简介及其他元数据保持不变。</p>
        <div class="artwork-candidates" data-art="candidates"></div>
      </div></details>
      <p class="artwork-state" data-art="state" role="status" aria-live="polite">正在检查可用能力…</p>
      <p class="artwork-error" data-art="error" role="alert" hidden></p>
      <div class="artwork-actions"><button type="button" class="btn" data-art="retry" hidden>重新读取状态</button><button type="button" class="btn" data-art="cancel" hidden>取消任务</button></div>
    </section>`;
    const el = key => host.querySelector('[data-art="' + key + '"]');
    const listen = (node, event, callback) => { node.addEventListener(event, callback); listeners.push(() => node.removeEventListener(event, callback)); };
    const state = message => { if (current()) el('state').textContent = message; };
    const error = message => { if (current()) { el('error').textContent = message; el('error').hidden = !message; } };
    function controls(snap) {
      if (!current()) return;
      const blocked = snap.blocked || searching;
      el('screenshot').disabled = !snap.ready || !capabilities.screenshot || blocked;
      el('search').disabled = !snap.ready || !capabilities.provider || blocked;
      el('cancel').hidden = !snap.accepted || snap.observingOther;
      el('cancel').disabled = snap.cancelling || !snap.targetVerified;
      el('retry').hidden = !snap.retry;
      for (const node of host.querySelectorAll('[data-candidate]')) node.disabled = blocked;
    }
    const runner = global.TaskRunner({
      apiRequest, movieKey, scope: 'artwork', current, confirm: confirmAction,
      runPath: '/artwork/run', cancelPath: '/artwork/cancel', statusBase: '/artwork/status',
      statusPath: (instance, taskID) => '/artwork/status?task_id=' + taskID + '&instance_id=' + encodeURIComponent(instance),
      onState: state, onError: error, onChange: controls,
      targetsMovie: status => Array.isArray(status.movie_ids) && status.movie_ids.some(id => String(id) === movieKey),
      completed: status => status.success > 0 || status.index_refresh_failed > 0,
      unavailable: () => [!capabilities.screenshot && '截图不可用：请检查 FFmpeg / FFprobe 配置',
        !capabilities.provider && '候选来源不可用：请检查 MetaTube 配置'].filter(Boolean).join('；'),
      idleText: '默认只补缺图。选择操作后将再次确认。',
      describe: (status, other) => `${other ? '其他影片任务 · ' : ''}${status.running ? '处理中' : status.cancelled ? '已取消' : '已结束'} · ${status.done || 0}/${status.total || 0} · 成功 ${status.success || 0} · 跳过 ${status.skipped || 0} · 失败 ${status.failed || 0}${status.index_refresh_failed ? ' · 索引刷新失败，请重新打开详情核对' : ''}`,
      onComplete: status => onComplete(status)
    });
    listen(el('position-mode'), 'change', () => {
      const seconds = el('position-mode').value === 'seconds';
      el('position-label').textContent = seconds ? '截图时间（秒）' : '影片位置（%）';
      el('position').value = seconds ? '0' : '10';
      if (seconds) el('position').removeAttribute('max'); else el('position').setAttribute('max', '100');
    });
    listen(el('screenshot'), 'click', () => {
      if (!capabilities.screenshot) return;
      const seconds = el('position-mode').value === 'seconds';
      const value = Number(el('position').value);
      if (!el('position').value.trim() || !Number.isFinite(value) || value < 0 || (!seconds && value >= 100)) { error('请输入有效位置：百分比须大于等于 0 且小于 100，秒数不小于 0。'); return; }
      const overwrite = el('overwrite').checked;
      runner.run({ mode: 'screenshot', movie_ids: [movie.id], overwrite, [seconds ? 'position_seconds' : 'position_percent']: value }, `${overwrite ? '覆盖已有封面' : '只补缺失封面'}：将从本片${value}${seconds ? '秒' : '% 位置'}截图。仅修改封面，不修改元数据。确认执行？`);
    });
    listen(el('search'), 'click', async () => {
      if (!current() || searching || runner.snapshot().blocked || !capabilities.provider) return;
      const q = el('query').value.trim(), provider = el('provider').value.trim();
      if (/https?:|\/\//i.test(q + provider)) { error('请搜索标题或番号，不接受 URL。'); return; }
      const generation = ++searchGeneration; searching = true; runner.refresh(); error(''); state('正在搜索封面候选…');
      clearImages(); el('candidates').innerHTML = '';
      try {
        const result = await runner.read('/artwork/candidates?movie_id=' + encodeURIComponent(movie.id) + '&provider=' + encodeURIComponent(provider) + '&q=' + encodeURIComponent(q));
        if (!current() || generation !== searchGeneration) return;
        const candidates = (result.candidates || []).filter(c => c && typeof c.provider === 'string' && typeof c.id === 'string' && c.provider && c.id).slice(0, 50);
        el('candidates').innerHTML = candidates.map((c, index) => `<button type="button" class="artwork-candidate" data-candidate="${index}" aria-label="${escape('选择 ' + c.provider + '：' + (c.title || c.id))}"><img loading="lazy" width="88" height="124" data-image-path="${escape(candidateImage(c))}" alt="候选封面"><span><strong>${escape(c.title || c.id)}</strong><small>${escape(c.provider)} · ${escape(c.number || c.id)}</small><small>选择并确认仅替换封面</small></span></button>`).join('');
        for (const node of host.querySelectorAll('[data-candidate]')) listen(node, 'click', () => {
          const c = candidates[Number(node.dataset.candidate)];
          if (!c) return;
          runner.run({ mode: 'provider', movie_ids: [movie.id], provider: c.provider, id: c.id, overwrite: true }, `确认使用 ${c.provider} 的「${c.title || c.id}」替换本片封面？只替换封面，标题、演员、简介及其他元数据均保持不变。`);
        });
        for (const node of host.querySelectorAll('[data-candidate]')) loadImage(node, candidates[Number(node.dataset.candidate)], generation);
        state(candidates.length ? `找到 ${candidates.length} 个封面候选，请核对来源后选择。` : '未找到封面候选，请调整关键词或来源。');
      } catch (_) { error('候选读取失败，请检查来源配置或稍后重新搜索。'); }
      finally { if (current() && generation === searchGeneration) { searching = false; runner.refresh(); } }
    });
    listen(el('retry'), 'click', () => runner.retry());
    listen(el('cancel'), 'click', () => runner.cancel());
    runner.refresh();
    (async () => {
      try {
        capabilities = await runner.read('/artwork/capabilities');
        if (!current()) return;
        await runner.recover();
      } catch (_) { error('无法读取封面维护能力，请重新打开详情重试。'); }
    })();
    return function dispose() {
      if (disposed) return;
      disposed = true; searchGeneration++; clearImages();
      runner.dispose();
      listeners.forEach(remove => remove());
      // Closing a drawer stops observation, never implicitly cancels server work.
    };
  }
  global.mountArtwork = mountArtwork;
})(globalThis);
