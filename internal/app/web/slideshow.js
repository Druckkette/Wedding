(() => {
  'use strict';

  const token = document.querySelector('meta[name="upload-token"]').content;
  const stage = document.querySelector('#slide-stage');
  const waiting = document.querySelector('#waiting');
  const celebration = document.querySelector('#challenge-celebration');
  const challengeWho = document.querySelector('#challenge-who');
  const challengeWhat = document.querySelector('#challenge-what');
  const confetti = document.querySelector('#confetti');
  const newBadge = document.querySelector('#new-badge');
  const caption = document.querySelector('#slide-caption');
  const liveIndicator = document.querySelector('.live-indicator');
  const liveLabel = document.querySelector('#live-label');
  const counter = document.querySelector('#slide-counter');
  const pauseButton = document.querySelector('#pause-button');
  const nextButton = document.querySelector('#next-button');
  const fullscreenButton = document.querySelector('#fullscreen-button');
  const known = new Set();
  const priority = [];
  let items = [];
  let initialized = false;
  let regularIndex = 0;
  let paused = false;
  let running = false;
  let advanceSignal = 0;

  for (let index = 0; index < 36; index += 1) {
    const piece = document.createElement('i');
    piece.style.left = `${(index * 29) % 100}%`;
    piece.style.animationDelay = `${(index % 12) * -.29}s`;
    piece.style.animationDuration = `${2.8 + (index % 6) * .22}s`;
    confetti.append(piece);
  }

  const sleep = (milliseconds) => new Promise((resolve) => window.setTimeout(resolve, milliseconds));

  async function waitForSlide(milliseconds) {
    const signal = advanceSignal;
    let elapsed = 0;
    while (elapsed < milliseconds && signal === advanceSignal) {
      await sleep(250);
      if (!paused) elapsed += 250;
    }
  }

  function createMedia(item) {
    const media = document.createElement(item.kind === 'video' ? 'video' : 'img');
    media.className = `slide-media${item.is_challenge ? ' challenge-frame' : ''}`;
    media.src = item.url;
    if (item.kind === 'video') {
      media.autoplay = true;
      media.muted = true;
      media.loop = true;
      media.playsInline = true;
      media.preload = 'auto';
    } else {
      media.alt = item.is_challenge ? `Challenge: ${item.challenge}` : 'Hochzeitsfoto';
    }
    return media;
  }

  async function celebrate(item) {
    challengeWho.textContent = item.challenge_by;
    challengeWhat.textContent = `„${item.challenge}“`;
    celebration.hidden = false;
    await waitForSlide(4300);
    celebration.hidden = true;
  }

  async function show(item, isNew) {
    waiting.hidden = true;
    stage.replaceChildren(createMedia(item));
    newBadge.hidden = !isNew;
    caption.hidden = true;
    if (item.is_challenge) {
      caption.textContent = `${item.challenge_by}: ${item.challenge}`;
      caption.hidden = false;
    } else if (item.guest_name) {
      caption.textContent = `Von ${item.guest_name}`;
      caption.hidden = false;
    }
    const position = items.findIndex((candidate) => candidate.id === item.id);
    counter.textContent = position >= 0 ? `${position + 1} / ${items.length}` : `${items.length} Aufnahmen`;
    await waitForSlide(item.kind === 'video' ? 15000 : 9500);
    newBadge.hidden = true;
  }

  async function run() {
    if (running) return;
    running = true;
    while (true) {
      if (paused || items.length === 0) {
        await sleep(500);
        continue;
      }
      let item;
      let isNew = false;
      if (priority.length) {
        item = priority.shift();
        isNew = true;
      } else {
        item = items[regularIndex % items.length];
        regularIndex += 1;
      }
      if (isNew && item.is_challenge) await celebrate(item);
      await show(item, isNew);
    }
  }

  async function refresh() {
    try {
      const response = await fetch('/api/media', { headers: { 'X-Upload-Token': token }, cache: 'no-store' });
      if (!response.ok) throw new Error('slideshow request failed');
      const payload = await response.json();
      if (!initialized) {
        payload.items.forEach((item) => known.add(item.id));
        initialized = true;
      } else {
        const additions = payload.items.filter((item) => !known.has(item.id)).reverse();
        additions.forEach((item) => {
          known.add(item.id);
          priority.push(item);
        });
      }
      items = payload.items;
      liveIndicator.classList.remove('offline');
      liveLabel.textContent = 'Live verbunden';
      if (items.length === 0) counter.textContent = 'Warte auf Aufnahmen';
      run();
    } catch (_) {
      liveIndicator.classList.add('offline');
      liveLabel.textContent = 'Verbindung wird wiederhergestellt';
    }
  }

  pauseButton.addEventListener('click', () => {
    paused = !paused;
    pauseButton.textContent = paused ? 'Fortsetzen' : 'Pause';
  });
  nextButton.addEventListener('click', () => { advanceSignal += 1; });
  fullscreenButton.addEventListener('click', async () => {
    if (!document.fullscreenElement) await document.documentElement.requestFullscreen();
    else await document.exitFullscreen();
  });

  if ('wakeLock' in navigator) navigator.wakeLock.request('screen').catch(() => {});
  refresh();
  window.setInterval(refresh, 3000);
})();
