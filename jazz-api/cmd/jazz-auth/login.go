package main

import (
	"golang.org/x/crypto/bcrypt"
	"html/template"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Return only to this site's private Jazz paths, never a supplied origin.
func safeNext(value string) string {
	u, err := url.Parse(value)
	if err != nil || u.IsAbs() || u.Host != "" || u.Path == "/jazz/sign-in" || !strings.HasPrefix(u.Path, "/jazz/") || strings.ContainsAny(u.Path, "\\\r\n") || strings.ContainsAny(value, "\r\n") {
		return "/jazz/"
	}
	return u.String()
}

var loginPage = template.Must(template.New("login").Parse(`<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Sign in · Jazz Project</title><style>
:root{color-scheme:dark}*{box-sizing:border-box}body{margin:0;background:#131a1b;color:#f0eee3;font:16px/1.5 system-ui}main{max-width:440px;margin:12vh auto;padding:28px}small{color:#b8e2d0;letter-spacing:.15em}h1{font:42px Georgia;margin:18px 0}p{color:#a4b5b3}label{display:block;margin-top:20px}input,button{font:inherit;width:100%;padding:13px;border-radius:7px;margin-top:7px;border:1px solid #526460;background:#1b2425;color:inherit}button{margin-top:28px;background:#b8e2d0;color:#13221c;font-weight:650;cursor:pointer}input:focus-visible,button:focus-visible{outline:2px solid #b8e2d0;outline-offset:3px}.error{color:#ffbbb1}</style></head><body><main><small>THE JAZZ PROJECT</small><h1>Back to the music.</h1><p>Sign in with your Jazz username and password. You’ll return to the page you opened.</p>{{if .Error}}<p class="error" role="alert">{{.Error}}</p>{{end}}<form method="post" action="/jazz/sign-in"><input type="hidden" name="next" value="{{.Next}}"><label>Username<input name="username" autocomplete="username" required maxlength="256" autofocus></label><label>Password<input type="password" name="password" autocomplete="current-password" required maxlength="256"></label><button type="submit">Sign in</button></form><p><small style="letter-spacing:0;color:#a4b5b3">This browser stays signed in for seven days.</small></p></main></body></html>`))

func (a auth) signIn(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	next := safeNext(r.URL.Query().Get("next"))
	message := ""
	status := http.StatusOK
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		if c, err := r.Cookie(cookieName); err == nil {
			if _, ok := a.valid(c.Value); ok {
				http.Redirect(w, r, next, http.StatusSeeOther)
				return
			}
		}
	case http.MethodPost:
		if r.Header.Get("Origin") != "https://zachbednarke.com" || r.Header.Get("Sec-Fetch-Site") == "cross-site" {
			http.Error(w, "Same-origin sign-in required", http.StatusForbidden)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		if r.ParseForm() != nil {
			http.Error(w, "Invalid sign-in form", http.StatusBadRequest)
			return
		}
		next = safeNext(r.PostForm.Get("next"))
		password := r.PostForm.Get("password")
		validPassword := bcrypt.CompareHashAndPassword([]byte(a.Hash), []byte(password)) == nil
		if r.PostForm.Get("username") != a.User || !validPassword {
			message = "That username or password wasn’t recognized. Please try again."
			status = http.StatusUnauthorized
		} else {
			exp := a.now().Add(lifetime).Truncate(time.Second)
			http.SetCookie(w, &http.Cookie{Name: cookieName, Value: a.token(exp), Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode, Expires: exp, MaxAge: int(lifetime.Seconds())})
			http.Redirect(w, r, next, http.StatusSeeOther)
			return
		}
	default:
		w.Header().Set("Allow", "GET, HEAD, POST")
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if r.Method != http.MethodHead {
		_ = loginPage.Execute(w, struct{ Next, Error string }{next, message})
	}
}
