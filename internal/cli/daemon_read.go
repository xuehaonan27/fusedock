package cli

import (
	"context"
	"encoding/json"
	"errors"
	"io"

	"github.com/xuehaonan27/fusedock/internal/daemon"
	brokerclient "github.com/xuehaonan27/fusedock/internal/providers/broker"
	"github.com/xuehaonan27/fusedock/pkg/provider"
)

func readDaemon(ctx context.Context, dir, operation, mountID, path string, offset, length int64, out io.Writer) error {
	if offset < 0 || length < -1 {
		return errors.New("offset must be nonnegative and length must be -1 or nonnegative")
	}
	var access daemon.Access
	if err := daemon.Call(ctx, dir, "attach", map[string]string{"id": mountID}, &access); err != nil {
		return err
	}
	session, err := brokerclient.New().Open(ctx, provider.OpenRequest{
		Options: map[string]string{"endpoint": access.Endpoint},
		Secrets: map[string][]byte{"token": []byte(access.Bootstrap)},
	})
	if err != nil {
		return err
	}
	defer session.Close()
	object, err := provider.Resolve(ctx, session, path)
	if err != nil {
		return err
	}
	if operation == "ls" {
		objects, err := session.List(ctx, object)
		if err != nil {
			return err
		}
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		return encoder.Encode(objects)
	}
	reader, err := session.OpenRead(ctx, object)
	if err != nil {
		return err
	}
	defer reader.Close()
	if snapshot, ok := reader.(provider.SnapshotReader); ok {
		object = snapshot.Snapshot()
	}
	remaining := max(0, object.Size-offset)
	if length >= 0 {
		remaining = min(remaining, length)
	}
	buffer := make([]byte, min(64<<10, remaining))
	for remaining > 0 {
		n, err := reader.ReadAt(ctx, buffer[:min(int64(len(buffer)), remaining)], offset)
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		if n == 0 {
			return io.ErrUnexpectedEOF
		}
		written, err := out.Write(buffer[:n])
		if err != nil {
			return err
		}
		if written != n {
			return io.ErrShortWrite
		}
		offset += int64(n)
		remaining -= int64(n)
	}
	return nil
}
