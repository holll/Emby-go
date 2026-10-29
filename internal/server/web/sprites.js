/* 独立雪碧图维护与精确时间轴预览；不修改封面或元数据。 */
(function (global) {
  'use strict';
  // Retain ambiguous submissions across drawer mounts. Never replay a POST.
  const submissions = new Map(), completedTasks = new Set();
  function mountSprites(host, options) {
    const { apiRequest, movie, onComplete = () => {}, isCurrent = () => true } = options;
    const confirmAction = options.confirm || (message => global.confirm(message));
    let disposed = false, acceptedTaskID = null, busy = false, ready = false, cancelling = false;
    const movieKey = String(movie.id);
    let uncertain = submissions.has(movieKey), observingOther = false, latestTaskID = 0;
    let acceptedInstance = null, latestInstance = null, targetVerified = false;
    const identityKey = (instance, taskID) => JSON.stringify([instance, taskID]);
    const taskPath = (instance, taskID) => '/sprites/status?task_id=' + taskID + '&instance_id=' + encodeURIComponent(instance);
    let capabilities = {}, timer = null, request = null, failures = 0;
    const listeners = [];
    let cancelTimeout = null;
    const current = () => !disposed && host.isConnected !== false && isCurrent();
    host.innerHTML = `<section class="sprites-panel" aria-label="视频预览雪碧图">
      <header><h3>视频预览雪碧图 <small>独立生成 · 不改封面</small></h3><p class="hint">仅从可信 STRM 生成时间轴预览。不会触发刮削、翻译或改写元数据；关闭面板不会停止后台任务。</p></header>
      <div class="sprites-fields"><p class="hint" data-sprite="density">正在读取媒体库采样密度…</p><a class="btn" href="#libraries">前往媒体库设置采样密度</a>
      <label class="sprites-check"><input data-sprite="overwrite" type="checkbox">覆盖已有预览（默认保护旧预览）</label>
      <button type="button" class="btn" data-sprite="generate" disabled>生成视频预览</button></div>
      <p class="sprites-state" data-sprite="state" role="status" aria-live="polite">正在检查可用能力…</p>
      <p class="sprites-error" data-sprite="error" role="alert" hidden></p>
      <div class="sprites-actions"><button type="button" class="btn" data-sprite="retry" hidden>重新读取状态</button><button type="button" class="btn" data-sprite="cancel" hidden>取消任务</button></div>
    </section>`;
    const el = key => host.querySelector('[data-sprite="' + key + '"]');
    const listen = (node, event, callback) => { node.addEventListener(event, callback); listeners.push(() => node.removeEventListener(event, callback)); };
    const state = message => { if (current()) el('state').textContent = message; };
    const error = message => { if (current()) { el('error').textContent = message; el('error').hidden = !message; } };
    function controls() {
      if (!current()) return;
      el('generate').disabled = !ready || !capabilities.enabled || busy || uncertain || acceptedTaskID !== null;
      el('cancel').hidden = acceptedTaskID === null || observingOther;
      el('cancel').disabled = cancelling || !targetVerified;
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
        const status = await read(saved?.taskID ? taskPath(saved.instance, saved.taskID) : '/sprites/status');
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
        const unavailable = !capabilities.enabled ? '预览生成不可用，请检查 FFmpeg / FFprobe 配置。' : '';
        state(uncertain ? '提交结果未确认，已禁止重复提交。请重新读取后台状态。' : unavailable || '默认保留已有预览。生成前将再次确认。');
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
        state(`${observingOther ? '其他影片任务 · ' : ''}${status.running ? '处理中' : status.cancelled ? '已取消' : '已结束'} · ${status.done || 0}/${status.total || 0} · 成功 ${status.success || 0} · 跳过 ${status.skipped || 0} · 失败 ${status.failed || 0} · 帧 ${status.frames_done || 0}/${status.frames_total || 0}`);
        if (status.error) error('任务报告错误，请检查服务配置或源文件后重试。');
        if (status.running) { schedule(1200); return; }
        acceptedTaskID = null; cancelling = false; controls();
        if (!observingOther && taskTargetsMovie(status) && !completedTasks.has(identityKey(instance, taskID)) && status.success > 0 && current()) {
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
        const pending = Promise.resolve().then(() => apiRequest('/sprites/run', { method: 'POST', body: JSON.stringify(payload) })).then(result => {
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
    listen(el('generate'), 'click', () => {
      const overwrite = el('overwrite').checked;
      run({ movie_ids: [movie.id], overwrite }, `${overwrite ? '覆盖已有预览' : '仅生成缺失预览'}：沿用媒体库采样密度（每 ${capabilities.interval_seconds} 秒）。仅生成雪碧图，不修改封面、标题或其他元数据。确认执行？`);
    });
    listen(el('retry'), 'click', () => { clearTimeout(timer); timer = null; if (acceptedTaskID !== null) poll(); else recover(); });
    listen(el('cancel'), 'click', async () => {
      if (!current() || acceptedTaskID === null || observingOther || !targetVerified || cancelling) return;
      const taskID = acceptedTaskID, instance = acceptedInstance; cancelling = true; controls();
      try {
        const result = await Promise.race([
          apiRequest('/sprites/cancel', { method: 'POST', body: JSON.stringify({ task_id: taskID, instance_id: instance }) }),
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
        capabilities = await read('/sprites/capabilities?movie_id=' + encodeURIComponent(movieKey));
        if (!current()) return;
        if (!Number.isSafeInteger(capabilities.interval_seconds) || capabilities.interval_seconds < 10 || capabilities.interval_seconds > 86400) throw new Error('媒体库采样密度无效');
        el('density').textContent = `继承媒体库采样密度：每 ${capabilities.interval_seconds} 秒一帧。修改库设置不会自动生成或覆盖已有预览。`;
        await recover();
      } catch (_) { error('无法读取视频预览能力，请重新打开详情重试。'); }
    })();
    return function dispose() {
      if (disposed) return;
      disposed = true; clearTimeout(timer); timer = null;
      clearTimeout(cancelTimeout); clearTimeout(submissions.get(movieKey)?.timeout);
      if (request) { clearTimeout(request.timeout); request.controller.abort(); }
      listeners.forEach(remove => remove());
      // Closing a drawer stops observation, never implicitly cancels server work.
    };
  }
  global.mountSprites = mountSprites;

  function mountLibrarySprites(host, options) {
    const { library, apiRequest, isCurrent = () => true, onSaved = () => {} } = options;
    const valid = value => Number.isSafeInteger(value) && value >= 10 && value <= 86400;
    let saved = valid(library.sprite_interval_seconds) ? library.sprite_interval_seconds : 10;
    let disposed = false, busy = false, controller = null, timeout = null;
    const listeners = [], current = () => !disposed && host.isConnected !== false && isCurrent();
    host.innerHTML = `<section class="library-sprites" aria-label="媒体库视频预览设置">
      <h3 data-library-sprite="title" tabindex="-1"></h3>
      <p class="hint">设置的是视频时间轴的采样间隔，不是每页雪碧图的间隔。采样越密，生成耗时和预览占用通常越高。修改只影响下次生成，不会自动生成或覆盖已有预览，也不会修改封面或元数据；更新旧预览需在影片详情中显式勾选覆盖。</p>
      <form novalidate><label>采样密度<select data-library-sprite="preset"><option value="300">每 5 分钟一帧</option><option value="60">每 1 分钟一帧</option><option value="10">每 10 秒一帧（最密）</option><option value="custom">自定义秒数</option></select></label>
      <label data-library-sprite="custom-field" hidden>自定义间隔（秒，10–86400）<input data-library-sprite="interval" type="number" min="10" max="86400" step="1" inputmode="numeric" aria-describedby="library-sprite-help"></label>
      <p id="library-sprite-help" class="hint">最低 10 秒，最高 86400 秒；每片最多 600 帧，超长影片可能自动增大实际采样间隔。</p>
      <button type="submit" class="btn btn-accent" data-library-sprite="save">保存采样密度</button></form>
      <p data-library-sprite="state" role="status" aria-live="polite"></p><p data-library-sprite="error" class="sprites-error" role="alert" hidden></p>
    </section>`;
    const el = key => host.querySelector('[data-library-sprite="' + key + '"]');
    const preset = el('preset'), input = el('interval'), save = el('save'), form = host.querySelector('form');
    el('title').textContent = '视频预览采样 · ' + String(library.Name || '媒体库');
    preset.value = [300, 60, 10].includes(saved) ? String(saved) : 'custom'; input.value = String(saved);
    const showCustom = () => { el('custom-field').hidden = preset.value !== 'custom'; };
    showCustom(); el('state').textContent = `当前已保存：每 ${saved} 秒一帧。`;
    const listen = (node, event, fn) => { node.addEventListener(event, fn); listeners.push(() => node.removeEventListener(event, fn)); };
    listen(preset, 'change', () => { if (!current() || busy) return; showCustom(); if (preset.value === 'custom') input.focus(); });
    listen(form, 'submit', async event => {
      event.preventDefault();
      if (!current() || busy) return;
      const text = preset.value === 'custom' ? input.value.trim() : preset.value, value = Number(text);
      if (!text || !valid(value)) {
        el('error').textContent = '请输入 10 至 86400 的整数秒数，草稿已保留。'; el('error').hidden = false;
        input.setAttribute('aria-invalid', 'true'); if (preset.value === 'custom') input.focus(); return;
      }
      input.removeAttribute('aria-invalid'); el('error').hidden = true; busy = true;
      save.disabled = preset.disabled = input.disabled = true; save.textContent = '正在保存…'; el('state').textContent = '正在保存媒体库采样密度…';
      controller = new AbortController();
      try {
        const result = await Promise.race([
          apiRequest('/libraries/' + encodeURIComponent(String(library.Id)) + '/sprites', { method: 'PUT', body: JSON.stringify({ interval_seconds: value }), signal: controller.signal }),
          new Promise((_, reject) => { timeout = setTimeout(() => { controller.abort(); reject(new Error('保存超时')); }, 10000); })
        ]);
        if (!current()) return;
        if (!valid(result.interval_seconds) || result.interval_seconds !== value) throw new Error('保存结果无效');
        saved = result.interval_seconds; el('state').textContent = `已保存：每 ${saved} 秒一帧。现有预览保持不变；需要更新时，请在影片详情中手动生成。`;
        onSaved(saved);
      } catch (_) {
        if (!current()) return;
        el('state').textContent = `上次确认的设置：每 ${saved} 秒一帧。`;
        el('error').textContent = '保存未确认，草稿已保留。请检查连接或权限后重新保存；不会自动重试或生成预览。'; el('error').hidden = false;
      } finally {
        clearTimeout(timeout); timeout = null; controller = null;
        if (current()) { busy = false; save.disabled = preset.disabled = input.disabled = false; save.textContent = '保存采样密度'; }
      }
    });
    return () => { disposed = true; clearTimeout(timeout); controller?.abort(); listeners.forEach(remove => remove()); };
  }
  global.mountLibrarySprites = mountLibrarySprites;

  function validateManifest(value, movieID) {
    const integer = (n, low, high) => Number.isSafeInteger(n) && n >= low && n <= high;
    if (!/^[1-9]\d*$/.test(String(movieID)) || !value || value.version !== 1 || !/^[a-f0-9]{32}$/.test(value.generation)) throw new Error('预览信息无效');
    const { width, height, columns, rows, count, interval_ms, duration_ms, sheets } = value;
    if (!integer(width, 1, 2048) || !integer(height, 1, 2048) || !integer(columns, 1, 100) || !integer(rows, 1, 100) || !integer(count, 1, 600) || !integer(interval_ms, 1, 4294967294) || !integer(duration_ms, 1, Number.MAX_SAFE_INTEGER) || count !== Math.ceil(duration_ms / interval_ms)) throw new Error('预览尺寸或时间无效');
    const capacity = columns * rows;
    if (!Array.isArray(sheets) || sheets.length !== Math.ceil(count / capacity) || sheets.length > 6 || value.sheet_count !== sheets.length) throw new Error('预览页数无效');
    const clean = sheets.map((sheet, index) => {
      const expectedCount = Math.min(capacity, count - index * capacity);
      const expectedURL = '/api/items/' + movieID + '/sprites/sheets/' + index + '?generation=' + value.generation;
      if (!sheet || sheet.url !== expectedURL || sheet.count !== expectedCount || !integer(sheet.width, 1, 8192) || !integer(sheet.height, 1, 8192) || sheet.width * sheet.height > 32000000 || sheet.width % width || sheet.height % height || sheet.width < Math.min(columns, expectedCount) * width || sheet.width > columns * width || sheet.height < Math.ceil(expectedCount / columns) * height || sheet.height > rows * height) throw new Error('预览图片无效');
      return { url: expectedURL, width: sheet.width, height: sheet.height, count: expectedCount };
    });
    return { version: 1, generation: value.generation, width, height, columns, rows, count, interval_ms, duration_ms, sheet_count: clean.length, sheets: clean };
  }
  function frameAt(manifest, milliseconds) {
    const time = Number.isFinite(milliseconds) ? Math.max(0, Math.min(milliseconds, manifest.duration_ms)) : 0;
    const index = Math.min(manifest.count - 1, Math.floor(time / manifest.interval_ms));
    const capacity = manifest.columns * manifest.rows, sheet = Math.floor(index / capacity), local = index % capacity;
    return { index, sheet, x: local % manifest.columns * manifest.width, y: Math.floor(local / manifest.columns) * manifest.height, start_ms: index * manifest.interval_ms, end_ms: Math.min((index + 1) * manifest.interval_ms, manifest.duration_ms) };
  }
  function timeLabel(milliseconds) {
    const total = Math.max(0, Math.floor(milliseconds / 1000));
    return [Math.floor(total / 3600), Math.floor(total / 60) % 60, total % 60].map(n => String(n).padStart(2, '0')).join(':');
  }
  function mountSpritePreview(art, options) {
    const { movieID, requestJSON, requestImage, stage, isCurrent = () => true } = options;
    let disposed = false, manifest = null, sequence = 0, selected = 0, active = false, visibleSheet = null, requestedSheet = null;
    const controllers = new Set(), cache = new Map(), pending = new Map(), removers = [];
    const player = art.template.$player;
    const bubble = document.createElement('div'); bubble.className = 'sprites-player-preview'; bubble.hidden = true;
    const picture = document.createElement('div'); picture.className = 'sprites-player-picture'; picture.setAttribute('role', 'img');
    const caption = document.createElement('span'); bubble.append(picture, caption); player.appendChild(bubble);
    const rail = document.createElement('section'); rail.className = 'sprites-timeline'; rail.hidden = true; rail.setAttribute('aria-label', '视频预览时间轴');
    rail.innerHTML = '<label>预览时间轴<input type="range" min="0" step="1000" value="0" aria-label="视频预览时间"></label><div class="sprites-timeline-actions"><output>00:00:00</output><button type="button" class="btn">跳转此时间</button></div><p class="hint" data-preview-error role="status" hidden></p>';
    stage.after(rail);
    const range = rail.querySelector('input'), output = rail.querySelector('output'), seek = rail.querySelector('button'), notice = rail.querySelector('[data-preview-error]');
    const current = () => !disposed && isCurrent() && !art.isDestroy;
    const listen = (node, name, fn) => { node.addEventListener(name, fn); removers.push(() => node.removeEventListener(name, fn)); };
    async function request(callback) {
      const controller = new AbortController(); controllers.add(controller);
      const timer = setTimeout(() => controller.abort(), 10000); controller.timer = timer;
      try { return await callback(controller.signal); }
      finally { clearTimeout(timer); controllers.delete(controller); }
    }
    function release(index) { const entry = cache.get(index); if (entry) { global.URL.revokeObjectURL(entry.url); cache.delete(index); } }
    async function sheetImage(index) {
      if (cache.has(index)) { const entry = cache.get(index); cache.delete(index); cache.set(index, entry); return entry; }
      if (pending.has(index)) return pending.get(index);
      const promise = request(async signal => {
        const blob = await requestImage(manifest.sheets[index].url, { signal });
        if (!current() || signal.aborted) return null;
        if (!/^image\/(?:jpeg|png|webp)$/i.test(blob.type) || blob.size > 16 * 1024 * 1024) throw new Error('图片格式无效');
        const url = global.URL.createObjectURL(blob), image = new Image();
        try {
          await new Promise((resolve, reject) => {
            const abort = () => reject(new Error('图片读取已取消'));
            signal.addEventListener('abort', abort, { once: true });
            image.onload = () => { signal.removeEventListener('abort', abort); resolve(); };
            image.onerror = () => { signal.removeEventListener('abort', abort); reject(new Error('图片不可用')); };
            image.src = url;
          });
          if (!current() || signal.aborted) { global.URL.revokeObjectURL(url); return null; }
          const sheet = manifest.sheets[index];
          if (image.naturalWidth !== sheet.width || image.naturalHeight !== sheet.height) throw new Error('图片尺寸不匹配');
          const entry = { url, width: sheet.width, height: sheet.height }; cache.set(index, entry);
          while (cache.size > 2) release([...cache.keys()].find(key => key !== visibleSheet && key !== requestedSheet));
          return entry;
        } catch (err) { global.URL.revokeObjectURL(url); throw err; }
      }).finally(() => pending.delete(index));
      pending.set(index, promise); return promise;
    }
    async function show(milliseconds, fraction) {
      if (!current() || !manifest) return;
      const ownSequence = ++sequence, frame = frameAt(manifest, milliseconds);
      requestedSheet = frame.sheet;
      active = true; bubble.hidden = true;
      try {
        const sheet = await sheetImage(frame.sheet);
        if (!current() || !sheet || sequence !== ownSequence || !active) return;
        const bottom = parseFloat(global.getComputedStyle(bubble).bottom) || 64;
        const available = Math.max(1, Math.min(160, player.clientWidth - 24, Math.max(1, player.clientHeight - bottom - 32) * manifest.width / manifest.height)), scale = available / manifest.width;
        picture.style.width = available + 'px'; picture.style.height = manifest.height * scale + 'px';
        picture.style.backgroundImage = 'url("' + sheet.url + '")';
        picture.style.backgroundSize = sheet.width * scale + 'px ' + sheet.height * scale + 'px';
        picture.style.backgroundPosition = -frame.x * scale + 'px ' + -frame.y * scale + 'px';
        const label = timeLabel(Math.max(0, Math.min(milliseconds, manifest.duration_ms)));
        visibleSheet = frame.sheet;
        if (cache.has(frame.sheet)) { cache.delete(frame.sheet); cache.set(frame.sheet, sheet); }
        caption.textContent = label; picture.setAttribute('aria-label', label + ' 视频预览');
        const left = Math.max(8, Math.min((fraction ?? 0.5) * player.clientWidth - available / 2, player.clientWidth - available - 8));
        bubble.style.left = left + 'px'; bubble.hidden = false; notice.hidden = true;
      } catch (_) {
        if (current() && sequence === ownSequence) { bubble.hidden = true; notice.hidden = false; notice.textContent = '当前预览图片不可用，仍可正常播放或选择时间跳转。'; }
      }
    }
    function hide() { active = false; sequence++; bubble.hidden = true; }
    function onBar(type, fraction, event) {
      if (!manifest || !Number.isFinite(fraction)) return;
      if (type === 'hover') {
        if (event?.type === 'mouseleave') { hide(); return; }
        show(Math.max(0, Math.min(1, fraction)) * manifest.duration_ms, fraction);
      } else if (type === 'played' && event && (event.type?.startsWith('touch') || event.pointerType === 'touch')) show(Math.max(0, Math.min(1, fraction)) * manifest.duration_ms, fraction);
    }
    art.on('setBar', onBar);
    const onDestroy = () => dispose(); art.on('destroy', onDestroy);
    listen(range, 'input', () => { selected = Number(range.value); output.textContent = timeLabel(selected); range.setAttribute('aria-valuetext', timeLabel(selected)); show(selected, manifest ? selected / manifest.duration_ms : 0); });
    listen(range, 'focus', () => { if (manifest) show(Number(range.value), Number(range.value) / manifest.duration_ms); });
    listen(seek, 'click', () => { if (current() && manifest) { art.seek = Math.min(selected / 1000, manifest.duration_ms / 1000); hide(); } });
    (async () => {
      try {
        const value = await request(signal => requestJSON('/api/items/' + movieID + '/sprites', { signal }));
        if (!current()) return;
        manifest = validateManifest(value, movieID);
        range.max = String(Math.max(0, manifest.duration_ms - 1)); range.setAttribute('aria-valuetext', timeLabel(0));
        rail.hidden = false; stage.parentElement?.classList.add('sprites-preview-shell');
      } catch (err) {
        if (current() && err.status !== 404) { rail.hidden = false; range.disabled = true; seek.disabled = true; notice.hidden = false; notice.textContent = '视频预览暂不可用，不影响播放。'; stage.parentElement?.classList.add('sprites-preview-shell'); }
      }
    })();
    function dispose() {
      if (disposed) return;
      disposed = true; hide();
      for (const controller of controllers) { clearTimeout(controller.timer); controller.abort(); }
      controllers.clear(); for (const index of cache.keys()) release(index);
      removers.forEach(remove => remove()); art.off('setBar', onBar); art.off('destroy', onDestroy);
      bubble.remove(); rail.remove(); stage.parentElement?.classList.remove('sprites-preview-shell');
    }
    return dispose;
  }
  global.SpritePreview = { validateManifest, frameAt, mount: mountSpritePreview };
})(globalThis);
