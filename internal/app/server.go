package app

import (
	"bufio"
	"crypto/rand"
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
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

//go:embed web/*
var webFiles embed.FS

var safeFilename = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

type Server struct {
	cfg       Config
	template  *template.Template
	assets    http.Handler
	limiter   *rateLimiter
	writeGate chan struct{}
	metaMu    sync.Mutex
	now       func() time.Time
}

type pageData struct {
	Title       string
	Subtitle    string
	UploadToken string
	MaxUploadMB int64
}

type uploadMetadata struct {
	StoredName   string `json:"stored_name"`
	OriginalName string `json:"original_name"`
	GuestName    string `json:"guest_name,omitempty"`
	ContentType  string `json:"content_type"`
	Size         int64  `json:"size"`
	RemoteIP     string `json:"remote_ip"`
	UploadedAt   string `json:"uploaded_at"`
}

func New(cfg Config) (http.Handler, error) {
	if err := os.MkdirAll(cfg.UploadDir, 0o750); err != nil {
		return nil, fmt.Errorf("create upload directory: %w", err)
	}
	if err := verifyWritable(cfg.UploadDir); err != nil {
		return nil, err
	}

	tmplBytes, err := webFiles.ReadFile("web/index.html")
	if err != nil {
		return nil, err
	}
	tmpl, err := template.New("index").Parse(string(tmplBytes))
	if err != nil {
		return nil, err
	}
	assetFS, err := fs.Sub(webFiles, "web")
	if err != nil {
		return nil, err
	}

	s := &Server{
		cfg:       cfg,
		template:  tmpl,
		assets:    http.FileServer(http.FS(assetFS)),
		limiter:   newRateLimiter(cfg.MaxUploadsPerHour, time.Hour),
		writeGate: make(chan struct{}, 4),
		now:       time.Now,
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
		s.serveUploadPage(w, r)
	case r.URL.Path == "/api/upload" && r.Method == http.MethodPost:
		s.handleUpload(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) serveUploadPage(w http.ResponseWriter, r *http.Request) {
	token, err := url.PathUnescape(strings.TrimPrefix(r.URL.Path, "/u/"))
	if err != nil || !s.validToken(token) {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := s.template.Execute(w, pageData{
		Title:       s.cfg.EventTitle,
		Subtitle:    s.cfg.EventSubtitle,
		UploadToken: s.cfg.UploadToken,
		MaxUploadMB: s.cfg.MaxUploadBytes >> 20,
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
		writeJSONError(w, http.StatusServiceUnavailable, "Gerade laden viele Gäste Bilder hoch. Bitte gleich erneut versuchen.")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, s.cfg.MaxUploadBytes+(2<<20))
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		status := http.StatusBadRequest
		message := "Die Datei konnte nicht gelesen werden."
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			status = http.StatusRequestEntityTooLarge
			message = fmt.Sprintf("Das Bild ist größer als %d MB.", s.cfg.MaxUploadBytes>>20)
		}
		writeJSONError(w, status, message)
		return
	}

	file, header, err := r.FormFile("photo")
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "Kein Bild ausgewählt.")
		return
	}
	defer file.Close()

	guestName := cleanGuestName(r.FormValue("guest_name"))
	metadata, err := s.storeUpload(file, header, guestName, remoteIP)
	if err != nil {
		var uploadErr *clientUploadError
		if errors.As(err, &uploadErr) {
			writeJSONError(w, uploadErr.status, uploadErr.message)
			return
		}
		log.Printf("store upload from %s: %v", remoteIP, err)
		writeJSONError(w, http.StatusInternalServerError, "Speichern fehlgeschlagen. Bitte erneut versuchen.")
		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "file": metadata.StoredName})
}

type clientUploadError struct {
	status  int
	message string
}

func (e *clientUploadError) Error() string { return e.message }

func (s *Server) storeUpload(file multipart.File, header *multipart.FileHeader, guestName, remoteIP string) (uploadMetadata, error) {
	reader := bufio.NewReader(file)
	peek, err := reader.Peek(512)
	if err != nil && !errors.Is(err, io.EOF) {
		return uploadMetadata{}, err
	}
	contentType, extension, ok := detectImage(peek, header.Filename)
	if !ok {
		return uploadMetadata{}, &clientUploadError{http.StatusUnsupportedMediaType, "Dieses Dateiformat wird nicht unterstützt."}
	}

	now := s.now().UTC()
	randomPart := make([]byte, 8)
	if _, err := rand.Read(randomPart); err != nil {
		return uploadMetadata{}, err
	}
	base := strings.TrimSuffix(filepath.Base(header.Filename), filepath.Ext(header.Filename))
	base = strings.Trim(safeFilename.ReplaceAllString(base, "-"), "-._")
	if base == "" {
		base = "foto"
	}
	if len(base) > 50 {
		base = base[:50]
	}
	storedName := fmt.Sprintf("%s_%s_%s%s", now.Format("20060102_150405.000"), base, hex.EncodeToString(randomPart), extension)
	finalPath := filepath.Join(s.cfg.UploadDir, storedName)
	temp, err := os.CreateTemp(s.cfg.UploadDir, ".upload-*")
	if err != nil {
		return uploadMetadata{}, err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)

	written, copyErr := io.Copy(temp, io.LimitReader(reader, s.cfg.MaxUploadBytes+1))
	if copyErr != nil {
		temp.Close()
		return uploadMetadata{}, copyErr
	}
	if written > s.cfg.MaxUploadBytes {
		temp.Close()
		return uploadMetadata{}, &clientUploadError{http.StatusRequestEntityTooLarge, fmt.Sprintf("Das Bild ist größer als %d MB.", s.cfg.MaxUploadBytes>>20)}
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
	if err := os.Rename(tempPath, finalPath); err != nil {
		return uploadMetadata{}, err
	}

	metadata := uploadMetadata{
		StoredName:   storedName,
		OriginalName: filepath.Base(header.Filename),
		GuestName:    guestName,
		ContentType:  contentType,
		Size:         written,
		RemoteIP:     remoteIP,
		UploadedAt:   now.Format(time.RFC3339Nano),
	}
	if err := s.appendMetadata(metadata); err != nil {
		log.Printf("append metadata for %s: %v", storedName, err)
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

func (s *Server) validToken(candidate string) bool {
	if len(candidate) != len(s.cfg.UploadToken) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(candidate), []byte(s.cfg.UploadToken)) == 1
}

func detectImage(header []byte, originalName string) (string, string, bool) {
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
	return "", "", false
}

func isHEIF(header []byte) bool {
	if len(header) < 12 || string(header[4:8]) != "ftyp" {
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

func cleanGuestName(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	runes := []rune(value)
	if len(runes) > 80 {
		value = string(runes[:80])
	}
	return value
}

func writeJSONError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": message})
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
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' blob: data:; connect-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
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
