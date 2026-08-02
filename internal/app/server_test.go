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

func uploadRequest(t *testing.T, filename string, content []byte, token string) *http.Request {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("guest_name", "  Anna   & Ben  "); err != nil {
		t.Fatal(err)
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
