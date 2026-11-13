'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const path = require('node:path');
const client = fs.readFileSync(path.join(__dirname, 'web/emby.js'), 'utf8');
const app = fs.readFileSync(path.join(__dirname, 'web/app.js'), 'utf8');
const tick = () => new Promise(setImmediate);
function context(values = {}) {
  return vm.createContext({ URLSearchParams, token: 'A', me: { id: '7', is_admin: true },
    localStorage: { getItem: () => 'A' }, WALL_PAGE_SIZE: 100, ...values });
}
function loadClient(box) { vm.runInContext(client, box); return box; }
function part(start, end) { return app.slice(app.indexOf(start), app.indexOf(end, app.indexOf(start))); }

test('Emby request uses visible /emby paths, token headers, JSON and preserves 204', async () => {
  const calls = [];
  const box = loadClient(context({ fetch: async (url, options) => { calls.push({ url, options }); return { ok: true, status: 204 }; } }));
  assert.equal(await box.embyRequest('/Users/7/PlayedItems/9', { method: 'POST', body: '{}' }), null);
  assert.equal(calls[0].url, '/emby/Users/7/PlayedItems/9');
  assert.equal(calls[0].options.headers['X-Emby-Token'], 'A');
  assert.equal(calls[0].options.headers['Content-Type'], 'application/json');
  assert.equal(calls.length, 1);
});

test('Emby request rejects before send and after delayed JSON when identity changed', async () => {
  let stored = 'B', calls = 0, resolve;
  const box = loadClient(context({ localStorage: { getItem: () => stored }, fetch: async () => { calls++; return { ok: true, status: 200, json: () => new Promise(r => { resolve = r; }) }; } }));
  await assert.rejects(box.embyRequest('/Users/7/Items'), e => e.code === 'IDENTITY_CHANGED');
  assert.equal(calls, 0);
  stored = 'A'; const pending = box.embyRequest('/Users/7/Items'); await tick(); stored = 'B'; resolve({ Items: [{ Id: 'private' }] });
  await assert.rejects(pending, e => e.code === 'IDENTITY_CHANGED'); assert.equal(calls, 1);
});

test('Emby actual 401 clears current credential; 403 keeps credential with status', async () => {
  for (const status of [401, 403]) {
    const removed = [], redirects = [];
    const box = loadClient(context({ localStorage: { getItem: () => 'A', removeItem: key => removed.push(key) }, window: { location: { replace: url => redirects.push(url) } },
      fetch: async () => ({ ok: false, status, json: async () => ({ error: 'denied' }) }) }));
    await assert.rejects(box.embyRequest('/Users/7/Items'), e => e.status === status);
    assert.equal(removed.length, status === 401 ? 1 : 0); assert.equal(redirects.length, status === 401 ? 1 : 0);
  }
});

test('Native wall query combines paging, sorting, library, entity and user Filters', () => {
  const box = loadClient(context({ entityIdOf: (_, v) => 'boxset:' + v }));
  const base = { offset: 100, sort: 'year', order: 'asc', search: 'A B', libraryID: '1000000000001', status: 'favorite', entity: ['genre', '类型', '剧情'] };
  const query = box.wallEmbyQuery(base);
  assert.equal(query.get('StartIndex'), '100'); assert.equal(query.get('Limit'), '100');
  assert.equal(query.get('SortBy'), 'ProductionYear'); assert.equal(query.get('SortOrder'), 'Ascending');
  assert.equal(query.get('SearchTerm'), 'A B'); assert.equal(query.get('ParentId'), '1000000000001'); assert.equal(query.get('Genres'), '剧情');
  for (const [status, filter] of [['favorite', 'IsFavorite'], ['played', 'IsPlayed'], ['unplayed', 'IsUnplayed']]) assert.equal(box.wallEmbyQuery({ ...base, status }).get('Filters'), filter);
  assert.equal(box.wallEmbyQuery({ ...base, libraryID: '', entity: ['collection', '合集', 'boxset:YQ'] }).get('ParentId'), 'boxset:YQ');
  assert.equal(query.has('library_id'), false); assert.equal(query.has('status'), false);
});

test('Collection navigation explicitly clears library selection instead of claiming an intersection', () => {
  let url;
  const box = context({ ENTITY_PARAMS: [['collection']], wallState: { libraryID: '1000000000001' }, location: { pathname: '/' }, history: { pushState: (_, __, value) => { url = value; } }, closeDetail() {}, pageItems() {} });
  vm.runInContext(part('function gotoEntity(', 'const fmtSize'), box);
  box.gotoEntity('collection', 'boxset:YQ'); assert.equal(new URL('http://fixture' + url).searchParams.has('library_id'), false);
  box.gotoEntity('genre', '剧情'); assert.equal(new URL('http://fixture' + url).searchParams.get('library_id'), '1000000000001');
});

