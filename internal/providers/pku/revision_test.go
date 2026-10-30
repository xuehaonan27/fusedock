package pku

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/xuehaonan27/fusedock/pkg/provider"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestRevisionSurvivesRenewalCurrentChangeAndMissingRevision(t *testing.T) {
	var requests atomic.Int32
	var removed atomic.Bool
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "osdownload") {
			var input struct {
				Rev string `json:"rev"`
			}
			json.NewDecoder(r.Body).Decode(&input)
			if input.Rev != "original" {
				t.Errorf("read switched revision to %q", input.Rev)
			}
			if removed.Load() {
				http.Error(w, `{"code":404002005}`, 404)
				return
			}
			n := requests.Add(1)
			json.NewEncoder(w).Encode(map[string]any{"authrequest": []string{"GET", fmt.Sprintf("%s/object/%d", server.URL, n)}})
			return
		}
		w.Header().Set("Content-Range", "bytes 0-2/3")
		w.WriteHeader(206)
		w.Write([]byte("old"))
	}))
	defer server.Close()
	client, err := newAnyShareClient(server.URL, staticToken("token"))
	if err != nil {
		t.Fatal(err)
	}
	s := &Session{client: client}
	defer s.Close()
	r, err := s.OpenReadRevision(context.Background(), provider.ObjectRevision{ObjectID: "file", ContentVersion: "original", Size: 3})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	for range 3 {
		b := make([]byte, 3)
		if _, err := r.ReadAt(context.Background(), b, 0); err != nil || string(b) != "old" {
			t.Fatalf("renewed read: %q %v", b, err)
		}
	}
	if requests.Load() != 3 {
		t.Fatal("signed URL was not renewed")
	}
	removed.Store(true)
	if _, err := r.ReadAt(context.Background(), make([]byte, 3), 0); !errors.Is(err, provider.ErrConflict) {
		t.Fatalf("missing revision: %v", err)
	}
}
