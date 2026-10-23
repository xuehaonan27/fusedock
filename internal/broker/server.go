package broker

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/xuehaonan/fusedock/pkg/provider"
)

const maxRequestBody = 64 << 10

type Server struct {
	session provider.Session

	mu           sync.Mutex
	bootstrap    string
	claimed      bool
	sessionToken string
	expectedHost string
	onClaim      func()
}

func NewServer(session provider.Session, bootstrap string) (*Server, error) {
	if session == nil {
		return nil, errors.New("broker session is required")
	}
	if len(bootstrap) < 32 {
		return nil, errors.New("broker bootstrap token is too short")
	}
	return &Server{session: session, bootstrap: bootstrap}, nil
}

func (s *Server) SetExpectedHost(host string) {
	s.mu.Lock()
	s.expectedHost = host
	s.mu.Unlock()
}

func (s *Server) expectedHostMatches(host string) bool {
	s.mu.Lock()
	expected := s.expectedHost
	s.mu.Unlock()
	return expected == "" || host == expected
}

// SetOnClaim installs a cleanup callback invoked after a bootstrap capability
// is accepted. The callback must not block on the broker's own request path.
func (s *Server) SetOnClaim(callback func()) { s.onClaim = callback }

func (s *Server) Handler() http.Handler { return http.HandlerFunc(s.serveHTTP) }

func (s *Server) Serve(ctx context.Context, listener net.Listener) error {
	s.SetExpectedHost(listener.Addr().String())
	server := &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    16 << 10,
	}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	select {
	case err := <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
		err := <-done
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func (s *Server) serveHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.Method != http.MethodPost {
		writeBrokerError(w, http.StatusMethodNotAllowed, "method", "POST is required")
		return
	}
	if r.Header.Get("Origin") != "" || r.Header.Get("Referer") != "" {
		writeBrokerError(w, http.StatusForbidden, "origin", "browser-originated requests are not allowed")
		return
	}
	if !s.expectedHostMatches(r.Host) {
		writeBrokerError(w, http.StatusMisdirectedRequest, "host", "unexpected broker host")
		return
	}
	if r.URL.RawQuery != "" {
		writeBrokerError(w, http.StatusBadRequest, "query", "query strings are not accepted")
		return
	}
	token, ok := bearerToken(r.Header.Get("Authorization"))
	if !ok {
		writeBrokerError(w, http.StatusUnauthorized, "auth", "bearer capability is required")
		return
	}

	if r.URL.Path == "/v1/claim" {
		s.mu.Lock()
		valid := !s.claimed && constantEqual(token, s.bootstrap)
		s.mu.Unlock()
		if !valid {
			writeBrokerError(w, http.StatusUnauthorized, "claim", "bootstrap capability is invalid or already used")
			return
		}
		sess, err := randomToken()
		if err != nil {
			writeBrokerError(w, http.StatusInternalServerError, "internal", "cannot create broker session")
			return
		}
		s.mu.Lock()
		if s.claimed || !constantEqual(token, s.bootstrap) {
			s.mu.Unlock()
			writeBrokerError(w, http.StatusConflict, "claim", "bootstrap capability has already been claimed")
			return
		}
		s.claimed, s.sessionToken = true, sess
		callback := s.onClaim
		s.mu.Unlock()
		if callback != nil {
			callback()
		}
		writeJSON(w, http.StatusOK, ClaimResponse{Version: ProtocolVersion, Token: sess})
		return
	}

	s.mu.Lock()
	validSession := s.sessionToken != "" && constantEqual(token, s.sessionToken)
	s.mu.Unlock()
	if !validSession {
		writeBrokerError(w, http.StatusUnauthorized, "auth", "broker session capability is invalid")
		return
	}

	switch r.URL.Path {
	case "/v1/root":
		s.handleRoot(w, r)
	case "/v1/list":
		s.handleList(w, r)
	case "/v1/lookup":
		s.handleLookup(w, r)
	case "/v1/stat":
		s.handleStat(w, r)
	case "/v1/read":
		s.handleRead(w, r)
	case "/v1/close":
		s.mu.Lock()
		s.sessionToken = ""
		s.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]bool{"closed": true})
	default:
		writeBrokerError(w, http.StatusNotFound, "route", "unknown broker operation")
	}
}

func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	if err := requireJSON(r); err != nil {
		writeBrokerError(w, http.StatusBadRequest, "request", err.Error())
		return
	}
	object, err := s.session.Root(r.Context())
	if err != nil {
		writeProviderError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, object)
}

func (s *Server) handleList(w http.ResponseWriter, r *http.Request) {
	var request ListRequest
	if err := decodeJSON(r, &request); err != nil {
		writeBrokerError(w, http.StatusBadRequest, "request", err.Error())
		return
	}
	if !validObjectID(request.ParentID) {
		writeBrokerError(w, http.StatusBadRequest, "object", "invalid parent object ID")
		return
	}
	objects, err := s.session.List(r.Context(), provider.Object{ID: request.ParentID, Kind: provider.KindDirectory})
	if err != nil {
		writeProviderError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, objects)
}

