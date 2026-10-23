// Package webdavservice adds read-only authentication and browser-access
// controls around a provider-backed WebDAV file system.
package webdavservice

import (
	"crypto/subtle"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	dav "golang.org/x/net/webdav"
)

type Options struct {
	FileSystem     dav.FileSystem
	Username       string
	Token          string
	Prefix         string
	AllowedOrigins []string
	ExpiresAt      time.Time
}

type Service struct {
	username       string
	token          string
	prefix         string
	allowedOrigins map[string]bool
	expiresAt      time.Time
	handler        *dav.Handler
}

func New(options Options) (*Service, error) {
	if options.FileSystem == nil {
		return nil, errors.New("WebDAV file system is required")
	}
	if len(options.Token) < 32 {
		return nil, errors.New("WebDAV token is too short")
	}
	if options.Username == "" {
		options.Username = "fusedock"
	}
	if options.Prefix == "" {
		options.Prefix = "/webdav"
	}
	if !strings.HasPrefix(options.Prefix, "/") || strings.HasSuffix(options.Prefix, "/") {
		return nil, errors.New("WebDAV prefix must start with a slash and have no trailing slash")
	}
	origins := make(map[string]bool, len(options.AllowedOrigins))
	for _, origin := range options.AllowedOrigins {
		u, err := url.Parse(origin)
		if err != nil || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "http" && u.Scheme != "https") {
			return nil, errors.New("WebDAV allow-origin must be an HTTP(S) origin")
		}
		origins[origin] = true
	}
	return &Service{
		username: options.Username, token: options.Token,
		prefix: options.Prefix, allowedOrigins: origins, expiresAt: options.ExpiresAt,
		handler: &dav.Handler{Prefix: options.Prefix, FileSystem: options.FileSystem, LockSystem: dav.NewMemLS()},
	}, nil
}

func (s *Service) Handler() http.Handler { return http.HandlerFunc(s.serveHTTP) }

func (s *Service) serveHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Vary", "Origin")
	w.Header().Set("DAV", "1")
	w.Header().Set("MS-Author-Via", "DAV")
	w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
	if r.URL.RawQuery != "" {
		http.Error(w, "query strings are not accepted", http.StatusBadRequest)
		return
	}
	if r.URL.Path != s.prefix && !strings.HasPrefix(r.URL.Path, s.prefix+"/") {
		http.NotFound(w, r)
		return
	}
	origin := r.Header.Get("Origin")
	if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		http.Error(w, "cross-site requests are not allowed", http.StatusForbidden)
		return
	}
	if origin != "" {
		if !s.allowedOrigins[origin] {
			http.Error(w, "origin is not allowed", http.StatusForbidden)
			return
		}
		s.setCORS(w, origin)
		w.Header().Set("Cross-Origin-Resource-Policy", "cross-origin")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
	}
	if !s.expiresAt.IsZero() && time.Now().After(s.expiresAt) {
		w.Header().Set("WWW-Authenticate", `Basic realm="FuseDock WebDAV"`)
		http.Error(w, "WebDAV access token expired", http.StatusUnauthorized)
		return
	}
	if !s.authorized(r) {
		w.Header().Set("WWW-Authenticate", `Basic realm="FuseDock WebDAV"`)
		http.Error(w, "WebDAV credentials are required", http.StatusUnauthorized)
		return
	}
	if r.Method == http.MethodOptions {
		s.setAllow(w)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if !readOnlyMethod(r.Method) {
		s.setAllow(w)
		http.Error(w, "WebDAV access is read-only", http.StatusMethodNotAllowed)
		return
	}
	s.handler.ServeHTTP(w, r)
}

func (s *Service) authorized(r *http.Request) bool {
	if header := r.Header.Get("Authorization"); strings.HasPrefix(header, "Bearer ") {
		return constantEqual(strings.TrimPrefix(header, "Bearer "), s.token)
	}
	username, password, ok := r.BasicAuth()
	if !ok {
		return false
	}
	return constantEqual(username, s.username) && constantEqual(password, s.token)
}

func (s *Service) setAllow(w http.ResponseWriter) {
	w.Header().Set("Allow", "OPTIONS, GET, HEAD, PROPFIND")
	w.Header().Set("DAV", "1")
	w.Header().Set("MS-Author-Via", "DAV")
}

func (s *Service) setCORS(w http.ResponseWriter, origin string) {
	w.Header().Set("Access-Control-Allow-Origin", origin)
	w.Header().Set("Access-Control-Allow-Methods", "GET, HEAD, PROPFIND, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Authorization, Depth, Content-Type, Range, If")
	w.Header().Set("Access-Control-Expose-Headers", "ETag, Content-Length, Content-Range, Accept-Ranges, Last-Modified, DAV")
	w.Header().Set("Access-Control-Max-Age", "600")
}

func readOnlyMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, "PROPFIND":
		return true
	default:
		return false
	}
}

func constantEqual(left, right string) bool {
	if len(left) != len(right) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(left), []byte(right)) == 1
}
