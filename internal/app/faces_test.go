package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestFacePeopleAndMediaFilter(t *testing.T) {
	cfg := testConfig(t)
	galleryPath := filepath.Join(cfg.UploadDir, "Fotograf")
	if err := os.MkdirAll(galleryPath, 0o750); err != nil {
		t.Fatal(err)
	}
	imagePath := filepath.Join(galleryPath, "gruppe.jpg")
	jpeg := []byte{0xff, 0xd8, 0xff, 0xe0, 0x00, 0x10, 'J', 'F', 'I', 'F', 0x00, 0xff, 0xd9}
	if err := os.WriteFile(imagePath, jpeg, 0o640); err != nil {
		t.Fatal(err)
	}

	indexDir := filepath.Join(cfg.UploadDir, faceIndexDirectory)
	if err := os.MkdirAll(filepath.Join(indexDir, "thumbs"), 0o750); err != nil {
		t.Fatal(err)
	}
	index := faceIndex{
		Version:     1,
		GeneratedAt: "2026-10-02T08:00:00Z",
		Stats:       faceIndexStats{Images: 1, Faces: 2, People: 2},
		People: []faceIndexPerson{
			{ID: "p0001", FaceCount: 1, PhotoCount: 1},
			{ID: "p0002", FaceCount: 1, PhotoCount: 1},
		},
		Media: map[string][]string{
			"Fotograf/gruppe.jpg": {"p0001", "p0002"},
		},
	}
	indexData, _ := json.Marshal(index)
	if err := os.WriteFile(filepath.Join(indexDir, "index.json"), indexData, 0o640); err != nil {
		t.Fatal(err)
	}
	namesData, _ := json.Marshal(faceNamesFile{Version: 1, Names: map[string]string{"p0001": "Anna"}})
	if err := os.WriteFile(filepath.Join(indexDir, "names.json"), namesData, 0o640); err != nil {
		t.Fatal(err)
	}

	handler, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}

	peopleReq := httptest.NewRequest(http.MethodGet, "/api/faces/people", nil)
	peopleReq.Header.Set("X-Upload-Token", testToken)
	peopleRes := httptest.NewRecorder()
	handler.ServeHTTP(peopleRes, peopleReq)
	if peopleRes.Code != http.StatusOK {
		t.Fatalf("people status: %d %s", peopleRes.Code, peopleRes.Body.String())
	}
	var peoplePayload struct {
		Available bool              `json:"available"`
		People    []faceIndexPerson `json:"people"`
	}
	if err := json.NewDecoder(peopleRes.Body).Decode(&peoplePayload); err != nil {
		t.Fatal(err)
	}
	if !peoplePayload.Available || len(peoplePayload.People) != 1 || peoplePayload.People[0].Name != "Anna" {
		t.Fatalf("unexpected people payload: %+v", peoplePayload)
	}

	mediaReq := httptest.NewRequest(http.MethodGet, "/api/media?person=p0001", nil)
	mediaReq.Header.Set("X-Upload-Token", testToken)
	mediaRes := httptest.NewRecorder()
	handler.ServeHTTP(mediaRes, mediaReq)
	if mediaRes.Code != http.StatusOK {
		t.Fatalf("media status: %d %s", mediaRes.Code, mediaRes.Body.String())
	}
	var mediaPayload struct {
		Items []publicMedia `json:"items"`
	}
	if err := json.NewDecoder(mediaRes.Body).Decode(&mediaPayload); err != nil {
		t.Fatal(err)
	}
	if len(mediaPayload.Items) != 1 || mediaPayload.Items[0].Filename != "gruppe.jpg" {
		t.Fatalf("unexpected media payload: %+v", mediaPayload.Items)
	}
}

