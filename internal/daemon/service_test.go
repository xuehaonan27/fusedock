package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/xuehaonan27/fusedock/internal/accounts"
	"github.com/xuehaonan27/fusedock/internal/credentials"
	"github.com/xuehaonan27/fusedock/internal/mountapi"
	"github.com/xuehaonan27/fusedock/internal/providers"
	brokerclient "github.com/xuehaonan27/fusedock/internal/providers/broker"
	"github.com/xuehaonan27/fusedock/pkg/provider"
	"github.com/zalando/go-keyring"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type secretStore struct {
	mu     sync.Mutex
	values map[string]string
}

func (s *secretStore) Get(_, key string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.values[key]
	if !ok {
		return "", keyring.ErrNotFound
	}
	return v, nil
}
func (s *secretStore) Set(_, key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.values[key] = value
	return nil
}
func (s *secretStore) Delete(_, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.values, key)
	return nil
}
func TestDaemonControlAndRestore(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "files")
	os.Mkdir(root, 0700)
	os.WriteFile(filepath.Join(root, "one.txt"), []byte("one"), 0600)
	store := &accounts.Store{Path: filepath.Join(base, "accounts", "accounts.json"), Secrets: &secretStore{values: map[string]string{}}}
	if err := store.Add(context.Background(), accounts.Account{Name: "local", Provider: "local", Username: "fixture", Options: map[string]string{"root": root}, BindingFields: map[string]string{"root": root}}, map[string][]byte{"fixture": []byte("fixture")}, true, false); err != nil {
		t.Fatal(err)
	}
	registry, err := providers.Builtin()
	if err != nil {
		t.Fatal(err)
	}
	ready := make(chan struct{}, 10)
	options := Options{Directory: filepath.Join(base, "daemon"), Accounts: store, Registry: registry, Mount: func(ctx context.Context, s provider.Session, _ string, o mountapi.Options) error {
		object, err := provider.Resolve(ctx, s, "one.txt")
		if err != nil {
			return err
		}
		r, err := s.OpenRead(ctx, object)
		if err != nil {
			return err
		}
		b := make([]byte, 3)
		_, err = r.ReadAt(ctx, b, 0)
		r.Close()
		if err != nil || string(b) != "one" {
			t.Errorf("mounted data: %q %v", b, err)
		}
		o.Ready()
		ready <- struct{}{}
		<-ctx.Done()
		return nil
	}}
	d, err := New(options)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Serve(ctx) }()
	deadline := time.Now().Add(3 * time.Second)
	for {
		var output any
		if Call(ctx, options.Directory, "status", struct{}{}, &output) == nil {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatal("control did not start")
		}
		time.Sleep(5 * time.Millisecond)
	}
	request := map[string]any{"mount": MountSpec{ID: "one", Account: "local", Path: filepath.Join(base, "mount"), Scope: "/", Persistent: true}}
	var mounted MountStatus
	if err := Call(ctx, options.Directory, "mount", request, &mounted); err != nil {
		cancel()
		t.Fatal(err)
	}
	select {
	case <-ready:
	case <-time.After(3 * time.Second):
		cancel()
		t.Fatal("mount did not start")
	}
	volume := d.Status()[0].VolumeID
	var access Access
	if err := Call(ctx, options.Directory, "attach", map[string]string{"id": "one"}, &access); err != nil {
		t.Fatal(err)
	}
	client, err := brokerclient.New().Open(ctx, provider.OpenRequest{Options: map[string]string{"endpoint": access.Endpoint}, Secrets: map[string][]byte{"token": []byte(access.Bootstrap)}})
	if err != nil {
		t.Fatal(err)
	}
	remote, err := provider.Resolve(ctx, client, "one.txt")
	if err != nil {
		t.Fatal(err)
	}
	read, err := client.OpenRead(ctx, remote)
	if err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 3)
	if _, err := read.ReadAt(ctx, b, 0); err != nil || string(b) != "one" {
		t.Fatalf("data plane: %q %v", b, err)
	}
	read.Close()
	client.Close()
	if err := store.Rename(ctx, "local", "renamed"); err != nil {
		t.Fatal(err)
	}
	if err := d.Reauthenticate(ctx, "renamed"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ready:
	case <-time.After(3 * time.Second):
		t.Fatal("renamed account did not reauthenticate")
	}
	if status := d.Status()[0]; status.VolumeID != volume || status.Spec.Account != "renamed" {
		t.Fatalf("reauthentication changed mount identity: %+v", status)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(options.Directory, "mounts.json"))
	if err != nil || !json.Valid(data) {
		t.Fatalf("saved mounts: %v", err)
	}
	d, err = New(options)
	if err != nil {
		t.Fatal(err)
	}
	d.Restore(context.Background())
	select {
	case <-ready:
	case <-time.After(3 * time.Second):
		t.Fatal("mount did not restore")
	}
	if d.Status()[0].VolumeID != volume {
		t.Fatal("restoration changed volume identity")
	}
	if err := d.Unmount(context.Background(), "one"); err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestUnmountFailureRemainsVisible(t *testing.T) {
	want := errors.New("native mount is busy")
	done := make(chan struct{})
	close(done)
	d := &Service{
		options: Options{Directory: t.TempDir()},
		mounts: map[string]*mounted{"busy": {
			ready:  true,
			status: MountStatus{Spec: MountSpec{ID: "busy"}, State: "failed", Error: want.Error()},
			result: want, done: done, cancel: func() {},
		}},
		desired: map[string]MountSpec{"busy": {ID: "busy"}},
	}
	if err := d.Unmount(context.Background(), "busy"); !errors.Is(err, want) {
		t.Fatalf("unmount returned %v", err)
	}
	if status := d.Status(); len(status) != 1 || status[0].State != "failed" {
		t.Fatalf("native failure disappeared from status: %+v", status)
	}
	if _, ok := d.desired["busy"]; !ok {
		t.Fatal("failed unmount discarded the saved mount")
	}
}

