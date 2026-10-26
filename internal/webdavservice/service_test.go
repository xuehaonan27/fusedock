package webdavservice

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xuehaonan/fusedock/internal/providers/local"
	"github.com/xuehaonan/fusedock/internal/webdavfs"
	"github.com/xuehaonan/fusedock/pkg/provider"
)

const testToken = "0123456789abcdef0123456789abcdef"

func TestWebDAVReadOnlySurface(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "sub"), 0700); err != nil {
		t.Fatal(err)
	}
	data := []byte("0123456789abcdefghijklmnopqrstuvwxyz")
	if err := os.WriteFile(filepath.Join(root, "sample.txt"), data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sub", "scoped.txt"), []byte("scoped"), 0600); err != nil {
		t.Fatal(err)
	}
	server := newTestServer(t, root, "/", nil)
	client := server.Client()

	request, _ := http.NewRequest(http.MethodGet, server.URL+"/webdav/sample.txt", nil)
	response := do(t, client, request)
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status=%d", response.StatusCode)
	}
	response.Body.Close()

	request, _ = http.NewRequest(http.MethodGet, server.URL+"/webdav/sample.txt", nil)
	request.SetBasicAuth("fusedock", testToken)
	request.Header.Set("Range", "bytes=10-19")
	response = do(t, client, request)
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusPartialContent || string(body) != string(data[10:20]) {
		t.Fatalf("range status=%d body=%q", response.StatusCode, body)
	}
	etag := response.Header.Get("ETag")
	if etag == "" || response.Header.Get("Accept-Ranges") != "bytes" {
		t.Fatalf("range headers ETag=%q Accept-Ranges=%q", etag, response.Header.Get("Accept-Ranges"))
	}
	request, _ = http.NewRequest(http.MethodGet, server.URL+"/webdav/sample.txt", nil)
	request.SetBasicAuth("fusedock", testToken)
	request.Header.Set("If-None-Match", etag)
	response = do(t, client, request)
	response.Body.Close()
	if response.StatusCode != http.StatusNotModified {
		t.Fatalf("conditional GET status=%d", response.StatusCode)
	}

	request, _ = http.NewRequest("PROPFIND", server.URL+"/webdav/", strings.NewReader(`<?xml version="1.0"?><D:propfind xmlns:D="DAV:"><D:allprop/></D:propfind>`))
	request.Header.Set("Authorization", "Bearer "+testToken)
	request.Header.Set("Depth", "1")
	request.Header.Set("Content-Type", "application/xml")
	response = do(t, client, request)
	body, _ = io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusMultiStatus || !strings.Contains(string(body), "sample.txt") || !strings.Contains(string(body), "sub") {
		t.Fatalf("propfind status=%d body=%s", response.StatusCode, body)
	}
	if response.Header.Get("DAV") != "1" {
		t.Fatalf("DAV class = %q", response.Header.Get("DAV"))
	}

	request, _ = http.NewRequest("PROPFIND", server.URL+"/webdav/", strings.NewReader(`<?xml version="1.0"?><D:propfind xmlns:D="DAV:"><D:allprop/></D:propfind>`))
	request.SetBasicAuth("fusedock", testToken)
	request.Header.Set("Depth", "infinity")
	request.Header.Set("Content-Type", "application/xml")
	response = do(t, client, request)
	response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("infinite PROPFIND status=%d, want 403", response.StatusCode)
	}

	request, _ = http.NewRequest(http.MethodPut, server.URL+"/webdav/new.txt", strings.NewReader("x"))
	request.SetBasicAuth("fusedock", testToken)
	response = do(t, client, request)
	response.Body.Close()
	if response.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("write status=%d", response.StatusCode)
	}
}

func TestWebDAVScopeAndCORS(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "sub"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sub", "scoped.txt"), []byte("scoped"), 0600); err != nil {
		t.Fatal(err)
	}
	server := newTestServer(t, root, "/sub", []string{"https://app.example"})
	client := server.Client()

	request, _ := http.NewRequest(http.MethodGet, server.URL+"/webdav/scoped.txt", nil)
	request.SetBasicAuth("fusedock", testToken)
	response := do(t, client, request)
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("scoped GET status=%d", response.StatusCode)
	}
	request, _ = http.NewRequest(http.MethodGet, server.URL+"/webdav/../outside", nil)
	request.SetBasicAuth("fusedock", testToken)
	response = do(t, client, request)
	response.Body.Close()
	if response.StatusCode == http.StatusOK {
		t.Fatal("scope escape returned data")
	}

	request, _ = http.NewRequest(http.MethodOptions, server.URL+"/webdav/scoped.txt", nil)
	request.Header.Set("Origin", "https://app.example")
	request.Header.Set("Access-Control-Request-Method", "PROPFIND")
	response = do(t, client, request)
	response.Body.Close()
	if response.StatusCode != http.StatusNoContent || response.Header.Get("Access-Control-Allow-Origin") != "https://app.example" {
		t.Fatalf("preflight status=%d origin=%q", response.StatusCode, response.Header.Get("Access-Control-Allow-Origin"))
	}
	request, _ = http.NewRequest(http.MethodGet, server.URL+"/webdav/scoped.txt", nil)
	request.SetBasicAuth("fusedock", testToken)
	request.Header.Set("Origin", "https://app.example")
	response = do(t, client, request)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || response.Header.Get("Access-Control-Allow-Origin") != "https://app.example" {
		t.Fatalf("allowed origin GET status=%d origin=%q", response.StatusCode, response.Header.Get("Access-Control-Allow-Origin"))
	}

	request, _ = http.NewRequest(http.MethodOptions, server.URL+"/webdav/scoped.txt", nil)
	request.Header.Set("Origin", "https://attacker.example")
	response = do(t, client, request)
	response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("disallowed origin status=%d", response.StatusCode)
	}

	request, _ = http.NewRequest(http.MethodGet, server.URL+"/webdav/scoped.txt", nil)
	request.SetBasicAuth("fusedock", testToken)
	request.Header.Set("Sec-Fetch-Site", "cross-site")
	response = do(t, client, request)
	response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-site request status=%d", response.StatusCode)
	}
}

func TestWebDAVScopeCannotResolveFile(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "file.txt"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	session, err := local.Factory{}.Open(context.Background(), provider.OpenRequest{Options: map[string]string{"root": root}})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if _, err := webdavfs.New(context.Background(), session, "/file.txt"); err == nil {
		t.Fatal("file was accepted as WebDAV scope")
	}
}

func newTestServer(t *testing.T, root, scope string, origins []string) *httptest.Server {
	t.Helper()
	session, err := local.Factory{}.Open(context.Background(), provider.OpenRequest{Options: map[string]string{"root": root}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	fs, err := webdavfs.New(context.Background(), session, scope)
	if err != nil {
		t.Fatal(err)
	}
	service, err := New(Options{FileSystem: fs, Username: "fusedock", Token: testToken, AllowedOrigins: origins})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(service.Handler())
	t.Cleanup(server.Close)
	return server
}

func do(t *testing.T, client *http.Client, request *http.Request) *http.Response {
	t.Helper()
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}
