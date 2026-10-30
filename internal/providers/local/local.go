// Package local mounts a directory through the same provider contract used by
// network backends. It is the reference implementation and a deterministic test
// fixture for mount, namespace, and range-read behavior.
package local

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/xuehaonan27/fusedock/pkg/provider"
)

const rootID = "local://root"

type Factory struct{}

func New() Factory { return Factory{} }

func (Factory) Descriptor() provider.Descriptor {
	return provider.Descriptor{
		ContractVersion: provider.ContractVersion,
		ID:              "local",
		Name:            "Local directory",
		Description:     "Expose a local directory through FuseDock's provider and mount layers.",
		Capabilities:    provider.CapRead,
		Options: []provider.OptionSpec{{
			Key: "root", Identity: true, Description: "Directory to expose", Required: true,
		}},
	}
}

func (Factory) Open(ctx context.Context, req provider.OpenRequest) (provider.Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	root := expandPath(req.Options["root"])
	if root == "" {
		return nil, errors.New("local provider requires --option root=PATH")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve local root: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("open local root: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("local root is not a directory: %s", abs)
	}
	osRoot, err := os.OpenRoot(abs)
	if err != nil {
		return nil, fmt.Errorf("open local root: %w", err)
	}
	name := filepath.Base(abs)
	if !provider.ValidName(name) {
		name = "Local"
	}
	return &Session{root: osRoot, name: name}, nil
}

type Session struct {
	root *os.Root
	name string
}

func (s *Session) Root(ctx context.Context) (provider.Object, error) {
	if err := ctx.Err(); err != nil {
		return provider.Object{}, err
	}
	return provider.Object{ID: rootID, Name: s.name, Kind: provider.KindDirectory}, nil
}

func (s *Session) Stat(ctx context.Context, object provider.Object) (provider.Object, error) {
	if err := ctx.Err(); err != nil {
		return provider.Object{}, err
	}
	if object.ID == rootID {
		return s.Root(ctx)
	}
	rel, err := s.relative(object.ID)
	if err != nil {
		return provider.Object{}, err
	}
	info, err := s.root.Stat(rel)
	if err != nil {
		return provider.Object{}, localError("stat", err)
	}
	return s.fromFileInfo(object.ID, object.ParentID, info), nil
}

func (s *Session) List(ctx context.Context, parent provider.Object) ([]provider.Object, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !parent.IsDir() {
		return nil, provider.ErrNotDirectory
	}
	rel := "."
	if parent.ID != rootID {
		var err error
		rel, err = s.relative(parent.ID)
		if err != nil {
			return nil, err
		}
	}
	dir, err := s.root.Open(rel)
	if err != nil {
		return nil, localError("list", err)
	}
	entries, readErr := dir.ReadDir(-1)
	closeErr := dir.Close()
	if readErr != nil {
		return nil, localError("list", readErr)
	}
	if closeErr != nil {
		return nil, localError("close directory", closeErr)
	}
	result := make([]provider.Object, 0, len(entries))
	for _, entry := range entries {
		childRel := entry.Name()
		if rel != "." {
			childRel = filepath.Join(rel, entry.Name())
		}
		// Stat through os.Root follows in-root symlinks while refusing paths that
		// escape the configured root. Special files are outside the provider model.
		info, err := s.root.Stat(childRel)
		if err != nil {
			continue
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			continue
		}
		id := filepath.ToSlash(childRel)
		object := s.fromFileInfo(id, parent.ID, info)
		object.Name = entry.Name()
		result = append(result, object)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, nil
}

func (s *Session) OpenRead(ctx context.Context, object provider.Object) (provider.Reader, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if object.IsDir() {
		return nil, provider.ErrNotDirectory
	}
	rel, err := s.relative(object.ID)
	if err != nil {
		return nil, err
	}
	file, err := s.root.Open(rel)
	if err != nil {
		return nil, localError("open", err)
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, localError("stat", err)
	}
	if info.IsDir() {
		file.Close()
		return nil, provider.ErrNotDirectory
	}
	return &reader{file: file}, nil
}

type reader struct{ file *os.File }

func (r *reader) ReadAt(ctx context.Context, dest []byte, off int64) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return r.file.ReadAt(dest, off)
}

func (r *reader) Close() error {
	if r.file == nil {
		return nil
	}
	err := r.file.Close()
	r.file = nil
	return err
}

func (s *Session) Close() error {
	if s.root == nil {
		return nil
	}
	err := s.root.Close()
	s.root = nil
	return err
}

func (s *Session) relative(id string) (string, error) {
	if id == "" || id == rootID || strings.HasPrefix(id, "/") || filepath.IsAbs(id) {
		return "", provider.ErrNotFound
	}
	for _, part := range strings.Split(filepath.ToSlash(id), "/") {
		if part == "" || part == "." || part == ".." || strings.ContainsRune(part, 0) {
			return "", provider.ErrNotFound
		}
	}
	return filepath.FromSlash(id), nil
}

func (s *Session) fromFileInfo(id, parent string, info os.FileInfo) provider.Object {
	kind := provider.KindFile
	size := info.Size()
	if info.IsDir() {
		kind, size = provider.KindDirectory, 0
	}
	return provider.Object{
		ID: id, ParentID: parent, Name: info.Name(), Kind: kind, Size: size,
		Version: fmt.Sprintf("%d:%d", info.ModTime().UnixNano(), info.Size()), Modified: info.ModTime(),
	}
}

func localError(operation string, err error) error {
	switch {
	case errors.Is(err, os.ErrNotExist):
		return fmt.Errorf("%w: %s", provider.ErrNotFound, operation)
	case errors.Is(err, os.ErrPermission):
		return fmt.Errorf("%w: %s", provider.ErrPermission, operation)
	case errors.Is(err, io.EOF):
		return err
	default:
		return fmt.Errorf("local %s: %w", operation, err)
	}
}

func expandPath(path string) string {
	if path == "~" || strings.HasPrefix(path, "~/") || strings.HasPrefix(path, `~\`) {
		home, err := os.UserHomeDir()
		if err == nil {
			if path == "~" {
				return home
			}
			return filepath.Join(home, filepath.FromSlash(path[2:]))
		}
	}
	return path
}
