(() => {
  'use strict';

  const token = document.querySelector('meta[name="upload-token"]').content;
  const grid = document.querySelector('#gallery-grid');
  const empty = document.querySelector('#gallery-empty');
  const count = document.querySelector('#media-count');
  const liveStatus = document.querySelector('#live-status');
  const lightbox = document.querySelector('#lightbox');
  const lightboxMedia = document.querySelector('#lightbox-media');
  const lightboxCaption = document.querySelector('#lightbox-caption');
  const lightboxClose = document.querySelector('#lightbox-close');
  const known = new Set();
  let initialized = false;

  function setLiveStatus(isOnline) {
    liveStatus.classList.toggle('offline', !isOnline);
    liveStatus.replaceChildren();
    if (isOnline) {
      const dot = document.createElement('i');
      liveStatus.append(dot, document.createTextNode(' Live'));
    } else {
      liveStatus.textContent = 'Verbindung wird wiederhergestellt …';
    }
  }

  function mediaElement(item, full = false) {
    const media = document.createElement(item.kind === 'video' ? 'video' : 'img');
    media.src = item.url;
    if (item.kind === 'video') {
      media.muted = !full;
      media.playsInline = true;
      media.preload = 'metadata';
      media.controls = full;
    } else {
      media.alt = item.is_challenge ? `Challenge: ${item.challenge}` : 'Hochzeitsfoto';
      media.loading = full ? 'eager' : 'lazy';
    }
    return media;
  }

  function openLightbox(item) {
    lightboxMedia.replaceChildren(mediaElement(item, true));
    lightboxCaption.replaceChildren();
    if (item.is_challenge) {
      const title = document.createElement('strong');
      title.textContent = `Challenge geschafft: ${item.challenge}`;
      const by = document.createElement('span');
      by.textContent = item.challenge_by;
      lightboxCaption.append(title, by);
    } else if (item.guest_name) {
      const by = document.createElement('span');
      by.textContent = `Hochgeladen von ${item.guest_name}`;
      lightboxCaption.append(by);
    }
    lightbox.hidden = false;
    document.body.style.overflow = 'hidden';
    lightboxClose.focus();
  }

  function closeLightbox() {
    lightbox.hidden = true;
    lightboxMedia.replaceChildren();
    document.body.style.overflow = '';
  }

  function createCard(item, isNew) {
    const card = document.createElement('button');
    card.className = `gallery-card${isNew ? ' is-new' : ''}`;
    card.type = 'button';
    card.setAttribute('aria-label', item.is_challenge ? `Challenge-Bild: ${item.challenge}` : 'Aufnahme öffnen');
    card.append(mediaElement(item));
    if (item.kind === 'video') {
      const kind = document.createElement('span');
      kind.className = 'media-kind';
      kind.textContent = '▶ Video';
      card.append(kind);
    }
    if (item.is_challenge) {
      const badge = document.createElement('span');
      badge.className = 'challenge-badge';
      badge.textContent = `★ ${item.challenge_by}`;
      card.append(badge);
    }
    card.addEventListener('click', () => openLightbox(item));
    return card;
  }

  async function refresh() {
    try {
      const response = await fetch('/api/media', { headers: { 'X-Upload-Token': token }, cache: 'no-store' });
      if (!response.ok) throw new Error('gallery request failed');
      const { items } = await response.json();
      const additions = items.filter((item) => !known.has(item.id));
      additions.slice().reverse().forEach((item) => {
        known.add(item.id);
        grid.prepend(createCard(item, initialized));
      });
      initialized = true;
      empty.hidden = items.length !== 0;
      grid.hidden = items.length === 0;
      count.textContent = `${items.length} ${items.length === 1 ? 'Aufnahme' : 'Aufnahmen'}`;
      setLiveStatus(true);
    } catch (_) {
      setLiveStatus(false);
    }
  }

  lightboxClose.addEventListener('click', closeLightbox);
  lightbox.addEventListener('click', (event) => { if (event.target === lightbox) closeLightbox(); });
  document.addEventListener('keydown', (event) => { if (event.key === 'Escape' && !lightbox.hidden) closeLightbox(); });
  refresh();
  window.setInterval(refresh, 4000);
})();
