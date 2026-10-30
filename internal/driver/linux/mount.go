//go:build linux

// Package fusefs translates the provider session into a read-only FUSE file
// system. It contains no provider-specific business logic.
package linux

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"

	"github.com/xuehaonan27/fusedock/internal/driver/gofuse"
	"github.com/xuehaonan27/fusedock/pkg/provider"
)

// Mount serves session until ctx is cancelled or the driver stops. The caller
// owns session.Close and may reuse it after the mount returns.
func Mount(ctx context.Context, session provider.Session, mountpoint string, options Options) error {
	if session == nil {
		return errors.New("missing provider session")
	}
	if !options.ReadOnly {
		return errors.New("only read-only mounts are supported")
	}
	point, err := ValidateMountpoint(mountpoint)
	if err != nil {
		return err
	}
	rootObject, err := session.Root(ctx)
	if err != nil {
		return err
	}
	if !rootObject.IsDir() {
		return provider.ErrNotDirectory
	}
	attrTTL, entryTTL := options.AttrTTL, options.EntryTTL
	if attrTTL <= 0 {
		attrTTL = 2 * time.Second
	}
	if entryTTL <= 0 {
		entryTTL = 2 * time.Second
	}
	name := options.VolumeName
	if name == "" {
		name = "FuseDock"
	}
	mountOptions := &fs.Options{
		MountOptions: fuse.MountOptions{
			Name: name, FsName: name, Debug: options.Debug, DisableXAttrs: true,
			Options: []string{"ro"},
		},
		EntryTimeout: &entryTTL,
		AttrTimeout:  &attrTTL,
	}
	root := gofuse.NewRoot(session, rootObject, attrTTL, entryTTL)
	server, err := fs.Mount(point, root, mountOptions)
	if err != nil {
		return mountError(err)
	}
	if options.Ready != nil {
		options.Ready()
	}
	done := make(chan struct{})
	go func() {
		server.Wait()
		close(done)
	}()
	select {
	case <-ctx.Done():
		if err := server.Unmount(); err != nil {
			return fmt.Errorf("unmount: %w", err)
		}
		<-done
		return nil
	case <-done:
		return nil
	}
}

func ValidateMountpoint(mountpoint string) (string, error) {
	if mountpoint == "" {
		return "", errors.New("mountpoint is required")
	}
	point, err := filepath.Abs(mountpoint)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(point)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", syscall.ENOTDIR
	}
	contents, err := os.ReadDir(point)
	if err != nil {
		return "", err
	}
	if len(contents) != 0 {
		return "", errors.New("mountpoint must be an empty directory")
	}
	return point, nil
}

func mountError(err error) error {
	message := err.Error()
	if strings.Contains(message, "no FUSE mount utility found") || strings.Contains(message, "/dev/fuse") {
		return fmt.Errorf("FUSE mount: %w\ninstall FUSE 3 and verify that /dev/fuse is accessible", err)
	}
	return fmt.Errorf("FUSE mount: %w", err)
}
