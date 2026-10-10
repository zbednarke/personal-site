package main

import (
	"crypto/sha256"
	"encoding/base64"
	"html/template"
	"net/http"
	"net/url"
)

// The sign-in pages are self-contained: no scripts, no web fonts, no
// third-party requests. The stylesheet is pinned by hash in the CSP.
const pageCSS = `
:root{color-scheme:dark;--bg:#08060f;--card:#110d1c;--ink:#efeafd;--fg:#cfc7e8;--dim:#9a92bd;--line:#2a2342;--accent:#f2ad5c;--bad:#f0a59a}
*{box-sizing:border-box;margin:0}
body{min-height:100vh;display:grid;place-items:center;padding:24px;background:radial-gradient(1200px 600px at 50% -10%,#1b1230 0,var(--bg) 60%) fixed;color:var(--fg);font:16px/1.6 -apple-system,"Helvetica Neue",Helvetica,Arial,sans-serif;-webkit-font-smoothing:antialiased}
main{width:100%;max-width:380px}
.card{background:var(--card);border:1px solid var(--line);border-radius:18px;padding:36px 32px 30px;box-shadow:0 30px 80px #0008}
.eyebrow{font:600 11px/1 ui-monospace,Menlo,Consolas,monospace;letter-spacing:.14em;color:var(--accent);text-transform:uppercase}
.eyebrow::before{content:"";display:inline-block;width:7px;height:7px;border-radius:50%;background:var(--accent);margin-right:8px;vertical-align:1px}
h1{margin-top:18px;font:700 34px/1.1 "Playfair Display",Georgia,"Times New Roman",serif;color:var(--ink);letter-spacing:-.01em}
.lede{margin-top:10px;color:var(--dim);font-size:15px}
.note{margin-top:22px;padding:11px 14px;border-radius:10px;border:1px solid var(--line);background:#0c0915;font-size:14px}
.note.bad{border-color:#5a3434;color:var(--bad)}
.actions{margin-top:26px;display:grid;gap:12px}
.google{display:flex;align-items:center;justify-content:center;gap:12px;min-height:46px;padding:0 18px;border-radius:999px;border:1px solid #8e918f;background:#131314;color:#e3e3e3;font:500 15px/1 Roboto,-apple-system,"Helvetica Neue",Arial,sans-serif;text-decoration:none;transition:background .15s,border-color .15s}
.google:hover{background:#1f1f22;border-color:#c4c7c5}
.google svg{flex:none}
a,summary{color:var(--accent)}
a:focus-visible,button:focus-visible,summary:focus-visible,input:focus-visible{outline:2px solid var(--accent);outline-offset:3px}
details{margin-top:22px;border-top:1px solid var(--line);padding-top:16px}
summary{cursor:pointer;font-size:14px;width:max-content}
form.password{display:grid;gap:10px;margin-top:14px}
label{font-size:13px;color:var(--dim)}
input{width:100%;margin-top:4px;padding:10px 12px;border-radius:10px;border:1px solid var(--line);background:#0c0915;color:var(--ink);font:inherit}
button{min-height:44px;padding:0 18px;border-radius:999px;border:1px solid var(--accent);background:var(--accent);color:#1a1205;font:600 15px/1 -apple-system,"Helvetica Neue",Arial,sans-serif;cursor:pointer}
.row{display:flex;gap:12px;align-items:center;flex-wrap:wrap}
@media (prefers-reduced-motion:reduce){*{transition:none!important}}
`

const googleLogo = `<svg width="18" height="18" viewBox="0 0 48 48" aria-hidden="true" focusable="false"><path fill="#EA4335" d="M24 9.5c3.54 0 6.71 1.22 9.21 3.6l6.85-6.85C35.9 2.38 30.47 0 24 0 14.62 0 6.51 5.38 2.56 13.22l7.98 6.19C12.43 13.72 17.74 9.5 24 9.5z"/><path fill="#4285F4" d="M46.98 24.55c0-1.57-.15-3.09-.38-4.55H24v9.02h12.94c-.58 2.96-2.26 5.48-4.78 7.18l7.73 6c4.51-4.18 7.09-10.36 7.09-17.65z"/><path fill="#FBBC05" d="M10.53 28.59c-.48-1.45-.76-2.99-.76-4.59s.27-3.14.76-4.59l-7.98-6.19C.92 16.46 0 20.12 0 24c0 3.88.92 7.54 2.56 10.78l7.97-6.19z"/><path fill="#34A853" d="M24 48c6.48 0 11.93-2.13 15.89-5.81l-7.73-6c-2.15 1.45-4.92 2.3-8.16 2.3-6.26 0-11.57-4.22-13.47-9.91l-7.98 6.19C6.51 42.62 14.62 48 24 48z"/></svg>`

