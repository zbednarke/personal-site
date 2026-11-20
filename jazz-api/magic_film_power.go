package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"google.golang.org/api/idtoken"
)

// Wake requests are immediate. An authenticated Scheduler reconciliation every
// minute repairs request failures and the enqueue/guest-poweroff race, using
// the persisted job queue rather than a goroutine on a throttled API instance.
type filmPowerConfig struct {
	Enabled                           bool
	Project, Zone, Instance           string
	SchedulerEmail, SchedulerAudience string
	IdleTimeout                       time.Duration
}

func loadFilmPowerConfig(privateWorker bool) (filmPowerConfig, error) {
	c := filmPowerConfig{Enabled: os.Getenv("MAGIC_FILM_VM_POWER_CONTROL") == "1", IdleTimeout: 5 * time.Minute}
	if !c.Enabled {
		return c, nil
	}
	seconds, err := strconv.Atoi(envOr("MAGIC_FILM_IDLE_SECONDS", "300"))
	if err != nil || seconds < 60 || seconds > 1800 {
		return c, errors.New("MAGIC_FILM_IDLE_SECONDS must be between 60 and 1800")
	}
	c.IdleTimeout = time.Duration(seconds) * time.Second
	if privateWorker {
		return c, nil // Guest shutdown needs no Compute or scheduler identity.
	}
	c.Project = strings.TrimSpace(os.Getenv("MAGIC_FILM_VM_PROJECT"))
	c.Zone = strings.TrimSpace(os.Getenv("MAGIC_FILM_VM_ZONE"))
	c.Instance = strings.TrimSpace(os.Getenv("MAGIC_FILM_VM_NAME"))
	c.SchedulerEmail = strings.TrimSpace(os.Getenv("MAGIC_FILM_WAKE_CALLER"))
	c.SchedulerAudience = strings.TrimRight(strings.TrimSpace(os.Getenv("MAGIC_FILM_WAKE_AUDIENCE")), "/")
	selector := regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
	account := regexp.MustCompile(`^[a-z][a-z0-9-]{4,28}[a-z0-9]@` + regexp.QuoteMeta(c.Project) + `\.iam\.gserviceaccount\.com$`)
	u, parseErr := url.Parse(c.SchedulerAudience)
	if !selector.MatchString(c.Project) || !selector.MatchString(c.Zone) || !selector.MatchString(c.Instance) ||
		!account.MatchString(c.SchedulerEmail) ||
		parseErr != nil || u.Scheme != "https" || !strings.HasSuffix(u.Hostname(), ".run.app") ||
		u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.Port() != "" {
		return c, errors.New("private film VM power control requires a VM target and a service-account scheduler identity/audience")
	}
	return c, nil
}

const runnablePrivateFilmSQL = `SELECT EXISTS(SELECT 1 FROM magic_film_jobs WHERE provider='codex' AND NOT cancel_requested
 AND (status IN ('queued','starting','preparing','editing','rendering')
 OR (status='waiting_usage' AND (next_attempt_at IS NULL OR next_attempt_at<=now()))))`

func (app *application) privateFilmHasRunnableWork(ctx context.Context) (bool, error) {
	var pending bool
	err := app.db.QueryRow(ctx, runnablePrivateFilmSQL).Scan(&pending)
	return pending, err
}

func schedulerFilmIdentity(payload *idtoken.Payload, email string) bool {
	if payload == nil || (payload.Issuer != "https://accounts.google.com" && payload.Issuer != "accounts.google.com") {
		return false
	}
	verified, ok := payload.Claims["email_verified"].(bool)
	return ok && verified && payload.Claims["email"] == email && email != ""
}

func (app *application) filmSchedulerAuthorized(r *http.Request) bool {
	if app.magicFilmWakeAuth != nil {
		return app.magicFilmWakeAuth(r)
	}
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, "Bearer ") || len(h) > 8192 {
		return false
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	payload, err := idtoken.Validate(ctx, strings.TrimPrefix(h, "Bearer "), app.cfg.FilmPower.SchedulerAudience)
	return err == nil && schedulerFilmIdentity(payload, app.cfg.FilmPower.SchedulerEmail)
}

