(() => {
  'use strict';

  const input = document.querySelector('#photo-input');
  const dropZone = document.querySelector('#drop-zone');
  const selection = document.querySelector('#selection');
  const fileList = document.querySelector('#file-list');
  const countLabel = document.querySelector('#selection-count');
  const clearButton = document.querySelector('#clear-button');
  const challengeToggle = document.querySelector('#challenge-toggle');
  const challengePanel = document.querySelector('#challenge-panel');
  const challengeText = document.querySelector('#challenge-text');
  const challengeBy = document.querySelector('#challenge-by');
  const challengeError = document.querySelector('#challenge-error');
  const uploadButton = document.querySelector('#upload-button');
  const uploadButtonLabel = document.querySelector('#upload-button-label');
  const guestName = document.querySelector('#guest-name');
  const result = document.querySelector('#result');
  const resultMessage = document.querySelector('#result-message');
  const moreButton = document.querySelector('#more-button');
  const token = document.querySelector('meta[name="upload-token"]').content;
  let entries = [];
  let uploading = false;

  const keyFor = (file) => `${file.name}:${file.size}:${file.lastModified}`;
  const formatBytes = (bytes) => {
    if (bytes < 1024 * 1024) return `${Math.max(1, Math.round(bytes / 1024))} KB`;
    return `${(bytes / 1024 / 1024).toFixed(1)} MB`;
  };

  function addFiles(fileCollection) {
    if (uploading) return;
    const known = new Set(entries.map((entry) => keyFor(entry.file)));
    Array.from(fileCollection).forEach((file) => {
      const supportedByType = file.type.startsWith('image/') || file.type.startsWith('video/');
      const supportedByName = /\.(heic|heif|avif|mp4|mov|m4v|webm|mkv|avi|mpeg|mpg|3gp|3gpp|ogv)$/i.test(file.name);
      if (!supportedByType && !supportedByName) return;
      if (known.has(keyFor(file))) return;
      const isVideo = file.type.startsWith('video/') || /\.(mp4|mov|m4v|webm|mkv|avi|mpeg|mpg|3gp|3gpp|ogv)$/i.test(file.name);
      entries.push({ file, url: URL.createObjectURL(file), isVideo, state: 'ready', progress: 0, request: null, cancelled: false, pending: false });
      known.add(keyFor(file));
    });
    render();
  }

  function render() {
    selection.hidden = entries.length === 0;
    dropZone.hidden = entries.length > 0;
    countLabel.textContent = `${entries.length} ${entries.length === 1 ? 'Datei ausgewählt' : 'Dateien ausgewählt'}`;
    fileList.replaceChildren();

    entries.forEach((entry, index) => {
      const li = document.createElement('li');
      li.className = 'file-item';
      const preview = document.createElement(entry.isVideo ? 'video' : 'img');
      preview.className = 'file-thumb';
      preview.src = entry.url;
      if (entry.isVideo) {
        preview.muted = true;
        preview.playsInline = true;
        preview.preload = 'metadata';
      } else {
        preview.alt = '';
      }
      const info = document.createElement('div');
      info.className = 'file-info';
      const name = document.createElement('span');
      name.className = 'file-name';
      name.textContent = entry.file.name;
      const status = document.createElement('span');
      status.className = `file-status ${entry.state === 'failed' ? 'error' : entry.state === 'done' ? 'done' : ''}`;
      status.textContent = statusText(entry);
      const track = document.createElement('div');
      track.className = 'progress-track';
      track.hidden = entry.state !== 'uploading';
      const bar = document.createElement('div');
      bar.className = 'progress-bar';
      bar.style.width = `${entry.progress}%`;
      track.append(bar);
      info.append(name, status, track);
      const remove = document.createElement('button');
      remove.className = 'remove-button';
      remove.type = 'button';
      remove.setAttribute('aria-label', entry.state === 'uploading' ? `Upload von ${entry.file.name} stoppen` : `${entry.file.name} entfernen`);
      remove.textContent = '×';
      remove.disabled = entry.state === 'done';
      remove.addEventListener('click', () => removeEntry(entry));
      li.append(preview, info, remove);
      fileList.append(li);
    });

    uploadButton.disabled = uploading || !entries.some((entry) => entry.state !== 'done');
    clearButton.disabled = uploading;
    challengeToggle.disabled = uploading;
  }

  function statusText(entry) {
    if (entry.state === 'uploading') return `${entry.progress}% · wird hochgeladen`;
    if (entry.state === 'done') return 'Sicher gespeichert';
    if (entry.state === 'failed') return entry.error || 'Upload fehlgeschlagen';
    return formatBytes(entry.file.size);
  }

  function removeEntry(entry) {
    entry.cancelled = true;
    if (entry.request) entry.request.abort();
    const index = entries.indexOf(entry);
    if (index >= 0) {
      URL.revokeObjectURL(entry.url);
      entries.splice(index, 1);
    }
    render();
  }

  function reset() {
    entries.forEach((entry) => URL.revokeObjectURL(entry.url));
    entries = [];
    input.value = '';
    uploading = false;
    uploadButtonLabel.textContent = 'Dateien hochladen';
    selection.hidden = true;
    result.hidden = true;
    dropZone.hidden = false;
    challengeToggle.checked = false;
    challengePanel.hidden = true;
    challengeText.value = '';
    challengeBy.value = '';
    challengeError.hidden = true;
    render();
  }

  function validateChallenge() {
    challengeError.hidden = true;
    if (!challengeToggle.checked) return true;
    if (entries.length !== 1 || entries[0].isVideo) {
      challengeError.textContent = 'Bitte genau ein Foto auswählen. Videos und Mehrfachauswahl sind für Challenges nicht möglich.';
      challengeError.hidden = false;
      return false;
    }
    if (!challengeText.value.trim() || !challengeBy.value.trim()) {
      challengeError.textContent = 'Bitte Challenge und Teilnehmer vollständig eintragen.';
      challengeError.hidden = false;
      return false;
    }
    return true;
  }

  function uploadFile(entry) {
    return new Promise((resolve) => {
      let settled = false;
      const finish = (success) => {
        if (settled) return;
        settled = true;
        entry.request = null;
        render();
        resolve(success);
      };
      const form = new FormData();
      form.append('guest_name', guestName.value.trim());
      form.append('is_challenge', challengeToggle.checked ? 'true' : 'false');
      if (challengeToggle.checked) {
        form.append('challenge', challengeText.value.trim());
        form.append('challenge_by', challengeBy.value.trim());
      }
      form.append('media', entry.file, entry.file.name);
      const request = new XMLHttpRequest();
      entry.request = request;
      request.open('POST', '/api/upload');
      request.setRequestHeader('X-Upload-Token', token);
      request.upload.addEventListener('progress', (event) => {
        if (!event.lengthComputable) return;
        entry.progress = Math.min(99, Math.round(event.loaded / event.total * 100));
        render();
      });
      request.addEventListener('load', () => {
        let body = {};
        try { body = JSON.parse(request.responseText); } catch (_) { /* ignored */ }
        if (request.status >= 200 && request.status < 300) {
          entry.state = 'done';
          entry.progress = 100;
          entry.pending = Boolean(body.pending);
        } else {
          entry.state = 'failed';
          entry.error = body.error || 'Upload fehlgeschlagen. Bitte erneut versuchen.';
        }
        finish(entry.state === 'done');
      });
      request.addEventListener('error', () => {
        if (entry.cancelled) return finish(false);
        entry.state = 'failed';
        entry.error = 'Keine Verbindung. Bitte WLAN oder Mobilfunk prüfen.';
        finish(false);
      });
      request.addEventListener('timeout', () => {
        entry.state = 'failed';
        entry.error = 'Der Upload hat zu lange gedauert. Bitte erneut versuchen.';
        finish(false);
      });
      request.addEventListener('abort', () => finish(false));
      request.send(form);
    });
  }

  async function uploadAll() {
    if (uploading) return;
    if (!validateChallenge()) return;
    const challengeUpload = challengeToggle.checked;
    uploading = true;
    const pending = entries.filter((entry) => entry.state !== 'done');
    let completed = 0;
    uploadButtonLabel.textContent = `0 von ${pending.length} hochgeladen`;
    render();

    for (const entry of pending) {
      if (!entries.includes(entry) || entry.cancelled) continue;
      entry.state = 'uploading';
      entry.progress = 0;
      render();
      const ok = await uploadFile(entry);
      if (ok) completed += 1;
      uploadButtonLabel.textContent = `${completed} von ${pending.length} hochgeladen`;
    }

    uploading = false;
    const failures = entries.filter((entry) => entry.state === 'failed');
    if (entries.length === 0) {
      uploadButtonLabel.textContent = 'Dateien hochladen';
      render();
      return;
    }
    if (failures.length === 0 && completed > 0) {
      selection.hidden = true;
      result.hidden = false;
      const awaitingApproval = entries.some((entry) => entry.state === 'done' && entry.pending);
      resultMessage.textContent = awaitingApproval
        ? `${completed} ${completed === 1 ? 'Datei wartet' : 'Dateien warten'} jetzt auf die Freigabe durch das Hochzeitsteam.`
        : challengeUpload
          ? 'Euer Challenge-Bild wurde gespeichert und erscheint gleich auf dem Beamer.'
          : `${completed} ${completed === 1 ? 'Datei wurde' : 'Dateien wurden'} sicher gespeichert.`;
      result.scrollIntoView({ behavior: 'smooth', block: 'center' });
    } else {
      uploadButtonLabel.textContent = failures.length ? 'Fehlgeschlagene erneut senden' : 'Dateien hochladen';
      render();
    }
  }

  input.addEventListener('change', () => addFiles(input.files));
  challengeToggle.addEventListener('change', () => {
    challengePanel.hidden = !challengeToggle.checked;
    challengeError.hidden = true;
    if (challengeToggle.checked) challengeText.focus();
  });
  clearButton.addEventListener('click', reset);
  uploadButton.addEventListener('click', uploadAll);
  moreButton.addEventListener('click', reset);

  ['dragenter', 'dragover'].forEach((name) => dropZone.addEventListener(name, (event) => {
    event.preventDefault();
    dropZone.classList.add('is-dragging');
  }));
  ['dragleave', 'drop'].forEach((name) => dropZone.addEventListener(name, (event) => {
    event.preventDefault();
    dropZone.classList.remove('is-dragging');
  }));
  dropZone.addEventListener('drop', (event) => addFiles(event.dataTransfer.files));
})();
