/* 独立封面维护：只发送候选身份，不接受自定义图片 URL。 */
(function (global) {
  'use strict';
  const escape = value => String(value ?? '').replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
  function candidateImage(c) {
    return '/api/admin/scrape/image?kind=' + (c.thumb ? 'thumb' : 'primary') + '&provider=' + encodeURIComponent(c.provider) + '&id=' + encodeURIComponent(c.id);
  }
  // Retain ambiguous submissions across drawer mounts. Never replay a POST.
  const submissions = new Map(), completedTasks = new Set();
  function mountArtwork(host, options) {
    const { apiRequest, movie, onComplete = () => {}, isCurrent = () => true } = options;
    const confirmAction = options.confirm || (message => global.confirm(message));
    let disposed = false, acceptedTaskID = null, busy = false, ready = false, cancelling = false;
    const movieKey = String(movie.id);
    let uncertain = submissions.has(movieKey), observingOther = false, latestTaskID = 0;
    let acceptedInstance = null, latestInstance = null, targetVerified = false;
    const identityKey = (instance, taskID) => JSON.stringify([instance, taskID]);
    const taskPath = (instance, taskID) => '/artwork/status?task_id=' + taskID + '&instance_id=' + encodeURIComponent(instance);
    let capabilities = {}, timer = null, request = null, failures = 0, searchGeneration = 0;
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
    let cancelTimeout = null;
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
    function controls() {
      if (!current()) return;
      el('screenshot').disabled = !ready || !capabilities.screenshot || busy || uncertain || acceptedTaskID !== null;
      el('search').disabled = !ready || !capabilities.provider || busy || uncertain || acceptedTaskID !== null;
      el('cancel').hidden = acceptedTaskID === null || observingOther;
      el('cancel').disabled = cancelling || !targetVerified;
      for (const node of host.querySelectorAll('[data-candidate]')) node.disabled = busy || uncertain || acceptedTaskID !== null;
    }
    async function read(path) {
      if (request) throw new Error('正在读取，请稍候');
      const controller = new AbortController();
      let timeout;
      const pending = { controller };
      request = pending;
      try {
        return await Promise.race([
          apiRequest(path, { signal: controller.signal }),
          new Promise((_, reject) => { timeout = setTimeout(() => { controller.abort(); reject(new Error('读取超时')); }, 10000); pending.timeout = timeout; })
        ]);
      } finally { clearTimeout(timeout); if (request === pending) request = null; }
    }
    function schedule(delay) { clearTimeout(timer); timer = setTimeout(() => { timer = null; poll(); }, delay); }
    function taskTargetsMovie(status) {
      return Array.isArray(status.movie_ids) && status.movie_ids.some(id => String(id) === movieKey);
    }
    async function recover() {
      if (!current() || request) return;
      clearTimeout(timer); timer = null;
      busy = true; controls();
      state('正在核对后台任务，请勿重复提交…');
      try {
        const saved = submissions.get(movieKey);
        const status = await read(saved?.taskID ? taskPath(saved.instance, saved.taskID) : '/artwork/status');
        if (!current()) return;
        if (typeof status.instance_id !== 'string' || !status.instance_id || !Number.isSafeInteger(status.task_id) || status.task_id < 0 || typeof status.running !== 'boolean') throw new Error('任务状态无效');
        if (saved?.taskID && (status.task_id !== saved.taskID || status.instance_id !== saved.instance)) throw new Error('恢复任务身份不匹配');
        ready = true; latestTaskID = status.task_id; latestInstance = status.instance_id;
        const recoverable = !saved || (saved.instance === status.instance_id && saved.taskID === status.task_id) || (saved.baselineInstance === status.instance_id && status.task_id > saved.baseline);
        if (recoverable && status.task_id > 0 && (status.running || taskTargetsMovie(status))) {
          acceptedTaskID = status.task_id; acceptedInstance = status.instance_id; targetVerified = taskTargetsMovie(status); observingOther = !targetVerified;
          if (!observingOther) { uncertain = false; submissions.delete(movieKey); }
          busy = false; failures = 0; controls(); poll();
          return;
        }
        busy = false; ready = true; el('retry').hidden = !uncertain; controls();
        const unavailable = [!capabilities.screenshot && '截图不可用：请检查 FFmpeg / FFprobe 配置', !capabilities.provider && '候选来源不可用：请检查 MetaTube 配置'].filter(Boolean).join('；');
        state(uncertain ? '提交结果未确认，已禁止重复提交。请重新读取后台状态。' : unavailable || '默认只补缺图。选择操作后将再次确认。');
        if (uncertain) error('尚未找到可确认的本片任务。后台请求可能仍在处理中，不会自动重复提交。');
      } catch (_) {
        if (!current()) return;
        busy = false; ready = false; controls(); el('retry').hidden = false;
        error('无法核对后台任务。为避免重复提交，操作暂不可用；请重新读取状态。');
      }
    }
    async function poll() {
      if (!current() || acceptedTaskID === null || request) return;
      const taskID = acceptedTaskID, instance = acceptedInstance;
      try {
        const status = await read(taskPath(instance, taskID));
        if (!current() || acceptedTaskID !== taskID || acceptedInstance !== instance) return;
        if (status.task_id !== taskID || status.instance_id !== instance || typeof status.running !== 'boolean') { targetVerified = false; controls(); throw new Error('任务身份不匹配'); }
        targetVerified = taskTargetsMovie(status);
        if (!observingOther && !targetVerified) { controls(); throw new Error('任务目标不匹配'); }
        controls();
        failures = 0; error(''); el('retry').hidden = true;
        state(`${observingOther ? '其他影片任务 · ' : ''}${status.running ? '处理中' : status.cancelled ? '已取消' : '已结束'} · ${status.done || 0}/${status.total || 0} · 成功 ${status.success || 0} · 跳过 ${status.skipped || 0} · 失败 ${status.failed || 0}${status.index_refresh_failed ? ' · 索引刷新失败，请重新打开详情核对' : ''}`);
        if (status.error) error('任务报告错误，请检查服务配置或源文件后重试。');
        if (status.running) { schedule(1200); return; }
        acceptedTaskID = null; cancelling = false; controls();
        if (!observingOther && taskTargetsMovie(status) && !completedTasks.has(identityKey(instance, taskID)) && (status.success > 0 || status.index_refresh_failed > 0) && current()) {
          completedTasks.add(identityKey(instance, taskID)); onComplete(status);
        }
        observingOther = false;
        if (uncertain && current()) recover();
      } catch (err) {
        if (!current() || acceptedTaskID !== taskID || acceptedInstance !== instance) return;
        failures++; error('状态读取失败，任务可能仍在运行。' + (err.status === 404 ? '任务已不在当前记录中，请重新打开详情核对。' : '可重新读取状态或取消；不会自动重复提交。'));
        el('retry').hidden = false;
        if (failures < 5 && ![401, 403, 404].includes(err.status)) schedule(Math.min(15000, 1000 * 2 ** failures));
      }
    }
    async function run(payload, message) {
      if (!current() || busy || uncertain || acceptedTaskID !== null || !ready) return;
      if (!confirmAction(message) || !current()) return;
      busy = true; uncertain = true;
      const submission = { baseline: latestTaskID, baselineInstance: latestInstance }; submissions.set(movieKey, submission);
      error(''); controls(); state('正在提交任务，请勿重复操作…');
      let timeout;
      try {
        const pending = Promise.resolve().then(() => apiRequest('/artwork/run', { method: 'POST', body: JSON.stringify(payload) })).then(result => {
          if (typeof result.instance_id !== 'string' || !result.instance_id || !Number.isSafeInteger(result.task_id) || result.task_id <= 0) throw new Error('无有效任务身份');
          submission.taskID = result.task_id; submission.instance = result.instance_id;
          return result;
        });
        const result = await Promise.race([pending, new Promise((_, reject) => { timeout = setTimeout(() => reject(new Error('提交超时')), 10000); submission.timeout = timeout; })]);
        if (!current()) return;
        submissions.delete(movieKey); uncertain = false;
        acceptedTaskID = result.task_id; acceptedInstance = result.instance_id; latestTaskID = result.task_id; latestInstance = result.instance_id; targetVerified = false; observingOther = false; failures = 0; busy = false; controls();
        state('任务已接受，正在读取进度…'); poll();
      } catch (err) {
        if (!current()) return;
        if ([400, 401, 403, 404, 409, 422].includes(err.status)) {
          submissions.delete(movieKey); uncertain = false; busy = false; controls();
          if (err.status === 409) { await recover(); return; }
          state('任务未提交'); error('服务端拒绝了本次操作，请检查权限、参数或服务配置。');
          return;
        }
        busy = false; controls(); el('retry').hidden = false;
        error('提交未确认，可能已在后台执行。不会自动重试，请重新读取状态核对。');
        state('提交结果未确认');
      } finally { clearTimeout(timeout); }
    }
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
      run({ mode: 'screenshot', movie_ids: [movie.id], overwrite, [seconds ? 'position_seconds' : 'position_percent']: value }, `${overwrite ? '覆盖已有封面' : '只补缺失封面'}：将从本片${value}${seconds ? '秒' : '% 位置'}截图。仅修改封面，不修改元数据。确认执行？`);
    });
    listen(el('search'), 'click', async () => {
      if (!current() || busy || uncertain || acceptedTaskID !== null || !capabilities.provider) return;
      const q = el('query').value.trim(), provider = el('provider').value.trim();
      if (/https?:|\/\//i.test(q + provider)) { error('请搜索标题或番号，不接受 URL。'); return; }
      const generation = ++searchGeneration; busy = true; controls(); error(''); state('正在搜索封面候选…');
      clearImages(); el('candidates').innerHTML = '';
      try {
        const result = await read('/artwork/candidates?movie_id=' + encodeURIComponent(movie.id) + '&provider=' + encodeURIComponent(provider) + '&q=' + encodeURIComponent(q));
        if (!current() || generation !== searchGeneration) return;
        const candidates = (result.candidates || []).filter(c => c && typeof c.provider === 'string' && typeof c.id === 'string' && c.provider && c.id).slice(0, 50);
        el('candidates').innerHTML = candidates.map((c, index) => `<button type="button" class="artwork-candidate" data-candidate="${index}" aria-label="${escape('选择 ' + c.provider + '：' + (c.title || c.id))}"><img loading="lazy" width="88" height="124" data-image-path="${escape(candidateImage(c))}" alt="候选封面"><span><strong>${escape(c.title || c.id)}</strong><small>${escape(c.provider)} · ${escape(c.number || c.id)}</small><small>选择并确认仅替换封面</small></span></button>`).join('');
        for (const node of host.querySelectorAll('[data-candidate]')) listen(node, 'click', () => {
          const c = candidates[Number(node.dataset.candidate)];
          if (!c) return;
          run({ mode: 'provider', movie_ids: [movie.id], provider: c.provider, id: c.id, overwrite: true }, `确认使用 ${c.provider} 的「${c.title || c.id}」替换本片封面？只替换封面，标题、演员、简介及其他元数据均保持不变。`);
        });
        for (const node of host.querySelectorAll('[data-candidate]')) loadImage(node, candidates[Number(node.dataset.candidate)], generation);
        state(candidates.length ? `找到 ${candidates.length} 个封面候选，请核对来源后选择。` : '未找到封面候选，请调整关键词或来源。');
      } catch (_) { error('候选读取失败，请检查来源配置或稍后重新搜索。'); }
      finally { if (current() && generation === searchGeneration) { busy = false; controls(); } }
    });
    listen(el('retry'), 'click', () => { clearTimeout(timer); timer = null; if (acceptedTaskID !== null) poll(); else recover(); });
    listen(el('cancel'), 'click', async () => {
      if (!current() || acceptedTaskID === null || observingOther || !targetVerified || cancelling) return;
      const taskID = acceptedTaskID, instance = acceptedInstance; cancelling = true; controls();
      try {
        const result = await Promise.race([
          apiRequest('/artwork/cancel', { method: 'POST', body: JSON.stringify({ task_id: taskID, instance_id: instance }) }),
          new Promise((_, reject) => { cancelTimeout = setTimeout(() => reject(new Error('取消超时')), 10000); })
        ]);
        if (!current() || acceptedTaskID !== taskID || acceptedInstance !== instance) return;
        if (result.task_id !== taskID || result.instance_id !== instance) throw new Error('任务身份不匹配');
        state('取消已请求；正在执行的写入可能已完成，等待服务端确认…');
        if (!request) { clearTimeout(timer); poll(); }
      } catch (_) { if (current() && acceptedTaskID === taskID) { error('取消未确认，任务可能仍在运行。请读取状态核对，不会自动重复发送取消请求。'); cancelling = false; controls(); el('retry').hidden = false; if (!request) schedule(1200); } }
      finally { clearTimeout(cancelTimeout); cancelTimeout = null; }
    });
    controls();
    (async () => {
      try {
        capabilities = await read('/artwork/capabilities');
        if (!current()) return;
        await recover();
      } catch (_) { error('无法读取封面维护能力，请重新打开详情重试。'); }
    })();
    return function dispose() {
      if (disposed) return;
      disposed = true; searchGeneration++; clearImages(); clearTimeout(timer); timer = null;
      clearTimeout(cancelTimeout); clearTimeout(submissions.get(movieKey)?.timeout);
      if (request) { clearTimeout(request.timeout); request.controller.abort(); }
      listeners.forEach(remove => remove());
      // Closing a drawer stops observation, never implicitly cancels server work.
    };
  }
  global.mountArtwork = mountArtwork;
})(globalThis);
