// Package providertest checks the invariants every FuseDock provider must
// preserve. It operates on an already-authenticated session, so provider tests
// can use fixtures without giving this package access to credentials.
package providertest

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/xuehaonan/fusedock/pkg/provider"
)

type Options struct {
	// MaxEntries limits how many root entries a conformance run may inspect.
	// The default is 1000.
	MaxEntries int
	// ReadObject selects a regular file for range-read validation. Nil skips
	// provider reads, which is useful for read-only metadata fixtures.
	ReadObject *provider.Object
	ReadOffset int64
	ReadLength int
	Timeout    time.Duration
}

// Run verifies root discovery, one directory listing, namespace invariants,
// and optionally a random-access read.
func Run(t *testing.T, session provider.Session, options Options) {
	t.Helper()
	if session == nil {
		t.Fatal("providertest: nil session")
	}
	if options.MaxEntries <= 0 {
		options.MaxEntries = 1000
	}
	if options.Timeout <= 0 {
		options.Timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), options.Timeout)
	defer cancel()

	root, err := session.Root(ctx)
	if err != nil {
		t.Fatalf("providertest: root: %v", err)
	}
	if !root.IsDir() {
		t.Fatal("providertest: root is not a directory")
	}
	if root.ID == "" {
		t.Fatal("providertest: root has an empty ID")
	}
	if !provider.ValidName(root.Name) {
		t.Fatalf("providertest: root has invalid name %q", root.Name)
	}

	objects, err := session.List(ctx, root)
	if err != nil {
		t.Fatalf("providertest: list root: %v", err)
	}
	if len(objects) > options.MaxEntries {
		t.Fatalf("providertest: root returned %d entries; limit is %d", len(objects), options.MaxEntries)
	}
	names := make(map[string]bool, len(objects))
	ids := make(map[string]bool, len(objects))
	for _, object := range objects {
		if object.ID == "" {
			t.Errorf("providertest: %q has an empty ID", object.Name)
		}
		if object.ID != "" && ids[object.ID] {
			t.Errorf("providertest: duplicate object ID %q", object.ID)
		}
		ids[object.ID] = true
		if !provider.ValidName(object.Name) {
			t.Errorf("providertest: invalid child name %q", object.Name)
		}
		if names[object.Name] {
			t.Errorf("providertest: duplicate child name %q", object.Name)
		}
		names[object.Name] = true
		if object.Kind != provider.KindFile && object.Kind != provider.KindDirectory {
			t.Errorf("providertest: %q has invalid kind %d", object.Name, object.Kind)
		}
		if object.Kind == provider.KindFile && object.Size < 0 {
			t.Errorf("providertest: file %q has unknown size", object.Name)
		}
		if object.ParentID != root.ID {
			t.Errorf("providertest: %q parent ID %q does not match root %q", object.Name, object.ParentID, root.ID)
		}
	}
	if options.ReadObject == nil {
		return
	}
	object, err := session.Stat(ctx, *options.ReadObject)
	if err != nil {
		t.Fatalf("providertest: stat read object: %v", err)
	}
	if object.IsDir() {
		t.Fatal("providertest: read object is a directory")
	}
	if object.Size < 0 {
		t.Fatal("providertest: read object has unknown size")
	}
	if options.ReadOffset < 0 || options.ReadOffset > object.Size {
		t.Fatalf("providertest: invalid read offset %d for size %d", options.ReadOffset, object.Size)
	}
	if options.ReadLength <= 0 {
		options.ReadLength = 64 << 10
	}
	length := min(int64(options.ReadLength), object.Size-options.ReadOffset)
	if length <= 0 {
		return
	}
	handle, err := session.OpenRead(ctx, object)
	if err != nil {
		t.Fatalf("providertest: open read: %v", err)
	}
	defer func() {
		if err := handle.Close(); err != nil {
			t.Errorf("providertest: close read: %v", err)
		}
	}()
	buf := make([]byte, int(length))
	n, err := handle.ReadAt(ctx, buf, options.ReadOffset)
	if n != len(buf) {
		t.Errorf("providertest: read returned %d bytes, want %d", n, len(buf))
	}
	if err != nil && !errors.Is(err, io.EOF) {
		t.Errorf("providertest: read: %v", err)
	}
}
