(() => {
  'use strict';

  const token = document.querySelector('meta[name="upload-token"]').content;
  const login = document.querySelector('#settings-login');
  const dashboard = document.querySelector('#settings-dashboard');
  const loginForm = document.querySelector('#login-form');
  const loginPassword = document.querySelector('#settings-password');
  const loginError = document.querySelector('#login-error');
  const logoutButton = document.querySelector('#logout-button');
  const moderationToggle = document.querySelector('#moderation-toggle');
  const moderationLabel = document.querySelector('#moderation-label');
  const styleInputs = Array.from(document.querySelectorAll('input[name="slideshow-style"]'));
  const intervalForm = document.querySelector('#interval-form');
  const intervalInput = document.querySelector('#slideshow-interval');
  const intervalError = document.querySelector('#interval-error');
  const intervalSuccess = document.querySelector('#interval-success');
  const shuffleToggle = document.querySelector('#shuffle-toggle');
  const shuffleLabel = document.querySelector('#shuffle-label');
  const faceAdminStats = document.querySelector('#face-admin-stats');
  const faceAdminUnavailable = document.querySelector('#face-admin-unavailable');
  const facePeopleGrid = document.querySelector('#face-people-grid');
  const filters = Array.from(document.querySelectorAll('[data-filter]'));
  const pendingCount = document.querySelector('#pending-count');
  const grid = document.querySelector('#moderation-grid');
  const empty = document.querySelector('#queue-empty');
  const passwordForm = document.querySelector('#password-form');
  const currentPassword = document.querySelector('#current-password');
  const newPassword = document.querySelector('#new-password');
  const confirmPassword = document.querySelector('#confirm-password');
  const passwordError = document.querySelector('#password-error');
  const passwordSuccess = document.querySelector('#password-success');
  let items = [];
  let activeFilter = 'pending';
  let saving = false;

  async function request(path, options = {}) {
    const response = await fetch(path, {
      ...options,
      cache: 'no-store',
      headers: {
        'X-Upload-Token': token,
        ...(options.body ? { 'Content-Type': 'application/json' } : {}),
        ...(options.headers || {}),
      },
    });
    let body = {};
    try { body = await response.json(); } catch (_) { /* ignored */ }
    if (!response.ok) {
      const error = new Error(body.error || 'Anfrage fehlgeschlagen.');
      error.status = response.status;
      throw error;
    }
    return body;
  }

  function showLogin(message = '') {
    dashboard.hidden = true;
    login.hidden = false;
    loginError.textContent = message;
    loginError.hidden = !message;
  }

  function mediaElement(item) {
    const media = document.createElement(item.kind === 'video' ? 'video' : 'img');
    media.src = item.url;
    if (item.kind === 'video') {
      media.muted = true;
      media.playsInline = true;
      media.preload = 'metadata';
    } else {
      media.alt = item.is_challenge ? `Challenge: ${item.challenge}` : 'Hochzeitsfoto';
      media.loading = 'lazy';
    }
    return media;
  }

  function createModerationItem(item) {
    const article = document.createElement('article');
    article.className = 'moderation-item';
    const media = document.createElement('div');
    media.className = 'moderation-media';
    media.append(mediaElement(item));
    const kind = document.createElement('span');
    kind.textContent = item.is_challenge ? '★ Challenge' : item.kind === 'video' ? '▶ Video' : 'Foto';
    media.append(kind);

    const info = document.createElement('div');
    info.className = 'moderation-info';
    const title = document.createElement('strong');
    title.textContent = item.is_challenge ? item.challenge : item.kind === 'video' ? 'Hochzeitsvideo' : 'Hochzeitsfoto';
    const details = document.createElement('p');
    details.textContent = item.is_challenge
      ? `Erledigt von ${item.challenge_by}`
      : item.kind === 'video' ? 'Video-Upload' : 'Foto-Upload';
    const actions = document.createElement('div');
    actions.className = 'moderation-actions';

    if (item.status !== 'approved') actions.append(actionButton('Freigeben', 'approved'));
    if (item.status !== 'pending') actions.append(actionButton('Zur Prüfung', 'pending', 'pending'));
    if (item.status !== 'rejected') actions.append(actionButton('Ausblenden', 'rejected', 'reject'));

    const deleteButton = document.createElement('button');
    deleteButton.type = 'button';
    deleteButton.textContent = 'Löschen';
    deleteButton.className = 'delete';
    deleteButton.addEventListener('click', () => deleteMedia(item, deleteButton));
    actions.append(deleteButton);

    function actionButton(label, status, className = '') {
      const button = document.createElement('button');
      button.type = 'button';
      button.textContent = label;
      button.className = className;
      button.addEventListener('click', () => updateMedia(item, status, button));
      return button;
    }

    info.append(title, details, actions);
    article.append(media, info);
    return article;
  }

  function renderItems() {
    grid.replaceChildren();
    const visible = items.filter((item) => item.status === activeFilter);
    visible.forEach((item) => grid.append(createModerationItem(item)));
    empty.hidden = visible.length !== 0;
    pendingCount.textContent = String(items.filter((item) => item.status === 'pending').length);
  }

  function applyState(state) {
    items = state.items || [];
    moderationToggle.checked = Boolean(state.moderation_enabled);
    moderationLabel.textContent = moderationToggle.checked ? 'An' : 'Aus';
    styleInputs.forEach((input) => { input.checked = input.value === state.slideshow_style; });
    intervalInput.value = String(state.slideshow_interval_seconds || 10);
    shuffleToggle.checked = Boolean(state.slideshow_shuffle);
    shuffleLabel.textContent = shuffleToggle.checked ? 'An' : 'Aus';
    login.hidden = true;
    dashboard.hidden = false;
    renderItems();
  }

  async function loadSettings() {
    try {
      applyState(await request('/api/settings'));
      await loadFaceAdmin();
    } catch (error) {
      if (error.status === 401) showLogin();
      else showLogin(error.message);
    }
  }

  async function loadFaceThumb(image, url) {
    try {
      const response = await fetch(url, { headers: { 'X-Upload-Token': token }, cache: 'no-store' });
      if (!response.ok) return;
      const objectURL = URL.createObjectURL(await response.blob());
      image.src = objectURL;
      image.addEventListener('load', () => URL.revokeObjectURL(objectURL), { once: true });
    } catch (_) { /* thumbnail is optional */ }
  }

  function createFacePerson(person) {
    const article = document.createElement('article');
    article.className = 'face-person';

    const portrait = document.createElement('div');
    portrait.className = 'face-person-image';
    const image = document.createElement('img');
    image.alt = person.name ? `Gesicht von ${person.name}` : 'Erkannte Person';
    image.loading = 'lazy';
    portrait.append(image);
    loadFaceThumb(image, person.thumbnail_url);

    const info = document.createElement('div');
    info.className = 'face-person-info';
    const meta = document.createElement('small');
    meta.textContent = `${person.photo_count} Fotos · ${person.face_count} erkannte Gesichter`;
    const input = document.createElement('input');
    input.type = 'text';
    input.maxLength = 80;
    input.value = person.name || '';
    input.placeholder = 'Name des Gastes';
    input.setAttribute('aria-label', `Name für ${person.id}`);
    const button = document.createElement('button');
    button.type = 'button';
    button.textContent = person.name ? 'Namen speichern' : 'Person benennen';
    const status = document.createElement('span');
    status.className = 'face-person-status';

    button.addEventListener('click', async () => {
      button.disabled = true;
      status.textContent = '';
      try {
        await request('/api/faces/admin/name', {
          method: 'POST',
          body: JSON.stringify({ id: person.id, name: input.value.trim() }),
        });
        person.name = input.value.trim();
        button.textContent = person.name ? 'Namen speichern' : 'Person benennen';
        status.textContent = person.name ? 'Gespeichert' : 'Name entfernt';
      } catch (error) {
        if (error.status === 401) showLogin('Bitte erneut anmelden.');
        else status.textContent = error.message;
      } finally {
        button.disabled = false;
      }
    });

    info.append(meta, input, button, status);
    article.append(portrait, info);
    return article;
  }

  async function loadFaceAdmin() {
    facePeopleGrid.replaceChildren();
    faceAdminStats.hidden = true;
    faceAdminUnavailable.hidden = true;
    try {
      const payload = await request('/api/faces/admin');
      if (!payload.available) {
        faceAdminUnavailable.hidden = false;
        return;
      }
      const stats = payload.stats || {};
      faceAdminStats.textContent = `${stats.people || 0} Personen · ${stats.faces || 0} Gesichter · ${stats.images || 0} Bilder`;
      faceAdminStats.hidden = false;
      (payload.people || []).forEach((person) => facePeopleGrid.append(createFacePerson(person)));
    } catch (error) {
      if (error.status === 401) throw error;
      faceAdminUnavailable.textContent = error.message || 'Gesichtsindex konnte nicht geladen werden.';
      faceAdminUnavailable.hidden = false;
    }
  }

  async function saveSettings(update) {
    if (saving) return false;
    saving = true;
    try {
      applyState(await request('/api/settings', { method: 'POST', body: JSON.stringify(update) }));
      return true;
    } catch (error) {
      if (error.status === 401) showLogin('Bitte erneut anmelden.');
      else window.alert(error.message);
      return false;
    } finally {
      saving = false;
    }
  }

  async function updateMedia(item, status, button) {
    button.disabled = true;
    try {
      await request('/api/settings/media', { method: 'POST', body: JSON.stringify({ id: item.id, status }) });
      item.status = status;
      renderItems();
    } catch (error) {
      button.disabled = false;
      if (error.status === 401) showLogin('Bitte erneut anmelden.');
      else window.alert(error.message);
    }
  }

  async function deleteMedia(item, button) {
    const confirmed = window.confirm('Diese Aufnahme wird dauerhaft vom NAS gelöscht. Wirklich löschen?');
    if (!confirmed) return;
    button.disabled = true;
    try {
      await request('/api/settings/media/delete', { method: 'POST', body: JSON.stringify({ id: item.id }) });
      items = items.filter((candidate) => candidate.id !== item.id);
      renderItems();
    } catch (error) {
      button.disabled = false;
      if (error.status === 401) showLogin('Bitte erneut anmelden.');
      else window.alert(error.message);
    }
  }

  loginForm.addEventListener('submit', async (event) => {
    event.preventDefault();
    loginError.hidden = true;
    try {
      await request('/api/settings/login', { method: 'POST', body: JSON.stringify({ password: loginPassword.value }) });
      loginPassword.value = '';
      await loadSettings();
    } catch (error) {
      showLogin(error.message);
      loginPassword.focus();
    }
  });

  logoutButton.addEventListener('click', async () => {
    try { await request('/api/settings/logout', { method: 'POST' }); } catch (_) { /* session is discarded either way */ }
    showLogin();
    loginPassword.focus();
  });

  moderationToggle.addEventListener('change', () => {
    moderationLabel.textContent = moderationToggle.checked ? 'An' : 'Aus';
    saveSettings({ moderation_enabled: moderationToggle.checked });
  });

  styleInputs.forEach((input) => input.addEventListener('change', () => {
    if (input.checked) saveSettings({ slideshow_style: input.value });
  }));

  intervalForm.addEventListener('submit', async (event) => {
    event.preventDefault();
    intervalError.hidden = true;
    intervalSuccess.hidden = true;
    const seconds = Number(intervalInput.value);
    if (!Number.isInteger(seconds) || seconds < 3 || seconds > 300) {
      intervalError.textContent = 'Bitte eine ganze Zahl zwischen 3 und 300 Sekunden eingeben.';
      intervalError.hidden = false;
      return;
    }
    const saved = await saveSettings({ slideshow_interval_seconds: seconds });
    if (!saved) return;
    intervalSuccess.textContent = `Die Wechselzeit beträgt jetzt ${seconds} Sekunden.`;
    intervalSuccess.hidden = false;
  });

  shuffleToggle.addEventListener('change', async () => {
    shuffleLabel.textContent = shuffleToggle.checked ? 'An' : 'Aus';
    const saved = await saveSettings({ slideshow_shuffle: shuffleToggle.checked });
    if (!saved) await loadSettings();
  });

  filters.forEach((button) => button.addEventListener('click', () => {
    activeFilter = button.dataset.filter;
    filters.forEach((candidate) => candidate.classList.toggle('active', candidate === button));
    renderItems();
  }));

  passwordForm.addEventListener('submit', async (event) => {
    event.preventDefault();
    passwordError.hidden = true;
    passwordSuccess.hidden = true;
    if (newPassword.value !== confirmPassword.value) {
      passwordError.textContent = 'Die neuen Passwörter stimmen nicht überein.';
      passwordError.hidden = false;
      return;
    }
    try {
      const saved = await saveSettings({ current_password: currentPassword.value, new_password: newPassword.value });
      if (!saved) return;
      currentPassword.value = '';
      newPassword.value = '';
      confirmPassword.value = '';
      passwordSuccess.textContent = 'Das Settings-Passwort wurde geändert.';
      passwordSuccess.hidden = false;
    } catch (_) { /* saveSettings handles display */ }
  });

  loadSettings();
})();
