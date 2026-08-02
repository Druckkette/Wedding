package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testToken = "0123456789abcdef0123456789abcdef"

func testConfig(t *testing.T) Config {
	t.Helper()
	return Config{
		ListenAddr:        ":0",
		UploadDir:         t.TempDir(),
		UploadToken:       testToken,
		EventTitle:        "Test Hochzeit",
		EventSubtitle:     "Test subtitle",
		SettingsPassword:  "test-settings-password",
		MaxUploadsPerHour: 10,
		ReadHeaderTimeout: time.Second,
		ReadTimeout:       time.Minute,
		WriteTimeout:      time.Minute,
		IdleTimeout:       time.Minute,
	}
}

func TestUploadPageRequiresToken(t *testing.T) {
	handler, err := New(testConfig(t))
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		path string
		want int
	}{
		{"/", http.StatusNotFound},
		{"/u/wrong", http.StatusNotFound},
		{"/u/" + testToken, http.StatusOK},
		{"/u/" + testToken + "/gallery", http.StatusOK},
		{"/u/" + testToken + "/slideshow", http.StatusOK},
		{"/u/" + testToken + "/settings", http.StatusOK},
		{"/u/" + testToken + "/unknown", http.StatusNotFound},
	} {
		req := httptest.NewRequest(http.MethodGet, test.path, nil)
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != test.want {
			t.Errorf("%s: got %d, want %d", test.path, res.Code, test.want)
		}
	}
}

func TestJPEGUploadIsStoredWithoutModification(t *testing.T) {
	cfg := testConfig(t)
	handler, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	jpeg := append([]byte{0xff, 0xd8, 0xff, 0xe0, 0x00, 0x10, 'J', 'F', 'I', 'F', 0x00}, bytes.Repeat([]byte{0x42}, 700)...)

	request := uploadRequest(t, "Urlaub 2026.jpeg", jpeg, testToken)
	request.Header.Set("Origin", "https://example.test")
	request.Host = "example.test"
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, request)
	if res.Code != http.StatusCreated {
		t.Fatalf("got %d: %s", res.Code, res.Body.String())
	}

	files, err := os.ReadDir(cfg.UploadDir)
	if err != nil {
		t.Fatal(err)
	}
	var imagePath string
	for _, file := range files {
		if strings.HasSuffix(file.Name(), ".jpg") {
			imagePath = filepath.Join(cfg.UploadDir, file.Name())
		}
	}
	if imagePath == "" {
		t.Fatal("stored image not found")
	}
	stored, err := os.ReadFile(imagePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stored, jpeg) {
		t.Fatal("stored bytes differ from upload")
	}

	metadataFile, err := os.Open(filepath.Join(cfg.UploadDir, "uploads.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer metadataFile.Close()
	var metadata uploadMetadata
	if err := json.NewDecoder(metadataFile).Decode(&metadata); err != nil {
		t.Fatal(err)
	}
	if metadata.OriginalName != "Urlaub 2026.jpeg" || metadata.GuestName != "Anna & Ben" {
		t.Fatalf("unexpected metadata: %+v", metadata)
	}
}

func TestUploadRejectsWrongTokenAndNonImage(t *testing.T) {
	handler, err := New(testConfig(t))
	if err != nil {
		t.Fatal(err)
	}

	wrongToken := uploadRequest(t, "photo.jpg", []byte("not an image"), "wrong")
	wrongRes := httptest.NewRecorder()
	handler.ServeHTTP(wrongRes, wrongToken)
	if wrongRes.Code != http.StatusNotFound {
		t.Fatalf("wrong token: got %d", wrongRes.Code)
	}

	nonImage := uploadRequest(t, "photo.jpg", []byte("not an image"), testToken)
	nonImageRes := httptest.NewRecorder()
	handler.ServeHTTP(nonImageRes, nonImage)
	if nonImageRes.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("non-image: got %d: %s", nonImageRes.Code, nonImageRes.Body.String())
	}
}

func TestHEICDetection(t *testing.T) {
	header := append([]byte{0, 0, 0, 24}, []byte("ftypheic0000mif1")...)
	mime, ext, ok := detectMedia(header, "IMG_1000.HEIC")
	if !ok || mime != "image/heic" || ext != ".heic" {
		t.Fatalf("got %q %q %t", mime, ext, ok)
	}
}

