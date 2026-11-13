/* Emby browsing client. Keep the wire DTOs and protocol paths visible in captures. */
async function embyRequest(path, options = {}) {
  const identity = token;
  const currentIdentity = () => token === identity && localStorage.getItem('emby_token') === identity;
  const identityError = () => Object.assign(new Error('登录状态已改变'), { status: 401, code: 'IDENTITY_CHANGED' });
  if (!currentIdentity()) throw identityError();
  const headers = { 'X-Emby-Token': identity, ...options.headers };
  if (options.body) headers['Content-Type'] = 'application/json';
  const response = await fetch('/emby' + path, { ...options, headers });
  if (!currentIdentity()) throw identityError();
  if (response.status === 401) {
    localStorage.removeItem('emby_token');
    window.location.replace('/');
    throw Object.assign(new Error('登录已过期'), { status: 401 });
  }
  if (!response.ok) {
    const data = await response.json().catch(() => ({}));
    if (!currentIdentity()) throw identityError();
    const error = new Error(data.error || `请求失败 (${response.status})`);
    error.status = response.status;
    throw error;
  }
  const data = response.status === 204 ? null : await response.json();
  if (!currentIdentity()) throw identityError();
  return data;
}

function embyUserPath() {
  return '/Users/' + encodeURIComponent(me.id);
}

function wallEmbyQuery(state) {
  const query = new URLSearchParams({
    IncludeItemTypes: 'Movie', Recursive: 'true', Limit: String(WALL_PAGE_SIZE),
    StartIndex: String(state.offset), SortBy: ({ title: 'SortName', 'title-desc': 'SortName', year: 'ProductionYear', communityrating: 'CommunityRating' })[state.sort] || 'DateCreated',
    SortOrder: state.order === 'asc' ? 'Ascending' : 'Descending', Fields: 'DateCreated,MediaSources'
  });
  if (state.view === 'collections' && !state.entity) {
    query.set('IncludeItemTypes', 'BoxSet');
    query.set('SortBy', 'SortName');
    query.delete('Fields');
    if (state.search) query.set('SearchTerm', state.search);
    return query;
  }
  if (state.search) query.set('SearchTerm', state.search);
  if (state.libraryID) query.set('ParentId', state.libraryID);
  const filter = ({ favorite: 'IsFavorite', played: 'IsPlayed', unplayed: 'IsUnplayed' })[state.status];
  if (filter) query.set('Filters', filter);
  if (state.entity) {
    const [kind, , value] = state.entity;
    const key = ({ person: 'Person', genre: 'Genres', tag: 'Tags', studio: 'Studios' })[kind];
    if (key) query.set(key, value);
    else if (kind === 'collection') query.set('ParentId', value.startsWith('boxset:') ? value : entityIdOf('boxset', value));
  }
  return query;
}

