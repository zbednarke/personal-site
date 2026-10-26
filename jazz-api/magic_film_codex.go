package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type magicFilmPause struct {
	Status  string
	RetryAt time.Time
}

// Keep the end of CLI output, where terminal error events appear, bounded.
type magicCodexLog struct{ data []byte }

func (log *magicCodexLog) Write(data []byte) (int, error) {
	const limit = 64 << 10
	log.data = append(log.data, data...)
	if len(log.data) > limit {
		log.data = log.data[len(log.data)-limit:]
	}
	return len(data), nil
}

func (p *magicFilmPause) Error() string { return p.Status }

// No model output, provider credentials, or cloud credentials enter logs.
// The root-owned launcher switches to an isolated OS user and strips the env.
func codexCommand(ctx context.Context, args ...string) *exec.Cmd {
	command := exec.CommandContext(ctx, envOr("MAGIC_FILM_CODEX_LAUNCHER", "/usr/local/bin/magic-film-codex"), args...)
	command.Env = []string{"PATH=/usr/local/bin:/usr/bin:/bin", "LANG=C.UTF-8"}
	command.Cancel = func() error { return command.Process.Signal(syscall.SIGTERM) }
	command.WaitDelay = 30 * time.Second
	return command
}

func codexSubscriptionReady(ctx context.Context) bool {
	checkCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	command := codexCommand(checkCtx, "login", "status")
	var output magicCodexLog
	command.Stdout, command.Stderr = &output, &output
	err := command.Run()
	return err == nil && strings.Contains(strings.ToLower(string(output.data)), "logged in using chatgpt")
}

func codexPauseFromOutput(output string, now time.Time) *magicFilmPause {
	text := strings.ToLower(output)
	switch {
	case strings.Contains(text, "usage_limit_reached"), strings.Contains(text, "usage limit"), strings.Contains(text, "rate_limit_exceeded"), strings.Contains(text, "rate limit reached"):
		// CLI messages do not always include a machine-readable reset time.
		// Back off conservatively and keep the existing evidence/plan.
		return &magicFilmPause{"waiting_usage", now.Add(30 * time.Minute)}
	case strings.Contains(text, "not logged in"), strings.Contains(text, "refresh_token_expired"), strings.Contains(text, "refresh_token_reused"), strings.Contains(text, "token_expired"), strings.Contains(text, "please log in"), strings.Contains(text, "401 unauthorized"):
		return &magicFilmPause{"sign_in_required", now.Add(2 * time.Minute)}
	}
	return nil
}

func requestCodexMagicFilmPlan(ctx context.Context, model, effort string, manifest magicFilmManifest, direction, workspace string) (magicFilmPlan, error) {
	var plan magicFilmPlan
	if !codexSubscriptionReady(ctx) {
		return plan, &magicFilmPause{"sign_in_required", time.Now().Add(2 * time.Minute)}
	}
	manifest.Recordings = nil // never give signed media URLs or object paths to Codex
	metadata, _ := json.Marshal(manifest)
	schema, _ := json.Marshal(magicFilmSchema())
	schemaPath := filepath.Join(workspace, "plan-schema.json")
	if err := os.WriteFile(schemaPath, schema, 0640); err != nil {
		return plan, err
	}
	outputDir := filepath.Join(workspace, "codex-output")
	if err := os.MkdirAll(outputDir, 0770); err != nil {
		return plan, err
	}
	// Keep the shared evidence group on files created by the separate CLI
	// identity, so the Go worker can read the final plan without world access.
	if err := os.Chmod(outputDir, 0770|os.ModeSetgid); err != nil {
		return plan, err
	}
	outputPath := filepath.Join(outputDir, "plan.json")
	// A failed invocation must never reuse the preceding invocation's output.
	if err := os.Remove(outputPath); err != nil && !os.IsNotExist(err) {
		return plan, err
	}
	args := []string{"exec", "--strict-config", "--ignore-user-config", "--ignore-rules", "--ephemeral", "--skip-git-repo-check", "--sandbox", "read-only",
		"-c", `forced_login_method="chatgpt"`, "-c", `model_provider="openai"`, "-c", `approval_policy="never"`,
		"-c", `features.shell_tool=false`, "-c", `web_search="disabled"`,
		"-c", "model_reasoning_effort=" + fmt.Sprintf("%q", effort), "--json", "--color", "never", "--cd", workspace,
		"--output-schema", schemaPath, "--output-last-message", outputPath}
	if model != "" && model != "codex-default" {
		args = append(args, "--model", model)
	}
	for _, candidate := range manifest.Candidates {
		for _, frame := range candidate.Frames {
			if filepath.Base(frame) != frame {
				return plan, errors.New("invalid evidence image path")
			}
			imagePath := filepath.Join(workspace, frame)
			if err := os.Chmod(imagePath, 0640); err != nil {
				return plan, err
			}
			args = append(args, "--image", imagePath)
		}
	}
	args = append(args, "-")
	command := codexCommand(ctx, args...)
	command.Stdin = strings.NewReader(magicFilmPrompt(manifest, direction) + "\nUse only the supplied metadata and attached images. No tools or filesystem inspection are needed.\n<candidate_manifest>\n" + string(metadata) + "\n</candidate_manifest>")
	var output magicCodexLog
	command.Stdout, command.Stderr = &output, &output
	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			return plan, ctx.Err()
		}
		if pause := codexPauseFromOutput(string(output.data), time.Now()); pause != nil {
			return plan, pause
		}
		return plan, errors.New("Codex could not produce an edit plan; no paid API fallback was used")
	}
	file, err := os.Open(outputPath)
	if err != nil {
		return plan, errors.New("Codex returned no edit plan")
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, (4<<20)+1))
	if err != nil {
		return plan, errors.New("Codex edit plan could not be read")
	}
	if len(data) > 4<<20 {
		return plan, errors.New("Codex edit plan exceeded the size limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&plan); err != nil {
		return plan, errors.New("Codex returned an invalid edit plan")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return plan, errors.New("Codex returned extra data after the edit plan")
	}
	return plan, nil
}