func TestMP4VideoUploadIsStoredWithoutModification(t *testing.T) {
	cfg := testConfig(t)
	handler, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	mp4 := append([]byte{0, 0, 0, 24}, []byte("ftypmp420000mp42isom")...)
	mp4 = append(mp4, bytes.Repeat([]byte{0x7a}, 2048)...)

	request := uploadRequest(t, "Hochzeitstanz.mp4", mp4, testToken)
	request.Header.Set("Origin", "https://example.test")
	request.Host = "example.test"
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, request)
	if res.Code != http.StatusCreated {
		t.Fatalf("got %d: %s", res.Code, res.Body.String())
	}

	files, err := os.ReadDir(cfg.UploadDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if !strings.HasSuffix(file.Name(), ".mp4") {
			continue
		}
		stored, err := os.ReadFile(filepath.Join(cfg.UploadDir, file.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(stored, mp4) {
			t.Fatal("stored video bytes differ from upload")
		}
		return
	}
	t.Fatal("stored video not found")
}

func TestChallengeAppearsInGalleryAndMediaIsProtected(t *testing.T) {
	cfg := testConfig(t)
	handler, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	jpeg := append([]byte{0xff, 0xd8, 0xff, 0xe0, 0x00, 0x10, 'J', 'F', 'I', 'F', 0x00}, bytes.Repeat([]byte{0x51}, 700)...)
	request := uploadRequestWithFields(t, "challenge.jpeg", jpeg, testToken, map[string]string{
		"is_challenge": "true",
		"challenge":    "Tanzt mit dem Brautpaar",
		"challenge_by": "Mia & Tom",
	})
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, request)
	if res.Code != http.StatusCreated {
		t.Fatalf("upload: got %d: %s", res.Code, res.Body.String())
	}

	listReq := httptest.NewRequest(http.MethodGet, "/api/media", nil)
	listReq.Header.Set("X-Upload-Token", testToken)
	listRes := httptest.NewRecorder()
	handler.ServeHTTP(listRes, listReq)
	if listRes.Code != http.StatusOK {
		t.Fatalf("gallery: got %d: %s", listRes.Code, listRes.Body.String())
	}
	responseBody := listRes.Body.Bytes()
	var payload struct {
		Items []publicMedia `json:"items"`
	}
	if err := json.NewDecoder(bytes.NewReader(responseBody)).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Items) != 1 {
		t.Fatalf("got %d gallery items", len(payload.Items))
	}
	item := payload.Items[0]
	if !item.IsChallenge || item.Challenge != "Tanzt mit dem Brautpaar" || item.ChallengeBy != "Mia & Tom" {
		t.Fatalf("unexpected challenge: %+v", item)
	}
	if strings.Contains(string(responseBody), "remote_ip") || strings.Contains(string(responseBody), "original_name") {
		t.Fatal("private upload metadata leaked through gallery API")
	}

	mediaReq := httptest.NewRequest(http.MethodGet, item.URL, nil)
	mediaRes := httptest.NewRecorder()
	handler.ServeHTTP(mediaRes, mediaReq)
	if mediaRes.Code != http.StatusOK || !bytes.Equal(mediaRes.Body.Bytes(), jpeg) {
		t.Fatalf("protected media: got %d with %d bytes", mediaRes.Code, mediaRes.Body.Len())
	}
	wrongMediaReq := httptest.NewRequest(http.MethodGet, strings.Replace(item.URL, testToken, "wrong", 1), nil)
	wrongMediaRes := httptest.NewRecorder()
	handler.ServeHTTP(wrongMediaRes, wrongMediaReq)
	if wrongMediaRes.Code != http.StatusNotFound {
		t.Fatalf("wrong media token: got %d", wrongMediaRes.Code)
	}
}

func TestChallengeRejectsVideoAndIncompleteDetails(t *testing.T) {
	for _, test := range []struct {
		name     string
		filename string
		content  []byte
		fields   map[string]string
	}{
		{
			name:     "video",
			filename: "challenge.mp4",
			content:  append(append([]byte{0, 0, 0, 24}, []byte("ftypmp420000mp42isom")...), bytes.Repeat([]byte{0x7a}, 128)...),
			fields:   map[string]string{"is_challenge": "true", "challenge": "Tanzen", "challenge_by": "Mia"},
		},
		{
			name:     "missing participants",
			filename: "challenge.jpg",
			content:  append([]byte{0xff, 0xd8, 0xff, 0xe0, 0x00, 0x10, 'J', 'F', 'I', 'F', 0x00}, bytes.Repeat([]byte{0x51}, 128)...),
			fields:   map[string]string{"is_challenge": "true", "challenge": "Tanzen"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := testConfig(t)
			handler, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			res := httptest.NewRecorder()
			handler.ServeHTTP(res, uploadRequestWithFields(t, test.filename, test.content, testToken, test.fields))
			if res.Code != http.StatusBadRequest {
				t.Fatalf("got %d: %s", res.Code, res.Body.String())
			}
			if count := countStoredMedia(t, cfg.UploadDir); count != 0 {
				t.Fatalf("rejected challenge left %d media file(s)", count)
			}
		})
	}
}

