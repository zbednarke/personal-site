package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Optional isolated preview for tools/workbench/browser-check.cjs: loopback
// only, a throwaway schema, a scripted fake Messages API and a fake GitHub.
// No credentials, no network, fictional data only.
func TestWorkbenchBrowserPreview(t *testing.T) {
	if os.Getenv("WORKBENCH_PREVIEW") != "1" {
		t.Skip("optional loopback preview")
	}
	dsn := os.Getenv("TRUMPETS_TEST_DATABASE_URL")
	if dsn == "" {
		t.Fatal("disposable database required")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	db, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	schema := "wb_preview_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err = db.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer db.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
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

	model := newFakeAnthropic(t)
	model.auto = previewReply
	gh := newFakeGitHub(t)
	objects := newMemoryObjects("/__preview_objects/")
	app := &application{db: isolated, cfg: config{GatewayKey: "preview-only"}, logger: slog.Default(), objects: objects}
	app.wb = newWorkbench(wbConfig{Model: "claude-opus-5-5", MonthlyCapUSD: 25, AnthropicKey: "preview-key", GitHubToken: "preview-gh", GitHubRepo: "fixture/site", GitHubAPI: gh.server.URL, MaxIterations: 6,
		VAPIDPublic: "BPreviewOnlyNotARealKeyAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}, option.WithBaseURL(model.server.URL), option.WithMaxRetries(0))
	app.wb.baseCtx = ctx
	app.wb.heartbeat = 5 * time.Second
	go app.wbListen(ctx)
	api := app.routes()
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	files := http.FileServer(http.Dir(root))
	addr := os.Getenv("WORKBENCH_PREVIEW_ADDR")
	if addr == "" {
		addr = "127.0.0.1:4175"
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{}
	server.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && r.URL.Path == "/__preview_stop":
			go server.Close()
		case r.URL.Path == "/__preview_issues":
			fmt.Fprint(w, gh.issueCount())
		case strings.HasPrefix(r.URL.Path, "/__preview_objects/"):
			objects.ServeHTTP(w, r)
		case strings.HasPrefix(r.URL.Path, "/workbench/api/"), strings.HasPrefix(r.URL.Path, "/jazz/api/"), strings.HasPrefix(r.URL.Path, "/trumpets/api/"), strings.HasPrefix(r.URL.Path, "/commonplace/api/"):
			// What Caddy does: strip the prefix, set the verified user and the gateway key.
			r.URL.Path = "/" + strings.SplitN(strings.TrimPrefix(r.URL.Path, "/"), "/api/", 2)[1]
			r.Header.Set("X-Jazz-Gateway-Key", "preview-only")
			r.Header.Set("X-Jazz-User", "visual-test")
			api.ServeHTTP(w, r)
		default:
			w.Header().Set("Cache-Control", "no-store")
			files.ServeHTTP(w, r)
		}
	})
	t.Log("isolated preview: http://" + addr + "/jazz/")
	if err = server.Serve(listener); err != http.ErrServerClosed {
		t.Fatal(err)
	}
	cancel()
	app.wb.runs.Wait()
}

// previewReply scripts the fake model: tool calls for "issue" and "status",
// otherwise a reply streamed slowly enough to watch on two devices.
func previewReply(body map[string]any) fakeTurn {
	msgs, _ := body["messages"].([]any)
	last := ""
	toolResult := false
	if len(msgs) > 0 {
		m, _ := msgs[len(msgs)-1].(map[string]any)
		content, _ := m["content"].([]any)
		for _, c := range content {
			block, _ := c.(map[string]any)
			if block["type"] == "tool_result" {
				toolResult = true
			}
			if text, ok := block["text"].(string); ok {
				last += text
			}
		}
	}
	lower := strings.ToLower(last)
	switch {
	case toolResult:
		return fakeTurn{Blocks: []fakeBlock{{Text: "Done. The card is up on every device."}}, Input: 800, Output: 12}
	case strings.Contains(lower, "file an issue"):
		return fakeTurn{Blocks: []fakeBlock{{Text: "Here is a draft."}, {ToolName: "github_create_issue", ToolInput: map[string]any{"title": "Sample: calmer metronome click", "body": "Fictional fixture issue for the browser check."}}}, Stop: "tool_use", Input: 900, Output: 40}
	case strings.Contains(lower, "status"):
		return fakeTurn{Blocks: []fakeBlock{{ToolName: "site_status", ToolInput: map[string]any{}}}, Stop: "tool_use", Input: 700, Output: 20}
	case strings.Contains(lower, "voice note attached"):
		return fakeTurn{Blocks: []fakeBlock{{Text: "Got your voice note. I can't listen to audio yet, so tell me the gist in a line."}}, Input: 500, Output: 20}
	}
	return fakeTurn{Blocks: []fakeBlock{{Text: "Sample reply, streamed word by word so every open device can watch it arrive: long tones first, then the Blue Bossa changes at a slow tempo, then one chorus of guide tones."}}, Input: 1200, Output: 60, Delay: 45 * time.Millisecond}
}
