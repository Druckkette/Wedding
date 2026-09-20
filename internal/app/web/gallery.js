(() => {
  'use strict';
  const token = document.querySelector('meta[name="upload-token"]').content;
  const grid = document.querySelector('#gallery-grid');
  const empty = document.querySelector('#gallery-empty');
  const count = document.querySelector('#media-count');
  const liveStatus = document.querySelector('#live-status');
  const gallerySelect = document.querySelector('#gallery-select');
  const sortSelect = document.querySelector('#sort-select');
  const menuButton = document.querySelector('#download-menu-button');
  const menu = document.querySelector('#download-menu');
  const selectionBar = document.querySelector('#selection-bar');
  const selectionCount = document.querySelector('#selection-count');
  const lightbox = document.querySelector('#lightbox');
  const lightboxMedia = document.querySelector('#lightbox-media');
  const lightboxCaption = document.querySelector('#lightbox-caption');
  const lightboxClose = document.querySelector('#lightbox-close');
  const lightboxPrev = document.querySelector('#lightbox-prev');
  const lightboxNext = document.querySelector('#lightbox-next');
  const lightboxShare = document.querySelector('#lightbox-share');
  const lightboxDownload = document.querySelector('#lightbox-download');
  let items = [];
  let currentIndex = -1;
  let initialized = false;
  let selecting = false;
  let selected = new Set();
  let touchStart = null;

  const headers = (extra = {}) => ({ 'X-Upload-Token': token, ...extra });

  function setLiveStatus(isOnline) {
    liveStatus.classList.toggle('offline', !isOnline);
    liveStatus.replaceChildren();
    if (isOnline) {
      const dot = document.createElement('i');
      liveStatus.append(dot, document.createTextNode(' Live'));
    } else liveStatus.textContent = 'Verbindung wird wiederhergestellt …';
  }

  function mediaElement(item, full = false) {
    const media = document.createElement(item.kind === 'video' ? 'video' : 'img');
    media.src = item.url;
    if (item.kind === 'video') {
      media.muted = !full; media.playsInline = true; media.preload = full ? 'metadata' : 'none'; media.controls = full;
    } else {
      media.alt = item.is_challenge ? `Challenge: ${item.challenge}` : item.filename;
      media.loading = full ? 'eager' : 'lazy'; media.decoding = 'async';
    }
    return media;
  }

  function updateSelectionUI() {
    selectionBar.hidden = !selecting;
    grid.classList.toggle('selection-mode', selecting);
    selectionCount.textContent = `${selected.size} ${selected.size === 1 ? 'Foto ausgewählt' : 'Fotos ausgewählt'}`;
  }

  function cardFor(item, isNew) {
    const card = document.createElement('button');
    card.className = `gallery-card${isNew ? ' is-new' : ''}${item.is_challenge ? ' is-challenge' : ''}`;
    card.type = 'button'; card.dataset.id = item.id;
    card.setAttribute('aria-label', item.is_challenge ? `Challenge-Bild: ${item.challenge}` : `${item.filename} öffnen`);
    card.append(mediaElement(item));
    const check = document.createElement('span'); check.className = 'selection-check'; check.textContent = '✓'; card.append(check);
    if (item.kind === 'video') {
      const kind = document.createElement('span'); kind.className = 'media-kind'; kind.textContent = '▶ Video'; card.append(kind);
    }
    if (item.is_challenge) {
      const badge = document.createElement('span'); badge.className = 'challenge-badge';
      badge.textContent = `★ FOTO-CHALLENGE · ${item.challenge_by}`; card.append(badge);
    }
    card.addEventListener('click', () => {
      if (selecting) {
        if (item.kind !== 'image') return;
        if (selected.has(item.id)) selected.delete(item.id); else selected.add(item.id);
        card.classList.toggle('selected', selected.has(item.id)); updateSelectionUI();
      } else openLightbox(items.findIndex((candidate) => candidate.id === item.id));
    });
    return card;
  }

  function render() {
    const existing = new Map(Array.from(grid.children).map((card) => [card.dataset.id, card]));
    grid.replaceChildren(...items.map((item) => {
      const card = existing.get(item.id) || cardFor(item, initialized);
      card.classList.toggle('selected', selected.has(item.id)); return card;
    }));
    initialized = true; empty.hidden = items.length !== 0; grid.hidden = items.length === 0;
    count.textContent = `${items.length} ${items.length === 1 ? 'Aufnahme' : 'Aufnahmen'}`;
  }

  async function loadGalleries() {
    const response = await fetch('/api/galleries', { headers: headers(), cache: 'no-store' });
    if (!response.ok) throw new Error('gallery list failed');
    const payload = await response.json();
    const previous = gallerySelect.value;
    gallerySelect.replaceChildren(...payload.galleries.map((gallery) => {
      const option = document.createElement('option'); option.value = gallery.name;
      option.textContent = `${gallery.name} (${gallery.count})`; return option;
    }));
    if (payload.galleries.some((gallery) => gallery.name === previous)) gallerySelect.value = previous;
  }

  async function refresh() {
    try {
      if (!gallerySelect.value) await loadGalleries();
      const query = new URLSearchParams({ gallery: gallerySelect.value, sort: sortSelect.value });
      const response = await fetch(`/api/media?${query}`, { headers: headers(), cache: 'no-store' });
      if (!response.ok) throw new Error('gallery request failed');
      items = (await response.json()).items;
      selected = new Set(Array.from(selected).filter((id) => items.some((item) => item.id === id)));
      render(); updateSelectionUI(); setLiveStatus(true);
    } catch (_) { setLiveStatus(false); }
  }

  function showCurrent() {
    const item = items[currentIndex];
    if (!item) return closeLightbox();
    lightboxMedia.replaceChildren(mediaElement(item, true)); lightboxCaption.replaceChildren();
    if (item.is_challenge) {
      const title = document.createElement('strong'); title.textContent = `Challenge geschafft: ${item.challenge}`;
      const by = document.createElement('span'); by.textContent = item.challenge_by; lightboxCaption.append(title, by);
    } else {
      const filename = document.createElement('span');
      filename.textContent = item.captured_at ? `${item.filename} · aufgenommen ${new Date(item.captured_at).toLocaleString('de-DE')}` : item.filename;
      lightboxCaption.append(filename);
    }
    lightboxPrev.disabled = currentIndex <= 0; lightboxNext.disabled = currentIndex >= items.length - 1;
    lightboxShare.hidden = item.kind !== 'image';
    [items[currentIndex - 1], items[currentIndex + 1]].forEach((candidate) => {
      if (candidate?.kind === 'image') { const image = new Image(); image.src = candidate.url; }
    });
  }

  function openLightbox(index) {
    currentIndex = index; showCurrent(); lightbox.hidden = false; document.body.style.overflow = 'hidden';
    lightboxClose.focus({ preventScroll: true });
  }

  function closeLightbox() {
    const item = items[currentIndex];
    lightbox.hidden = true; lightboxMedia.replaceChildren(); document.body.style.overflow = '';
    if (item) document.querySelector(`[data-id="${CSS.escape(item.id)}"]`)?.scrollIntoView({ block: 'nearest' });
  }

  function move(direction) {
    const next = currentIndex + direction;
    if (next < 0 || next >= items.length) return;
    currentIndex = next; showCurrent();
  }

  async function originalFile(item) {
    const response = await fetch(item.download_url, { headers: headers() });
    if (!response.ok) throw new Error('Original konnte nicht geladen werden.');
    return new File([await response.blob()], item.filename, { type: item.content_type });
  }

  async function shareCurrent() {
    const item = items[currentIndex];
    if (!item) return;
    try {
      const file = await originalFile(item);
      if (navigator.canShare?.({ files: [file] })) await navigator.share({ files: [file], title: item.filename });
      else downloadOriginal(item);
    } catch (error) { if (error.name !== 'AbortError') window.alert(error.message || 'Teilen ist nicht verfügbar.'); }
  }

  async function downloadOriginal(item) {
    try {
      const file = await originalFile(item); const link = document.createElement('a');
      link.href = URL.createObjectURL(file); link.download = item.filename; link.click();
      window.setTimeout(() => URL.revokeObjectURL(link.href), 30000);
    } catch (error) { window.alert(error.message); }
  }

  async function downloadZIP(ids = []) {
    const button = ids.length ? document.querySelector('#download-selection') : document.querySelector('#download-all');
    const label = button.textContent; button.disabled = true; button.textContent = 'ZIP wird erstellt …';
    try {
      const response = await fetch('/api/download/zip', {
        method: 'POST', headers: headers({ 'Content-Type': 'application/json' }), body: JSON.stringify({ gallery: gallerySelect.value, ids }),
      });
      if (!response.ok) { const body = await response.json().catch(() => ({})); throw new Error(body.error || 'Download fehlgeschlagen.'); }
      const link = document.createElement('a'); link.href = URL.createObjectURL(await response.blob());
      link.download = `${gallerySelect.value}.zip`; link.click(); window.setTimeout(() => URL.revokeObjectURL(link.href), 30000);
    } catch (error) { window.alert(error.message); }
    finally { button.disabled = false; button.textContent = label; }
  }

  async function downloadSelection() {
    const chosen = items.filter((item) => selected.has(item.id) && item.kind === 'image');
    if (!chosen.length) return;
    const smallEnoughForShare = chosen.length <= 5 && chosen.reduce((total, item) => total + Number(item.size || 0), 0) <= 100 * 1024 * 1024;
    if (smallEnoughForShare && navigator.share && navigator.canShare) {
      try {
        const files = await Promise.all(chosen.map(originalFile));
        if (navigator.canShare({ files })) {
          await navigator.share({ files, title: gallerySelect.value });
          return;
        }
      } catch (error) {
        if (error.name === 'AbortError') return;
      }
    }
    await downloadZIP(chosen.map((item) => item.id));
  }

  function beginSelection() { selecting = true; selected.clear(); menu.hidden = true; updateSelectionUI(); }
  function endSelection() { selecting = false; selected.clear(); updateSelectionUI(); render(); }
  menuButton.addEventListener('click', () => { menu.hidden = !menu.hidden; menuButton.setAttribute('aria-expanded', String(!menu.hidden)); });
  document.querySelector('#download-all').addEventListener('click', () => downloadZIP());
  document.querySelector('#select-download').addEventListener('click', beginSelection);
  document.querySelector('#select-all').addEventListener('click', () => { selected = new Set(items.filter((item) => item.kind === 'image').map((item) => item.id)); render(); updateSelectionUI(); });
  document.querySelector('#clear-selection').addEventListener('click', () => { selected.clear(); render(); updateSelectionUI(); });
  document.querySelector('#cancel-selection').addEventListener('click', endSelection);
  document.querySelector('#download-selection').addEventListener('click', downloadSelection);
  gallerySelect.addEventListener('change', () => { selected.clear(); refresh(); }); sortSelect.addEventListener('change', refresh);
  lightboxClose.addEventListener('click', closeLightbox); lightboxPrev.addEventListener('click', () => move(-1)); lightboxNext.addEventListener('click', () => move(1));
  lightboxShare.addEventListener('click', shareCurrent); lightboxDownload.addEventListener('click', () => items[currentIndex] && downloadOriginal(items[currentIndex]));
  lightbox.addEventListener('click', (event) => { if (event.target === lightbox) closeLightbox(); });
  lightbox.addEventListener('pointerdown', (event) => { if (event.pointerType !== 'mouse') touchStart = { x: event.clientX, y: event.clientY, at: Date.now() }; });
  lightbox.addEventListener('pointerup', (event) => {
    if (!touchStart) return;
    const dx = event.clientX - touchStart.x, dy = event.clientY - touchStart.y;
    if (Math.abs(dx) >= 55 && Math.abs(dx) > Math.abs(dy) * 1.35 && Date.now() - touchStart.at < 900) move(dx < 0 ? 1 : -1);
    touchStart = null;
  });
  document.addEventListener('keydown', (event) => {
    if (lightbox.hidden) return;
    if (event.key === 'Escape') closeLightbox();
    if (event.key === 'ArrowLeft') move(-1);
    if (event.key === 'ArrowRight') move(1);
  });
  refresh().then(() => window.setInterval(refresh, 10000));
})();
