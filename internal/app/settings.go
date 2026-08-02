package app

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const settingsSessionCookie = "wedding_settings_session"

type persistedSettings struct {
	ModerationEnabled bool              `json:"moderation_enabled"`
	SlideshowStyle    string            `json:"slideshow_style"`
	PasswordSalt      string            `json:"password_salt"`
	PasswordHash      string            `json:"password_hash"`
	Media             map[string]string `json:"media"`
}

type settingsStore struct {
	mu    sync.RWMutex
	path  string
	state persistedSettings
}

type sessionStore struct {
	mu       sync.Mutex
	sessions map[string]time.Time
}

type settingsUpdate struct {
	ModerationEnabled *bool  `json:"moderation_enabled"`
	SlideshowStyle    string `json:"slideshow_style"`
	CurrentPassword   string `json:"current_password"`
	NewPassword       string `json:"new_password"`
}

type moderationUpdate struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

type adminMedia struct {
	publicMedia
	Status string `json:"status"`
}

func newSettingsStore(uploadDir, initialPassword string) (*settingsStore, error) {
	store := &settingsStore{path: filepath.Join(uploadDir, "settings.json")}
	data, err := os.ReadFile(store.path)
	if err == nil {
		if err := json.Unmarshal(data, &store.state); err != nil {
			return nil, fmt.Errorf("parse settings: %w", err)
		}
		if store.state.Media == nil {
			store.state.Media = make(map[string]string)
		}
		if store.state.SlideshowStyle != "polaroid" {
			store.state.SlideshowStyle = "classic"
		}
		if store.state.PasswordSalt == "" || store.state.PasswordHash == "" {
			return nil, errors.New("settings password hash is missing")
		}
		return store, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read settings: %w", err)
	}

	salt, err := randomHex(16)
	if err != nil {
		return nil, err
	}
	store.state = persistedSettings{
		SlideshowStyle: "classic",
		PasswordSalt:   salt,
		PasswordHash:   passwordDigest(salt, initialPassword),
		Media:          make(map[string]string),
	}
	if err := store.saveLocked(store.state); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *settingsStore) snapshot() persistedSettings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneSettings(s.state)
}

func (s *settingsStore) verifyPassword(password string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	digest := passwordDigest(s.state.PasswordSalt, password)
	return subtle.ConstantTimeCompare([]byte(digest), []byte(s.state.PasswordHash)) == 1
}

func (s *settingsStore) update(update settingsUpdate) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := cloneSettings(s.state)
	if update.ModerationEnabled != nil {
		next.ModerationEnabled = *update.ModerationEnabled
	}
	if update.SlideshowStyle != "" {
		if update.SlideshowStyle != "classic" && update.SlideshowStyle != "polaroid" {
			return errors.New("invalid slideshow style")
		}
		next.SlideshowStyle = update.SlideshowStyle
	}
	if update.NewPassword != "" {
		if len(update.NewPassword) < 6 || len(update.NewPassword) > 128 {
			return errors.New("new password must contain between 6 and 128 characters")
		}
		currentDigest := passwordDigest(s.state.PasswordSalt, update.CurrentPassword)
		if subtle.ConstantTimeCompare([]byte(currentDigest), []byte(s.state.PasswordHash)) != 1 {
			return errors.New("current password is invalid")
		}
		salt, err := randomHex(16)
		if err != nil {
			return err
		}
		next.PasswordSalt = salt
		next.PasswordHash = passwordDigest(salt, update.NewPassword)
	}
	if err := s.saveLocked(next); err != nil {
		return err
	}
	s.state = next
	return nil
}

func (s *settingsStore) recordUpload(name string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := cloneSettings(s.state)
	status := "approved"
	if next.ModerationEnabled {
		status = "pending"
	}
	next.Media[name] = status
	if err := s.saveLocked(next); err != nil {
		return false, err
	}
	s.state = next
	return status == "pending", nil
}

