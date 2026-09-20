package app

import (
	"bufio"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

//go:embed web/*
var webFiles embed.FS

type Server struct {
	cfg             Config
	templates       map[string]*template.Template
	assets          http.Handler
	limiter         *rateLimiter
	settingsLimiter *rateLimiter
	writeGate       chan struct{}
	metaMu          sync.Mutex
	filenameMu      sync.Mutex
	imageMetaMu     sync.Mutex
	imageMetaCache  map[string]cachedImageMetadata
	captureLocation *time.Location
	settings        *settingsStore
	sessions        *sessionStore
	now             func() time.Time
}

type pageData struct {
	Title       string
	Subtitle    string
	UploadToken string
}

type uploadMetadata struct {
	StoredName   string `json:"stored_name"`
	OriginalName string `json:"original_name"`
	GuestName    string `json:"guest_name,omitempty"`
	ContentType  string `json:"content_type"`
	Size         int64  `json:"size"`
	RemoteIP     string `json:"remote_ip"`
	UploadedAt   string `json:"uploaded_at"`
	Gallery      string `json:"gallery,omitempty"`
	SHA256       string `json:"sha256,omitempty"`
	IsChallenge  bool   `json:"is_challenge,omitempty"`
	Challenge    string `json:"challenge,omitempty"`
	ChallengeBy  string `json:"challenge_by,omitempty"`
}

type publicMedia struct {
	ID          string   `json:"id"`
	URL         string   `json:"url"`
	Kind        string   `json:"kind"`
	ContentType string   `json:"content_type"`
	Size        int64    `json:"size"`
	GuestName   string   `json:"guest_name,omitempty"`
	UploadedAt  string   `json:"uploaded_at"`
	Gallery     string   `json:"gallery"`
	Filename    string   `json:"filename"`
	CapturedAt  string   `json:"captured_at,omitempty"`
	DownloadURL string   `json:"download_url"`
	Latitude    *float64 `json:"latitude,omitempty"`
	Longitude   *float64 `json:"longitude,omitempty"`
	Width       int      `json:"width,omitempty"`
	Height      int      `json:"height,omitempty"`
	IsChallenge bool     `json:"is_challenge,omitempty"`
	Challenge   string   `json:"challenge,omitempty"`
	ChallengeBy string   `json:"challenge_by,omitempty"`
}

func New(cfg Config) (http.Handler, error) {
	if err := os.MkdirAll(cfg.UploadDir, 0o750); err != nil {
		return nil, fmt.Errorf("create upload directory: %w", err)
	}
	if err := verifyWritable(cfg.UploadDir); err != nil {
		return nil, err
	}
	captureLocation, err := loadCaptureLocation(cfg.EventTimezone)
	if err != nil {
		return nil, fmt.Errorf("load event timezone: %w", err)
	}
	stats := syncImageCaptureTimes(cfg.UploadDir, captureLocation)
	if stats.Updated > 0 || stats.Failed > 0 {
		log.Printf("capture-time sync: updated=%d unchanged=%d missing=%d failed=%d", stats.Updated, stats.Unchanged, stats.Missing, stats.Failed)
	}

	settings, err := newSettingsStore(cfg.UploadDir, cfg.SettingsPassword)
	if err != nil {
		return nil, fmt.Errorf("initialize settings: %w", err)
	}

	templates := make(map[string]*template.Template, 4)
	for _, name := range []string{"index", "gallery", "slideshow", "settings"} {
		tmplBytes, err := webFiles.ReadFile("web/" + name + ".html")
		if err != nil {
			return nil, err
		}
		tmpl, err := template.New(name).Parse(string(tmplBytes))
		if err != nil {
			return nil, err
		}
		templates[name] = tmpl
	}
	assetFS, err := fs.Sub(webFiles, "web")
	if err != nil {
		return nil, err
	}

	s := &Server{
		cfg:             cfg,
		templates:       templates,
		assets:          http.FileServer(http.FS(assetFS)),
		limiter:         newRateLimiter(cfg.MaxUploadsPerHour, time.Hour),
		settingsLimiter: newRateLimiter(12, 15*time.Minute),
		writeGate:       make(chan struct{}, 4),
		imageMetaCache:  make(map[string]cachedImageMetadata),
		captureLocation: captureLocation,
		settings:        settings,
		sessions:        newSessionStore(),
		now:             time.Now,
	}
	migratedChallenges, err := s.migrateChallengeGallery()
	if err != nil {
		return nil, fmt.Errorf("migrate challenge gallery: %w", err)
	}
	if migratedChallenges > 0 {
		log.Printf("challenge gallery migration: moved=%d", migratedChallenges)
	}
	return s.securityHeaders(http.HandlerFunc(s.route)), nil
}

func (s *Server) route(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/healthz" && r.Method == http.MethodGet:
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok\n")
	case strings.HasPrefix(r.URL.Path, "/assets/") && r.Method == http.MethodGet:
		r.URL.Path = strings.TrimPrefix(r.URL.Path, "/assets")
		s.assets.ServeHTTP(w, r)
	case strings.HasPrefix(r.URL.Path, "/u/") && r.Method == http.MethodGet:
		s.serveProtectedPage(w, r)
	case strings.HasPrefix(r.URL.Path, "/m/") && r.Method == http.MethodGet:
		s.serveMedia(w, r)
	case r.URL.Path == "/api/media" && r.Method == http.MethodGet:
		s.handleMediaList(w, r)
	case r.URL.Path == "/api/galleries" && r.Method == http.MethodGet:
		s.handleGalleryList(w, r)
	case r.URL.Path == "/api/download" && r.Method == http.MethodGet:
		s.handleOriginalDownload(w, r)
	case r.URL.Path == "/api/download/zip" && r.Method == http.MethodPost:
		s.handleZIPDownload(w, r)
	case r.URL.Path == "/api/upload" && r.Method == http.MethodPost:
		s.handleUpload(w, r)
	case r.URL.Path == "/api/settings/login" && r.Method == http.MethodPost:
		s.handleSettingsLogin(w, r)
	case r.URL.Path == "/api/settings/logout" && r.Method == http.MethodPost:
		s.handleSettingsLogout(w, r)
	case r.URL.Path == "/api/settings" && (r.Method == http.MethodGet || r.Method == http.MethodPost):
		s.handleSettings(w, r)
	case r.URL.Path == "/api/settings/media" && r.Method == http.MethodPost:
		s.handleModerationUpdate(w, r)
	case r.URL.Path == "/api/settings/media/delete" && r.Method == http.MethodPost:
		s.handleMediaDelete(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) serveProtectedPage(w http.ResponseWriter, r *http.Request) {
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/u/"), "/")
	parts := strings.Split(rest, "/")
	if len(parts) == 0 || len(parts) > 2 {
		http.NotFound(w, r)
		return
	}
	token, err := url.PathUnescape(parts[0])
	if err != nil || !s.validToken(token) {
		http.NotFound(w, r)
		return
	}
	page := "index"
	if len(parts) == 2 {
		page = parts[1]
	}
	tmpl, ok := s.templates[page]
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := tmpl.Execute(w, pageData{
		Title:       s.cfg.EventTitle,
		Subtitle:    s.cfg.EventSubtitle,
		UploadToken: s.cfg.UploadToken,
	}); err != nil {
		log.Printf("render upload page: %v", err)
	}
}

func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	if !s.validToken(r.Header.Get("X-Upload-Token")) {
		writeJSONError(w, http.StatusNotFound, "Upload-Link ungültig.")
		return
	}
	if !sameOrigin(r) {
		writeJSONError(w, http.StatusForbidden, "Anfrage nicht erlaubt.")
		return
	}

	remoteIP := clientIP(r)
	if !s.limiter.Allow(remoteIP, s.now()) {
		w.Header().Set("Retry-After", "3600")
		writeJSONError(w, http.StatusTooManyRequests, "Zu viele Uploads. Bitte versucht es später erneut.")
		return
	}

	select {
	case s.writeGate <- struct{}{}:
		defer func() { <-s.writeGate }()
	default:
		writeJSONError(w, http.StatusServiceUnavailable, "Gerade laden viele Gäste Dateien hoch. Bitte gleich erneut versuchen.")
		return
	}

	reader, err := r.MultipartReader()
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "Die Anfrage konnte nicht gelesen werden.")
		return
	}

	var guestName string
	var galleryName string
	var challengeFlag string
	var challengeText string
	var challengeBy string
	var metadata uploadMetadata
	stored := false
	for {
		part, partErr := reader.NextPart()
		if errors.Is(partErr, io.EOF) {
			break
		}
		if partErr != nil {
			writeJSONError(w, http.StatusBadRequest, "Die Datei konnte nicht gelesen werden.")
			return
		}

		switch part.FormName() {
		case "guest_name", "gallery", "is_challenge", "challenge", "challenge_by":
			value, readErr := io.ReadAll(io.LimitReader(part, 4<<10))
			if readErr == nil {
				switch part.FormName() {
				case "guest_name":
					guestName = cleanText(string(value), 80)
				case "gallery":
					galleryName = strings.TrimSpace(string(value))
				case "is_challenge":
					challengeFlag = strings.TrimSpace(string(value))
				case "challenge":
					challengeText = cleanText(string(value), 180)
				case "challenge_by":
					challengeBy = cleanText(string(value), 120)
				}
			}
		case "media", "photo":
			if stored || part.FileName() == "" {
				part.Close()
				continue
			}
			metadata, err = s.storeUpload(part, part.FileName(), remoteIP, galleryName)
			if err != nil {
				part.Close()
				var uploadErr *clientUploadError
				if errors.As(err, &uploadErr) {
					writeJSONError(w, uploadErr.status, uploadErr.message)
					return
				}
				log.Printf("store upload from %s: %v", remoteIP, err)
				writeJSONError(w, http.StatusInternalServerError, "Speichern fehlgeschlagen. Bitte erneut versuchen.")
				return
			}
			stored = true
		}
		part.Close()
	}

	if !stored {
		writeJSONError(w, http.StatusBadRequest, "Kein Foto oder Video ausgewählt.")
		return
	}
	metadata.GuestName = guestName
	metadata.IsChallenge = strings.EqualFold(challengeFlag, "true") || challengeFlag == "1"
	if metadata.IsChallenge {
		if !strings.HasPrefix(metadata.ContentType, "image/") {
			s.removeStoredUpload(metadata.StoredName)
			writeJSONError(w, http.StatusBadRequest, "Ein Challenge-Bild muss ein Foto sein.")
			return
		}
		if challengeText == "" || challengeBy == "" {
			s.removeStoredUpload(metadata.StoredName)
			writeJSONError(w, http.StatusBadRequest, "Bitte Challenge und Teilnehmer vollständig angeben.")
			return
		}
		metadata.Challenge = challengeText
		metadata.ChallengeBy = challengeBy
		metadata, err = s.moveUploadToGallery(metadata, challengeGalleryName)
		if err != nil {
			s.removeStoredUpload(metadata.StoredName)
			log.Printf("move challenge upload to gallery: %v", err)
			writeJSONError(w, http.StatusInternalServerError, "Challenge-Foto konnte nicht einsortiert werden.")
			return
		}
	}
	if err := s.appendMetadata(metadata); err != nil {
		log.Printf("append metadata for %s: %v", metadata.StoredName, err)
	}
	pending, err := s.settings.recordUpload(metadata.StoredName)
	if err != nil {
		s.removeStoredUpload(metadata.StoredName)
		log.Printf("record moderation state for %s: %v", metadata.StoredName, err)
		writeJSONError(w, http.StatusInternalServerError, "Speichern fehlgeschlagen. Bitte erneut versuchen.")
		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "file": metadata.StoredName, "pending": pending})
}

