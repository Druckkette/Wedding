package app

import (
	"bytes"
	"encoding/json"
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
			files, err := os.ReadDir(cfg.UploadDir)
			if err != nil {
				t.Fatal(err)
			}
			if len(files) != 0 {
				t.Fatalf("rejected challenge left %d file(s)", len(files))
			}
		})
	}
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