func TestSettingsModerateUploadsAndChangePassword(t *testing.T) {
	cfg := testConfig(t)
	handler, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}

	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, jsonRequest(t, http.MethodGet, "/api/settings", nil, nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("settings without login: got %d", unauthorized.Code)
	}

	wrongLogin := httptest.NewRecorder()
	handler.ServeHTTP(wrongLogin, jsonRequest(t, http.MethodPost, "/api/settings/login", map[string]any{"password": "wrong-password"}, nil))
	if wrongLogin.Code != http.StatusUnauthorized {
		t.Fatalf("wrong login: got %d", wrongLogin.Code)
	}
	cookie := settingsLogin(t, handler, "test-settings-password")

	moderationEnabled := true
	update := jsonRequest(t, http.MethodPost, "/api/settings", map[string]any{
		"moderation_enabled": moderationEnabled,
		"slideshow_style":    "polaroid",
	}, cookie)
	updateRes := httptest.NewRecorder()
	handler.ServeHTTP(updateRes, update)
	if updateRes.Code != http.StatusOK {
		t.Fatalf("settings update: got %d: %s", updateRes.Code, updateRes.Body.String())
	}

	jpeg := append([]byte{0xff, 0xd8, 0xff, 0xe0, 0x00, 0x10, 'J', 'F', 'I', 'F', 0x00}, bytes.Repeat([]byte{0x35}, 700)...)
	uploadRes := httptest.NewRecorder()
	handler.ServeHTTP(uploadRes, uploadRequest(t, "moderated.jpg", jpeg, testToken))
	if uploadRes.Code != http.StatusCreated || !strings.Contains(uploadRes.Body.String(), `"pending":true`) {
		t.Fatalf("moderated upload: got %d: %s", uploadRes.Code, uploadRes.Body.String())
	}

	publicListRes := httptest.NewRecorder()
	handler.ServeHTTP(publicListRes, jsonRequest(t, http.MethodGet, "/api/media", nil, nil))
	var publicPayload struct {
		Items          []publicMedia `json:"items"`
		SlideshowStyle string        `json:"slideshow_style"`
	}
	if err := json.NewDecoder(publicListRes.Body).Decode(&publicPayload); err != nil {
		t.Fatal(err)
	}
	if len(publicPayload.Items) != 0 || publicPayload.SlideshowStyle != "polaroid" {
		t.Fatalf("unexpected public payload: %+v", publicPayload)
	}

	settingsRes := httptest.NewRecorder()
	handler.ServeHTTP(settingsRes, jsonRequest(t, http.MethodGet, "/api/settings", nil, cookie))
	var settingsPayload struct {
		Items []adminMedia `json:"items"`
	}
	if err := json.NewDecoder(settingsRes.Body).Decode(&settingsPayload); err != nil {
		t.Fatal(err)
	}
	if len(settingsPayload.Items) != 1 || settingsPayload.Items[0].Status != "pending" {
		t.Fatalf("unexpected moderation queue: %+v", settingsPayload.Items)
	}
	item := settingsPayload.Items[0]
	publicMediaRes := httptest.NewRecorder()
	handler.ServeHTTP(publicMediaRes, httptest.NewRequest(http.MethodGet, item.URL, nil))
	if publicMediaRes.Code != http.StatusNotFound {
		t.Fatalf("pending media was public: got %d", publicMediaRes.Code)
	}
	adminMediaReq := httptest.NewRequest(http.MethodGet, item.URL, nil)
	adminMediaReq.AddCookie(cookie)
	adminMediaRes := httptest.NewRecorder()
	handler.ServeHTTP(adminMediaRes, adminMediaReq)
	if adminMediaRes.Code != http.StatusOK {
		t.Fatalf("pending media unavailable in settings: got %d", adminMediaRes.Code)
	}

	approveRes := httptest.NewRecorder()
	handler.ServeHTTP(approveRes, jsonRequest(t, http.MethodPost, "/api/settings/media", map[string]any{"id": item.ID, "status": "approved"}, cookie))
	if approveRes.Code != http.StatusOK {
		t.Fatalf("approve media: got %d: %s", approveRes.Code, approveRes.Body.String())
	}
	approvedListRes := httptest.NewRecorder()
	handler.ServeHTTP(approvedListRes, jsonRequest(t, http.MethodGet, "/api/media", nil, nil))
	if err := json.NewDecoder(approvedListRes.Body).Decode(&publicPayload); err != nil {
		t.Fatal(err)
	}
	if len(publicPayload.Items) != 1 {
		t.Fatalf("approved media missing from gallery: %+v", publicPayload.Items)
	}

	unauthorizedDeleteRes := httptest.NewRecorder()
	handler.ServeHTTP(unauthorizedDeleteRes, jsonRequest(t, http.MethodPost, "/api/settings/media/delete", map[string]any{"id": item.ID}, nil))
	if unauthorizedDeleteRes.Code != http.StatusUnauthorized {
		t.Fatalf("delete without login: got %d", unauthorizedDeleteRes.Code)
	}
	deleteRes := httptest.NewRecorder()
	handler.ServeHTTP(deleteRes, jsonRequest(t, http.MethodPost, "/api/settings/media/delete", map[string]any{"id": item.ID}, cookie))
	if deleteRes.Code != http.StatusOK {
		t.Fatalf("delete media: got %d: %s", deleteRes.Code, deleteRes.Body.String())
	}
	if _, err := os.Stat(filepath.Join(cfg.UploadDir, item.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("deleted media still exists: %v", err)
	}
	deletedMediaRes := httptest.NewRecorder()
	handler.ServeHTTP(deletedMediaRes, httptest.NewRequest(http.MethodGet, item.URL, nil))
	if deletedMediaRes.Code != http.StatusNotFound {
		t.Fatalf("deleted media remains available: got %d", deletedMediaRes.Code)
	}
	afterDeleteSettingsRes := httptest.NewRecorder()
	handler.ServeHTTP(afterDeleteSettingsRes, jsonRequest(t, http.MethodGet, "/api/settings", nil, cookie))
	if err := json.NewDecoder(afterDeleteSettingsRes.Body).Decode(&settingsPayload); err != nil {
		t.Fatal(err)
	}
	if len(settingsPayload.Items) != 0 {
		t.Fatalf("deleted media remains in settings: %+v", settingsPayload.Items)
	}
	metadataAfterDelete, err := os.ReadFile(filepath.Join(cfg.UploadDir, "uploads.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(metadataAfterDelete, []byte(item.ID)) {
		t.Fatal("deleted media remains in uploads.jsonl")
	}
	settingsAfterDelete, err := os.ReadFile(filepath.Join(cfg.UploadDir, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var persistedAfterDelete persistedSettings
	if err := json.Unmarshal(settingsAfterDelete, &persistedAfterDelete); err != nil {
		t.Fatal(err)
	}
	if _, exists := persistedAfterDelete.Media[item.ID]; exists {
		t.Fatal("deleted media remains in settings.json")
	}

	passwordRes := httptest.NewRecorder()
	handler.ServeHTTP(passwordRes, jsonRequest(t, http.MethodPost, "/api/settings", map[string]any{
		"current_password": "test-settings-password",
		"new_password":     "new-private-password",
	}, cookie))
	if passwordRes.Code != http.StatusOK {
		t.Fatalf("password update: got %d: %s", passwordRes.Code, passwordRes.Body.String())
	}
	oldLogin := httptest.NewRecorder()
	handler.ServeHTTP(oldLogin, jsonRequest(t, http.MethodPost, "/api/settings/login", map[string]any{"password": "test-settings-password"}, nil))
	if oldLogin.Code != http.StatusUnauthorized {
		t.Fatalf("old password still accepted: got %d", oldLogin.Code)
	}
	_ = settingsLogin(t, handler, "new-private-password")

	settingsFile, err := os.ReadFile(filepath.Join(cfg.UploadDir, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(settingsFile, []byte("test-settings-password")) || bytes.Contains(settingsFile, []byte("new-private-password")) {
		t.Fatal("settings password was stored in plaintext")
	}
}

func settingsLogin(t *testing.T, handler http.Handler, password string) *http.Cookie {
	t.Helper()
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, jsonRequest(t, http.MethodPost, "/api/settings/login", map[string]any{"password": password}, nil))
	if res.Code != http.StatusOK {
		t.Fatalf("settings login: got %d: %s", res.Code, res.Body.String())
	}
	cookies := res.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != settingsSessionCookie {
		t.Fatalf("settings login did not set session cookie: %+v", cookies)
	}
	return cookies[0]
}

func jsonRequest(t *testing.T, method, path string, payload any, cookie *http.Cookie) *http.Request {
	t.Helper()
	var body bytes.Buffer
	if payload != nil {
		if err := json.NewEncoder(&body).Encode(payload); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, &body)
	req.Header.Set("X-Upload-Token", testToken)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	return req
}

func countStoredMedia(t *testing.T, dir string) int {
	t.Helper()
	files, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, file := range files {
		if storedMediaName.MatchString(file.Name()) {
			count++
		}
	}
	return count
}

func uploadRequest(t *testing.T, filename string, content []byte, token string) *http.Request {
	return uploadRequestWithFields(t, filename, content, token, nil)
}

func uploadRequestWithFields(t *testing.T, filename string, content []byte, token string, fields map[string]string) *http.Request {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("guest_name", "  Anna   & Ben  "); err != nil {
		t.Fatal(err)
	}
	for name, value := range fields {
		if err := writer.WriteField(name, value); err != nil {
			t.Fatal(err)
		}
	}
	part, err := writer.CreateFormFile("photo", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/upload", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("X-Upload-Token", token)
	return req
}
