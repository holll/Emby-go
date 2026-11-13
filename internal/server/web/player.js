/* Emby ThumbnailSet discovery and authenticated timeline thumbnail rendering. */
function mountEmbyPreview(art, itemID, stage, identity, isCurrent) {
  const controller = new AbortController();
  let imageController = null, disposed = false, objectURL = '', thumbnails = [], sequence = 0;
  const rail = document.createElement('section');
  rail.className = 'sprites-timeline'; rail.hidden = true;
  rail.setAttribute('aria-label', '视频预览时间轴');
  rail.innerHTML = '<label>预览时间轴<input type="range" min="0" step="1" value="0" aria-label="视频预览时间"></label><div class="sprites-timeline-actions"><output>00:00:00</output><button type="button" class="btn">跳转此时间</button></div><img alt="视频预览" hidden><p role="status" hidden></p>';
  stage.after(rail);
  const range = rail.querySelector('input'), output = rail.querySelector('output'), image = rail.querySelector('img'), notice = rail.querySelector('p');
  image.style.maxWidth = '160px';
  const current = () => !disposed && isCurrent() && token === identity && localStorage.getItem('emby_token') === identity;
  const label = ticks => {
    const seconds = Math.floor(ticks / 10000000);
    return [Math.floor(seconds / 3600), Math.floor(seconds / 60) % 60, seconds % 60].map(value => String(value).padStart(2, '0')).join(':');
  };
  async function show(index) {
    const thumb = thumbnails[index];
    if (!thumb || !current()) return;
    const ownSequence = ++sequence;
    output.textContent = label(thumb.PositionTicks);
    range.setAttribute('aria-valuetext', output.textContent);
    imageController?.abort(); imageController = new AbortController();
    const signal = imageController.signal;
    const query = new URLSearchParams({ Width: '320', PositionTicks: String(thumb.PositionTicks), tag: thumb.ImageTag });
    try {
      const response = await fetch(`/emby/Items/${encodeURIComponent(itemID)}/Images/Thumbnail?${query}`, { signal, headers: { 'X-Emby-Token': identity } });
      if (!response.ok) throw new Error('预览图片不可用');
      const blob = await response.blob();
      if (!current() || ownSequence !== sequence) return;
      if (objectURL) URL.revokeObjectURL(objectURL);
      objectURL = URL.createObjectURL(blob); image.src = objectURL; image.hidden = false; notice.hidden = true;
    } catch (error) {
      if (current() && ownSequence === sequence && !signal.aborted) { image.hidden = true; notice.hidden = false; notice.textContent = '预览图片不可用，不影响播放。'; }
    }
  }
  const select = () => void show(Number(range.value));
  const seek = () => { const thumb = thumbnails[Number(range.value)]; if (thumb && current()) art.seek = thumb.PositionTicks / 10000000; };
  const hover = (type, fraction, event) => {
    if (type !== 'hover' || event?.type === 'mouseleave' || !thumbnails.length || !Number.isFinite(fraction)) return;
    const ticks = Math.max(0, Math.min(1, fraction)) * art.duration * 10000000;
    let index = thumbnails.findIndex(thumb => thumb.PositionTicks > ticks);
    index = index < 0 ? thumbnails.length - 1 : Math.max(0, index - 1);
    if (Number(range.value) !== index || image.hidden) { range.value = String(index); void show(index); }
  };
  range.addEventListener('input', select); rail.querySelector('button').addEventListener('click', seek); art.on('setBar', hover);
  void (async () => {
    try {
      const result = await embyRequest(`/Items/${encodeURIComponent(itemID)}/ThumbnailSet?Width=320`, { signal: controller.signal });
      if (!current()) return;
      thumbnails = result.Thumbnails || [];
      if (!thumbnails.length) return;
      range.max = String(thumbnails.length - 1); rail.hidden = false; output.textContent = label(0);
    } catch (error) {
      if (current() && error.status !== 404) { rail.hidden = false; range.disabled = true; rail.querySelector('button').disabled = true; notice.hidden = false; notice.textContent = '视频预览暂不可用，不影响播放。'; }
    }
  })();
  return () => {
    disposed = true; controller.abort(); imageController?.abort();
    if (objectURL) URL.revokeObjectURL(objectURL);
    range.removeEventListener('input', select); art.off('setBar', hover); rail.remove();
  };
}
