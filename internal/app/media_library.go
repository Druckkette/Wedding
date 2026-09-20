package app

import (
	"archive/zip"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	defaultGalleryName   = "Gäste-Uploads"
	challengeGalleryName = "Fotochallenge"
)

var ignoredGalleryNames = map[string]bool{
	"@eadir": true, "#recycle": true, "thumbs": true, "thumbnails": true,
	"previews": true, "cache": true, "originals": true,
}

var supportedExtensions = map[string]string{
	".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".png": "image/png",
	".gif": "image/gif", ".webp": "image/webp", ".heic": "image/heic",
	".heif": "image/heif", ".avif": "image/avif", ".mp4": "video/mp4",
	".mov": "video/quicktime", ".m4v": "video/x-m4v", ".3gp": "video/3gpp",
	".3gpp": "video/3gpp", ".ogv": "video/ogg", ".avi": "video/x-msvideo",
	".webm": "video/webm", ".mkv": "video/x-matroska", ".mpeg": "video/mpeg",
	".mpg": "video/mpeg",
}

type galleryInfo struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

type cachedImageMetadata struct {
	Size            int64
	ModifiedNS      int64
	CapturedAt      string
	Latitude        *float64
	Longitude       *float64
	Width           int
	Height          int
	capturePriority int
}

func isSupportedMediaName(name string) bool {
	_, ok := supportedExtensions[strings.ToLower(filepath.Ext(name))]
	return ok
}

func mediaContentType(name string) string {
	if value := supportedExtensions[strings.ToLower(filepath.Ext(name))]; value != "" {
		return value
	}
	return mime.TypeByExtension(filepath.Ext(name))
}

func validGalleryName(name string) bool {
	name = strings.TrimSpace(name)
	return name != "" && name != "." && name != ".." && filepath.Base(name) == name &&
		!strings.HasPrefix(name, ".") && !ignoredGalleryNames[strings.ToLower(name)]
}

func (s *Server) galleryList() ([]galleryInfo, error) {
	entries, err := os.ReadDir(s.cfg.UploadDir)
	if err != nil {
		return nil, err
	}
	galleries := make([]galleryInfo, 0)
	legacyCount := 0
	for _, entry := range entries {
		if entry.Type().IsRegular() && !strings.HasPrefix(entry.Name(), ".") && isSupportedMediaName(entry.Name()) {
			legacyCount++
		}
	}
	for _, entry := range entries {
		if !entry.IsDir() || !validGalleryName(entry.Name()) {
			continue
		}
		children, err := os.ReadDir(filepath.Join(s.cfg.UploadDir, entry.Name()))
		if err != nil {
			continue
		}
		count := 0
		for _, child := range children {
			if child.Type().IsRegular() && !strings.HasPrefix(child.Name(), ".") && isSupportedMediaName(child.Name()) {
				count++
			}
		}
		galleries = append(galleries, galleryInfo{Name: entry.Name(), Count: count})
	}
	if legacyCount > 0 {
		found := false
		for index := range galleries {
			if galleries[index].Name == defaultGalleryName {
				galleries[index].Count += legacyCount
				found = true
			}
		}
		if !found {
			if err := os.MkdirAll(filepath.Join(s.cfg.UploadDir, defaultGalleryName), 0o750); err != nil {
				return nil, err
			}
			galleries = append(galleries, galleryInfo{Name: defaultGalleryName, Count: legacyCount})
		}
	}
	if len(galleries) == 0 {
		path := filepath.Join(s.cfg.UploadDir, defaultGalleryName)
		if err := os.MkdirAll(path, 0o750); err != nil {
			return nil, err
		}
		galleries = append(galleries, galleryInfo{Name: defaultGalleryName})
	}
	sort.Slice(galleries, func(i, j int) bool {
		return strings.ToLower(galleries[i].Name) < strings.ToLower(galleries[j].Name)
	})
	return galleries, nil
}

func (s *Server) resolveGallery(name string) (string, string, error) {
	if !validGalleryName(name) {
		return "", "", os.ErrNotExist
	}
	root, err := filepath.Abs(s.cfg.UploadDir)
	if err != nil {
		return "", "", err
	}
	path, err := filepath.Abs(filepath.Join(root, name))
	if err != nil || filepath.Dir(path) != root {
		return "", "", os.ErrNotExist
	}
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return "", "", os.ErrNotExist
	}
	return path, name, nil
}

func (s *Server) resolveMediaPath(relative string) (string, error) {
	relative = filepath.FromSlash(relative)
	if relative == "" || filepath.IsAbs(relative) || filepath.Clean(relative) != relative {
		return "", os.ErrNotExist
	}
	parts := strings.Split(filepath.ToSlash(relative), "/")
	if len(parts) != 2 || !validGalleryName(parts[0]) || filepath.Base(parts[1]) != parts[1] || !isSupportedMediaName(parts[1]) {
		return "", os.ErrNotExist
	}
	galleryPath, _, err := s.resolveGallery(parts[0])
	if err != nil {
		return "", err
	}
	path := filepath.Join(galleryPath, parts[1])
	info, err := os.Stat(path)
	if (err != nil || !info.Mode().IsRegular()) && parts[0] == defaultGalleryName {
		path = filepath.Join(s.cfg.UploadDir, parts[1])
		info, err = os.Stat(path)
	}
	if err != nil || !info.Mode().IsRegular() {
		return "", os.ErrNotExist
	}
	return path, nil
}

