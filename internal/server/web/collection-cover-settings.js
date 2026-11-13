'use strict';

// 合集封面 + 媒体库封面：自动触发开关 + 一次性生成入口。
// 两者用同一套 Emby 拼贴（合集 640×360、媒体库 960×540），生成都会把结果写成
// 对应对象的 Primary 封面（apply），属于改数据，所以默认开启与 Emby 一致、但要给用户
// 一个关掉的开关；一次性生成仍需显式确认。预览/回退仍在 API 层，这里不重复暴露。
async function mountCollectionCoverSettings(host, apiRequest, isCurrent = () => host.isConnected) {
  host.innerHTML = `
    <div class="panel-head"><h2 id="cc-title">合集与媒体库封面</h2><button type="button" id="cc-reload" class="btn btn-sm">重新加载</button></div>
    <p class="hint">按 Emby 原生四列倒影拼贴复刻：取最多 4 部影片的 Primary 海报（缺失时用 Thumb 横图）横向拼接。合集封面 640×360，媒体库封面 960×540。生成会覆盖对应的 Primary 图，影片海报原文件不受影响。</p>
    <form id="cc-form">
      <fieldset id="cc-fields" disabled style="border:0;padding:0;margin:0;min-width:0">
        <legend class="hint">自动生成</legend>
        <label class="check"><input id="cc-auto" type="checkbox">扫描成功后自动生成有变化的合集封面</label>
        <label class="check"><input id="lc-auto" type="checkbox">扫描成功后自动生成有变化的媒体库封面</label>
        <p id="cc-help" class="hint">只处理扫描确实提交了索引变更的情况；成员与海报都没变的会直接跳过，不重复写盘。媒体库根目录已有海报或管理端上传过封面时，自动拼贴不会覆盖它们。</p>
      </fieldset>
      <p id="cc-status" class="hint" role="status" aria-live="polite">正在加载封面设置…</p>
      <div class="form-foot">
        <button id="cc-save" class="btn btn-accent" type="submit" disabled>保存开关</button>
        <button id="cc-run" class="btn" type="button" disabled>立即生成全部合集</button>
        <button id="lc-run" class="btn" type="button" disabled>立即生成全部媒体库</button>
      </div>
    </form>`;
  const find = s => host.querySelector(s);
  let config = null, busy = false;
  const current = () => isCurrent() && host.isConnected;
  const status = (text, error = false) => { find('#cc-status').textContent = text; find('#cc-status').setAttribute('role', error ? 'alert' : 'status'); };
  const setBusy = value => {
    busy = value;
    find('#cc-fields').disabled = value || !config;
    find('#cc-save').disabled = value || !config;
    find('#cc-run').disabled = value || !config;
    find('#lc-run').disabled = value || !config;
    find('#cc-reload').disabled = value;
  };
  const valid = d => d && typeof d.auto === 'boolean';

  async function load() {
    if (busy || !current()) return;
    config = null; setBusy(true);
    try {
      const collection = await apiRequest('/collection-cover/settings');
      if (!current()) return;
      const library = await apiRequest('/library-cover/settings');
      if (!current()) return;
      if (!valid(collection) || !valid(library)) throw new Error('封面设置响应无效，请重新加载。');
      config = { collection: collection.auto, library: library.auto };
      find('#cc-auto').checked = config.collection;
      find('#lc-auto').checked = config.library;
      status(config.collection || config.library
        ? `自动生成已开启（合集${config.collection ? '开' : '关'}，媒体库${config.library ? '开' : '关'}）。`
        : '自动生成已关闭。');
    } catch (e) {
      if (current()) status(e.message || '封面设置读取失败。', true);
    } finally { if (current()) setBusy(false); }
  }

  find('#cc-reload').onclick = () => { if (busy || !current()) return; return load(); };

  find('#cc-form').onsubmit = async event => {
    event.preventDefault();
    if (busy || !config || !current()) return;
    const collection = find('#cc-auto').checked, library = find('#lc-auto').checked;
    setBusy(true);
    try {
      const first = await apiRequest('/collection-cover/settings', { method: 'PUT', body: JSON.stringify({ auto: collection }) });
      if (!current()) return;
      if (!valid(first)) throw new Error('保存结果未确认，请重新加载核对。');
      const second = await apiRequest('/library-cover/settings', { method: 'PUT', body: JSON.stringify({ auto: library }) });
      if (!current()) return;
      if (!valid(second)) throw new Error('保存结果未确认，请重新加载核对。');
      config = { collection: first.auto, library: second.auto };
      find('#cc-auto').checked = config.collection;
      find('#lc-auto').checked = config.library;
      status('已保存：下次扫描提交变更后按开关自动生成。');
    } catch (e) {
      if (current()) status(e.message || '保存失败，已保留输入。', true);
    } finally { if (current()) setBusy(false); }
  };

  const runAll = (base, label) => async () => {
    if (busy || !current()) return;
    if (!confirm(`将为所有${label}生成并生效拼贴封面，覆盖它们当前的 Primary 图（手动上传的自定义封面会保留）。继续？`)) return;
    setBusy(true);
    status('已提交，正在生成…');
    try {
      const task = await apiRequest(`${base}/run`, { method: 'POST', body: JSON.stringify({ all: true, apply: true }) });
      if (!current()) return;
      if (!task || !task.task_id || !task.instance_id) throw new Error('任务提交未返回任务号，请到「任务」页核对。');
      await waitForCoverTask(base, task);
    } catch (e) {
      if (current()) status(e.message || '提交失败，请稍后重试。', true);
    } finally { if (current()) setBusy(false); }
  };

  find('#cc-run').onclick = runAll('/collection-cover', '合集');
  find('#lc-run').onclick = runAll('/library-cover', '媒体库');

  async function waitForCoverTask(base, task) {
    const path = `${base}/status?task_id=${encodeURIComponent(task.task_id)}&instance_id=${encodeURIComponent(task.instance_id)}`;
    for (let i = 0; i < 600; i++) {
      if (!current()) return;
      let data;
      try {
        data = await apiRequest(path);
      } catch (e) {
        if (e.status === 404) { status('任务状态已过期，请到「任务」页查看结果。', true); return; }
        throw e;
      }
      if (!data || typeof data.running !== 'boolean') throw new Error('任务状态响应无效。');
      if (data.running) {
        status(`正在生成：${data.current || '准备中'}（${data.done}/${data.total}）`);
        await new Promise(resolve => setTimeout(resolve, 1000));
        continue;
      }
      if (data.error) { status(`完成但有失败：成功 ${data.success}，跳过 ${data.skipped}，失败 ${data.failed}（${data.error}）`, true); return; }
      status(`完成：成功 ${data.success}，跳过 ${data.skipped}，失败 ${data.failed}。`);
      return;
    }
    status('等待任务结束超时，请到「任务」页查看结果。', true);
  }

  await load();
}
