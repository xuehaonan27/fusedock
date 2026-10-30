package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xuehaonan27/fusedock/internal/accounts"
	"github.com/xuehaonan27/fusedock/internal/daemon"
	"github.com/xuehaonan27/fusedock/internal/mountapi"
	"github.com/xuehaonan27/fusedock/internal/providers"
	"github.com/xuehaonan27/fusedock/pkg/provider"
)

func TestDaemonCLIListsAndReadsResolvedTarget(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "file"), []byte("daemon shared data"), 0600); err != nil {
		t.Fatal(err)
	}
	store := testAccountStore(t)
	if err := store.Add(ctx, accounts.Account{Name: "fixture", Provider: "local", Username: "fixture", Options: map[string]string{"root": root}, BindingFields: map[string]string{"root": root}}, map[string][]byte{"fixture": []byte("unused")}, true, false); err != nil {
		t.Fatal(err)
	}
	registry, err := providers.Builtin()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "daemon")
	ready := make(chan struct{})
	d, err := daemon.New(daemon.Options{Directory: dir, Accounts: store, Registry: registry, Mount: func(ctx context.Context, _ provider.Session, _ string, o mountapi.Options) error {
		o.Ready()
		close(ready)
		<-ctx.Done()
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- d.Serve(ctx) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	for {
		if daemon.Call(ctx, dir, "status", struct{}{}, nil) == nil {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(time.Millisecond):
		}
	}
	if _, err := d.Mount(ctx, daemon.MountSpec{ID: "fixture", Account: "fixture", Path: filepath.Join(root, "mount")}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ready:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	for _, tc := range []struct {
		operation string
		flags     []string
		expected  string
	}{
		{"cat", []string{"--path", "/file", "--offset", "7", "--length", "6"}, "shared"},
		{"cat", []string{"--path", "/file", "--offset", "999"}, ""},
		{"ls", []string{"--path", "/"}, "\"name\": \"file\""},
	} {
		var out, stderr bytes.Buffer
		args := append([]string{"daemon", tc.operation, "--directory", dir, "--id", "fixture"}, tc.flags...)
		if code := Run(ctx, args, IO{Out: &out, Err: &stderr, Registry: registry, Accounts: store}); code != 0 {
			t.Fatalf("%s returned %d: %s", tc.operation, code, &stderr)
		}
		if tc.operation == "cat" && out.String() != tc.expected || tc.operation == "ls" && !strings.Contains(out.String(), tc.expected) {
			t.Fatalf("%s output: %q", tc.operation, out.String())
		}
	}
}
