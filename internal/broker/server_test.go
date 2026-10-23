package broker

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xuehaonan/fusedock/internal/providers/local"
	"github.com/xuehaonan/fusedock/pkg/provider"
)

const testBootstrap = "0123456789abcdef0123456789abcdef"

type fixture struct {
	server     *Server
	httpServer *httptest.Server
	client     *http.Client
	token      string
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "hello.txt"), []byte("hello broker range"), 0600); err != nil {
		t.Fatal(err)
	}
	session, err := local.Factory{}.Open(context.Background(), provider.OpenRequest{Options: map[string]string{"root": root}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	server, err := NewServer(session, testBootstrap)
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	t.Cleanup(httpServer.Close)
	client := &http.Client{}
	claim := post(t, client, httpServer.URL, testBootstrap, "/v1/claim", struct{}{})
	var response ClaimResponse
	decodeTestJSON(t, claim, &response)
	if response.Version != ProtocolVersion || len(response.Token) < 32 {
		t.Fatalf("unexpected claim: %+v", response)
	}
	return fixture{server: server, httpServer: httpServer, client: client, token: response.Token}
}

func TestBrokerClaimAndReadOnlyOperations(t *testing.T) {
	f := newFixture(t)
	rootResponse := post(t, f.client, f.httpServer.URL, f.token, "/v1/root", struct{}{})
	var root provider.Object
	decodeTestJSON(t, rootResponse, &root)
	if !root.IsDir() || root.ID == "" {
		t.Fatalf("unexpected root: %+v", root)
	}
	listResponse := post(t, f.client, f.httpServer.URL, f.token, "/v1/list", ListRequest{ParentID: root.ID})
	var objects []provider.Object
	decodeTestJSON(t, listResponse, &objects)
	if len(objects) != 1 || objects[0].Name != "hello.txt" {
		t.Fatalf("unexpected listing: %+v", objects)
	}
	lookup := post(t, f.client, f.httpServer.URL, f.token, "/v1/lookup", LookupRequest{ParentID: root.ID, Name: "hello.txt"})
	var file provider.Object
	decodeTestJSON(t, lookup, &file)
	read := postRaw(t, f.client, f.httpServer.URL, f.token, "/v1/read", ReadRequest{Object: file, Offset: 6, Length: 6})
	if string(read) != "broker" {
		t.Fatalf("unexpected range: %q", read)
	}
	stat := post(t, f.client, f.httpServer.URL, f.token, "/v1/stat", StatRequest{Object: file})
	var statObject provider.Object
	decodeTestJSON(t, stat, &statObject)
	if statObject.ID != file.ID || statObject.Size != file.Size {
		t.Fatalf("unexpected stat: %+v", statObject)
	}
}

func TestBrokerRejectsUnauthenticatedAndBootstrapDataRequests(t *testing.T) {
	f := newFixture(t)
	for _, tc := range []struct{ token, path string }{
		{"", "/v1/root"},
		{"wrong-token", "/v1/root"},
		{testBootstrap, "/v1/root"},
	} {
		response := post(t, f.client, f.httpServer.URL, tc.token, tc.path, struct{}{})
		if response.StatusCode != http.StatusUnauthorized {
			t.Errorf("token=%q path=%s status=%d", tc.token, tc.path, response.StatusCode)
		}
		response.Body.Close()
	}
	claim := post(t, f.client, f.httpServer.URL, testBootstrap, "/v1/claim", struct{}{})
	defer claim.Body.Close()
	if claim.StatusCode != http.StatusUnauthorized && claim.StatusCode != http.StatusConflict {
		t.Fatalf("bootstrap claimed twice: %d", claim.StatusCode)
	}
}

func TestBrokerRejectsBrowserOriginAndWrongHost(t *testing.T) {
	f := newFixture(t)
	req, _ := http.NewRequest(http.MethodPost, f.httpServer.URL+"/v1/root", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer "+f.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://attacker.example")
	response, err := f.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("origin status=%d", response.StatusCode)
	}
	response.Body.Close()

	f.server.SetExpectedHost("trusted.invalid:1234")
	req, _ = http.NewRequest(http.MethodPost, f.httpServer.URL+"/v1/root", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer "+f.token)
	req.Header.Set("Content-Type", "application/json")
	response, err = f.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusMisdirectedRequest {
		t.Fatalf("host status=%d", response.StatusCode)
	}
	response.Body.Close()
}

func TestBrokerRejectsUnknownWriteAndOversizedRead(t *testing.T) {
	f := newFixture(t)
	response := post(t, f.client, f.httpServer.URL, f.token, "/v1/write", map[string]string{"id": "x"})
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("write route status=%d", response.StatusCode)
	}
	response.Body.Close()
	response = post(t, f.client, f.httpServer.URL, f.token, "/v1/read", ReadRequest{
		Object: provider.Object{ID: "file", Name: "file", Kind: provider.KindFile, Size: MaxReadBytes + 1},
		Length: MaxReadBytes + 1,
	})
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("oversized read status=%d", response.StatusCode)
	}
	response.Body.Close()
}

func TestBrokerRejectsWrongContentType(t *testing.T) {
	f := newFixture(t)
	req, _ := http.NewRequest(http.MethodPost, f.httpServer.URL+"/v1/root", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer "+f.token)
	req.Header.Set("Content-Type", "text/plain")
	response, err := f.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("content type status=%d", response.StatusCode)
	}
}

func post(t *testing.T, client *http.Client, url, token, path string, value any) *http.Response {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(url, "/")+path, bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func postRaw(t *testing.T, client *http.Client, url, token, path string, value any) []byte {
	t.Helper()
	response := post(t, client, url, token, path, value)
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("status=%d body=%s", response.StatusCode, body)
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func decodeTestJSON(t *testing.T, response *http.Response, output any) {
	t.Helper()
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("status=%d body=%s", response.StatusCode, body)
	}
	if err := json.NewDecoder(response.Body).Decode(output); err != nil {
		t.Fatal(err)
	}
}
