package main

import (
	"compress/gzip"
	"net/http"
	"strings"
	"sync"
)

// The board is ~1.6 MB of JSON for ~300 listings and neither Cloud Run nor the
// gateway compresses it; gzip cuts it several-fold (repetitive JSON). Only JSON
// responses to clients that accept gzip are compressed (never redirects,
// images or audio).

var trumpetGzipWriters = sync.Pool{New: func() any {
	w, _ := gzip.NewWriterLevel(nil, gzip.BestSpeed)
	return w
}}

type trumpetGzipResponse struct {
	http.ResponseWriter
	gz      *gzip.Writer
	decided bool
}

func (g *trumpetGzipResponse) WriteHeader(status int) {
	if !g.decided {
		g.decided = true
		h := g.Header()
		if status == http.StatusOK && strings.HasPrefix(h.Get("Content-Type"), "application/json") && h.Get("Content-Encoding") == "" {
			h.Del("Content-Length")
			h.Set("Content-Encoding", "gzip")
			g.gz = trumpetGzipWriters.Get().(*gzip.Writer)
			g.gz.Reset(g.ResponseWriter)
		}
	}
	g.ResponseWriter.WriteHeader(status)
}

func (g *trumpetGzipResponse) Write(b []byte) (int, error) {
	if !g.decided {
		g.WriteHeader(http.StatusOK)
	}
	if g.gz != nil {
		return g.gz.Write(b)
	}
	return g.ResponseWriter.Write(b)
}

func (g *trumpetGzipResponse) close() {
	if g.gz != nil {
		g.gz.Close()
		trumpetGzipWriters.Put(g.gz)
		g.gz = nil
	}
}

func acceptsGzip(r *http.Request) bool {
	for _, part := range strings.Split(r.Header.Get("Accept-Encoding"), ",") {
		coding, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		if strings.EqualFold(strings.TrimSpace(coding), "gzip") && strings.ReplaceAll(strings.TrimSpace(params), " ", "") != "q=0" {
			return true
		}
	}
	return false
}

func trumpetGzip(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Add("Vary", "Accept-Encoding")
		if !acceptsGzip(r) {
			next.ServeHTTP(w, r)
			return
		}
		g := &trumpetGzipResponse{ResponseWriter: w}
		defer g.close()
		next.ServeHTTP(g, r)
	})
}