async function openDetail(item) {
  closeDetail();
  const gen = pageGen, session = ++detailGen, identity = token;
  const current = () => gen === pageGen && session === detailGen && token === identity && localStorage.getItem('emby_token') === identity;
  detailDrawer.innerHTML = '<div class="detail-toolbar"><button id="detail-close" class="icon-btn" aria-label="关闭详情">×</button></div><p class="overlay-feedback" hidden></p><div id="detail-content"><div class="detail-body" role="status">加载中…</div></div>';
  document.querySelector('#detail-close').addEventListener('click', closeDetail);
  overlayPush('#detail-overlay');
  try {
    const m = await embyRequest(embyUserPath() + '/Items/' + encodeURIComponent(item.Id));
    if (!current()) return;
    const collections = await embyRequest(embyUserPath() + '/Items/' + encodeURIComponent(item.Id) + '/Collections');
    if (!current()) return;
    let ud = m.UserData || {};
    const posterKind = m.ImageTags?.Primary ? 'Primary' : 'Thumb';
    const poster = embyImageURL(m, posterKind, 240);
    const posterSrcset = poster ? `${embyImageURL(m, posterKind, 120)} 1x, ${poster} 2x` : '';
    const backdropKind = m.BackdropImageTags?.length ? 'Backdrop' : (m.ImageTags?.Thumb ? 'Thumb' : 'Primary');
    const backdrop = embyImageURL(m, backdropKind, 1080);
    const backdropSrcset = backdrop ? `${embyImageURL(m, backdropKind, 540)} 1x, ${backdrop} 2x` : '';
    const actors = (m.People || []).filter(person => person.Type === 'Actor');
    document.querySelector('#detail-content').innerHTML = `
      <div class="detail-hero">${backdrop ? `<img class="detail-backdrop" src="${esc(backdrop)}" srcset="${esc(backdropSrcset)}" alt="">` : ''}<div class="detail-hero-body">${poster ? `<img class="detail-poster" width="104" height="156" src="${esc(poster)}" srcset="${esc(posterSrcset)}" alt="">` : ''}<div class="detail-hero-text"><h2>${esc(m.Name)}</h2><p class="detail-sub">${esc([m.ProductionYear, fmtDuration((m.RunTimeTicks || 0) / 10000000), m.CommunityRating ? '★ ' + m.CommunityRating : ''].filter(Boolean).join(' · '))}</p></div></div></div><div class="detail-body">
      <div class="detail-actions"><button id="detail-play" class="btn btn-accent">${icon('play')}<span>播放</span></button><button id="detail-favorite" class="btn"></button><button id="detail-played" class="btn"></button><button id="detail-like" class="btn"></button><button id="detail-dislike" class="btn"></button></div>
      ${me.is_admin ? '<button id="detail-maintenance" class="btn">维护与元数据编辑</button>' : ''}
      ${m.Overview ? `<section class="detail-section"><h3>简介</h3><p class="detail-plot">${esc(m.Overview)}</p></section>` : ''}
      <section class="detail-section"><h3>元数据</h3><dl class="detail-meta">${detailMetaRow('原名', m.OriginalTitle)}${detailMetaRow('分级', m.OfficialRating)}${detailMetaRow('类型', m.Genres, 'genre')}${detailMetaRow('标签', m.Tags, 'tag')}${detailMetaRow('厂商', (m.Studios || []).map(studio => studio.Name), 'studio')}${detailMetaRow('导演', (m.People || []).filter(person => person.Type === 'Director').map(person => person.Name))}</dl>
      <div class="filters">${(collections.Items || []).map(collection => `<button class="pill" data-entity-key="collection" data-entity-value="${esc(collection.Id)}">合集：${esc(collection.Name)}</button>`).join('')}</div></section>
      <section class="detail-section"><h3>演员 <small>${actors.length}</small></h3><div class="avatar-grid">${actors.map(actor => `<button class="avatar-card" data-entity-key="person" data-entity-value="${esc(actor.Name)}">${actor.PrimaryImageTag ? `<img loading="lazy" src="/emby/Items/${encodeURIComponent(actor.Id)}/Images/Primary?maxWidth=200&tag=${encodeURIComponent(actor.PrimaryImageTag)}" alt="">` : `<span class="avatar-fallback">${esc(actor.Name.slice(0, 1))}</span>`}<small>${esc(actor.Name)}</small></button>`).join('')}</div></section>
      <section class="detail-section"><h3>媒体信息</h3>${(m.MediaSources || []).map(source => `<div class="detail-file"><strong>${esc(source.Name || m.Name)}</strong><span class="hint">${esc(fmtSize(source.Size))}</span>${streamTable(source.MediaStreams || [])}<p class="detail-path mono">${esc(source.Path || '')}</p></div>`).join('')}</section>
      <section class="detail-section"><h3>文件与时间</h3><dl class="detail-meta">${detailMetaRow('源文件', m.Path)}${detailMetaRow('入库时间', fmtTime(m.DateCreated))}${detailMetaRow('最后修改', fmtTime(m.DateModified))}</dl></section></div>`;
    const favorite = document.querySelector('#detail-favorite'), played = document.querySelector('#detail-played');
    const like = document.querySelector('#detail-like'), dislike = document.querySelector('#detail-dislike');
    const updateButtons = () => {
      for (const [button, active, label] of [[favorite, ud.IsFavorite, '收藏'], [played, ud.Played, '已看'], [like, ud.Likes === true, '喜欢'], [dislike, ud.Likes === false, '不喜欢']]) {
        button.textContent = (active ? '取消' : '') + label;
        button.setAttribute('aria-pressed', String(!!active));
      }
    };
    updateButtons();
    for (const [button, path, active] of [
      [favorite, '/FavoriteItems/' + m.Id, () => ud.IsFavorite],
      [played, '/PlayedItems/' + m.Id, () => ud.Played],
      [like, '/Items/' + m.Id + '/Rating?Likes=true', () => ud.Likes === true],
      [dislike, '/Items/' + m.Id + '/Rating?Likes=false', () => ud.Likes === false]
    ]) button.addEventListener('click', async () => {
      if (!current()) return;
      button.disabled = true;
      try {
        const next = await embyRequest(embyUserPath() + path, { method: active() ? 'DELETE' : 'POST' });
        if (!current()) return;
        ud = next; updateButtons();
        if (wallState) reloadWall();
      } catch (error) { if (current()) toast(error.message, 'error'); }
      finally { if (current()) button.disabled = false; }
    });
    document.querySelector('#detail-play').addEventListener('click', () => { if (!current()) return; closeDetail(); void openPlayer(m); });
    document.querySelector('#detail-maintenance')?.addEventListener('click', () => { if (current()) openMaintenanceDetail({ id: m.Id }); });
    detailDrawer.querySelectorAll('[data-entity-key]').forEach(button => button.addEventListener('click', () => { if (current()) gotoEntity(button.dataset.entityKey, button.dataset.entityValue); }));
    overlayFocusFirst('#detail-overlay');
  } catch (error) {
    if (!current()) return;
    document.querySelector('#detail-content').innerHTML = `<p role="alert">${esc(error.message)}</p><button id="detail-retry" class="btn">重试加载</button>`;
    document.querySelector('#detail-retry').addEventListener('click', () => openDetail(item));
  }
}

function embyImageURL(item, kind, width) {
  const tag = item.ImageTags?.[kind] || (kind === 'Backdrop' ? item.BackdropImageTags?.[0] : '');
  if (!tag) return '';
  return `/emby/Items/${encodeURIComponent(item.Id)}/Images/${kind}?maxWidth=${width}&tag=${encodeURIComponent(tag)}`;
}
