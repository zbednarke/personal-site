package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// A small GitHub REST client for the site's own repository. The token comes
// from Secret Manager via WORKBENCH_GITHUB_TOKEN; without it, reads of the
// public repository still work (unauthenticated) and writes are refused.

type wbGitHub struct {
	token string
	repo  string
	api   string
	http  *http.Client
}

func (g *wbGitHub) canWrite() bool { return g.token != "" }

func (g *wbGitHub) do(ctx context.Context, method, path string, body any, out any) error {
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, g.api+path, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "zachbednarke-workbench")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if g.token != "" {
		req.Header.Set("Authorization", "Bearer "+g.token)
	}
	resp, err := g.http.Do(req)
	if err != nil {
		return fmt.Errorf("GitHub is unreachable: %w", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode >= 300 {
		var e struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(data, &e)
		return fmt.Errorf("GitHub %s %s: %d %s", method, strings.SplitN(path, "?", 2)[0], resp.StatusCode, e.Message)
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}

type wbIssue struct {
	Number    int       `json:"number"`
	Title     string    `json:"title"`
	State     string    `json:"state"`
	URL       string    `json:"url"`
	Labels    []string  `json:"labels"`
	UpdatedAt time.Time `json:"updatedAt"`
	Excerpt   string    `json:"excerpt,omitempty"`
}

type ghIssue struct {
	Number      int       `json:"number"`
	Title       string    `json:"title"`
	State       string    `json:"state"`
	HTMLURL     string    `json:"html_url"`
	Body        string    `json:"body"`
	UpdatedAt   time.Time `json:"updated_at"`
	PullRequest any       `json:"pull_request"`
	Labels      []struct {
		Name string `json:"name"`
	} `json:"labels"`
}

func (i ghIssue) short() wbIssue {
	out := wbIssue{Number: i.Number, Title: i.Title, State: i.State, URL: i.HTMLURL, UpdatedAt: i.UpdatedAt, Labels: []string{}, Excerpt: wbCleanText(i.Body, 280)}
	for _, l := range i.Labels {
		out.Labels = append(out.Labels, l.Name)
	}
	return out
}

func (g *wbGitHub) listIssues(ctx context.Context, state, labels, query string, limit int) ([]wbIssue, error) {
	if limit < 1 || limit > 30 {
		limit = 15
	}
	if state != "closed" && state != "all" {
		state = "open"
	}
	var raw []ghIssue
	if strings.TrimSpace(query) != "" {
		q := "repo:" + g.repo + " is:issue " + strings.TrimSpace(query)
		if state != "all" {
			q += " is:" + state
		}
		var res struct {
			Items []ghIssue `json:"items"`
		}
		if err := g.do(ctx, "GET", fmt.Sprintf("/search/issues?per_page=%d&q=%s", limit, url.QueryEscape(q)), nil, &res); err != nil {
			return nil, err
		}
		raw = res.Items
	} else {
		path := fmt.Sprintf("/repos/%s/issues?state=%s&per_page=%d", g.repo, state, limit)
		if labels != "" {
			path += "&labels=" + url.QueryEscape(labels)
		}
		if err := g.do(ctx, "GET", path, nil, &raw); err != nil {
			return nil, err
		}
	}
	out := []wbIssue{}
	for _, i := range raw {
		if i.PullRequest == nil {
			out = append(out, i.short())
		}
	}
	return out, nil
}

func (g *wbGitHub) createIssue(ctx context.Context, in wbIssueInput) (wbIssue, error) {
	if !g.canWrite() {
		return wbIssue{}, errors.New("GitHub writes are not configured (WORKBENCH_GITHUB_TOKEN is unset)")
	}
	body := map[string]any{"title": in.Title, "body": in.Body}
	if len(in.Labels) > 0 {
		body["labels"] = in.Labels
	}
	var out ghIssue
	if err := g.do(ctx, "POST", "/repos/"+g.repo+"/issues", body, &out); err != nil {
		return wbIssue{}, err
	}
	return out.short(), nil
}

func (g *wbGitHub) comment(ctx context.Context, in wbCommentInput) (string, error) {
	if !g.canWrite() {
		return "", errors.New("GitHub writes are not configured (WORKBENCH_GITHUB_TOKEN is unset)")
	}
	var out struct {
		HTMLURL string `json:"html_url"`
	}
	if err := g.do(ctx, "POST", fmt.Sprintf("/repos/%s/issues/%d/comments", g.repo, in.Number), map[string]any{"body": in.Body}, &out); err != nil {
		return "", err
	}
	return out.HTMLURL, nil
}

// ---- CI, PR and deploy status ------------------------------------------------------

type wbRunStatus struct {
	Name       string    `json:"name"`
	Status     string    `json:"status"`
	Conclusion string    `json:"conclusion"`
	URL        string    `json:"url"`
	SHA        string    `json:"sha"`
	Branch     string    `json:"branch"`
	Event      string    `json:"event"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

type wbPRStatus struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	URL    string `json:"url"`
	Draft  bool   `json:"draft"`
	Branch string `json:"branch"`
	Checks struct {
		Passed  int `json:"passed"`
		Failed  int `json:"failed"`
		Pending int `json:"pending"`
	} `json:"checks"`
}

type wbStatusCard struct {
	Kind   string       `json:"kind"`
	Repo   string       `json:"repo"`
	CI     *wbRunStatus `json:"ci"`
	Deploy *wbRunStatus `json:"deploy"`
	PRs    []wbPRStatus `json:"prs"`
	At     time.Time    `json:"at"`
}

type ghRun struct {
	Name       string    `json:"name"`
	Status     string    `json:"status"`
	Conclusion string    `json:"conclusion"`
	HTMLURL    string    `json:"html_url"`
	HeadSHA    string    `json:"head_sha"`
	HeadBranch string    `json:"head_branch"`
	Event      string    `json:"event"`
	UpdatedAt  time.Time `json:"updated_at"`
}

func (r ghRun) status() *wbRunStatus {
	sha := r.HeadSHA
	if len(sha) > 7 {
		sha = sha[:7]
	}
	return &wbRunStatus{Name: r.Name, Status: r.Status, Conclusion: r.Conclusion, URL: r.HTMLURL, SHA: sha, Branch: r.HeadBranch, Event: r.Event, UpdatedAt: r.UpdatedAt}
}

const (
	wbCIWorkflow     = "Trumpet and Jazz tests"
	wbDeployWorkflow = "Deploy"
)

func (g *wbGitHub) siteStatus(ctx context.Context) (wbStatusCard, error) {
	card := wbStatusCard{Kind: "status", Repo: g.repo, PRs: []wbPRStatus{}, At: time.Now().UTC()}
	var runs struct {
		WorkflowRuns []ghRun `json:"workflow_runs"`
	}
	if err := g.do(ctx, "GET", "/repos/"+g.repo+"/actions/runs?per_page=30", nil, &runs); err != nil {
		return card, err
	}
	for _, r := range runs.WorkflowRuns {
		if card.CI == nil && r.Name == wbCIWorkflow && r.HeadBranch == "main" {
			card.CI = r.status()
		}
		if card.Deploy == nil && r.Name == wbDeployWorkflow {
			card.Deploy = r.status()
		}
	}
	var pulls []struct {
		Number  int    `json:"number"`
		Title   string `json:"title"`
		HTMLURL string `json:"html_url"`
		Draft   bool   `json:"draft"`
		Head    struct {
			Ref string `json:"ref"`
			SHA string `json:"sha"`
		} `json:"head"`
	}
	if err := g.do(ctx, "GET", "/repos/"+g.repo+"/pulls?state=open&per_page=8", nil, &pulls); err != nil {
		return card, err
	}
	for i, p := range pulls {
		pr := wbPRStatus{Number: p.Number, Title: p.Title, URL: p.HTMLURL, Draft: p.Draft, Branch: p.Head.Ref}
		if i < 5 {
			var checks struct {
				CheckRuns []struct {
					Status     string `json:"status"`
					Conclusion string `json:"conclusion"`
				} `json:"check_runs"`
			}
			if err := g.do(ctx, "GET", "/repos/"+g.repo+"/commits/"+p.Head.SHA+"/check-runs?per_page=50", nil, &checks); err == nil {
				for _, c := range checks.CheckRuns {
					switch {
					case c.Status != "completed":
						pr.Checks.Pending++
					case c.Conclusion == "success" || c.Conclusion == "skipped" || c.Conclusion == "neutral":
						pr.Checks.Passed++
					default:
						pr.Checks.Failed++
					}
				}
			}
		}
		card.PRs = append(card.PRs, pr)
	}
	return card, nil
}