func (s *Server) removeStoredUpload(name string) {
	path, err := s.resolveMediaPath(name)
	if err != nil {
		return
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Printf("remove rejected challenge upload %s: %v", name, err)
	}
}

type clientUploadError struct {
	status  int
	message string
}

func (e *clientUploadError) Error() string { return e.message }

func (s *Server) storeUpload(file io.Reader, originalName, remoteIP, galleryName string) (uploadMetadata, error) {
	reader := bufio.NewReader(file)
	peek, err := reader.Peek(512)
	if err != nil && !errors.Is(err, io.EOF) {
		return uploadMetadata{}, err
	}
	contentType, extension, ok := detectMedia(peek, originalName)
	if !ok {
		return uploadMetadata{}, &clientUploadError{http.StatusUnsupportedMediaType, "Dieses Foto- oder Videoformat wird nicht unterstützt."}
	}

	now := s.now().UTC()
	if strings.TrimSpace(galleryName) == "" {
		galleries, listErr := s.galleryList()
		if listErr != nil || len(galleries) == 0 {
			return uploadMetadata{}, listErr
		}
		galleryName = galleries[0].Name
	}
	galleryPath, cleanGallery, err := s.resolveGallery(galleryName)
	if err != nil {
		return uploadMetadata{}, &clientUploadError{http.StatusBadRequest, "Bitte eine gültige Galerie auswählen."}
	}
	cleanOriginal := sanitizeOriginalFilename(originalName, preferredOriginalExtension(originalName, extension))
	temp, err := os.CreateTemp(galleryPath, ".upload-*")
	if err != nil {
		return uploadMetadata{}, err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)

	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(temp, hash), reader)
	if copyErr != nil {
		temp.Close()
		return uploadMetadata{}, copyErr
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return uploadMetadata{}, err
	}
	if err := temp.Chmod(0o640); err != nil {
		temp.Close()
		return uploadMetadata{}, err
	}
	if err := temp.Close(); err != nil {
		return uploadMetadata{}, err
	}
	s.filenameMu.Lock()
	storedFilename, finalPath, err := availableFilename(galleryPath, cleanOriginal)
	if err == nil {
		err = os.Rename(tempPath, finalPath)
	}
	s.filenameMu.Unlock()
	if err != nil {
		return uploadMetadata{}, err
	}
	storedName := filepath.ToSlash(filepath.Join(cleanGallery, storedFilename))
	if _, _, err := applyImageCaptureTime(finalPath, s.captureLocation); err != nil {
		log.Printf("set capture time for %s: %v", storedName, err)
	}

	metadata := uploadMetadata{
		StoredName:   storedName,
		OriginalName: filepath.Base(originalName),
		ContentType:  contentType,
		Size:         written,
		RemoteIP:     remoteIP,
		UploadedAt:   now.Format(time.RFC3339Nano),
		Gallery:      cleanGallery,
		SHA256:       hex.EncodeToString(hash.Sum(nil)),
	}
	return metadata, nil
}

