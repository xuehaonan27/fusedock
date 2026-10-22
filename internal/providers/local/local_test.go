package local

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/xuehaonan/fusedock/pkg/provider"
	"github.com/xuehaonan/fusedock/pkg/providertest"
)

func TestSessionReadsWithinRoot(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	exposed := filepath.Join(base, "exposed")
	if err := os.MkdirAll(filepath.Join(exposed, "sub"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(exposed, "hello.txt"), []byte("hello world"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "outside.txt"), []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	symlinkAvailable := os.Symlink(filepath.Join(base, "outside.txt"), filepath.Join(exposed, "escape")) == nil

	factory, err := Factory{}.Open(ctx, provider.OpenRequest{Options: map[string]string{"root": exposed}})
	if err != nil {
		t.Fatal(err)
	}
	defer factory.Close()
	root, err := factory.Root(ctx)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := factory.List(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]provider.Object{}
	for _, entry := range entries {
		names[entry.Name] = entry
	}
	if _, ok := names["hello.txt"]; !ok {
		t.Fatal("regular file missing from listing")
	}
	if _, ok := names["sub"]; !ok {
		t.Fatal("directory missing from listing")
	}
	if symlinkAvailable {
		if _, ok := names["escape"]; ok {
			t.Fatal("escaping symlink was exposed")
		}
	}

	object, err := provider.Resolve(ctx, factory, "/hello.txt")
	if err != nil {
		t.Fatal(err)
	}
	reader, err := factory.OpenRead(ctx, object)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 5)
	if n, err := reader.ReadAt(ctx, buf, 6); n != 5 || err != nil && !errors.Is(err, io.EOF) {
		t.Fatalf("ReadAt=(%d,%v)", n, err)
	}
	if string(buf) != "world" {
		t.Fatalf("read %q", buf)
	}
	reader.Close()
	providertest.Run(t, factory, providertest.Options{
		MaxEntries: 10, ReadObject: &object, ReadOffset: 1, ReadLength: 4,
	})
}

func TestSessionRejectsTraversalIDs(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	session, err := Factory{}.Open(ctx, provider.OpenRequest{Options: map[string]string{"root": root}})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	for _, id := range []string{"../outside", "/etc/passwd", "..", ".", "sub/../outside", "sub//file"} {
		if _, err := session.Stat(ctx, provider.Object{ID: id}); !errors.Is(err, provider.ErrNotFound) {
			t.Errorf("stat %q: %v", id, err)
		}
	}
}
