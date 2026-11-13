'use strict';
const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const source = fs.readFileSync(`${__dirname}/web/app.js`, 'utf8');
const context = vm.createContext({ esc: String, icon: () => '', numberUnlessInTitle: () => '', STATUS_TEXT: {}, me: { is_admin: false }, embyImageURL: () => '' });
vm.runInContext(source.slice(source.indexOf('function cleanDisplayName('), source.indexOf('const WALL_PAGE_SIZE')), context);

test('display names strip only terminal valid CD parts and retain source fields', () => {
  for (const [value, want] of [['Movie-CD1', 'Movie'], ['Movie_cd2', 'Movie'], ['Movie.cd10', 'Movie'], ['Movie CD123', 'Movie'], ['Movie - CD2', 'Movie'], ['Movie-CD0', 'Movie-CD0'], ['MovieCD1', 'MovieCD1'], ['Movie-CD1-U', 'Movie-CD1-U'], ['CD1 的故事', 'CD1 的故事']]) {
    assert.equal(context.cleanDisplayName(value), want);
  }
  const item = { id: 7, source_path: '/media/dir-CD1/FC2-PPV-1050737-CD1.strm', Status: 'pending', AdditionalParts: ['/media/part-CD2.strm'] };
  const before = JSON.stringify(item);
  assert.equal(context.movieDisplayTitle(item), 'FC2-PPV-1050737');
  const embyItem = { Id: '7', Name: 'FC2-PPV-1050737-CD1', Path: item.source_path, PartCount: 2, UserData: {} };
  const embyBefore = JSON.stringify(embyItem);
  const card = context.wallCard(embyItem);
  assert.equal(JSON.stringify(embyItem), embyBefore);
  assert.match(card, />FC2-PPV-1050737<\/button>/);
  assert.match(card, />2 段<\/span>/);
  assert.doesNotMatch(card, /CD×/);
  // Diagnostic hover retains the real path; visible card text does not.
  assert.doesNotMatch(card.replace(/title="[^"]*"/g, ''), /CD1|CD2/);
  assert.equal(JSON.stringify(item), before);
  assert.equal(context.movieDisplayTitle({ Title: 'Title-CD2', Number: 'ABF-018' }), 'Title');
  assert.equal(context.movieDisplayTitle({ Number: 'ABF-018-CD1' }), 'ABF-018');
  assert.equal(context.movieDisplayTitle({ source_path: 'C:\\media\\Movie-CD10.strm' }), 'Movie');
});

test('detail and player display use the helper while editing preserves original title', () => {
  assert.match(source, /const title = movieDisplayTitle\(m\)/);
  assert.match(source, /const title = movieDisplayTitle\(item\) \|\| '播放'/);
  assert.ok(source.includes('value="${esc(m.Title || \'\')}"'));
  assert.ok(source.includes("numberUnlessInTitle(title, cleanDisplayName(m.Number))"));
  assert.ok(source.includes("detailMetaRow('番号', cleanDisplayName(m.Number))"));
  assert.ok(source.includes("detailMetaRow('原名', cleanDisplayName(m.OriginalTitle))"));
});
