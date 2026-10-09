package main

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Optional isolated local preview: no GCS credentials and no production data.
// This is a test-only server and always binds to loopback. Stop it after visual
// verification; its schema is deleted on graceful shutdown.
func TestTrumpetBrowserPreview(t *testing.T) {
	if os.Getenv("TRUMPETS_PREVIEW") != "1" {
		t.Skip("optional loopback preview")
	}
	dsn := os.Getenv("TRUMPETS_TEST_DATABASE_URL")
	if dsn == "" {
		t.Fatal("disposable database required")
	}
	ctx := context.Background()
	db, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	schema := "preview_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err = db.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer db.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	isolated, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer isolated.Close()
	if err = migrate(ctx, isolated); err != nil {
		t.Fatal(err)
	}
	// Inspiration images live in memory and previews come from local fixtures:
	// the preview never contacts YouTube, GCS or any other third party.
	objects := newMemoryObjects("/__preview_objects/")
	app := &application{db: isolated, cfg: config{GatewayKey: "preview-only"}, logger: slog.Default(), objects: objects, inspirationHTTP: newSafeHTTPClient(offlineResolver{}, true)}
	youtubeOEmbedEndpoint = "http://127.0.0.1:4173/__preview_fixtures/oembed?url="
	youtubeThumbnailBase = "http://127.0.0.1:4173/__preview_fixtures/vi/"
	thumbnail := previewThumbnail(t)
	api := app.routes()
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	files := http.FileServer(http.Dir(root))
	listener, err := net.Listen("tcp", "127.0.0.1:4173")
	if err != nil {
		t.Fatal(err)
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "private, no-store")
		if strings.HasPrefix(r.URL.Path, "/__preview_objects/") {
			objects.ServeHTTP(w, r)
			return
		}
		if r.URL.Path == "/__preview_fixtures/oembed" {
			w.Header().Set("Content-Type", "application/json")
			title := "Upswept bell study · TEST FIXTURE"
			if strings.Contains(r.URL.Query().Get("url"), "qHetQ-t4Wi0") {
				title = "1st Harrelson Muse with MBS Technology · TEST FIXTURE"
			}
			json.NewEncoder(w).Encode(map[string]string{"title": title, "author_name": "Visual fixture channel", "thumbnail_url": "http://127.0.0.1:4173/__preview_fixtures/thumb.png"})
			return
		}
		if strings.HasPrefix(r.URL.Path, "/__preview_fixtures/") {
			w.Header().Set("Content-Type", "image/png")
			w.Write(thumbnail)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/trumpets/api/") {
			r.URL.Path = strings.TrimPrefix(r.URL.Path, "/trumpets/api")
			r.Header.Set("X-Jazz-Gateway-Key", "preview-only")
			r.Header.Set("X-Jazz-User", "visual-test")
			api.ServeHTTP(w, r)
			return
		}
		files.ServeHTTP(w, r)
	})
	server := &http.Server{Handler: handler}
	handlerWithStop := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && r.URL.Path == "/__preview_stop" {
			go server.Close()
			return
		}
		handler.ServeHTTP(w, r)
	})
	server.Handler = handlerWithStop
	t.Log("isolated preview: http://127.0.0.1:4173/trumpets/")
	if err = server.Serve(listener); err != http.ErrServerClosed {
		t.Fatal(err)
	}
}

// previewThumbnail is a synthetic brass-toned 9:16 gradient, clearly not a
// photograph, used as the fixture thumbnail for screenshots.
func previewThumbnail(t *testing.T) []byte {
	img := image.NewRGBA(image.Rect(0, 0, 270, 480))
	for y := 0; y < 480; y++ {
		for x := 0; x < 270; x++ {
			d := (x-135)*(x-135) + (y-220)*(y-220)
			glow := 0
			if d < 110*110 {
				glow = 60 - d/220
			}
			img.Set(x, y, color.RGBA{uint8(70 + y/6 + glow), uint8(58 + y/9 + glow/2), uint8(36 + x/10), 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
