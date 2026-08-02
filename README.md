# Hochzeitsfotos und -videos direkt aufs NAS

Eine kleine, für iOS und Android optimierte Web-App, über die Hochzeitsgäste ohne separate App mehrere Fotos und Videos auswählen und direkt auf ein Synology-NAS laden können.

## Eigenschaften

- Mehrfachauswahl aus der Mediathek und direkter Kamerazugriff
- Fotos: JPEG, PNG, GIF, WebP, HEIC/HEIF und AVIF
- Videos: MP4, MOV, M4V, WebM/MKV, AVI, MPEG, 3GP und OGV
- Kein festes Dateigrößen- oder Upload-Zeitlimit; Dateien werden gestreamt
- Einzelner Fortschritt pro Datei und gezielter Wiederholungsversuch
- Originaldateien ohne Komprimierung oder Konvertierung
- Geheimer, QR-Code-tauglicher Upload-Link
- Private, automatisch aktualisierte Galerie für Fotos und Videos
- Beamer-Diashow nur für Fotos, mit Vollbild, Dauerrotation und bevorzugter Einblendung neuer Aufnahmen
- Normaler Beamer-Stil oder hereinfliegende Polaroids
- Challenge-Bilder mit Aufgabe, Namen, eigener Animation und deutlich erkennbarem Spezialrahmen
- Passwortgeschützte Settings mit optionaler Freigabe neuer Uploads
- Einzelne laufende Uploads lassen sich unabhängig abbrechen
- Medienzugriff ausschließlich über den geheimen Event-Link
- Serverseitige Typprüfung, Rate-Limit und atomisches Speichern
- Metadaten als `uploads.jsonl` neben den Dateien
- Unprivilegierter, schreibgeschützter Docker-Container

## Lokal starten

```sh
cp .env.example .env
# UPLOAD_TOKEN in .env durch einen sicheren Wert ersetzen
docker compose up --build
```

Danach sind diese Seiten erreichbar:

- Upload: `http://localhost:18787/u/<UPLOAD_TOKEN>`
- Galerie: `http://localhost:18787/u/<UPLOAD_TOKEN>/gallery`
- Beamer-Diashow: `http://localhost:18787/u/<UPLOAD_TOKEN>/slideshow`
- Settings: `http://localhost:18787/u/<UPLOAD_TOKEN>/settings`

Im produktiven Einsatz gehört die App hinter einen HTTPS-Reverse-Proxy; der Container-Port wird absichtlich nur an `127.0.0.1` gebunden. Die Galerie und Diashow fragen neue Aufnahmen automatisch ab. Neue Challenge-Bilder unterbrechen die reguläre Reihenfolge kurz für die Challenge-Animation und werden danach direkt angezeigt.

## Konfiguration

| Variable | Standard | Bedeutung |
| --- | --- | --- |
| `UPLOAD_TOKEN` | – | Pflichtwert mit mindestens 32 URL-sicheren Zeichen |
| `SETTINGS_PASSWORD` | – | Initiales Settings-Passwort mit mindestens 6 Zeichen; spätere Änderungen werden gehasht auf dem NAS gespeichert |
| `EVENT_TITLE` | `Unsere Hochzeit` | Überschrift der Upload-Seite |
| `EVENT_SUBTITLE` | siehe `.env.example` | Einladungstext |
| `MAX_UPLOADS_PER_HOUR` | `120` | Schutzlimit je IP-Adresse |
| `HOST_PORT` | `18787` | Nur lokal gebundener Reverse-Proxy-Port |
| `NAS_UPLOAD_DIR` | `/volume1/photo/Hochzeit-Uploads` | Persistenter Zielordner auf dem NAS |
| `PUID` / `PGID` | `1024` / `100` | DSM-Benutzer und -Gruppe für neue Dateien |

Der Token wird bewusst nicht ins Repository eingecheckt. Wird der QR-Code weitergegeben oder verloren, genügt ein neuer Token in `.env` und `docker compose up -d --force-recreate`.

## Betrieb

```sh
docker compose ps
docker compose logs --tail=100
curl -fsS http://127.0.0.1:18787/healthz
```

Backups sollten mindestens den Ordner aus `NAS_UPLOAD_DIR` umfassen. Dort liegen neben den Medien auch `uploads.jsonl` und die passwortgeschützten Moderationseinstellungen in `settings.json`. Ausgeblendete Medien werden nicht gelöscht und können in Settings wieder freigegeben werden. Die zusätzliche Aktion „Löschen“ entfernt eine Aufnahme nach einer Sicherheitsabfrage dauerhaft vom NAS.

Die Beamer-Wechselzeit lässt sich in Settings sekundengenau zwischen 3 und 300 Sekunden einstellen. Bestehende Installationen verwenden automatisch den Standardwert von 10 Sekunden.