func sanitizeOriginalFilename(name, detectedExtension string) string {
	name = strings.TrimSpace(filepath.Base(strings.ReplaceAll(name, "\\", "/")))
	base := strings.TrimSuffix(name, filepath.Ext(name))
	base = strings.Map(func(r rune) rune {
		if r < 32 || r == '/' || r == '\\' || r == ':' {
			return '-'
		}
		return r
	}, base)
	base = strings.Trim(base, " .")
	if base == "" {
		base = "Foto"
	}
	if len([]rune(base)) > 180 {
		base = string([]rune(base)[:180])
	}
	return base + detectedExtension
}

func preferredOriginalExtension(name, detected string) string {
	ext := strings.ToLower(filepath.Ext(filepath.Base(name)))
	if _, supported := supportedExtensions[ext]; supported {
		sameJPEG := (detected == ".jpg" && (ext == ".jpg" || ext == ".jpeg"))
		sameMPEG := (detected == ".mpeg" && (ext == ".mpeg" || ext == ".mpg"))
		if ext == detected || sameJPEG || sameMPEG {
			return ext
		}
	}
	return detected
}

func availableFilename(dir, requested string) (string, string, error) {
	base := strings.TrimSuffix(requested, filepath.Ext(requested))
	ext := filepath.Ext(requested)
	for index := 1; index < 10000; index++ {
		name := requested
		if index > 1 {
			name = fmt.Sprintf("%s (%d)%s", base, index, ext)
		}
		path := filepath.Join(dir, name)
		_, err := os.Stat(path)
		if errors.Is(err, os.ErrNotExist) {
			return name, path, nil
		}
		if err != nil {
			return "", "", err
		}
	}
	return "", "", errors.New("too many files with the same name")
}

func encodeMediaID(relative string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(filepath.ToSlash(relative)))
}

func decodeMediaID(id string) (string, error) {
	value, err := base64.RawURLEncoding.DecodeString(id)
	if err != nil {
		return "", os.ErrNotExist
	}
	return string(value), nil
}

func (s *Server) scanMedia(gallery string) ([]publicMedia, error) {
	return s.scanMediaWithVisibility(gallery, false)
}

func (s *Server) scanMediaWithVisibility(gallery string, includeHidden bool) ([]publicMedia, error) {
	galleries, err := s.galleryList()
	if err != nil {
		return nil, err
	}
	metadataRows, err := s.readMetadata()
	if err != nil {
		return nil, err
	}
	metadata := make(map[string]uploadMetadata, len(metadataRows))
	for _, item := range metadataRows {
		metadata[filepath.ToSlash(item.StoredName)] = item
	}
	items := make([]publicMedia, 0)
	for _, current := range galleries {
		if gallery != "" && current.Name != gallery {
			continue
		}
		dir, _, err := s.resolveGallery(current.Name)
		if err != nil {
			continue
		}
		sources := []struct {
			dir    string
			legacy bool
		}{{dir: dir}}
		if current.Name == defaultGalleryName {
			sources = append(sources, struct {
				dir    string
				legacy bool
			}{dir: s.cfg.UploadDir, legacy: true})
		}
		seenNames := make(map[string]bool)
		for _, source := range sources {
			entries, err := os.ReadDir(source.dir)
			if err != nil {
				continue
			}
			for _, entry := range entries {
				if !entry.Type().IsRegular() || strings.HasPrefix(entry.Name(), ".") || !isSupportedMediaName(entry.Name()) {
					continue
				}
				if seenNames[entry.Name()] {
					continue
				}
				seenNames[entry.Name()] = true
				relative := filepath.ToSlash(filepath.Join(current.Name, entry.Name()))
				metadataKey := relative
				if source.legacy {
					metadataKey = entry.Name()
				}
				if !includeHidden && !s.settings.isPublic(metadataKey) {
					continue
				}
				info, err := entry.Info()
				if err != nil {
					continue
				}
				row := metadata[metadataKey]
				if row.StoredName == "" {
					row = metadata[relative]
				}
				contentType := row.ContentType
				if contentType == "" {
					contentType = mediaContentType(entry.Name())
				}
				uploaded := row.UploadedAt
				if uploaded == "" {
					uploaded = info.ModTime().UTC().Format(time.RFC3339Nano)
				}
				captured := cachedImageMetadata{}
				if strings.HasPrefix(contentType, "image/") {
					captured = s.imageMetadata(relative, filepath.Join(source.dir, entry.Name()), info)
				}
				id := encodeMediaID(relative)
				item := publicMedia{
					ID: id, URL: "/m/" + url.PathEscape(s.cfg.UploadToken) + "/" + id,
					DownloadURL: "/api/download?id=" + url.QueryEscape(id), Kind: "video",
					ContentType: contentType, Size: info.Size(), UploadedAt: uploaded,
					Gallery: current.Name, Filename: entry.Name(), CapturedAt: captured.CapturedAt,
					Latitude: captured.Latitude, Longitude: captured.Longitude, Width: captured.Width, Height: captured.Height,
					IsChallenge: row.IsChallenge, Challenge: row.Challenge, ChallengeBy: row.ChallengeBy,
				}
				if strings.HasPrefix(contentType, "image/") {
					item.Kind = "image"
				}
				if item.IsChallenge {
					item.GuestName = row.GuestName
				}
				items = append(items, item)
			}
		}
	}
	return items, nil
}

