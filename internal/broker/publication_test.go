package broker

import (
	"context"
	"net/http"
	"testing"
)

func TestPublicationRequiresSessionAndChildNames(t *testing.T) {
	f := newFixture(t)
	var calls []PublicationRequest
	f.server.SetNamespacePublisher(func(_ context.Context, r PublicationRequest) error {
		calls = append(calls, r)
		return nil
	})
	for _, token := range []string{"", testBootstrap, "invalid"} {
		response := post(t, f.client, f.httpServer.URL, token, "/v2/publish", PublicationRequest{From: "stage", To: "file"})
		if response.StatusCode != http.StatusUnauthorized {
			t.Errorf("unauthenticated publication: %d", response.StatusCode)
		}
		response.Body.Close()
	}
	for _, name := range []string{"", ".", "..", "../outside", "/absolute", "child/file", "nul\x00name"} {
		for _, request := range []PublicationRequest{{From: name, To: "file"}, {From: "stage", To: name}} {
			response := post(t, f.client, f.httpServer.URL, f.token, "/v2/publish", request)
			if response.StatusCode != http.StatusBadRequest {
				t.Errorf("path publication %q: %d", name, response.StatusCode)
			}
			response.Body.Close()
		}
	}
	if len(calls) != 0 {
		t.Fatal("rejected request reached publisher")
	}
	request := PublicationRequest{From: ".fusedock-publish-example", To: "file.txt"}
	response := post(t, f.client, f.httpServer.URL, f.token, "/v2/publish", request)
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || len(calls) != 1 || calls[0] != request {
		t.Fatalf("publication: %d %v", response.StatusCode, calls)
	}
}

func TestPublicationUnavailableWithoutHostBinding(t *testing.T) {
	f := newFixture(t)
	response := post(t, f.client, f.httpServer.URL, f.token, "/v2/publish", PublicationRequest{From: "stage", To: "file"})
	defer response.Body.Close()
	if response.StatusCode == http.StatusOK {
		t.Fatal("publication enabled without a host binding")
	}
}
