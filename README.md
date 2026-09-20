# Hochzeitsfotos und -videos direkt aufs NAS

Eine kleine, für iOS und Android optimierte Web-App, über die Hochzeitsgäste ohne separate App mehrere Fotos und Videos auswählen und direkt auf ein Synology-NAS laden können.

## Eigenschaften

- Mehrfachauswahl aus der Mediathek und direkter Kamerazugriff
- Fotos: JPEG, PNG, GIF, WebP, HEIC/HEIF und AVIF
- Videos: MP4, MOV, M4V, WebM/MKV, AVI, MPEG, 3GP und OGV
- Kein festes Dateigrößen- oder Upload-Zeitlimit; Dateien werden gestreamt
- Einzelner Fortschritt pro Datei und gezielter Wiederholungsversuch
- Originaldateien ohne Komprimierung oder Konvertierung
- NAS-Unterordner werden automatisch als Galerien erkannt (inklusive Leerzeichen, Umlauten und Sonderzeichen)
- Originaldateinamen bleiben erhalten; bei Kollisionen wird nur eine laufende Nummer ergänzt
- SHA-256-Prüfsumme und technische Uploaddaten werden getrennt von den eingebetteten Bildmetadaten protokolliert
- Sortierung nach EXIF-Aufnahmezeit (sekundengenau) oder Dateiname
- Einzel-, Auswahl- und Komplettdownload unveränderter Originale; große Auswahlen werden als gestreamtes ZIP erzeugt
- Responsive Vollbildansicht ohne Beschnitt, Swipe-Navigation, Pfeiltasten und Nachbarbild-Preloading
- Native Teilen-Funktion auf kompatiblen iOS-/Android-Browsern mit Download-Fallback
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
| `EVENT_TIMEZONE` | `Europe/Berlin` | Zeitzone für EXIF-Aufnahmezeit und NAS-Dateidatum |
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

## Ordner und Originaldateien

Jeder direkte, nicht versteckte Unterordner von `NAS_UPLOAD_DIR` ist eine Galerie und zugleich ein mögliches Uploadziel. Ordner wie `@eaDir`, `#recycle`, `cache`, `thumbnails` und `previews` werden ignoriert. Gibt es noch keinen Galerieordner, wird einmalig `Gäste-Uploads` angelegt. Medien aus älteren Installationen, die direkt im Wurzelordner liegen, bleiben über diese Galerie sichtbar und müssen nicht migriert werden.

Challenge-Fotos werden automatisch in der Galerie `Fotochallenge` gespeichert. Beim Serverstart werden auch Challenge-Fotos aus älteren Installationen dorthin verschoben; Challenge-Zuordnung und Moderationsstatus werden dabei mitgeführt.

Der Datenfluss ist bewusst verlustfrei:

```text
Browser File → Multipart-Stream → temporäre Datei im Zielordner
             → fsync → atomisches Umbenennen → NAS-Original
```

Es gibt dabei kein Canvas, Resizing, Re-Encoding oder automatisches Drehen. EXIF, IPTC, XMP, GPS und alle weiteren eingebetteten Daten bleiben bytegenau Bestandteil des Originals. `uploaded_at`, Dateigröße, MIME-Typ und SHA-256 werden ausschließlich separat in `uploads.jsonl` abgelegt. Das EXIF-Aufnahmedatum wird lesend aus JPEG/TIFF-Daten ermittelt und anhand Dateigröße plus Änderungszeit im Arbeitsspeicher gecacht. Beim Upload und bei jedem Serverstart wird dieses Aufnahmedatum zusätzlich – unter Berücksichtigung von `EVENT_TIMEZONE` – als Zugriffs- und Änderungszeit der NAS-Datei gesetzt, ohne die Bilddatei umzuschreiben. Fehlt das Aufnahmedatum im Original, bleibt die Datei unverändert und die Galerie fällt auf die vorhandene Dateiänderungszeit zurück.

Galerie und Vollbild verwenden derzeit das Original mit Browser-Lazy-Loading; es werden keine Preview-Dateien erzeugt. Beim Bildwechsel wird nur das vorherige und nächste Bild vorgeladen. Einzel- und ZIP-Downloads lesen dieselbe Originaldatei direkt vom NAS und kopieren sie ohne Bilddecoder oder Encoder in die HTTP-Antwort. ZIPs werden fortlaufend geschrieben und nicht vollständig im RAM aufgebaut.

Die Vollbildansicht verwendet `100dvh`, Safe-Area-Abstände und `object-fit: contain`. Dadurch bleiben Portrait, Landscape, Quadrat und Panorama vollständig und unverzerrt sichtbar. Horizontaler Swipe wechselt erst ab einer klaren Mindestdistanz das Bild; vertikale Gesten und kurze Bewegungen werden ignoriert. Auf Desktop stehen zusätzlich sichtbare Pfeile, `ArrowLeft`, `ArrowRight` und `Escape` zur Verfügung.

Die Beamer-Wechselzeit lässt sich in Settings sekundengenau zwischen 3 und 300 Sekunden einstellen. Bestehende Installationen verwenden automatisch den Standardwert von 10 Sekunden.

Optional mischt der Shuffle-Modus die Bilder für jede Runde neu. Frisch hochgeladene beziehungsweise freigegebene Bilder werden weiterhin zuerst eingeblendet.

Normale Fotos und Videos zeigen öffentlich keinen Namen der einreichenden Person. Nur Challenge-Fotos enthalten weiterhin die zugehörigen Namen. Bilder mit Upload-Datum 04. oder 05.09.2026 werden in der Diashow dreifach gewichtet; ältere Startbilder bleiben mit einfacher Gewichtung im Umlauf.

Im Vollbild blendet die Diashow Verbindungsstatus, Bildzähler, Steuerleiste und Mauszeiger nach drei Sekunden ohne Mausbewegung aus. Eine Mausbewegung zeigt Zeiger und Bedienelemente wieder kurz an.