func TestFacePeopleUnavailableWithoutIndex(t *testing.T) {
	handler, err := New(testConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/faces/people", nil)
	req.Header.Set("X-Upload-Token", testToken)
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("got %d: %s", res.Code, res.Body.String())
	}
	var payload struct {
		Available bool `json:"available"`
	}
	if err := json.NewDecoder(res.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.Available {
		t.Fatal("face search unexpectedly available")
	}
}


func TestFacePeopleGroupsSameAssignedName(t *testing.T) {
	cfg := testConfig(t)
	galleryPath := filepath.Join(cfg.UploadDir, "Fotografin")
	if err := os.MkdirAll(galleryPath, 0o750); err != nil {
		t.Fatal(err)
	}
	jpeg := []byte{0xff, 0xd8, 0xff, 0xe0, 0x00, 0x10, 'J', 'F', 'I', 'F', 0x00, 0xff, 0xd9}
	for _, name := range []string{"eins.jpg", "zwei.jpg"} {
		if err := os.WriteFile(filepath.Join(galleryPath, name), jpeg, 0o640); err != nil {
			t.Fatal(err)
		}
	}

	indexDir := filepath.Join(cfg.UploadDir, faceIndexDirectory)
	if err := os.MkdirAll(indexDir, 0o750); err != nil {
		t.Fatal(err)
	}
	index := faceIndex{
		Version:     1,
		GeneratedAt: "2026-10-02T09:00:00Z",
		Stats:       faceIndexStats{Images: 2, Faces: 2, People: 2},
		People: []faceIndexPerson{
			{ID: "p0001", FaceCount: 1, PhotoCount: 1},
			{ID: "p0002", FaceCount: 1, PhotoCount: 1},
		},
		Media: map[string][]string{
			"Fotografin/eins.jpg": {"p0001"},
			"Fotografin/zwei.jpg": {"p0002"},
		},
	}
	indexData, _ := json.Marshal(index)
	if err := os.WriteFile(filepath.Join(indexDir, "index.json"), indexData, 0o640); err != nil {
		t.Fatal(err)
	}
	namesData, _ := json.Marshal(faceNamesFile{
		Version: 1,
		Names: map[string]string{
			"p0001": "Pascal",
			"p0002": "  PASCAL  ",
		},
	})
	if err := os.WriteFile(filepath.Join(indexDir, "names.json"), namesData, 0o640); err != nil {
		t.Fatal(err)
	}

	handler, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}

	peopleReq := httptest.NewRequest(http.MethodGet, "/api/faces/people", nil)
	peopleReq.Header.Set("X-Upload-Token", testToken)
	peopleRes := httptest.NewRecorder()
	handler.ServeHTTP(peopleRes, peopleReq)
	if peopleRes.Code != http.StatusOK {
		t.Fatalf("people status: %d %s", peopleRes.Code, peopleRes.Body.String())
	}
	var peoplePayload struct {
		People []faceIndexPerson `json:"people"`
	}
	if err := json.NewDecoder(peopleRes.Body).Decode(&peoplePayload); err != nil {
		t.Fatal(err)
	}
	if len(peoplePayload.People) != 1 {
		t.Fatalf("expected one grouped person, got %+v", peoplePayload.People)
	}
	if peoplePayload.People[0].Name != "Pascal" || peoplePayload.People[0].PhotoCount != 2 {
		t.Fatalf("unexpected grouped person: %+v", peoplePayload.People[0])
	}

	mediaReq := httptest.NewRequest(http.MethodGet, "/api/media?person=p0001", nil)
	mediaReq.Header.Set("X-Upload-Token", testToken)
	mediaRes := httptest.NewRecorder()
	handler.ServeHTTP(mediaRes, mediaReq)
	if mediaRes.Code != http.StatusOK {
		t.Fatalf("media status: %d %s", mediaRes.Code, mediaRes.Body.String())
	}
	var mediaPayload struct {
		Items []publicMedia `json:"items"`
	}
	if err := json.NewDecoder(mediaRes.Body).Decode(&mediaPayload); err != nil {
		t.Fatal(err)
	}
	if len(mediaPayload.Items) != 2 {
		t.Fatalf("expected both Pascal clusters, got %+v", mediaPayload.Items)
	}
}
