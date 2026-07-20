package llm_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/subosito/mow/internal/llm"
)

func TestGenerateImageB64(t *testing.T) {
	var gotPath string
	var gotComponent string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotComponent = r.Header.Get("X-Mow-Component")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]string{{"b64_json": base64.StdEncoding.EncodeToString([]byte("PNG"))}},
		})
	}))
	defer srv.Close()

	c := &llm.MediaClient{BaseURL: srv.URL + "/v1", APIKey: "k"}
	res, err := c.GenerateImage(context.Background(), "img-m", "a cat", "512x512")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(gotPath, "/images/generations") {
		t.Fatalf("path=%s", gotPath)
	}
	if gotComponent != "tool.generate_image" {
		t.Fatalf("component=%q", gotComponent)
	}
	raw, _ := base64.StdEncoding.DecodeString(res.B64JSON)
	if string(raw) != "PNG" {
		t.Fatalf("b64=%q", res.B64JSON)
	}
}

func TestDescribeImage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), "image_url") {
			t.Errorf("expected multimodal body: %s", body)
		}
		if r.Header.Get("X-Mow-Component") != "tool.understand_image" {
			t.Errorf("component=%q", r.Header.Get("X-Mow-Component"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]string{"content": "a red button"}},
			},
		})
	}))
	defer srv.Close()

	c := &llm.MediaClient{BaseURL: srv.URL + "/v1", APIKey: "k"}
	text, err := c.DescribeImage(context.Background(), "vl-m", "what?", llm.DataURL("image/png", []byte{1, 2, 3}))
	if err != nil {
		t.Fatal(err)
	}
	if text != "a red button" {
		t.Fatalf("text=%q", text)
	}
}

func TestGenerateVideoWait(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/videos/generations"):
			_ = json.NewEncoder(w).Encode(map[string]any{"request_id": "vid_1", "status": "pending"})
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/videos/vid_1"):
			if hits < 3 {
				_ = json.NewEncoder(w).Encode(map[string]any{"request_id": "vid_1", "status": "processing"})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"request_id": "vid_1",
				"status":     "completed",
				"data": []map[string]string{
					{"b64_json": base64.StdEncoding.EncodeToString([]byte("MP4DATA"))},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := &llm.MediaClient{BaseURL: srv.URL + "/v1", APIKey: "k"}
	// Speed up: GenerateVideoWait uses 2s ticker — use short timeout context only if needed.
	// For test, patch by waiting — 2s * 2 polls ≈ 4s+. Accept.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	res, err := c.GenerateVideoWait(ctx, "vid-m", "waves")
	if err != nil {
		t.Fatal(err)
	}
	if string(res.Bytes) != "MP4DATA" {
		t.Fatalf("bytes=%q status=%s job=%s", res.Bytes, res.Status, res.Job)
	}
	if res.ID != "vid_1" {
		t.Fatalf("id=%q", res.ID)
	}
}

func TestGenerateVideoWait_xaiVideoURL(t *testing.T) {
	// xAI returns status=done + video.url (no request_id on final body).
	const remoteBody = "XAI-MP4"
	fileSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(remoteBody))
	}))
	defer fileSrv.Close()

	var hits int
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/videos/generations"):
			_ = json.NewEncoder(w).Encode(map[string]any{"request_id": "xai_1", "status": "pending"})
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/videos/xai_1"):
			if hits < 3 {
				_ = json.NewEncoder(w).Encode(map[string]any{"request_id": "xai_1", "status": "processing"})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": "done",
				"video":  map[string]any{"url": fileSrv.URL + "/clip.mp4", "duration": 8},
				"model":  "grok-imagine-video",
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer api.Close()

	c := &llm.MediaClient{BaseURL: api.URL + "/v1", APIKey: "k"}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	res, err := c.GenerateVideoWait(ctx, "vid-m", "ball")
	if err != nil {
		t.Fatal(err)
	}
	if string(res.Bytes) != remoteBody {
		t.Fatalf("bytes=%q status=%s job=%s", res.Bytes, res.Status, res.Job)
	}
	if !strings.Contains(res.URL, "/clip.mp4") {
		t.Fatalf("url=%q", res.URL)
	}
}

func TestDataURLAndMIME(t *testing.T) {
	u := llm.DataURL("image/png", []byte("x"))
	if !strings.HasPrefix(u, "data:image/png;base64,") {
		t.Fatal(u)
	}
	if llm.MIMEFromPath("a.PNG") != "image/png" {
		t.Fatal(llm.MIMEFromPath("a.PNG"))
	}
}