func (s *Server) storageKey(relative string) string {
	parts := strings.Split(filepath.ToSlash(relative), "/")
	if len(parts) == 2 && parts[0] == defaultGalleryName {
		if info, err := os.Stat(filepath.Join(s.cfg.UploadDir, parts[1])); err == nil && info.Mode().IsRegular() {
			if _, err := os.Stat(filepath.Join(s.cfg.UploadDir, defaultGalleryName, parts[1])); errors.Is(err, os.ErrNotExist) {
				return parts[1]
			}
		}
	}
	return filepath.ToSlash(relative)
}

func (s *Server) imageMetadata(relative, path string, info os.FileInfo) cachedImageMetadata {
	s.imageMetaMu.Lock()
	defer s.imageMetaMu.Unlock()
	if cached, ok := s.imageMetaCache[relative]; ok && cached.Size == info.Size() && cached.ModifiedNS == info.ModTime().UnixNano() {
		return cached
	}
	metadata := readImageMetadata(path)
	metadata.Size = info.Size()
	metadata.ModifiedNS = info.ModTime().UnixNano()
	s.imageMetaCache[relative] = metadata
	return metadata
}

func (s *Server) handleGalleryList(w http.ResponseWriter, r *http.Request) {
	if !s.validToken(r.Header.Get("X-Upload-Token")) {
		writeJSONError(w, http.StatusNotFound, "Galerie-Link ungültig.")
		return
	}
	galleries, err := s.galleryList()
	if err != nil {
		writeJSONError(w, http.StatusServiceUnavailable, "Galerien konnten nicht geladen werden.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"galleries": galleries})
}

func (s *Server) handleOriginalDownload(w http.ResponseWriter, r *http.Request) {
	if !s.validToken(r.Header.Get("X-Upload-Token")) {
		writeJSONError(w, http.StatusNotFound, "Download-Link ungültig.")
		return
	}
	relative, err := decodeMediaID(r.URL.Query().Get("id"))
	if err != nil || !s.settings.isPublic(s.storageKey(relative)) {
		http.NotFound(w, r)
		return
	}
	path, err := s.resolveMediaPath(relative)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	filename := filepath.Base(path)
	w.Header().Set("Content-Disposition", contentDisposition("attachment", filename))
	w.Header().Set("Cache-Control", "private, no-store")
	http.ServeFile(w, r, path)
}

func (s *Server) handleZIPDownload(w http.ResponseWriter, r *http.Request) {
	if !s.validToken(r.Header.Get("X-Upload-Token")) || !sameOrigin(r) {
		writeJSONError(w, http.StatusNotFound, "Download-Link ungültig.")
		return
	}
	var request struct {
		Gallery string   `json:"gallery"`
		IDs     []string `json:"ids"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&request); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Auswahl konnte nicht gelesen werden.")
		return
	}
	all, err := s.scanMedia(request.Gallery)
	if err != nil {
		writeJSONError(w, http.StatusServiceUnavailable, "Galerie ist gerade nicht erreichbar.")
		return
	}
	selected := make(map[string]bool, len(request.IDs))
	for _, id := range request.IDs {
		selected[id] = true
	}
	wanted := make([]publicMedia, 0, len(all))
	for _, item := range all {
		if item.Kind == "image" && (len(selected) == 0 || selected[item.ID]) {
			wanted = append(wanted, item)
		}
	}
	if len(wanted) == 0 {
		writeJSONError(w, http.StatusBadRequest, "Keine Fotos ausgewählt.")
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", contentDisposition("attachment", safeArchiveName(request.Gallery)+".zip"))
	zw := zip.NewWriter(w)
	defer zw.Close()
	for _, item := range wanted {
		relative, _ := decodeMediaID(item.ID)
		path, err := s.resolveMediaPath(relative)
		if err != nil {
			continue
		}
		file, err := os.Open(path)
		if err != nil {
			continue
		}
		entry, err := zw.Create(filepath.Base(path))
		if err == nil {
			_, err = io.Copy(entry, file)
		}
		file.Close()
		if err != nil {
			return
		}
	}
}

func contentDisposition(kind, filename string) string {
	filename = strings.ReplaceAll(strings.ReplaceAll(filename, "\\", "_"), "\"", "'")
	return fmt.Sprintf("%s; filename=\"%s\"; filename*=UTF-8''%s", kind, filename, url.PathEscape(filename))
}

func safeArchiveName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "Hochzeitsfotos"
	}
	name = strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r < 32 {
			return '-'
		}
		return r
	}, name)
	return name
}