test('Native detail only reads Emby DTOs; userdata mutations and maintenance remain explicit', async () => {
  const nodes = new Map(), calls = []; let maintenance = 0, stored = 'A';
  const node = id => { if (!nodes.has(id)) nodes.set(id, { hidden: false, innerHTML: '', handlers: {}, setAttribute() {}, addEventListener(type, fn) { this.handlers[type] = fn; }, querySelectorAll: () => [] }); return nodes.get(id); };
  const box = loadClient(context({ pageGen: 1, detailGen: 0, wallState: null, localStorage: { getItem: () => stored },
    detailOverlay: node('overlay'), detailDrawer: node('drawer'), document: { querySelector: node }, closeDetail() {}, overlayPush() {}, overlayFocusFirst() {},
    esc: String, icon: () => '', fmtDuration: () => '', fmtSize: () => '', fmtTime: () => '', detailMetaRow: () => '', streamTable: () => '', toast() {},
    openMaintenanceDetail: () => { maintenance++; }, fetch: async (url, options) => { calls.push([url, options.method || 'GET']);
      const data = url.endsWith('/Collections') ? { Items: [{ Id: 'boxset:YQ', Name: '合集' }] } : url.includes('/FavoriteItems/') ? { IsFavorite: true } :
        { Id: '9', Name: 'native title', UserData: {}, People: [{ Id: 'person:YQ', Name: '演员', Type: 'Actor', PrimaryImageTag: 'v1' }], MediaSources: [], ImageTags: { Primary: 'v2' } };
      return { ok: true, status: 200, json: async () => data }; } }));
  await box.openDetail({ Id: '9' });
  assert.deepEqual(calls, [['/emby/Users/7/Items/9', 'GET'], ['/emby/Users/7/Items/9/Collections', 'GET']]);
  assert.match(node('#detail-content').innerHTML, /native title/); assert.match(node('#detail-content').innerHTML, /\/emby\/Items\/person%3AYQ\/Images\/Primary/);
  assert.equal(maintenance, 0);
  await node('#detail-favorite').handlers.click(); assert.deepEqual(calls.at(-1), ['/emby/Users/7/FavoriteItems/9', 'POST']);
  assert.equal(node('#detail-favorite').textContent, '取消收藏');
  node('#detail-maintenance').handlers.click(); assert.equal(maintenance, 1);
  stored = 'B'; box.token = 'B'; const before = calls.length;
  await node('#detail-favorite').handlers.click(); node('#detail-play').handlers.click(); node('#detail-maintenance').handlers.click();
  assert.equal(calls.length, before, '旧身份DOM不能用新token继续写用户状态'); assert.equal(maintenance, 1);
});

test('Native detail identity change between reads does not request Collections or render old data', async () => {
  const nodes = new Map(), calls = []; let resolve;
  const node = id => { if (!nodes.has(id)) nodes.set(id, { innerHTML: '', addEventListener() {} }); return nodes.get(id); };
  const box = loadClient(context({ pageGen: 1, detailGen: 0, detailOverlay: node('overlay'), detailDrawer: node('drawer'),
    closeDetail() {}, overlayPush() {}, document: { querySelector: node } }));
  box.embyRequest = url => { calls.push(url); return new Promise(r => { resolve = r; }); };
  const pending = box.openDetail({ Id: '9' }); box.token = 'B'; resolve({ Id: '9', Name: 'old private data' }); await pending;
  assert.deepEqual(calls, ['/Users/7/Items/9']); assert.equal(node('#detail-content').innerHTML, '');
});

test('Direct collection URL normalizes conflicting library and only requests native Views', async () => {
  let replaced = '', adminCalls = 0;
  const element = { addEventListener() {}, value: '' };
  const box = context({ me: null, pageGen: 1, wallGen: 0, wallState: null,
    ENTITY_PARAMS: [['collection', '合集']], content: { innerHTML: '' }, esc: String, icon: () => '',
    location: { search: '?library_id=1000000000001&collection=boxset%3AYQ', hash: '#items', pathname: '/' },
    history: { replaceState: (_, __, url) => { replaced = url; } }, document: { querySelector: () => element, querySelectorAll: () => [] },
    debounce: fn => fn, runProbeAll() {}, stopTranslationPolling() {}, stopProbePolling() {}, stopLogsPolling() {}, stopScanPolling() {},
    api: () => { adminCalls++; }, embyUserPath: () => '/Users/7',
    embyRequest: async url => {
      // 只看原生 Emby：库列表 + 合集实体详情，不再走任何 /api/admin。
      if (url === '/Users/7/Views') return { Items: [{ Id: '1000000000001', Name: 'library A' }] };
      assert.equal(url, '/Users/7/Items/boxset%3AYQ');
      return { Id: 'boxset:YQ', Name: '系列合集', ChildCount: 2 };
    }, loadWallPage: async () => {} });
  vm.runInContext(part('async function pageItems()', '// reloadWall'), box); await box.pageItems();
  assert.equal(box.wallState.libraryID, ''); assert.equal(new URL('http://fixture' + replaced).searchParams.has('library_id'), false);
  assert.equal(box.wallState.entity[2], 'boxset:YQ'); assert.equal(adminCalls, 0);
});