var pageTemplate = template.Must(template.New("page").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="robots" content="noindex, nofollow">
<meta name="referrer" content="no-referrer">
<meta name="theme-color" content="#08060f">
<title>{{.Title}} · Zach Bednarke</title>
<link rel="icon" href="data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 16 16'%3E%3Ccircle cx='8' cy='8' r='6' fill='%23f2ad5c'/%3E%3C/svg%3E">
<style>` + pageCSS + `</style>
</head>
<body>
<main>
<section class="card" aria-labelledby="title">
<p class="eyebrow">Zach Bednarke · Private</p>
{{- if .Logout}}
<h1 id="title">Sign out</h1>
{{- if .SignedIn}}
<p class="lede">Sign out of the private pages on this browser?</p>
<form class="actions" method="post" action="/auth/logout">
<div class="row"><button type="submit">Sign out</button> <a href="{{.Next}}">Cancel</a></div>
</form>
{{- else}}
<p class="lede">This browser isn't signed in.</p>
<p class="actions"><a href="{{.LoginHref}}">Sign in</a></p>
{{- end}}
{{- else if .Back}}
<h1 id="title">{{.Title}}</h1>
<p class="note bad" role="alert">{{.Message}}</p>
<p class="actions"><a href="{{.LoginHref}}">Back to sign in</a></p>
{{- else}}
<h1 id="title">Sign in</h1>
<p class="lede">The Jazz Project, the Trumpet Observatory and notebooks are private.</p>
{{- if .Message}}
<p class="note{{if .Error}} bad{{end}}" role="{{if .Error}}alert{{else}}status{{end}}">{{.Message}}
{{- if .SwitchHref}} <a href="{{.SwitchHref}}">Use a different account</a>{{end}}</p>
{{- end}}
{{- if .Google}}
<div class="actions"><a class="google" href="{{.GoogleHref}}">` + googleLogo + `<span>Sign in with Google</span></a></div>
{{- end}}
{{- if .Password}}
<details{{if .PasswordOpen}} open{{end}}>
<summary>Use password</summary>
<form class="password" method="post" action="/auth/password">
<input type="hidden" name="next" value="{{.Next}}">
<label>Username <input name="user" autocomplete="username" autocapitalize="none" spellcheck="false" required></label>
<label>Password <input name="password" type="password" autocomplete="current-password" required></label>
<button type="submit">Sign in</button>
</form>
</details>
{{- end}}
{{- end}}
</section>
</main>
</body>
</html>
`))

var pageCSP = func() string {
	sum := sha256.Sum256([]byte(pageCSS))
	return "default-src 'none'; style-src 'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'; img-src data:; form-action 'self'; frame-ancestors 'none'; base-uri 'none'"
}()

type pageData struct {
	Title, Message, Next          string
	Error, Back, Logout, SignedIn bool
	Google, Password              bool
	PasswordOpen                  bool
	GoogleHref, SwitchHref        string
}

func (p pageData) LoginHref() string { return loginURL(p.Next) }

func (s *server) render(w http.ResponseWriter, status int, data pageData) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Content-Security-Policy", pageCSP)
	h.Set("X-Frame-Options", "DENY")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Robots-Tag", "noindex, nofollow")
	w.WriteHeader(status)
	pageTemplate.Execute(w, data)
}

var loginMessages = map[string]string{
	"expired":     "That sign-in took too long or began in another tab. Please try again.",
	"denied":      "Sign-in was cancelled.",
	"not_allowed": "That Google account doesn't have access.",
	"failed":      "Google sign-in didn't complete. Please try again.",
	"password":    "That username and password didn't match.",
}

func (s *server) loginPage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	q := r.URL.Query()
	next := safeNext(q.Get("next"))
	if _, ok := s.session(r); ok && q.Get("error") == "" {
		http.Redirect(w, r, next, http.StatusSeeOther)
		return
	}
	data := pageData{Title: "Sign in", Next: next, Google: s.google != nil, Password: s.cfg.AllowPassword}
	start := "/auth/google/start?next=" + url.QueryEscape(next)
	data.GoogleHref = start
	if code := q.Get("error"); loginMessages[code] != "" {
		data.Message, data.Error = loginMessages[code], true
		if code == "not_allowed" && data.Google {
			data.SwitchHref = start + "&switch=1"
		}
		data.PasswordOpen = code == "password"
	} else if q.Get("signed_out") == "1" {
		data.Message = "You're signed out."
	} else if !data.Google {
		data.PasswordOpen = true
	}
	s.render(w, http.StatusOK, data)
}
