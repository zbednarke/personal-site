package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/caddyserver/caddy/v2"
)

func TestPatchedStandardRuntimeServesHTTPAndHTTP2TLS(t *testing.T) {
	folder := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", folder)
	t.Setenv("XDG_DATA_HOME", folder)
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "localhost"},
		DNSNames: []string{"localhost"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	private, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certFile, keyFile := filepath.Join(folder, "test.pem"), filepath.Join(folder, "test-key.pem")
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := os.WriteFile(certFile, certPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: private}), 0o600); err != nil {
		t.Fatal(err)
	}
	listeners := make([]net.Listener, 2)
	addresses := make([]string, 2)
	for i := range listeners {
		listeners[i], err = net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addresses[i] = listeners[i].Addr().String()
		defer listeners[i].Close()
	}
	for _, listener := range listeners {
		listener.Close()
	}
	routes := []any{map[string]any{"handle": []any{map[string]any{"handler": "static_response", "body": "runtime-secure"}}}}
	config := map[string]any{
		"admin":   map[string]any{"disabled": true, "config": map[string]any{"persist": false}},
		"storage": map[string]any{"module": "file_system", "root": folder},
		"apps": map[string]any{
			"tls": map[string]any{"certificates": map[string]any{"load_files": []any{map[string]any{"certificate": certFile, "key": keyFile}}}},
			"http": map[string]any{"servers": map[string]any{
				"plain": map[string]any{"listen": []string{addresses[0]}, "routes": routes, "protocols": []string{"h1"}},
				"secure": map[string]any{"listen": []string{addresses[1]}, "routes": routes, "protocols": []string{"h1", "h2"},
					"tls_connection_policies": []any{map[string]any{}}, "automatic_https": map[string]any{"disable": true}},
			}},
		},
	}
	data, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := caddy.Load(data, true); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := caddy.Stop(); err != nil {
			t.Error(err)
		}
	})
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(certPEM) {
		t.Fatal("test certificate not loaded")
	}
	transport := &http.Transport{ForceAttemptHTTP2: true, TLSClientConfig: &tls.Config{RootCAs: roots, ServerName: "localhost"}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	for i, scheme := range []string{"http", "https"} {
		response, err := client.Get(scheme + "://" + addresses[i] + "/check")
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil || response.StatusCode != 200 || string(body) != "runtime-secure" {
			t.Fatalf("%s response: status=%d body=%q err=%v", scheme, response.StatusCode, body, err)
		}
		if i == 1 && response.ProtoMajor != 2 {
			t.Fatalf("want HTTP/2 over verified TLS, got %s", response.Proto)
		}
	}
}
