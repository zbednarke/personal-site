package main

import (
	"context"
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

// Optional isolated local preview for the Commonplace browser check: no GCS
// credentials, no production data, loopback only. Media lives in memory and
// the schema is dropped on shutdown. Every fixture is fictional.
func TestCommonplaceBrowserPreview(t *testing.T) {
	if os.Getenv("COMMONPLACE_PREVIEW") != "1" {
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
	schema := "cp_preview_" + strings.ReplaceAll(uuid.NewString(), "-", "")
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
	objects := newMemoryObjects("/__preview_objects/")
	app := &application{db: isolated, cfg: config{GatewayKey: "preview-only"}, logger: slog.Default(), objects: objects}
	api := app.routes()
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	files := http.FileServer(http.Dir(root))
	addr := os.Getenv("COMMONPLACE_PREVIEW_ADDR")
	if addr == "" {
		addr = "127.0.0.1:4174"
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{}
	server.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "private, no-store")
		switch {
		case r.Method == "POST" && r.URL.Path == "/__preview_stop":
			go server.Close()
		case strings.HasPrefix(r.URL.Path, "/__preview_objects/"):
			objects.ServeHTTP(w, r)
		case strings.HasPrefix(r.URL.Path, "/commonplace/api/"):
			// What Caddy does: strip the prefix, set the verified user and the gateway key.
			r.URL.Path = strings.TrimPrefix(r.URL.Path, "/commonplace/api")
			r.Header.Set("X-Jazz-Gateway-Key", "preview-only")
			r.Header.Set("X-Jazz-User", "visual-test")
			api.ServeHTTP(w, r)
		default:
			files.ServeHTTP(w, r)
		}
	})
	t.Log("isolated preview: http://" + addr + "/commonplace/")
	if err = server.Serve(listener); err != http.ErrServerClosed {
		t.Fatal(err)
	}
}