func (s *Server) handleLookup(w http.ResponseWriter, r *http.Request) {
	var request LookupRequest
	if err := decodeJSON(r, &request); err != nil {
		writeBrokerError(w, http.StatusBadRequest, "request", err.Error())
		return
	}
	if !validObjectID(request.ParentID) || !provider.ValidName(request.Name) {
		writeBrokerError(w, http.StatusBadRequest, "object", "invalid lookup request")
		return
	}
	objects, err := s.session.List(r.Context(), provider.Object{ID: request.ParentID, Kind: provider.KindDirectory})
	if err != nil {
		writeProviderError(w, err)
		return
	}
	for _, object := range objects {
		if object.Name == request.Name {
			writeJSON(w, http.StatusOK, object)
			return
		}
	}
	writeBrokerError(w, http.StatusNotFound, "not_found", "object not found")
}

func (s *Server) handleStat(w http.ResponseWriter, r *http.Request) {
	var request StatRequest
	if err := decodeJSON(r, &request); err != nil {
		writeBrokerError(w, http.StatusBadRequest, "request", err.Error())
		return
	}
	if !validObject(request.Object) {
		writeBrokerError(w, http.StatusBadRequest, "object", "invalid object metadata")
		return
	}
	object, err := s.session.Stat(r.Context(), request.Object)
	if err != nil {
		writeProviderError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, object)
}

func (s *Server) handleRead(w http.ResponseWriter, r *http.Request) {
	var request ReadRequest
	if err := decodeJSON(r, &request); err != nil {
		writeBrokerError(w, http.StatusBadRequest, "request", err.Error())
		return
	}
	if !validObject(request.Object) || request.Object.IsDir() || request.Offset < 0 || request.Length < 0 || request.Length > MaxReadBytes {
		writeBrokerError(w, http.StatusBadRequest, "range", "invalid read object or range")
		return
	}
	if request.Length == 0 {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		return
	}
	reader, err := s.session.OpenRead(r.Context(), request.Object)
	if err != nil {
		writeProviderError(w, err)
		return
	}
	defer reader.Close()
	buffer := make([]byte, int(request.Length))
	n, readErr := reader.ReadAt(r.Context(), buffer, request.Offset)
	if n < 0 || n > len(buffer) {
		writeBrokerError(w, http.StatusInternalServerError, "internal", "provider returned an invalid read count")
		return
	}
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		writeProviderError(w, readErr)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", fmt.Sprint(n))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(buffer[:n])
}

func decodeJSON(r *http.Request, output any) error {
	if err := requireJSON(r); err != nil {
		return err
	}
	defer r.Body.Close()
	decoder := json.NewDecoder(io.LimitReader(r.Body, maxRequestBody+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		return errors.New("invalid JSON request")
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("request must contain one JSON value")
	}
	return nil
}

func requireJSON(r *http.Request) error {
	contentType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || contentType != "application/json" {
		return errors.New("content type must be application/json")
	}
	return nil
}

func validObject(object provider.Object) bool {
	return validObjectID(object.ID) && (object.Kind == provider.KindFile || object.Kind == provider.KindDirectory)
}

func validObjectID(id string) bool {
	if id == "" || len(id) > 8192 || strings.TrimSpace(id) != id {
		return false
	}
	for _, r := range id {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func bearerToken(header string) (string, bool) {
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return "", false
	}
	token := strings.TrimPrefix(header, prefix)
	return token, token != "" && len(token) <= 256
}

func constantEqual(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func randomToken() (string, error) {
	var data [32]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(data[:]), nil
}

func writeProviderError(w http.ResponseWriter, err error) {
	code, status := "internal", http.StatusInternalServerError
	message := "provider operation failed"
	switch {
	case errors.Is(err, provider.ErrNotFound):
		code, status, message = "not_found", http.StatusNotFound, "object not found"
	case errors.Is(err, provider.ErrPermission):
		code, status, message = "permission", http.StatusForbidden, "permission denied"
	case errors.Is(err, provider.ErrConflict):
		code, status, message = "conflict", http.StatusConflict, "object changed"
	case errors.Is(err, provider.ErrNotDirectory):
		code, status, message = "not_directory", http.StatusBadRequest, "object is not a directory"
	case errors.Is(err, provider.ErrUnsupported):
		code, status, message = "unsupported", http.StatusNotImplemented, "operation is not supported"
	case errors.Is(err, context.Canceled):
		code, status, message = "cancelled", http.StatusRequestTimeout, "request was cancelled"
	case errors.Is(err, context.DeadlineExceeded):
		code, status, message = "timeout", http.StatusGatewayTimeout, "provider request timed out"
	}
	writeBrokerError(w, status, code, message)
}

func writeBrokerError(w http.ResponseWriter, status int, code, message string) {
	if status == http.StatusUnauthorized {
		w.Header().Set("WWW-Authenticate", "Bearer")
	}
	writeJSON(w, status, ErrorResponse{Code: code, Message: message})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