func TestLegacyAccountPersistenceRequiresStableIdentity(t *testing.T) {
	base := t.TempDir()
	path := filepath.Join(base, "accounts", "accounts.json")
	if err := credentials.EnsurePrivateParent(path); err != nil {
		t.Fatal(err)
	}
	config := `{"version":1,"default":"old","accounts":[{"name":"old","provider":"pku","username":"fixture"}]}`
	if err := os.WriteFile(path, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	registry, err := providers.Builtin()
	if err != nil {
		t.Fatal(err)
	}
	d, err := New(Options{Directory: filepath.Join(base, "daemon"), Accounts: &accounts.Store{Path: path, Secrets: &secretStore{values: map[string]string{}}}, Registry: registry, Mount: func(context.Context, provider.Session, string, mountapi.Options) error {
		t.Error("legacy persistence reached native mounting")
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	_, err = d.Mount(context.Background(), MountSpec{ID: "test", Account: "old", Path: filepath.Join(base, "mount"), Persistent: true})
	if err == nil || !strings.Contains(err.Error(), "AccountID") {
		t.Fatalf("legacy persistence returned %v", err)
	}
}

func TestRetryableUnmountKeepsSessionAndStopIntent(t *testing.T) {
	for _, stop := range []bool{false, true} {
		t.Run(fmt.Sprint(stop), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan struct{})
			spec := MountSpec{ID: "busy"}
			attempts := 0
			m := &mounted{status: MountStatus{Spec: spec, State: "mounted"}, cancel: cancel, done: done}
			m.unmount = func(context.Context) error {
				attempts++
				if attempts == 1 {
					return errors.New("native busy")
				}
				return nil
			}
			d := &Service{options: Options{Directory: t.TempDir()}, mounts: map[string]*mounted{"busy": m}, desired: map[string]MountSpec{"busy": spec}}
			go func() { <-ctx.Done(); close(done) }()
			detach := func() error {
				if stop {
					return d.PrepareStop(context.Background())
				}
				return d.Unmount(context.Background(), "busy")
			}
			if err := detach(); err == nil {
				t.Fatal("busy unmount succeeded")
			}
			if ctx.Err() != nil {
				t.Fatal("busy unmount cancelled native service")
			}
			if got := d.Status()[0]; got.State != "mounted" || got.Error == "" {
				t.Fatalf("busy status: %+v", got)
			}
			if _, ok := d.desired["busy"]; !ok {
				t.Fatal("busy volume lost mount intent")
			}
			if err := detach(); err != nil {
				t.Fatal(err)
			}
			if len(d.mounts) != 0 {
				t.Fatal("successful detach retained mount")
			}
			_, saved := d.desired["busy"]
			if saved != stop {
				t.Fatalf("saved=%v stop=%v", saved, stop)
			}
		})
	}
}

func TestExternalDetachAndFailedStartAllowRemount(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "files")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	store := &accounts.Store{Path: filepath.Join(base, "accounts", "accounts.json"), Secrets: &secretStore{values: map[string]string{}}}
	if err := store.Add(context.Background(), accounts.Account{Name: "local", Provider: "local", Username: "fixture", Options: map[string]string{"root": root}, BindingFields: map[string]string{"root": root}}, map[string][]byte{"fixture": []byte("fixture")}, true, false); err != nil {
		t.Fatal(err)
	}
	registry, err := providers.Builtin()
	if err != nil {
		t.Fatal(err)
	}
	attached := make(chan chan struct{}, 1)
	starts := 0
	d, err := New(Options{Directory: filepath.Join(base, "daemon"), Accounts: store, Registry: registry, Mount: func(ctx context.Context, s provider.Session, path string, o mountapi.Options) error {
		starts++
		if starts == 1 {
			return errors.New("initial native mount failed")
		}
		eject := make(chan struct{})
		o.RegisterUnmount(func(context.Context) error { close(eject); return nil })
		o.Ready()
		attached <- eject
		select {
		case <-eject:
			return nil
		case <-ctx.Done():
			return nil
		}
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	spec := MountSpec{ID: "test", Account: "local", Path: filepath.Join(base, "mount"), Scope: "/"}
	if _, err := d.Mount(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	wait := func() {
		t.Helper()
		d.mu.Lock()
		done := d.mounts[spec.ID].done
		d.mu.Unlock()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("mount worker did not exit")
		}
	}
	wait()
	if _, err := d.Mount(context.Background(), spec); err != nil {
		t.Fatalf("failed start blocked retry: %v", err)
	}
	close(<-attached)
	wait()
	d.mu.Lock()
	_, saved := d.desired[spec.ID]
	d.mu.Unlock()
	if saved {
		t.Fatal("external eject retained automatic restore intent")
	}
	if _, err := d.Mount(context.Background(), spec); err != nil {
		t.Fatalf("external eject blocked remount: %v", err)
	}
	<-attached
	if err := d.PrepareStop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, saved := d.desired[spec.ID]; !saved {
		t.Fatal("daemon stop discarded restore intent")
	}
}

func TestStopAfterFailedStartupReleasesRegistration(t *testing.T) {
	done := make(chan struct{})
	close(done)
	d := &Service{options: Options{Directory: t.TempDir()}, mounts: map[string]*mounted{"failed": {
		status: MountStatus{Spec: MountSpec{ID: "failed"}, State: "failed"}, result: errors.New("mountpoint missing"), done: done, cancel: func() {},
	}}, desired: map[string]MountSpec{"failed": {ID: "failed"}}}
	if err := d.PrepareStop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(d.mounts) != 0 || len(d.desired) != 1 {
		t.Fatal("failed-start cleanup changed mount intent")
	}
}
