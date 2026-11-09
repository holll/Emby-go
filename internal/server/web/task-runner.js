/* 封面维护与雪碧图共用的后台任务状态机：提交、轮询、身份核对与"绝不重放 POST"。 */
(function (global) {
  'use strict';
  // 未确认的提交跨面板挂载保留：重开面板只能核对状态，绝不重发 POST。
  const submissions = new Map();

  // spec: apiRequest, movieKey, runPath, cancelPath, statusPath(instance, taskID), statusBase,
  //       current(), onState(msg), onError(msg), onChange(snapshot), describe(status, observingOther),
  //       targetsMovie(status), completed(status), unavailable(), idleText, onComplete(status)
  function taskRunner(spec) {
    const { apiRequest, movieKey, runPath, cancelPath, statusPath, statusBase } = spec;
    // 面板作用域隔离：封面与雪碧图的任务注册表各自独立，即使同一影片。
    const submissionKey = spec.scope + '|' + movieKey;
    let disposed = false, ready = false, busy = false, uncertain = submissions.has(submissionKey);
    let acceptedTaskID = null, acceptedInstance = null, latestTaskID = 0, latestInstance = null;
    let targetVerified = false, observingOther = false, cancelling = false, failures = 0, retry = false;
    let timer = null, request = null, cancelTimeout = null;
    const current = () => !disposed && spec.current();
    const snapshot = () => ({ ready, busy, uncertain, accepted: acceptedTaskID !== null, blocked: busy || uncertain || acceptedTaskID !== null, observingOther, targetVerified, cancelling, retry });
    const changed = () => spec.onChange(snapshot());
    const state = message => spec.onState(message);
    const error = message => spec.onError(message);
    const validStatus = status => typeof status.instance_id === 'string' && status.instance_id !== '' &&
      Number.isSafeInteger(status.task_id) && status.task_id >= 0 && typeof status.running === 'boolean';

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

    async function recover() {
      if (!current() || request) return;
      clearTimeout(timer); timer = null;
      busy = true; changed();
      state('正在核对后台任务，请勿重复提交…');
      try {
        const saved = submissions.get(submissionKey);
        const status = await read(saved && saved.taskID ? statusPath(saved.instance, saved.taskID) : statusBase);
        if (!current()) return;
        if (!validStatus(status)) throw new Error('任务状态无效');
        if (saved && saved.taskID && (status.task_id !== saved.taskID || status.instance_id !== saved.instance)) throw new Error('恢复任务身份不匹配');
        ready = true; latestTaskID = status.task_id; latestInstance = status.instance_id;
        const recoverable = !saved || (saved.instance === status.instance_id && saved.taskID === status.task_id) ||
          (saved.baselineInstance === status.instance_id && status.task_id > saved.baseline);
        if (recoverable && status.task_id > 0 && (status.running || spec.targetsMovie(status))) {
          acceptedTaskID = status.task_id; acceptedInstance = status.instance_id;
          targetVerified = spec.targetsMovie(status); observingOther = !targetVerified;
          if (!observingOther) { uncertain = false; submissions.delete(submissionKey); }
          busy = false; failures = 0; changed(); poll();
          return;
        }
        busy = false; ready = true; retry = uncertain; changed();
        const unavailable = spec.unavailable();
        state(uncertain ? '提交结果未确认，已禁止重复提交。请重新读取后台状态。' : unavailable || spec.idleText);
        if (uncertain) error('尚未找到可确认的本片任务。后台请求可能仍在处理中，不会自动重复提交。');
      } catch (_) {
        if (!current()) return;
        busy = false; ready = false; retry = true; changed();
        error('无法核对后台任务。为避免重复提交，操作暂不可用；请重新读取状态。');
      }
    }

    async function poll() {
      if (!current() || acceptedTaskID === null || request) return;
      const taskID = acceptedTaskID, instance = acceptedInstance;
      try {
        const status = await read(statusPath(instance, taskID));
        if (!current() || acceptedTaskID !== taskID || acceptedInstance !== instance) return;
        if (status.task_id !== taskID || status.instance_id !== instance || typeof status.running !== 'boolean') { targetVerified = false; changed(); throw new Error('任务身份不匹配'); }
        targetVerified = spec.targetsMovie(status);
        if (!observingOther && !targetVerified) { changed(); throw new Error('任务目标不匹配'); }
        changed();
        failures = 0; error(''); retry = false;
        state(spec.describe(status, observingOther));
        if (status.error) error('任务报告错误，请检查服务配置或源文件后重试。');
        if (status.running) { schedule(1200); return; }
        acceptedTaskID = null; cancelling = false; changed();
        if (!observingOther && targetVerified && spec.completed(status) && current()) {
          spec.onComplete(status);
        }
        observingOther = false;
        if (uncertain && current()) recover();
      } catch (err) {
        if (!current() || acceptedTaskID !== taskID || acceptedInstance !== instance) return;
        failures++; retry = true;
        error('状态读取失败，任务可能仍在运行。' + (err.status === 404 ? '任务已不在当前记录中，请重新打开详情核对。' : '可重新读取状态或取消；不会自动重复提交。'));
        changed();
        if (failures < 5 && ![401, 403, 404].includes(err.status)) schedule(Math.min(15000, 1000 * 2 ** failures));
      }
    }

    async function run(payload, message) {
      if (!current() || busy || uncertain || acceptedTaskID !== null || !ready) return;
      if (!spec.confirm(message) || !current()) return;
      busy = true; uncertain = true;
      const submission = { baseline: latestTaskID, baselineInstance: latestInstance }; submissions.set(submissionKey, submission);
      error(''); changed(); state('正在提交任务，请勿重复操作…');
      let timeout;
      try {
        const pending = Promise.resolve().then(() => apiRequest(runPath, { method: 'POST', body: JSON.stringify(payload) })).then(result => {
          if (typeof result.instance_id !== 'string' || !result.instance_id || !Number.isSafeInteger(result.task_id) || result.task_id <= 0) throw new Error('无有效任务身份');
          submission.taskID = result.task_id; submission.instance = result.instance_id;
          return result;
        });
        const result = await Promise.race([pending, new Promise((_, reject) => { timeout = setTimeout(() => reject(new Error('提交超时')), 10000); submission.timeout = timeout; })]);
        if (!current()) return;
        submissions.delete(submissionKey); uncertain = false;
        acceptedTaskID = result.task_id; acceptedInstance = result.instance_id; latestTaskID = result.task_id; latestInstance = result.instance_id;
        targetVerified = false; observingOther = false; failures = 0; busy = false; changed();
        state('任务已接受，正在读取进度…'); poll();
      } catch (err) {
        if (!current()) return;
        if ([400, 401, 403, 404, 409, 422].includes(err.status)) {
          submissions.delete(submissionKey); uncertain = false; busy = false; changed();
          if (err.status === 409) { await recover(); return; }
          state('任务未提交'); error('服务端拒绝了本次操作，请检查权限、参数或服务配置。');
          return;
        }
        busy = false; retry = true; changed();
        error('提交未确认，可能已在后台执行。不会自动重试，请重新读取状态核对。');
        state('提交结果未确认');
      } finally { clearTimeout(timeout); }
    }

    async function cancel() {
      if (!current() || acceptedTaskID === null || observingOther || !targetVerified || cancelling) return;
      const taskID = acceptedTaskID, instance = acceptedInstance; cancelling = true; changed();
      try {
        const result = await Promise.race([
          apiRequest(cancelPath, { method: 'POST', body: JSON.stringify({ task_id: taskID, instance_id: instance }) }),
          new Promise((_, reject) => { cancelTimeout = setTimeout(() => reject(new Error('取消超时')), 10000); })
        ]);
        if (!current() || acceptedTaskID !== taskID || acceptedInstance !== instance) return;
        if (result.task_id !== taskID || result.instance_id !== instance) throw new Error('任务身份不匹配');
        state('取消已请求；正在执行的写入可能已完成，等待服务端确认…');
        if (!request) { clearTimeout(timer); poll(); }
      } catch (_) {
        if (current() && acceptedTaskID === taskID) {
          error('取消未确认，任务可能仍在运行。请读取状态核对，不会自动重复发送取消请求。');
          cancelling = false; retry = true; changed();
          if (!request) schedule(1200);
        }
      } finally { clearTimeout(cancelTimeout); cancelTimeout = null; }
    }

    function retryNow() { clearTimeout(timer); timer = null; if (acceptedTaskID !== null) poll(); else recover(); }

    function dispose() {
      if (disposed) return;
      disposed = true;
      clearTimeout(timer); timer = null;
      clearTimeout(cancelTimeout); clearTimeout(submissions.get(submissionKey) && submissions.get(submissionKey).timeout);
      if (request) { clearTimeout(request.timeout); request.controller.abort(); }
      // 关闭面板只停止观察，绝不隐式取消服务端任务。
    }

    return { read, run, cancel, recover, poll, retry: retryNow, refresh: changed, snapshot, dispose };
  }
  global.TaskRunner = taskRunner;
})(globalThis);
