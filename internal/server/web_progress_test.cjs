'use strict';

// Run with: node --test internal/server/web_progress_test.cjs
const assert = require('node:assert/strict');
const { readFileSync } = require('node:fs');
const { test } = require('node:test');
const vm = require('node:vm');

const source = readFileSync(`${__dirname}/web/app.js`, 'utf8');
function functionSource(name) {
  const match = source.match(new RegExp(`^function ${name}\\([^]*?^}`, 'm'));
  assert.ok(match, `missing function ${name}`);
  return match[0];
}

function element() {
  return {
    hidden: true, textContent: '', innerHTML: '', style: {}, attributes: {}, classes: {},
    setAttribute(name, value) { this.attributes[name] = value; },
    classList: { toggle(name, value) { this.owner.classes[name] = !!value; } }
  };
}

function harness(kind) {
  const els = Object.fromEntries(['panel', 'title', 'count', 'fill', 'detail', 'failures'].map(key => {
    const el = element();
    el.classList.owner = el;
    return [key, el];
  }));
  const context = vm.createContext({
    document: {
      querySelector(selector) {
        if (selector === `#${kind}-progress`) return els.panel;
        if (selector === `#${kind}-failures`) return els.failures;
        return els[selector.slice(`#${kind}-progress-`.length)];
      }
    },
    scanPanel: els.panel, scanTitle: els.title, scanCount: els.count,
    scanFill: els.fill, scanDetail: els.detail, scanClose: els.close || element(),
    esc: value => String(value)
  });
  vm.runInContext(['progressCounts', 'scrapeEls', 'probeEls', 'baseName',
    'renderScrapeProgress', 'renderProbeProgress', 'renderScanProgress']
    .map(functionSource).join('\n'), context);
  return { els, counts: context.progressCounts, render: context[`render${kind[0].toUpperCase() + kind.slice(1)}Progress`] };
}

const cases = [
  { name: 'missing counters', p: {}, total: 0, done: 0, count: '0', percent: 100 },
  { name: 'null counters', p: { total: null, done: null }, total: 0, done: 0, count: '0', percent: 100 },
  { name: 'zero total with work', p: { total: 0, done: 7 }, total: 0, done: 7, count: '7', percent: 100 },
  { name: 'round down', p: { total: 3, done: 1 }, total: 3, done: 1, count: '1/3', percent: 33 },
  { name: 'round up', p: { total: 3, done: 2 }, total: 3, done: 2, count: '2/3', percent: 67 },
  { name: 'half percent rounds up', p: { total: 200, done: 1 }, total: 200, done: 1, count: '1/200', percent: 1 },
  { name: 'upper clamp only', p: { total: 2, done: 5 }, total: 2, done: 5, count: '5/2', percent: 100 },
  { name: 'negative done is not clamped', p: { total: 4, done: -1 }, total: 4, done: -1, count: '-1/4', percent: -25 },
  { name: 'negative total is truthy', p: { total: -4, done: 1 }, total: -4, done: 1, count: '1/-4', percent: -25 },
  { name: 'two negatives', p: { total: -4, done: -2 }, total: -4, done: -2, count: '-2/-4', percent: 50 },
  { name: 'numeric strings retain counts', p: { total: '4', done: '1' }, total: '4', done: '1', count: '1/4', percent: 25 },
  { name: 'string zero is truthy', p: { total: '0', done: 1 }, total: '0', done: 1, count: '1/0', percent: 100 },
  { name: 'NaN falls back', p: { total: NaN, done: NaN }, total: 0, done: 0, count: '0', percent: 100 },
  { name: 'invalid truthy total stays NaN', p: { total: 'bad', done: 1 }, total: 'bad', done: 1, count: '1/bad', percent: NaN },
  { name: 'false counters fall back', p: { total: false, done: '' }, total: 0, done: 0, count: '0', percent: 100 }
];

for (const c of cases) {
  test(`pure counts: ${c.name}`, () => {
    const { counts } = harness('scrape');
    const input = Object.freeze({ ...c.p });
    const actual = counts(input);
    assert.equal(actual.total, c.total);
    assert.equal(actual.done, c.done);
    assert.equal(actual.percent, c.percent);
  });
}

for (const kind of ['scrape', 'probe', 'scan']) {
  test(`${kind}: count, width and indeterminate DOM boundaries`, () => {
    const { els, render } = harness(kind);
    for (const c of cases) {
      for (const running of [true, false]) {
        render({ ...c.p, running, finished_at: running ? '' : 'finished' });
        assert.equal(els.panel.hidden, false, c.name);
        assert.equal(els.count.textContent, c.count, c.name);
        assert.equal(els.fill.style.width, `${c.percent}%`, c.name);
        assert.equal(els.fill.classes['is-indeterminate'], !c.total && running, c.name);
      }
    }
    render(null);
    assert.equal(els.panel.hidden, true);
    render({ running: false });
    assert.equal(els.panel.hidden, true);
  });
}

test('scan groups are independent from movie counters; zero changes and cleanup safety remain visible', () => {
  const { els, render } = harness('scan');
  for (const mode of ['incremental', 'full']) {
    render({ running: false, finished_at: 'now', mode, groups_total: 5, groups_done: 2,
      total: 20, done: 17, success: 10, failed: 1, unchanged_groups: 1, added: 0, updated: 2, deleted: 0,
      incomplete: true, cleanup_performed: false });
    assert.equal(els.count.textContent, '2/5 组'); assert.equal(els.fill.style.width, '40%');
    assert.match(els.title.textContent, /存在错误/); assert.match(els.detail.textContent, /未变化 1 · 新增 0 · 更新 2 · 删除 0/);
    assert.match(els.detail.textContent, /未执行缺失索引清理（保留原索引）/); assert.match(els.detail.textContent, /部分成果已保留/);
    assert.equal(els.detail.attributes.role, 'status');
  }
  render({ running: false, finished_at: 'now', mode: 'incremental', groups_total: 3, groups_done: 3,
    unchanged_groups: 3, cleanup_performed: true });
  assert.match(els.detail.textContent, /已完成缺失索引清理检查/);
  assert.doesNotMatch(els.detail.textContent, /未执行缺失索引/); assert.match(els.title.textContent, /扫描完成/);
});

test('task-specific titles and details remain distinct', () => {
  const progress = { running: true, total: 3, done: 1, success: 1, skipped: 2,
    failed: 3, pending: 4, current: '/media/movie.strm', error: 'oops' };
  const expected = {
    scrape: ['正在补演员头像', '成功 1 · 跳过 2 · 失败 3 · 当前 /media/movie.strm · 错误：oops'],
    probe: ['正在探测媒体信息', '成功 1 · 跳过 2 · 失败 3 · 当前 /media/movie.strm · 错误：oops'],
    scan: ['正在扫描 媒体库', '已刮削 1 · 未刮削 4 · 跳过 2 · 失败 3 · 当前 movie.strm · 错误：oops']
  };
  for (const kind of ['scrape', 'probe', 'scan']) {
    const { els, render } = harness(kind);
    render({ ...progress, kind: 'scrape_avatars' });
    assert.equal(els.title.textContent, expected[kind][0]);
    assert.equal(els.detail.textContent, expected[kind][1]);
    render({ running: true });
    assert.equal(els.detail.textContent, { scrape: '正在处理…', probe: '正在读取源信息…', scan: '正在读取目录…' }[kind]);
    render({ running: false, finished_at: 'finished', cancelled: true, error: 'oops' });
    assert.equal(els.title.textContent, { scrape: '刮削结束', probe: '探测已中止', scan: '扫描结束（存在错误） 媒体库' }[kind]);
  }
});
