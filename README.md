# Hochzeitsfotos direkt aufs NAS

Eine kleine, für iOS und Android optimierte Web-App, über die Hochzeitsgäste ohne separate App mehrere Fotos auswählen und direkt auf ein Synology-NAS laden können.

## Eigenschaften

- Mehrfachauswahl aus der Fotomediathek und direkter Kamerazugriff
- JPEG, PNG, GIF, WebP, HEIC/HEIF und AVIF
- Einzelner Fortschritt pro Bild und gezielter Wiederholungsversuch
- Originaldateien ohne Komprimierung oder Konvertierung
- Geheimer, QR-Code-tauglicher Upload-Link
- Keine öffentliche Galerie und keine Download-Route
- Serverseitige Typprüfung, Größenlimit, Rate-Limit und atomisches Speichern
- Metadaten als `uploads.jsonl` neben den Bildern
- Unprivilegierter, schreibgeschützter Docker-Container

## Lokal starten

```sh
cp .env.example .env
# UPLOAD_TOKEN in .env durch einen sicheren Wert ersetzen
docker compose up --build
```

Danach ist die Seite unter `http://localhost:18787/u/<UPLOAD_TOKEN>` erreichbar. Im produktiven Einsatz gehört die App hinter einen HTTPS-Reverse-Proxy; der Container-Port wird absichtlich nur an `127.0.0.1` gebunden.

## Konfiguration

| Variable | Standard | Bedeutung |
| --- | --- | --- |
| `UPLOAD_TOKEN` | – | Pflichtwert mit mindestens 32 URL-sicheren Zeichen |
| `EVENT_TITLE` | `Unsere Hochzeit` | Überschrift der Upload-Seite |
| `EVENT_SUBTITLE` | siehe `.env.example` | Einladungstext |
| `MAX_UPLOAD_MB` | `75` | Maximale Größe je Bild |
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

Backups sollten mindestens den Ordner aus `NAS_UPLOAD_DIR` umfassen. Die App bietet absichtlich keine Löschfunktion.
