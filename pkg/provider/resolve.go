package provider

import (
	"context"
	"fmt"
)

// Resolve walks a display path from the session root. It uses List rather than
// provider paths, so every provider gets the same path and traversal rules.
func Resolve(ctx context.Context, session Session, path string) (Object, error) {
	parts, err := CleanRelativePath(path)
	if err != nil {
		return Object{}, err
	}
	current, err := session.Root(ctx)
	if err != nil {
		return Object{}, err
	}
	for _, part := range parts {
		if !current.IsDir() {
			return Object{}, ErrNotDirectory
		}
		objects, err := session.List(ctx, current)
		if err != nil {
			return Object{}, err
		}
		found := false
		for _, object := range objects {
			if object.Name == part {
				current, found = object, true
				break
			}
		}
		if !found {
			return Object{}, fmt.Errorf("%w: %s", ErrNotFound, path)
		}
	}
	return current, nil
}