function playerFixture(playbackInfo) {
  const nodes = new Map(), players = [], calls = [], notices = [];
  const node = id => { if (!nodes.has(id)) nodes.set(id, { hidden: true, innerHTML: '', textContent: '' }); return nodes.get(id); };
  class Artplayer { constructor(options) { this.options = options; this.events = {}; this.currentTime = 0; this.duration = 100; this.playing = true; players.push(this); } on(name, fn) { this.events[name] = fn; } destroy() { this.destroyed = true; } }
  const box = context({ Artplayer, document: { querySelector: node, body: { classList: { add() {}, remove() {} } } }, overlayPush() {}, overlayPop() {}, movieDisplayTitle: m => m.Name, mountEmbyPreview: () => () => {}, esc: String,
    toast: message => notices.push(message), embyRequest: async (url, options) => { calls.push({ url, body: JSON.parse(options.body), options });
      if (url.endsWith('/PlaybackInfo')) return playbackInfo ? playbackInfo() : { PlaySessionId: 'session-' + calls.length, MediaSources: [{ Id: 'source9', Container: 'mp4', DirectStreamUrl: '/Videos/9/stream.mp4?api_key=A' }] }; return null; } });
  vm.runInContext(part('let artInstance = null;', '/* ---------------------------------------------------------------- 设置 */'), box);
  return { box, calls, players, node, notices };
}

test('Player negotiates source, resumes ticks, serializes start/progress/stop and stops once', async () => {
  const env = playerFixture(); await env.box.openPlayer({ Id: '9', Name: 'native', UserData: { PlaybackPositionTicks: 200000000 } });
  assert.equal(env.calls[0].url, '/Items/9/PlaybackInfo'); assert.equal(env.calls[0].body.UserId, '7');
  const player = env.players[0]; assert.equal(player.options.url, '/emby/Videos/9/stream.mp4?api_key=A');
  player.events['video:loadedmetadata'](); assert.equal(player.seek, 20);
  player.currentTime = 25; player.events['video:playing'](); player.events['video:pause'](); env.box.closePlayer(); env.box.closePlayer(); await tick();
  const reports = env.calls.slice(1); assert.deepEqual(reports.map(c => c.url), ['/Sessions/Playing', '/Sessions/Playing/Progress', '/Sessions/Playing/Stopped']);
  assert.equal(reports[0].body.PositionTicks, 250000000); assert.equal(reports[0].body.PlaySessionId, reports[2].body.PlaySessionId); assert.equal(reports[2].options.keepalive, true);
});

test('Natural replay negotiates a fresh PlaySessionId and starts at zero', async () => {
  const env = playerFixture(); await env.box.openPlayer({ Id: '9', UserData: { PlaybackPositionTicks: 200000000 } });
  env.players[0].events['video:playing'](); env.players[0].events['video:ended'](); await tick();
  env.players[0].events['video:playing'](); await tick();
  assert.equal(env.players.length, 2); assert.equal(env.players[0].destroyed, true);
  env.players[1].events['video:loadedmetadata'](); assert.equal(env.players[1].seek, undefined);
  env.players[1].events['video:playing'](); await tick();
  const starts = env.calls.filter(c => c.url === '/Sessions/Playing'); assert.equal(starts.length, 2); assert.notEqual(starts[0].body.PlaySessionId, starts[1].body.PlaySessionId);
});

test('Closing during PlaybackInfo suppresses late player construction and no-source error is visible', async () => {
  let resolve; const env = playerFixture(() => new Promise(r => { resolve = r; }));
  const pending = env.box.openPlayer({ Id: '9' }); env.box.closePlayer(); resolve({ PlaySessionId: 'late', MediaSources: [{ DirectStreamUrl: '/Videos/9/stream.mp4' }] }); await pending;
  assert.equal(env.players.length, 0);
  const missing = playerFixture(() => ({ MediaSources: [] })); await missing.box.openPlayer({ Id: '9' }); assert.match(missing.node('#player-stage').innerHTML, /暂无可用播放源/);
});
