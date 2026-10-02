package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	faceIndexDirectory = ".face-index"
	faceIndexMaxBytes  = 64 << 20
	faceSelfieMaxBytes = 8 << 20
)

type faceIndex struct {
	Version     int                 `json:"version"`
	GeneratedAt string              `json:"generated_at"`
	Model       map[string]any      `json:"model,omitempty"`
	Stats       faceIndexStats      `json:"stats"`
	People      []faceIndexPerson   `json:"people"`
	Media       map[string][]string `json:"media"`
}

type faceIndexStats struct {
	Images       int `json:"images"`
	Faces        int `json:"faces"`
	People       int `json:"people"`
	FailedImages int `json:"failed_images,omitempty"`
}

type faceIndexPerson struct {
	ID          string `json:"id"`
	Name        string `json:"name,omitempty"`
	FaceCount   int    `json:"face_count"`
	PhotoCount  int    `json:"photo_count"`
	SampleMedia string `json:"sample_media,omitempty"`
}

type faceNamesFile struct {
	Version int               `json:"version"`
	Names   map[string]string `json:"names"`
}

type faceServiceResult struct {
	Found      bool    `json:"found"`
	PersonID   string  `json:"person_id"`
	Similarity float64 `json:"similarity"`
	Detail     string  `json:"detail"`
}

func (s *Server) faceIndexPath(name string) string {
	return filepath.Join(s.cfg.UploadDir, faceIndexDirectory, name)
}

func fileVersion(path string) (int64, int64) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, 0
	}
	return info.ModTime().UnixNano(), info.Size()
}

func (s *Server) loadFaceIndex() (*faceIndex, error) {
	indexPath := s.faceIndexPath("index.json")
	namesPath := s.faceIndexPath("names.json")
	indexMod, indexSize := fileVersion(indexPath)
	if indexMod == 0 {
		return nil, os.ErrNotExist
	}
	namesMod, namesSize := fileVersion(namesPath)

	s.faceIndexMu.Lock()
	defer s.faceIndexMu.Unlock()
	if s.faceIndexCache != nil &&
		s.faceIndexModNS == indexMod &&
		s.faceIndexSize == indexSize &&
		s.faceNamesModNS == namesMod &&
		s.faceNamesSize == namesSize {
		return s.faceIndexCache, nil
	}

	file, err := os.Open(indexPath)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var index faceIndex
	decoder := json.NewDecoder(io.LimitReader(file, faceIndexMaxBytes))
	if err := decoder.Decode(&index); err != nil {
		return nil, fmt.Errorf("decode face index: %w", err)
	}
	if index.Version != 1 || index.Media == nil {
		return nil, errors.New("unsupported face index")
	}

	names := faceNamesFile{Names: map[string]string{}}
	if namesFile, err := os.Open(namesPath); err == nil {
		_ = json.NewDecoder(io.LimitReader(namesFile, 1<<20)).Decode(&names)
		_ = namesFile.Close()
	}
	if names.Names == nil {
		names.Names = map[string]string{}
	}
	for i := range index.People {
		index.People[i].Name = cleanText(names.Names[index.People[i].ID], 80)
	}

	s.faceIndexCache = &index
	s.faceIndexModNS = indexMod
	s.faceIndexSize = indexSize
	s.faceNamesModNS = namesMod
	s.faceNamesSize = namesSize
	return s.faceIndexCache, nil
}

func (index *faceIndex) hasPerson(id string) bool {
	for _, person := range index.People {
		if person.ID == id {
			return true
		}
	}
	return false
}

func (index *faceIndex) person(id string) (faceIndexPerson, bool) {
	for _, person := range index.People {
		if person.ID == id {
			return person, true
		}
	}
	return faceIndexPerson{}, false
}

func (index *faceIndex) containsPerson(relative, personID string) bool {
	for _, id := range index.Media[filepath.ToSlash(relative)] {
		if id == personID {
			return true
		}
	}
	return false
}