func (app *application) cleanPrivateFilmWorkspaces(ctx context.Context) {
	root := envOr("MAGIC_FILM_WORKSPACE", "/var/lib/magic-film/jobs")
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, entry := range entries {
		id, err := uuid.Parse(entry.Name())
		if err != nil || !entry.IsDir() {
			continue
		}
		var terminal bool
		if err := app.db.QueryRow(ctx, `SELECT status IN ('complete','failed','cancelled') FROM magic_film_jobs WHERE id=$1 AND provider='codex'`, id).Scan(&terminal); err == nil && terminal {
			_ = os.RemoveAll(filepath.Join(root, entry.Name()))
		}
	}
}

// One DB session lock serializes the credential across the entire deployment,
// not merely the process. On lost lock/DB connection the worker stops and
// systemd restarts it; only the lock holder may recover interrupted jobs.
func (app *application) runPrivateMagicFilmWorker(ctx context.Context) error {
	conn, err := app.db.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	var locked bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(762913045)`).Scan(&locked); err != nil {
		return err
	}
	if !locked {
		return errors.New("another private film worker holds the subscription lock")
	}
	defer conn.Exec(context.Background(), `SELECT pg_advisory_unlock(762913045)`)
	workerCtx, stop := context.WithCancel(ctx)
	defer stop()
	heartbeatDone := make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-workerCtx.Done():
				return
			case <-ticker.C:
				pingCtx, cancel := context.WithTimeout(workerCtx, 5*time.Second)
				err := conn.Conn().Ping(pingCtx)
				cancel()
				if err != nil {
					stop()
					return
				}
			}
		}
	}()
	defer func() { stop(); <-heartbeatDone }()
	_, err = app.db.Exec(workerCtx, `UPDATE magic_film_jobs SET
	 status=CASE WHEN cancel_requested THEN 'cancelled' ELSE 'queued' END,
	 message='Resuming after worker restart',updated_at=now()
	 WHERE provider='codex' AND status IN ('preparing','editing','rendering','starting')`)
	if err != nil {
		return err
	}
	var cleanedAt time.Time
	lastActivity := time.Now()
	var nextIdleCheck time.Time
	for workerCtx.Err() == nil {
		if time.Since(cleanedAt) >= 5*time.Minute {
			app.cleanPrivateFilmWorkspaces(workerCtx)
			cleanedAt = time.Now()
		}
		var id string
		err := app.db.QueryRow(workerCtx, `SELECT id::text FROM magic_film_jobs WHERE provider='codex'
		 AND status IN ('queued','waiting_usage','sign_in_required') AND NOT cancel_requested
		 AND (next_attempt_at IS NULL OR next_attempt_at<=now()) ORDER BY created_at LIMIT 1`).Scan(&id)
		if err == nil {
			jobCtx, cancel := context.WithTimeout(workerCtx, 60*time.Minute)
			if err := app.runMagicFilmJob(jobCtx, id); err != nil && workerCtx.Err() == nil {
				app.logger.Error("private film job stopped", "job_id", id, "error_code", describeMagicFilmFailure(err.Error(), "").Code)
			}
			cancel()
			var status string
			if err := app.db.QueryRow(workerCtx, `SELECT status FROM magic_film_jobs WHERE id=$1`, id).Scan(&status); err != nil {
				return err // Do not power off after an unverified queue read.
			}
			if status != "waiting_usage" && status != "sign_in_required" {
				lastActivity = time.Now()
			}
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if app.cfg.FilmPower.Enabled && time.Now().After(nextIdleCheck) {
			stopping, err := app.stopIdlePrivateFilmVM(workerCtx, lastActivity, time.Now())
			nextIdleCheck = time.Now().Add(30 * time.Second)
			if err != nil && workerCtx.Err() == nil {
				app.logger.Warn("private film idle shutdown deferred", "error_code", "shutdown_unavailable")
			}
			if stopping {
				app.logger.Info("private film VM idle shutdown requested")
				// Never begin another job after a shutdown request was accepted.
				select {
				case <-workerCtx.Done():
					return nil
				case <-time.After(2 * time.Minute):
					return errors.New("accepted idle shutdown did not finish")
				}
			}
		}
		select {
		case <-workerCtx.Done():
		case <-time.After(5 * time.Second):
		}
	}
	if ctx.Err() != nil {
		return nil
	}
	return errors.New("private film worker lost its database lock connection")
}