func (app *application) reconcilePrivateFilmVM(w http.ResponseWriter, r *http.Request) {
	if !app.cfg.FilmPower.Enabled {
		http.NotFound(w, r)
		return
	}
	if !app.filmSchedulerAuthorized(r) {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	pending, err := app.privateFilmHasRunnableWork(ctx)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "worker queue temporarily unavailable")
		return
	}
	if !pending {
		writeJSON(w, http.StatusOK, map[string]string{"status": "idle"})
		return
	}
	if err := app.wakePrivateFilmVM(ctx); err != nil {
		// A non-2xx response asks Scheduler to retry. Persisted jobs remain queued.
		writeError(w, http.StatusServiceUnavailable, "worker wake-up pending")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "awake_or_starting"})
}

func (app *application) wakePrivateFilmVM(ctx context.Context) error {
	if !app.cfg.FilmPower.Enabled {
		return nil
	}
	if app.magicFilmVMWake != nil {
		return app.magicFilmVMWake(ctx)
	}
	if app.tokenSource == nil {
		return errors.New("worker wake credentials unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	token, err := app.tokenSource.Token()
	if err != nil {
		return errors.New("worker wake credentials unavailable")
	}
	c := app.cfg.FilmPower
	endpoint := "https://compute.googleapis.com/compute/v1/projects/" + url.PathEscape(c.Project) + "/zones/" + url.PathEscape(c.Zone) + "/instances/" + url.PathEscape(c.Instance)
	client := app.httpClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	request := func(method, suffix string) ([]byte, int, error) {
		req, err := http.NewRequestWithContext(ctx, method, endpoint+suffix, nil)
		if err != nil {
			return nil, 0, err
		}
		req.Header.Set("Authorization", "Bearer "+token.AccessToken)
		resp, err := client.Do(req)
		if err != nil {
			return nil, 0, errors.New("worker control request unavailable")
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(io.LimitReader(resp.Body, 8192))
		return body, resp.StatusCode, err
	}
	getStatus := func() (string, error) {
		body, code, err := request(http.MethodGet, "?fields=status")
		var instance struct{ Status string }
		if err != nil || code != http.StatusOK || json.Unmarshal(body, &instance) != nil {
			return "", errors.New("worker VM status unavailable")
		}
		return instance.Status, nil
	}
	status, err := getStatus()
	if err != nil {
		return err
	}
	switch status {
	case "RUNNING", "PROVISIONING", "STAGING":
		return nil
	case "TERMINATED":
		_, code, err := request(http.MethodPost, "/start")
		if err == nil && code >= 200 && code < 300 {
			return nil
		}
		// Concurrent wakes are harmless; re-read after a conflicting start.
		if code == http.StatusConflict || code == http.StatusBadRequest {
			if status, err := getStatus(); err == nil && (status == "RUNNING" || status == "PROVISIONING" || status == "STAGING") {
				return nil
			}
		}
		return errors.New("worker VM start pending")
	default:
		// In particular, do not treat STOPPING as a completed wake. Scheduler
		// will observe TERMINATED on a later retry and start the VM then.
		return errors.New("worker VM transition pending")
	}
}

func (app *application) requestPrivateFilmWake(ctx context.Context) {
	if err := app.wakePrivateFilmVM(ctx); err != nil && app.logger != nil {
		app.logger.Warn("private film VM wake delayed; scheduled reconciliation will retry", "error_code", "wake_pending")
	}
}

func (app *application) stopIdlePrivateFilmVM(ctx context.Context, lastActivity, now time.Time) (bool, error) {
	c := app.cfg.FilmPower
	if !c.Enabled || now.Sub(lastActivity) < c.IdleTimeout {
		return false, nil
	}
	checkCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	pending, err := app.privateFilmHasRunnableWork(checkCtx)
	if err != nil || pending {
		return false, err
	}
	if app.magicFilmShutdown != nil {
		err = app.magicFilmShutdown(checkCtx)
	} else {
		command := exec.CommandContext(checkCtx, "sudo", "-n", "/usr/local/libexec/magic-film-idle-stop")
		command.Env = []string{"PATH=/usr/bin:/bin", "LANG=C.UTF-8"}
		err = command.Run()
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 75 {
		return false, nil // An operator or Codex process is still active.
	}
	if err != nil {
		return false, errors.New("idle shutdown unavailable")
	}
	return true, nil
}