func validFacePersonID(value string) bool {
	if len(value) < 2 || len(value) > 32 || value[0] != 'p' {
		return false
	}
	for _, char := range value[1:] {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}

func (s *Server) filterMediaByFaceQuery(w http.ResponseWriter, r *http.Request, items []publicMedia) ([]publicMedia, bool) {
	personID := strings.TrimSpace(r.URL.Query().Get("person"))
	if personID == "" {
		return items, true
	}
	if !validFacePersonID(personID) {
		writeJSONError(w, http.StatusBadRequest, "Ungültiger Personenfilter.")
		return nil, false
	}
	index, err := s.loadFaceIndex()
	if errors.Is(err, os.ErrNotExist) {
		writeJSONError(w, http.StatusServiceUnavailable, "Die Gesichtssuche ist noch nicht eingerichtet.")
		return nil, false
	}
	if err != nil {
		writeJSONError(w, http.StatusServiceUnavailable, "Der Gesichtsindex konnte nicht geladen werden.")
		return nil, false
	}
	if !index.hasPerson(personID) {
		writeJSONError(w, http.StatusNotFound, "Person wurde im Gesichtsindex nicht gefunden.")
		return nil, false
	}

	filtered := make([]publicMedia, 0, len(items))
	for _, item := range items {
		relative, err := decodeMediaID(item.ID)
		if err == nil && index.containsPerson(relative, personID) {
			filtered = append(filtered, item)
		}
	}
	return filtered, true
}

func (s *Server) handleFacePeople(w http.ResponseWriter, r *http.Request) {
	if !s.validToken(r.Header.Get("X-Upload-Token")) {
		writeJSONError(w, http.StatusNotFound, "Galerie-Link ungültig.")
		return
	}
	index, err := s.loadFaceIndex()
	if errors.Is(err, os.ErrNotExist) {
		writeJSON(w, http.StatusOK, map[string]any{"available": false, "people": []any{}})
		return
	}
	if err != nil {
		writeJSONError(w, http.StatusServiceUnavailable, "Der Gesichtsindex konnte nicht geladen werden.")
		return
	}

	type guestPerson struct {
		ID         string `json:"id"`
		Name       string `json:"name"`
		PhotoCount int    `json:"photo_count"`
	}
	people := make([]guestPerson, 0)
	for _, person := range index.People {
		if strings.TrimSpace(person.Name) != "" {
			people = append(people, guestPerson{ID: person.ID, Name: person.Name, PhotoCount: person.PhotoCount})
		}
	}
	sort.Slice(people, func(i, j int) bool {
		return strings.ToLower(people[i].Name) < strings.ToLower(people[j].Name)
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"available":    true,
		"generated_at": index.GeneratedAt,
		"people":       people,
	})
}

func (s *Server) handleFaceSearch(w http.ResponseWriter, r *http.Request) {
	if !s.validToken(r.Header.Get("X-Upload-Token")) {
		writeJSONError(w, http.StatusNotFound, "Galerie-Link ungültig.")
		return
	}
	if !sameOrigin(r) {
		writeJSONError(w, http.StatusForbidden, "Anfrage nicht erlaubt.")
		return
	}
	if !s.faceLimiter.Allow(clientIP(r), s.now()) {
		w.Header().Set("Retry-After", "900")
		writeJSONError(w, http.StatusTooManyRequests, "Zu viele Gesichtssuchen. Bitte versucht es später erneut.")
		return
	}
	if strings.TrimSpace(s.cfg.FaceServiceURL) == "" {
		writeJSONError(w, http.StatusServiceUnavailable, "Die Selfie-Suche ist derzeit nicht aktiviert.")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, faceSelfieMaxBytes+(1<<20))
	if err := r.ParseMultipartForm(faceSelfieMaxBytes); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Das Selfie konnte nicht gelesen werden.")
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "Bitte ein Selfie auswählen.")
		return
	}
	defer file.Close()
	contentType := strings.ToLower(strings.TrimSpace(header.Header.Get("Content-Type")))
	if !strings.HasPrefix(contentType, "image/") {
		writeJSONError(w, http.StatusUnsupportedMediaType, "Bitte eine Bilddatei verwenden.")
		return
	}

	var payload bytes.Buffer
	writer := multipart.NewWriter(&payload)
	part, err := writer.CreateFormFile("file", filepath.Base(header.Filename))
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "Selfie-Suche fehlgeschlagen.")
		return
	}
	written, err := io.Copy(part, io.LimitReader(file, faceSelfieMaxBytes+1))
	if err != nil || written > faceSelfieMaxBytes {
		writeJSONError(w, http.StatusRequestEntityTooLarge, "Das Selfie ist zu groß.")
		return
	}
	if err := writer.Close(); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "Selfie-Suche fehlgeschlagen.")
		return
	}

	endpoint := strings.TrimRight(s.cfg.FaceServiceURL, "/") + "/search"
	request, err := http.NewRequestWithContext(r.Context(), http.MethodPost, endpoint, &payload)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "Selfie-Suche fehlgeschlagen.")
		return
	}
	request.Header.Set("Content-Type", writer.FormDataContentType())
	client := &http.Client{Timeout: 30 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		writeJSONError(w, http.StatusServiceUnavailable, "Die Selfie-Suche ist gerade nicht erreichbar.")
		return
	}
	defer response.Body.Close()

	var result faceServiceResult
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&result); err != nil {
		writeJSONError(w, http.StatusServiceUnavailable, "Die Selfie-Suche hat ungültig geantwortet.")
		return
	}
	if response.StatusCode != http.StatusOK {
		message := cleanText(result.Detail, 180)
		if message == "" {
			message = "Das Selfie konnte nicht ausgewertet werden."
		}
		writeJSONError(w, response.StatusCode, message)
		return
	}
	if !result.Found {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":         true,
			"found":      false,
			"similarity": result.Similarity,
		})
		return
	}
	if !validFacePersonID(result.PersonID) {
		writeJSONError(w, http.StatusServiceUnavailable, "Die Selfie-Suche hat einen ungültigen Treffer geliefert.")
		return
	}

	index, err := s.loadFaceIndex()
	if err != nil || !index.hasPerson(result.PersonID) {
		writeJSONError(w, http.StatusServiceUnavailable, "Der Treffer ist im aktuellen Gesichtsindex nicht verfügbar.")
		return
	}
	person, _ := index.person(result.PersonID)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":          true,
		"found":       true,
		"person_id":   result.PersonID,
		"name":        person.Name,
		"photo_count": person.PhotoCount,
		"similarity":  result.Similarity,
	})
}

