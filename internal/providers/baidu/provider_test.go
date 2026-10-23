package baidu

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xuehaonan/fusedock/pkg/provider"
)

const content = "0123456789abcdefghijklmnopqrstuvwxyz"

func TestDeviceAuthorizationAndReadOnlySession(t *testing.T) {
	var polls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth/2.0/device/code":
			json.NewEncoder(w).Encode(map[string]any{
				"device_code": "device", "user_code": "USER-CODE",
				"verification_url": "https://example.test/verify", "expires_in": 30, "interval": 1,
			})
		case "/oauth/2.0/token":
			if r.URL.Query().Get("grant_type") == "device_token" {
				if polls.Add(1) == 1 {
					w.WriteHeader(http.StatusBadRequest)
					json.NewEncoder(w).Encode(map[string]string{"error": "authorization_pending"})
					return
				}
				json.NewEncoder(w).Encode(map[string]any{"access_token": "access", "refresh_token": "refresh", "expires_in": 3600})
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"access_token": "refreshed", "refresh_token": "refresh2", "expires_in": 3600})
		case "/rest/2.0/passport/users/getInfo":
			json.NewEncoder(w).Encode(map[string]string{"openid": "baidu-user"})
		case "/rest/2.0/xpan/file":
			if r.URL.Query().Get("access_token") == "" {
				http.Error(w, "missing token", http.StatusUnauthorized)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{
				"errno": 0,
				"list": []map[string]any{{
					"fs_id": 42, "server_filename": "sample.txt", "path": "/sample.txt", "size": len(content),
					"md5": "m1", "isdir": 0, "server_mtime": 1700000000,
				}},
			})
		case "/rest/2.0/xpan/multimedia":
			json.NewEncoder(w).Encode(map[string]any{
				"errno": 0,
				"list":  []map[string]any{{"dlink": serverURL(r) + "/download?signature=test"}},
			})
		case "/download":
			if r.Header.Get("User-Agent") != userAgent {
				http.Error(w, "bad user agent", http.StatusForbidden)
				return
			}
			if r.Header.Get("Range") != "bytes=5-10" {
				http.Error(w, "bad range", http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Range", fmt.Sprintf("bytes 5-10/%d", len(content)))
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write([]byte(content[5:11]))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	var openedURL string
	auth, err := (Factory{}).Authorize(context.Background(), provider.AuthorizeRequest{
		Options: map[string]string{"oauth_endpoint": server.URL, "api_endpoint": server.URL},
		Secrets: map[string][]byte{"app_key": []byte("app"), "secret_key": []byte("secret")},
		Notify:  func(string) {},
		OpenURL: func(target string) error { openedURL = target; return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if openedURL != "https://example.test/verify" || auth.Username != "baidu-user" || len(auth.State) == 0 {
		t.Fatalf("unexpected authorization: %+v opened=%q", auth, openedURL)
	}

	session, err := (Factory{}).Open(context.Background(), provider.OpenRequest{
		Username: auth.Username,
		Options:  map[string]string{"api_endpoint": server.URL, "oauth_endpoint": server.URL},
		Secrets:  auth.Secrets, State: auth.State,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	root, err := session.Root(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	objects, err := session.List(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if len(objects) != 1 || objects[0].Name != "sample.txt" || objects[0].Size != int64(len(content)) {
		t.Fatalf("unexpected listing: %+v", objects)
	}
	reader, err := session.OpenRead(context.Background(), objects[0])
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	buf := make([]byte, 6)
	if n, err := reader.ReadAt(context.Background(), buf, 5); n != 6 || err != nil {
		t.Fatalf("ReadAt=(%d,%v)", n, err)
	}
	if string(buf) != content[5:11] {
		t.Fatalf("range=%q", buf)
	}
}

func TestOpenRefreshesExpiredState(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth/2.0/token":
			if r.URL.Query().Get("grant_type") != "refresh_token" || r.URL.Query().Get("refresh_token") != "old-refresh" {
				http.Error(w, "bad refresh", http.StatusBadRequest)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"access_token": "new-access", "refresh_token": "new-refresh", "expires_in": 3600})
		case "/rest/2.0/xpan/file":
			if r.URL.Query().Get("access_token") != "new-access" {
				http.Error(w, "old token", http.StatusUnauthorized)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"errno": 0, "list": []any{}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	state, _ := json.Marshal(savedState{
		Version: stateVersion, APIEndpoint: server.URL, OAuthEndpoint: server.URL, AppKey: "app",
		Username: "baidu-user", AccessToken: "old-access", RefreshToken: "old-refresh", ExpiresAt: time.Now().Add(-time.Hour).Unix(),
	})
	var persisted savedState
	session, err := (Factory{}).Open(context.Background(), provider.OpenRequest{
		Username: "baidu-user",
		Options:  map[string]string{"api_endpoint": server.URL, "oauth_endpoint": server.URL},
		Secrets:  map[string][]byte{"app_key": []byte("app"), "secret_key": []byte("secret")},
		State:    state,
		SaveState: func(_ context.Context, data []byte) error {
			return json.Unmarshal(data, &persisted)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if _, err := session.List(context.Background(), provider.Object{ID: rootID, Kind: provider.KindDirectory}); err != nil {
		t.Fatal(err)
	}
	if persisted.AccessToken != "new-access" || persisted.RefreshToken != "new-refresh" {
		t.Fatalf("unexpected persisted state: %+v", persisted)
	}
}

func TestBaiduRejectsUntrustedEndpoint(t *testing.T) {
	for _, endpoint := range []string{"http://pan.baidu.com", "https://user@pan.baidu.com", "https://pan.baidu.com/path"} {
		if _, err := (Factory{}).Open(context.Background(), provider.OpenRequest{
			Options: map[string]string{"api_endpoint": endpoint},
			Secrets: map[string][]byte{"app_key": []byte("a"), "secret_key": []byte("s"), "access_token": []byte("t")},
		}); err == nil {
			t.Errorf("accepted endpoint %q", endpoint)
		}
	}
}

func serverURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}
