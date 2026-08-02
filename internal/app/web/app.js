(() => {
  'use strict';

  const input = document.querySelector('#photo-input');
  const dropZone = document.querySelector('#drop-zone');
  const selection = document.querySelector('#selection');
  const fileList = document.querySelector('#file-list');
  const countLabel = document.querySelector('#selection-count');
  const clearButton = document.querySelector('#clear-button');
  const uploadButton = document.querySelector('#upload-button');
  const uploadButtonLabel = document.querySelector('#upload-button-label');
  const guestName = document.querySelector('#guest-name');
  const result = document.querySelector('#result');
  const resultMessage = document.querySelector('#result-message');
  const moreButton = document.querySelector('#more-button');
  const token = document.querySelector('meta[name="upload-token"]').content;
  const maxBytes = Number(document.querySelector('meta[name="max-upload-mb"]').content) * 1024 * 1024;
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
      if (!file.type.startsWith('image/') && !/\.(heic|heif|avif)$/i.test(file.name)) return;
      if (known.has(keyFor(file))) return;
      entries.push({ file, url: URL.createObjectURL(file), state: file.size > maxBytes ? 'oversize' : 'ready', progress: 0 });
      known.add(keyFor(file));
    });
    render();
  }

  function render() {
    selection.hidden = entries.length === 0;
    dropZone.hidden = entries.length > 0;
    countLabel.textContent = `${entries.length} ${entries.length === 1 ? 'Foto ausgewählt' : 'Fotos ausgewählt'}`;
    fileList.replaceChildren();

    entries.forEach((entry, index) => {
      const li = document.createElement('li');
      li.className = 'file-item';
      const img = document.createElement('img');
      img.className = 'file-thumb';
      img.src = entry.url;
      img.alt = '';
      const info = document.createElement('div');
      info.className = 'file-info';
      const name = document.createElement('span');
      name.className = 'file-name';
      name.textContent = entry.file.name;
      const status = document.createElement('span');
      status.className = `file-status ${entry.state === 'failed' || entry.state === 'oversize' ? 'error' : entry.state === 'done' ? 'done' : ''}`;
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
      remove.setAttribute('aria-label', `${entry.file.name} entfernen`);
      remove.textContent = '×';
      remove.disabled = uploading;
      remove.addEventListener('click', () => removeEntry(index));
      li.append(img, info, remove);
      fileList.append(li);
    });

    uploadButton.disabled = uploading || !entries.some((entry) => entry.state !== 'oversize' && entry.state !== 'done');
    clearButton.disabled = uploading;
  }

  function statusText(entry) {
    if (entry.state === 'oversize') return `Zu groß · maximal ${Math.round(maxBytes / 1024 / 1024)} MB`;
    if (entry.state === 'uploading') return `${entry.progress}% · wird hochgeladen`;
    if (entry.state === 'done') return 'Sicher gespeichert';
    if (entry.state === 'failed') return entry.error || 'Upload fehlgeschlagen';
    return formatBytes(entry.file.size);
  }

  function removeEntry(index) {
    URL.revokeObjectURL(entries[index].url);
    entries.splice(index, 1);
    render();
  }

  function reset() {
    entries.forEach((entry) => URL.revokeObjectURL(entry.url));
    entries = [];
    input.value = '';
    uploading = false;
    uploadButtonLabel.textContent = 'Fotos hochladen';
    selection.hidden = true;
    result.hidden = true;
    dropZone.hidden = false;
    render();
  }

  function uploadFile(entry) {
    return new Promise((resolve) => {
      const form = new FormData();
      form.append('photo', entry.file, entry.file.name);
      form.append('guest_name', guestName.value.trim());
      const request = new XMLHttpRequest();
      request.open('POST', '/api/upload');
      request.setRequestHeader('X-Upload-Token', token);
      request.timeout = 15 * 60 * 1000;
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
        } else {
          entry.state = 'failed';
          entry.error = body.error || 'Upload fehlgeschlagen. Bitte erneut versuchen.';
        }
        render();
        resolve(entry.state === 'done');
      });
      request.addEventListener('error', () => {
        entry.state = 'failed';
        entry.error = 'Keine Verbindung. Bitte WLAN oder Mobilfunk prüfen.';
        render();
        resolve(false);
      });
      request.addEventListener('timeout', () => {
        entry.state = 'failed';
        entry.error = 'Der Upload hat zu lange gedauert. Bitte erneut versuchen.';
        render();
        resolve(false);
      });
      request.send(form);
    });
  }

  async function uploadAll() {
    if (uploading) return;
    uploading = true;
    const pending = entries.filter((entry) => entry.state !== 'oversize' && entry.state !== 'done');
    let completed = 0;
    uploadButtonLabel.textContent = `0 von ${pending.length} hochgeladen`;
    render();

    for (const entry of pending) {
      entry.state = 'uploading';
      entry.progress = 0;
      render();
      const ok = await uploadFile(entry);
      if (ok) completed += 1;
      uploadButtonLabel.textContent = `${completed} von ${pending.length} hochgeladen`;
    }

    uploading = false;
    const failures = entries.filter((entry) => entry.state === 'failed');
    if (failures.length === 0 && completed > 0) {
      selection.hidden = true;
      result.hidden = false;
      resultMessage.textContent = `${completed} ${completed === 1 ? 'Foto wurde' : 'Fotos wurden'} sicher gespeichert.`;
      result.scrollIntoView({ behavior: 'smooth', block: 'center' });
    } else {
      uploadButtonLabel.textContent = failures.length ? 'Fehlgeschlagene erneut senden' : 'Fotos hochladen';
      render();
    }
  }

  input.addEventListener('change', () => addFiles(input.files));
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