func (s *Server) handleFaceAdmin(w http.ResponseWriter, r *http.Request) {
	if !s.requireSettingsSession(w, r) {
		return
	}
	index, err := s.loadFaceIndex()
	if errors.Is(err, os.ErrNotExist) {
		writeJSON(w, http.StatusOK, map[string]any{"available": false})
		return
	}
	if err != nil {
		writeJSONError(w, http.StatusServiceUnavailable, "Der Gesichtsindex konnte nicht geladen werden.")
		return
	}
	people := append([]faceIndexPerson(nil), index.People...)
	sort.Slice(people, func(i, j int) bool {
		if people[i].PhotoCount == people[j].PhotoCount {
			return people[i].ID < people[j].ID
		}
		return people[i].PhotoCount > people[j].PhotoCount
	})
	type adminPerson struct {
		faceIndexPerson
		ThumbnailURL string `json:"thumbnail_url"`
	}
	adminPeople := make([]adminPerson, 0, len(people))
	for _, person := range people {
		adminPeople = append(adminPeople, adminPerson{
			faceIndexPerson: person,
			ThumbnailURL:    "/api/faces/admin/thumb?id=" + person.ID,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"available":    true,
		"generated_at": index.GeneratedAt,
		"stats":        index.Stats,
		"people":       adminPeople,
	})
}

func (s *Server) handleFaceAdminName(w http.ResponseWriter, r *http.Request) {
	if !s.requireSettingsSession(w, r) {
		return
	}
	if !sameOrigin(r) {
		writeJSONError(w, http.StatusForbidden, "Anfrage nicht erlaubt.")
		return
	}
	var update struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := decodeJSON(r, &update); err != nil || !validFacePersonID(update.ID) {
		writeJSONError(w, http.StatusBadRequest, "Ungültige Person.")
		return
	}
	index, err := s.loadFaceIndex()
	if err != nil || !index.hasPerson(update.ID) {
		writeJSONError(w, http.StatusNotFound, "Person wurde im Gesichtsindex nicht gefunden.")
		return
	}
	if err := s.saveFaceName(update.ID, cleanText(update.Name, 80)); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "Name konnte nicht gespeichert werden.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) saveFaceName(id, name string) error {
	s.faceIndexMu.Lock()
	defer s.faceIndexMu.Unlock()

	path := s.faceIndexPath("names.json")
	names := faceNamesFile{Version: 1, Names: map[string]string{}}
	if file, err := os.Open(path); err == nil {
		_ = json.NewDecoder(io.LimitReader(file, 1<<20)).Decode(&names)
		_ = file.Close()
	}
	if names.Names == nil {
		names.Names = map[string]string{}
	}
	if name == "" {
		delete(names.Names, id)
	} else {
		names.Names[id] = name
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".face-names-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	encoder := json.NewEncoder(temp)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(names); err != nil {
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
	if err := os.Rename(tempPath, path); err != nil {
		return err
	}
	s.faceIndexCache = nil
	s.faceNamesModNS = 0
	s.faceNamesSize = 0
	return nil
}

func (s *Server) handleFaceAdminThumb(w http.ResponseWriter, r *http.Request) {
	if !s.requireSettingsSession(w, r) {
		return
	}
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if !validFacePersonID(id) {
		http.NotFound(w, r)
		return
	}
	index, err := s.loadFaceIndex()
	if err != nil || !index.hasPerson(id) {
		http.NotFound(w, r)
		return
	}
	path := s.faceIndexPath(filepath.Join("thumbs", id+".jpg"))
	root := filepath.Clean(s.faceIndexPath("thumbs"))
	clean := filepath.Clean(path)
	if filepath.Dir(clean) != root {
		http.NotFound(w, r)
		return
	}
	if info, err := os.Stat(clean); err != nil || !info.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Type", "image/jpeg")
	http.ServeFile(w, r, clean)
}
