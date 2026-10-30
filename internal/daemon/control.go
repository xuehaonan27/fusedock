package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/xuehaonan27/fusedock/internal/localtransport"
	"github.com/xuehaonan27/fusedock/internal/namespace"
)

var requests atomic.Uint64

// Serve exposes control only on an OS-authenticated local transport.
func (d *Service) Serve(ctx context.Context) error {
	listener, err := localtransport.Listen(d.options.Directory)
	if err != nil {
		return err
	}
	defer listener.Close()
	server := &http.Server{Handler: http.HandlerFunc(d.control), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, IdleTimeout: time.Minute, MaxHeaderBytes: 16 << 10}
	stop := context.AfterFunc(ctx, d.cancel)
	defer stop()
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	go d.Restore(d.ctx)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			d.revokeChangedAccounts()
		case err := <-done:
			if errors.Is(err, http.ErrServerClosed) {
				return nil
			}
			return err
		case <-d.ctx.Done():
			shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			err := server.Shutdown(shutdown)
			cancel()
			if err != nil {
				server.Close()
			}
			<-done
			return d.Close()
		}
	}
}
func (d *Service) control(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Request-ID", fmt.Sprint(requests.Add(1)))
	if r.Method != "POST" || r.Host != "fusedock" || r.Header.Get("Origin") != "" || r.URL.RawQuery != "" {
		http.Error(w, "invalid local control request", 400)
		return
	}
	var input struct {
		Mount     MountSpec           `json:"mount"`
		ID        string              `json:"id"`
		Account   string              `json:"account"`
		Namespace namespace.Namespace `json:"namespace"`
		Bytes     int64               `json:"bytes"`
	}
	body := http.MaxBytesReader(w, r.Body, 64<<10)
	decoder := json.NewDecoder(body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		http.Error(w, "invalid control request", 400)
		return
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		http.Error(w, "invalid control request", 400)
		return
	}
	var result any = struct{}{}
	var err error
	switch r.URL.Path {
	case "/control/v1/status":
		result = struct {
			Mounts []MountStatus `json:"mounts"`
			Cache  any           `json:"cache"`
		}{d.Status(), d.cache.Stats()}
	case "/control/v1/accounts":
		result, err = d.options.Accounts.Load(r.Context())
	case "/control/v1/attach":
		result, err = d.Attach(input.ID)
	case "/control/v1/mount":
		result, err = d.Mount(r.Context(), input.Mount)
	case "/control/v1/unmount":
		err = d.Unmount(r.Context(), input.ID)
	case "/control/v1/cache/info":
		result = d.cache.Stats()
	case "/control/v1/cache/clear":
		err = input.Namespace.Validate()
		if err == nil {
			err = d.cache.Clear(input.Namespace)
		}
	case "/control/v1/cache/prune":
		err = d.cache.Prune(input.Bytes)
	case "/control/v1/cache/verify":
		var count int
		count, err = d.cache.Verify()
		result = map[string]int{"verified": count}
	case "/control/v1/reauthenticate":
		err = d.Reauthenticate(r.Context(), input.Account)
	case "/control/v1/refresh":
		var key string
		if input.Account != "" {
			account, lookupErr := d.account(r.Context(), MountSpec{Account: input.Account})
			if lookupErr != nil {
				err = lookupErr
				break
			}
			key = accountKey(account)
		}
		d.mu.Lock()
		for _, s := range d.sessions {
			if key == "" || accountKey(s.account) == key {
				s.vfs.Invalidate("")
			}
		}
		d.mu.Unlock()
	case "/control/v1/shutdown":
		d.cancel()
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
		w.WriteHeader(http.StatusConflict)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	json.NewEncoder(w).Encode(result)
}
func Call(ctx context.Context, dir, operation string, input any, output any) error {
	data, err := json.Marshal(input)
	if err != nil {
		return err
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) { return localtransport.Dial(ctx, dir) }, DisableCompression: true}
	defer transport.CloseIdleConnections()
	req, err := http.NewRequestWithContext(ctx, "POST", "http://fusedock/control/v1/"+operation, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := transport.RoundTrip(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	decoder := json.NewDecoder(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode != 200 {
		var failure struct {
			Error string `json:"error"`
		}
		if decoder.Decode(&failure) == nil && failure.Error != "" {
			return errors.New(failure.Error)
		}
		return fmt.Errorf("control HTTP %d", resp.StatusCode)
	}
	if output == nil {
		return nil
	}
	return decoder.Decode(output)
}
func (d *Service) revokeChangedAccounts() {
	config, err := d.options.Accounts.Load(d.ctx)
	if err != nil {
		return
	}
	d.mu.Lock()
	var closeSessions []*session
	for key, s := range d.sessions {
		valid := false
		for _, a := range config.Accounts {
			if s.account.AccountID != "" {
				valid = a.AccountID == s.account.AccountID && a.Binding == s.account.Binding
			} else {
				valid = a.Name == s.account.Name && a.Provider == s.account.Provider && a.Username == s.account.Username
			}
			if valid {
				break
			}
		}
		if valid {
			continue
		}
		delete(d.sessions, key)
		closeSessions = append(closeSessions, s)
		for id, m := range d.mounts {
			if boundAccount(m.status.Spec, s.account) {
				for _, endpoint := range d.data {
					if endpoint.mountID == id {
						endpoint.cancel()
					}
				}
				if m.target != nil {
					m.target.Revoke()
				}
				m.cancel()
			}
		}
	}
	d.mu.Unlock()
	for _, s := range closeSessions {
		s.vfs.Close()
	}
}