func (s *settingsStore) setMediaStatus(name, status string) error {
	if status != "approved" && status != "pending" && status != "rejected" {
		return errors.New("invalid moderation status")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	next := cloneSettings(s.state)
	next.Media[name] = status
	if err := s.saveLocked(next); err != nil {
		return err
	}
	s.state = next
	return nil
}

func (s *settingsStore) status(name string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	status := s.state.Media[name]
	if status == "" {
		return "approved"
	}
	return status
}

func (s *settingsStore) isPublic(name string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	status := s.state.Media[name]
	if status == "rejected" {
		return false
	}
	if s.state.ModerationEnabled && status == "pending" {
		return false
	}
	return true
}

func (s *settingsStore) saveLocked(state persistedSettings) error {
	dir := filepath.Dir(s.path)
	temp, err := os.CreateTemp(dir, ".settings-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	encoder := json.NewEncoder(temp)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(state); err != nil {
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
	return os.Rename(tempPath, s.path)
}

func cloneSettings(state persistedSettings) persistedSettings {
	clone := state
	clone.Media = make(map[string]string, len(state.Media))
	for name, status := range state.Media {
		clone.Media[name] = status
	}
	return clone
}

func passwordDigest(salt, password string) string {
	decodedSalt, _ := hex.DecodeString(salt)
	seed := append(append([]byte{}, decodedSalt...), []byte(password)...)
	digest := sha256.Sum256(seed)
	var block [48]byte
	copy(block[32:], decodedSalt)
	for iteration := 0; iteration < 120000; iteration++ {
		copy(block[:32], digest[:])
		digest = sha256.Sum256(block[:])
	}
	return hex.EncodeToString(digest[:])
}

func randomHex(size int) (string, error) {
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

func newSessionStore() *sessionStore {
	return &sessionStore{sessions: make(map[string]time.Time)}
}

func (s *sessionStore) create(now time.Time) (string, error) {
	token, err := randomHex(32)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for existing, expires := range s.sessions {
		if !expires.After(now) {
			delete(s.sessions, existing)
		}
	}
	s.sessions[token] = now.Add(12 * time.Hour)
	return token, nil
}

func (s *sessionStore) valid(token string, now time.Time) bool {
	if token == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	expires, ok := s.sessions[token]
	if !ok || !expires.After(now) {
		delete(s.sessions, token)
		return false
	}
	return true
}

func (s *sessionStore) delete(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, token)
}

func (s *Server) handleSettingsLogin(w http.ResponseWriter, r *http.Request) {
	if !s.validToken(r.Header.Get("X-Upload-Token")) || !sameOrigin(r) {
		writeJSONError(w, http.StatusNotFound, "Settings-Link ungültig.")
		return
	}
	if !s.settingsLimiter.Allow(clientIP(r), s.now()) {
		w.Header().Set("Retry-After", "900")
		writeJSONError(w, http.StatusTooManyRequests, "Zu viele Anmeldeversuche. Bitte später erneut versuchen.")
		return
	}
	var request struct {
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &request); err != nil || !s.settings.verifyPassword(request.Password) {
		writeJSONError(w, http.StatusUnauthorized, "Passwort ist nicht korrekt.")
		return
	}
	token, err := s.sessions.create(s.now())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "Anmeldung fehlgeschlagen.")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     settingsSessionCookie,
		Value:    token,
		Path:     "/",
		MaxAge:   12 * 60 * 60,
		HttpOnly: true,
		Secure:   requestIsHTTPS(r),
		SameSite: http.SameSiteStrictMode,
	})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleSettingsLogout(w http.ResponseWriter, r *http.Request) {
	if !s.validToken(r.Header.Get("X-Upload-Token")) || !sameOrigin(r) {
		writeJSONError(w, http.StatusNotFound, "Settings-Link ungültig.")
		return
	}
	if cookie, err := r.Cookie(settingsSessionCookie); err == nil {
		s.sessions.delete(cookie.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: settingsSessionCookie, Path: "/", MaxAge: -1, HttpOnly: true, Secure: requestIsHTTPS(r), SameSite: http.SameSiteStrictMode})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	if !s.requireSettingsSession(w, r) {
		return
	}
	if r.Method == http.MethodPost {
		if !sameOrigin(r) {
			writeJSONError(w, http.StatusForbidden, "Anfrage nicht erlaubt.")
			return
		}
		var update settingsUpdate
		if err := decodeJSON(r, &update); err != nil {
			writeJSONError(w, http.StatusBadRequest, "Settings konnten nicht gelesen werden.")
			return
		}
		if err := s.settings.update(update); err != nil {
			writeJSONError(w, http.StatusBadRequest, settingsErrorMessage(err))
			return
		}
	}

	state := s.settings.snapshot()
	media, err := s.adminMediaList()
	if err != nil {
		logSettingsError("read media for settings", err)
		writeJSONError(w, http.StatusInternalServerError, "Medien konnten nicht geladen werden.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":                 true,
		"moderation_enabled": state.ModerationEnabled,
		"slideshow_style":    state.SlideshowStyle,
		"items":              media,
	})
}