func (s *Server) appendMetadata(metadata uploadMetadata) error {
	s.metaMu.Lock()
	defer s.metaMu.Unlock()

	path := filepath.Join(s.cfg.UploadDir, "uploads.jsonl")
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o640)
	if err != nil {
		return err
	}
	defer file.Close()
	return json.NewEncoder(file).Encode(metadata)
}

func (s *Server) deleteMedia(name string) error {
	s.metaMu.Lock()
	defer s.metaMu.Unlock()
	metadataKey := s.storageKey(name)

	mediaPath, err := s.resolveMediaPath(name)
	if err != nil {
		return err
	}

	randomPart, err := randomHex(8)
	if err != nil {
		return err
	}
	stagedPath := filepath.Join(s.cfg.UploadDir, ".deleting-"+randomPart)
	if err := os.Rename(mediaPath, stagedPath); err != nil {
		return err
	}
	restoreMedia := true
	defer func() {
		if restoreMedia {
			if err := os.Rename(stagedPath, mediaPath); err != nil {
				log.Printf("restore media after failed deletion: %v", err)
			}
		}
	}()

	metadataPath := filepath.Join(s.cfg.UploadDir, "uploads.jsonl")
	metadataFile, err := os.Open(metadataPath)
	if errors.Is(err, os.ErrNotExist) {
		restoreMedia = false
		return os.Remove(stagedPath)
	}
	if err != nil {
		return err
	}

	temp, err := os.CreateTemp(s.cfg.UploadDir, ".metadata-delete-*")
	if err != nil {
		metadataFile.Close()
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)

	scanner := bufio.NewScanner(metadataFile)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	for scanner.Scan() {
		line := append([]byte(nil), scanner.Bytes()...)
		var item uploadMetadata
		if json.Unmarshal(line, &item) == nil && (item.StoredName == name || item.StoredName == metadataKey) {
			continue
		}
		if _, err := temp.Write(append(line, '\n')); err != nil {
			metadataFile.Close()
			temp.Close()
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		metadataFile.Close()
		temp.Close()
		return err
	}
	if err := metadataFile.Close(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Chmod(0o640); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempPath, metadataPath); err != nil {
		return err
	}

	restoreMedia = false
	return os.Remove(stagedPath)
}

func (s *Server) handleMediaList(w http.ResponseWriter, r *http.Request) {
	token := r.Header.Get("X-Upload-Token")
	if !s.validToken(token) {
		writeJSONError(w, http.StatusNotFound, "Galerie-Link ungültig.")
		return
	}

	gallery := strings.TrimSpace(r.URL.Query().Get("gallery"))
	if gallery != "" {
		if _, _, err := s.resolveGallery(gallery); err != nil {
			writeJSONError(w, http.StatusNotFound, "Galerie wurde nicht gefunden.")
			return
		}
	}
	items, err := s.scanMedia(gallery)
	if err != nil {
		log.Printf("read gallery metadata: %v", err)
		writeJSONError(w, http.StatusInternalServerError, "Galerie konnte nicht geladen werden.")
		return
	}
	sortMode := r.URL.Query().Get("sort")
	sort.SliceStable(items, func(i, j int) bool {
		left, right := items[i].CapturedAt, items[j].CapturedAt
		if left == "" {
			left = items[i].UploadedAt
		}
		if right == "" {
			right = items[j].UploadedAt
		}
		switch sortMode {
		case "captured_asc":
			return left < right
		case "name_asc":
			return strings.ToLower(items[i].Filename) < strings.ToLower(items[j].Filename)
		default:
			return left > right
		}
	})

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	settings := s.settings.snapshot()
	_ = json.NewEncoder(w).Encode(map[string]any{
		"items":                      items,
		"slideshow_style":            settings.SlideshowStyle,
		"slideshow_interval_seconds": settings.SlideshowIntervalSeconds,
		"slideshow_shuffle":          settings.SlideshowShuffle,
	})
}

func (s *Server) readMetadata() ([]uploadMetadata, error) {
	s.metaMu.Lock()
	defer s.metaMu.Unlock()

	file, err := os.Open(filepath.Join(s.cfg.UploadDir, "uploads.jsonl"))
	if errors.Is(err, os.ErrNotExist) {
		return []uploadMetadata{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()

	items := make([]uploadMetadata, 0)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	for scanner.Scan() {
		var item uploadMetadata
		if err := json.Unmarshal(scanner.Bytes(), &item); err != nil {
			continue
		}
		items = append(items, item)
	}
	return items, scanner.Err()
}

func (s *Server) serveMedia(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/m/")
	parts := strings.SplitN(rest, "/", 2)
	if len(parts) != 2 {
		http.NotFound(w, r)
		return
	}
	token, tokenErr := url.PathUnescape(parts[0])
	id, idErr := url.PathUnescape(parts[1])
	relative, decodeErr := decodeMediaID(id)
	if tokenErr != nil || idErr != nil || decodeErr != nil || !s.validToken(token) {
		http.NotFound(w, r)
		return
	}
	if !s.settings.isPublic(s.storageKey(relative)) && !s.validSettingsSession(r) {
		http.NotFound(w, r)
		return
	}
	path, err := s.resolveMediaPath(relative)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Disposition", "inline")
	http.ServeFile(w, r, path)
}

func (s *Server) validToken(candidate string) bool {
	if len(candidate) != len(s.cfg.UploadToken) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(candidate), []byte(s.cfg.UploadToken)) == 1
}

func detectMedia(header []byte, originalName string) (string, string, bool) {
	detected := http.DetectContentType(header)
	switch detected {
	case "image/jpeg":
		return detected, ".jpg", true
	case "image/png":
		return detected, ".png", true
	case "image/gif":
		return detected, ".gif", true
	case "image/webp":
		return detected, ".webp", true
	}
	if isHEIF(header) {
		ext := strings.ToLower(filepath.Ext(originalName))
		if ext == ".avif" {
			return "image/avif", ".avif", true
		}
		if ext == ".heif" {
			return "image/heif", ".heif", true
		}
		return "image/heic", ".heic", true
	}
	if isISOBaseMedia(header) {
		ext := strings.ToLower(filepath.Ext(originalName))
		switch ext {
		case ".mov":
			return "video/quicktime", ".mov", true
		case ".m4v":
			return "video/x-m4v", ".m4v", true
		case ".3gp", ".3gpp":
			return "video/3gpp", ".3gp", true
		default:
			return "video/mp4", ".mp4", true
		}
	}
	if len(header) >= 4 && string(header[:4]) == "OggS" {
		return "video/ogg", ".ogv", true
	}
	if len(header) >= 12 && string(header[:4]) == "RIFF" && string(header[8:12]) == "AVI " {
		return "video/x-msvideo", ".avi", true
	}
	if len(header) >= 4 && header[0] == 0x1a && header[1] == 0x45 && header[2] == 0xdf && header[3] == 0xa3 {
		if strings.EqualFold(filepath.Ext(originalName), ".mkv") || strings.Contains(strings.ToLower(string(header)), "matroska") {
			return "video/x-matroska", ".mkv", true
		}
		return "video/webm", ".webm", true
	}
	if len(header) >= 4 && header[0] == 0x00 && header[1] == 0x00 && header[2] == 0x01 && (header[3] == 0xba || header[3] == 0xb3) {
		return "video/mpeg", ".mpeg", true
	}
	return "", "", false
}

func isISOBaseMedia(header []byte) bool {
	return len(header) >= 12 && string(header[4:8]) == "ftyp"
}

func isHEIF(header []byte) bool {
	if !isISOBaseMedia(header) {
		return false
	}
	brands := []string{"heic", "heix", "hevc", "hevx", "heim", "heis", "mif1", "msf1", "avif", "avis"}
	payload := string(header[8:])
	for _, brand := range brands {
		if strings.Contains(payload, brand) {
			return true
		}
	}
	return false
}

func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	host := r.Host
	if forwarded := r.Header.Get("X-Forwarded-Host"); forwarded != "" {
		host = strings.TrimSpace(strings.Split(forwarded, ",")[0])
	}
	return strings.EqualFold(u.Host, host)
}

func clientIP(r *http.Request) string {
	if value := strings.TrimSpace(r.Header.Get("X-Real-IP")); net.ParseIP(value) != nil {
		return value
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}

func cleanText(value string, limit int) string {
	value = strings.Join(strings.Fields(value), " ")
	runes := []rune(value)
	if len(runes) > limit {
		value = string(runes[:limit])
	}
	return value
}

func writeJSONError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{"ok": false, "error": message})
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func verifyWritable(dir string) error {
	file, err := os.CreateTemp(dir, ".write-check-*")
	if err != nil {
		return fmt.Errorf("upload directory is not writable: %w", err)
	}
	name := file.Name()
	if err := file.Close(); err != nil {
		return err
	}
	return os.Remove(name)
}

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' blob: data:; media-src 'self' blob:; connect-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		w.Header().Set("Permissions-Policy", "camera=(self), geolocation=(), microphone=()")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}

type rateLimiter struct {
	mu      sync.Mutex
	limit   int
	window  time.Duration
	clients map[string]*rateEntry
}

type rateEntry struct {
	start time.Time
	count int
}

func newRateLimiter(limit int, window time.Duration) *rateLimiter {
	return &rateLimiter{limit: limit, window: window, clients: make(map[string]*rateEntry)}
}

func (l *rateLimiter) Allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	entry, ok := l.clients[key]
	if !ok || now.Sub(entry.start) >= l.window {
		l.clients[key] = &rateEntry{start: now, count: 1}
		return true
	}
	if entry.count >= l.limit {
		return false
	}
	entry.count++
	return true
}
