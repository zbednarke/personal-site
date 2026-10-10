package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// memoryObjects is the test/preview stand-in for the private GCS bucket.
type memoryObjects struct {
	mu         sync.Mutex
	objects    map[string][]byte
	types      map[string]string
	metadata   map[string]map[string]string
	signedBase string
	failDelete bool
}

func newMemoryObjects(signedBase string) *memoryObjects {
	return &memoryObjects{objects: map[string][]byte{}, types: map[string]string{}, metadata: map[string]map[string]string{}, signedBase: signedBase}
}

func (m *memoryObjects) Put(ctx context.Context, name, contentType string, metadata map[string]string, body []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.objects[name] = append([]byte(nil), body...)
	m.types[name] = contentType
	m.metadata[name] = metadata
	return nil
}

func (m *memoryObjects) Delete(ctx context.Context, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failDelete {
		return errors.New("simulated storage outage")
	}
	delete(m.objects, name)
	return nil
}

func (m *memoryObjects) Get(ctx context.Context, name string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	body, ok := m.objects[name]
	if !ok {
		return nil, errors.New("object not found")
	}
	return append([]byte(nil), body...), nil
}

func (m *memoryObjects) SignedGet(ctx context.Context, name string, expires time.Time) (string, error) {
	return m.signedBase + name + "?expires=" + expires.UTC().Format(time.RFC3339), nil
}

func (m *memoryObjects) count(prefix string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for k := range m.objects {
		if strings.HasPrefix(k, prefix) {
			n++
		}
	}
	return n
}

// ServeHTTP plays the role of the signed GCS URL in the browser preview.
func (m *memoryObjects) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/__preview_objects/")
	m.mu.Lock()
	body, ok := m.objects[name]
	ct := m.types[name]
	m.mu.Unlock()
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", ct)
	w.Write(body)
}

// offlineResolver keeps the browser preview off the network: only literal
// loopback fixture URLs (which skip DNS) can be fetched.
type offlineResolver struct{}

func (offlineResolver) LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error) {
	return nil, errors.New("preview: network disabled")
}
