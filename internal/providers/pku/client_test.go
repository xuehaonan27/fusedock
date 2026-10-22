package pku

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/xuehaonan/fusedock/pkg/provider"
)

type staticToken string

func (s staticToken) AccessToken() string                 { return string(s) }
func (staticToken) Refresh(context.Context, string) error { return nil }

func TestAnyShareNamespaceAndRangeRead(t *testing.T) {
	var server *httptest.Server
	content := []byte("hello")
	mux := http.NewServeMux()
	mux.HandleFunc("/api/efast/v1/entry-doc-lib", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer token" {
			http.Error(w, "missing token", http.StatusUnauthorized)
			return
		}
		json.NewEncoder(w).Encode([]map[string]any{{"id": "gns://library", "name": "Library", "size": -1, "type": "doc_lib"}})
	})
	mux.HandleFunc("/api/efast/v1/folders/", func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/sub_objects") || r.URL.Query().Get("sort") != "name" {
			http.Error(w, "bad query", http.StatusBadRequest)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"dirs": []any{}, "files": []map[string]any{{
			"id": "gns://library/hello.txt", "name": "hello.txt", "size": len(content), "rev": "r1",
			"modified_at": "2026-10-22T08:00:00Z",
		}}})
	})
	mux.HandleFunc("/api/efast/v1/file/osdownload", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"authrequest": []string{"GET", server.URL + "/object", "X-Signed: yes"}})
	})
	mux.HandleFunc("/object", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Signed") != "yes" || r.Header.Get("Range") != "bytes=1-3" {
			http.Error(w, "bad signed range", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes 1-3/%d", len(content)))
		w.WriteHeader(http.StatusPartialContent)
		w.Write(content[1:4])
	})
	server = httptest.NewServer(mux)
	defer server.Close()

	client, err := newAnyShareClient(server.URL, staticToken("token"))
	if err != nil {
		t.Fatal(err)
	}
	session := &Session{client: client, endpoint: server.URL}
	defer session.Close()
	ctx := context.Background()
	root, err := session.Root(ctx)
	if err != nil {
		t.Fatal(err)
	}
	object, err := provider.Resolve(ctx, session, "Library/hello.txt")
	if err != nil {
		t.Fatal(err)
	}
	if object.ID != "gns://library/hello.txt" || object.Size != int64(len(content)) || object.ParentID != "gns://library" {
		t.Fatalf("unexpected object: %+v", object)
	}
	reader, err := session.OpenRead(ctx, object)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	buf := make([]byte, 3)
	if n, err := reader.ReadAt(ctx, buf, 1); n != 3 || err != nil {
		t.Fatalf("ReadAt=(%d,%v)", n, err)
	}
	if string(buf) != "ell" {
		t.Fatalf("range read got %q", buf)
	}
	if object.ParentID != "gns://library" {
		t.Fatalf("root=%+v", root)
	}
}

func TestParsePage(t *testing.T) {
	page := `<html><body><script id="__NEXT_DATA__" type="application/json">{"props":{"pageProps":{"challenge":"change-me","authconfig":{"thirdauth":{"config":{"authServer":"/web/v1/sso"}}}}}}</script></body></html>`
	props, err := parsePage([]byte(page))
	if err != nil {
		t.Fatal(err)
	}
	if props.Challenge != "change-me" || props.Auth.Third.Config.Server != "/web/v1/sso" {
		t.Fatalf("unexpected page: %+v", props)
	}
}

func TestClassifyHTTPError(t *testing.T) {
	if !errors.Is(classifyHTTPError(http.StatusNotFound, nil), provider.ErrNotFound) {
		t.Fatal("404 was not mapped to ErrNotFound")
	}
	if !errors.Is(classifyHTTPError(http.StatusUnauthorized, nil), provider.ErrPermission) {
		t.Fatal("401 was not mapped to ErrPermission")
	}
	if err := classifyHTTPError(http.StatusInternalServerError, []byte(strings.Repeat("x", 20))); err == nil {
		t.Fatal("500 was not reported")
	}
}

func TestSignedReadRejectsWriteMethods(t *testing.T) {
	client, err := newAnyShareClient("https://disk.example", staticToken("token"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.signedRequest(context.Background(), []string{"POST", "https://objects.example/file"}, nil); err == nil {
		t.Fatal("signed write method was accepted by a read-only provider")
	}
}

func TestSafeTransportErrorDropsSignedURL(t *testing.T) {
	signed := "https://objects.example/file?X-Amz-Credential=secret"
	err := safeHTTPError(&url.Error{Op: "Get", URL: signed, Err: errors.New("connection reset")})
	if strings.Contains(err.Error(), "objects.example") || strings.Contains(err.Error(), "secret") {
		t.Fatalf("transport error leaked signed URL: %v", err)
	}
}

func TestRestoreRefreshesAndPersistsState(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/anyshare/oauth2/login/refreshToken" {
			http.NotFound(w, r)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "client.oauth2_token", Value: "new-access", Path: "/"})
		http.SetCookie(w, &http.Cookie{Name: "client.oauth2_refresh_token", Value: "new-refresh", Path: "/"})
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	var saved State
	auth, err := newAuthSession(server.URL, server.URL, func(_ context.Context, state State) error {
		saved = state
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer auth.Close()
	if err := auth.Restore(context.Background(), State{AccessToken: "old-access", RefreshToken: "old-refresh"}); err != nil {
		t.Fatal(err)
	}
	if auth.AccessToken() != "new-access" || saved.AccessToken != "new-access" || saved.RefreshToken != "new-refresh" {
		t.Fatalf("token=%q saved=%+v", auth.AccessToken(), saved)
	}
}

func TestRestoreRejectsExpiredState(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "expired", http.StatusUnauthorized)
	}))
	defer server.Close()
	auth, err := newAuthSession(server.URL, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer auth.Close()
	err = auth.Restore(context.Background(), State{AccessToken: "old-access", RefreshToken: "old-refresh"})
	if !errors.Is(err, ErrStateExpired) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLivePKULoginPage(t *testing.T) {
	if os.Getenv("FUSEDOCK_LIVE_PKU") != "1" {
		t.Skip("set FUSEDOCK_LIVE_PKU=1 to check the public PKU login page")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	auth, err := newAuthSession(defaultEndpoint, defaultIAAA, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer auth.Close()
	data, _, err := auth.get(ctx, auth.base.String()+"/anyshare/oauth2/login?lang=zh-cn&redirect="+url.QueryEscape("/anyshare/"))
	if err != nil {
		t.Fatal(err)
	}
	page, err := parsePage(data)
	if err != nil {
		t.Fatal(err)
	}
	if page.Challenge == "" || page.Auth.Third.Config.Server == "" {
		t.Fatalf("live login page omitted required configuration: %+v", page)
	}
}