func (s *Server) handleModerationUpdate(w http.ResponseWriter, r *http.Request) {
	if !s.requireSettingsSession(w, r) {
		return
	}
	if !sameOrigin(r) {
		writeJSONError(w, http.StatusForbidden, "Anfrage nicht erlaubt.")
		return
	}
	var update moderationUpdate
	if err := decodeJSON(r, &update); err != nil || !storedMediaName.MatchString(update.ID) {
		writeJSONError(w, http.StatusBadRequest, "Ungültige Aufnahme.")
		return
	}
	if info, err := os.Stat(filepath.Join(s.cfg.UploadDir, update.ID)); err != nil || !info.Mode().IsRegular() {
		writeJSONError(w, http.StatusNotFound, "Aufnahme wurde nicht gefunden.")
		return
	}
	if err := s.settings.setMediaStatus(update.ID, update.Status); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Moderationsstatus ist ungültig.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "status": update.Status})
}

func (s *Server) requireSettingsSession(w http.ResponseWriter, r *http.Request) bool {
	if !s.validToken(r.Header.Get("X-Upload-Token")) {
		writeJSONError(w, http.StatusNotFound, "Settings-Link ungültig.")
		return false
	}
	if !s.validSettingsSession(r) {
		writeJSONError(w, http.StatusUnauthorized, "Bitte erneut bei Settings anmelden.")
		return false
	}
	return true
}

func (s *Server) validSettingsSession(r *http.Request) bool {
	cookie, err := r.Cookie(settingsSessionCookie)
	return err == nil && s.sessions.valid(cookie.Value, s.now())
}

func (s *Server) adminMediaList() ([]adminMedia, error) {
	metadata, err := s.readMetadata()
	if err != nil {
		return nil, err
	}
	items := make([]adminMedia, 0, len(metadata))
	for _, item := range metadata {
		if !storedMediaName.MatchString(item.StoredName) {
			continue
		}
		if info, err := os.Stat(filepath.Join(s.cfg.UploadDir, item.StoredName)); err != nil || !info.Mode().IsRegular() {
			continue
		}
		items = append(items, adminMedia{
			publicMedia: s.toPublicMedia(item),
			Status:      s.settings.status(item.StoredName),
		})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].UploadedAt > items[j].UploadedAt })
	return items, nil
}

func decodeJSON(r *http.Request, destination any) error {
	decoder := json.NewDecoder(io.LimitReader(r.Body, 64<<10))
	decoder.DisallowUnknownFields()
	return decoder.Decode(destination)
}

func requestIsHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Proto"), ",")[0]), "https")
}

func settingsErrorMessage(err error) string {
	switch err.Error() {
	case "current password is invalid":
		return "Das aktuelle Passwort ist nicht korrekt."
	case "new password must contain between 6 and 128 characters":
		return "Das neue Passwort muss mindestens 6 Zeichen lang sein."
	default:
		return "Settings konnten nicht gespeichert werden."
	}
}

func logSettingsError(context string, err error) {
	log.Printf("%s: %v", context, err)
}
