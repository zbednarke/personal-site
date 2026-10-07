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
	app := &application{db: isolated, cfg: config{GatewayKey: "preview-only"}, logger: slog.Default()}
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
