//go:build darwin || linux || windows

package baidu

import (
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/xuehaonan/fusedock/internal/accounts"
	"github.com/xuehaonan/fusedock/pkg/provider"
)

// TestLiveBaiduDigestReadConsistency is read-only. It probes whether XPAN's
// listed MD5 matches the bytes returned through the current dlink reader.
// Passing it is necessary, but not sufficient, for enabling the small-file
// whole-file cache exception.
func TestLiveBaiduDigestReadConsistency(t *testing.T) {
	if os.Getenv("FUSEDOCK_LIVE_ACCOUNTS") != "1" {
		t.Skip("set FUSEDOCK_LIVE_ACCOUNTS=1 to run live account conformance")
	}
	name := strings.TrimSpace(os.Getenv("FUSEDOCK_LIVE_ACCOUNT"))
	if name == "" {
		name = "baidu"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	session := openLiveBaidu(t, ctx, name)
	defer session.Close()
	wire := session

	root, err := wire.Root(ctx)
	if err != nil {
		t.Fatal(err)
	}
	files, dirs, listed := collectLiveFiles(ctx, t, wire, root, 24, 2<<20, 8)
	t.Logf("listed_entries=%d candidate_files=%d remaining_directories=%d", listed, len(files), len(dirs))
	if len(files) == 0 {
		t.Fatal("no file at or below 2 MiB was found in the first 24 accessible directories")
	}

	checked, missingDigest, invalidDigest := 0, 0, 0
	for _, object := range files {
		wire.mu.Lock()
		entry, ok := wire.known[object.ID]
		wire.mu.Unlock()
		if !ok {
			t.Fatalf("object %q disappeared from session metadata", object.ID)
		}
		if entry.MD5 == "" {
			missingDigest++
			continue
		}
		first, err := readWhole(ctx, wire, object)
		if err != nil {
			t.Fatalf("read object %q: %v", object.ID, err)
		}
		second, err := readWhole(ctx, wire, object)
		if err != nil {
			t.Fatalf("second read object %q: %v", object.ID, err)
		}
		want, err := hex.DecodeString(entry.MD5)
		if err != nil || len(want) != md5.Size {
			invalidDigest++
			continue
		}
		downloaded := md5.Sum(first)
		if string(downloaded[:]) != string(want) {
			t.Fatalf("listed MD5 does not match downloaded bytes for object %q", object.ID)
		}
		if sha256.Sum256(first) != sha256.Sum256(second) {
			t.Fatalf("two sequential downloads differ for object %q", object.ID)
		}
		if len(first) > 0 {
			start := int64(len(first) / 3)
			length := min(int64(64<<10), int64(len(first))-start)
			if err := checkRange(ctx, wire, object, first, start, length); err != nil {
				t.Fatalf("range verification for object %q: %v", object.ID, err)
			}
		}
		checked++
	}
	t.Logf("digest_matches=%d files_without_digest=%d invalid_digest_fields=%d", checked, missingDigest, invalidDigest)
	if checked == 0 {
		t.Fatal("no candidate had a non-empty MD5 to compare")
	}
}

// TestLiveBaiduDigestFieldFormat inspects metadata only. Invalid digest fields
// are not eligible for the whole-file digest cache exception.
func TestLiveBaiduDigestFieldFormat(t *testing.T) {
	if os.Getenv("FUSEDOCK_LIVE_ACCOUNTS") != "1" {
		t.Skip("set FUSEDOCK_LIVE_ACCOUNTS=1 to run live account conformance")
	}
	name := strings.TrimSpace(os.Getenv("FUSEDOCK_LIVE_ACCOUNT"))
	if name == "" {
		name = "baidu"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	session := openLiveBaidu(t, ctx, name)
	defer session.Close()
	root, err := session.Root(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, _, _ = collectLiveFiles(ctx, t, session, root, 64, 2<<20, 1<<20)
	session.mu.Lock()
	entries := make([]meta, 0, len(session.known))
	for _, entry := range session.known {
		if !entry.Dir {
			entries = append(entries, entry)
		}
	}
	session.mu.Unlock()
	formats := make(map[string]int)
	invalidPattern := ""
	for _, entry := range entries {
		format := digestFormat(entry.MD5)
		formats[format]++
		if format == "32_non_hex" && invalidPattern == "" {
			invalidPattern = digestPattern(entry.MD5)
		}
	}
	t.Logf("file_metadata=%d digest_formats=%v", len(entries), formats)
	if invalidPattern != "" {
		t.Logf("non_hex_pattern=%s", invalidPattern)
	}
	if formats["32_hex"] == 0 {
		t.Fatal("no file had a canonical 32-digit hexadecimal MD5")
	}
}

func openLiveBaidu(t *testing.T, ctx context.Context, name string) *Session {
	t.Helper()
	store, err := accounts.Default()
	if err != nil {
		t.Fatal(err)
	}
	account, secrets, state, err := store.Credentials(ctx, name)
	if err != nil {
		t.Fatalf("read saved account: %v", err)
	}
	defer clearBytes(secrets)
	defer clear(state)
	if account.Provider != "baidu" {
		t.Fatalf("account provider = %q, want baidu", account.Provider)
	}
	opened, err := (Factory{}).Open(ctx, provider.OpenRequest{
		Username: account.Username,
		Options:  account.Options,
		Secrets:  secrets,
		State:    state,
	})
	if err != nil {
		t.Fatalf("open Baidu account: %v", err)
	}
	return opened.(*Session)
}

func digestFormat(value string) string {
	if value == "" {
		return "empty"
	}
	if len(value) != 32 {
		return fmt.Sprintf("length_%d", len(value))
	}
	if _, err := hex.DecodeString(value); err != nil {
		return "32_non_hex"
	}
	return "32_hex"
}

func digestPattern(value string) string {
	result := make([]byte, 0, len(value))
	for _, r := range value {
		switch {
		case r >= '0' && r <= '9':
			result = append(result, 'd')
		case r >= 'a' && r <= 'f':
			result = append(result, 'l')
		case r >= 'A' && r <= 'F':
			result = append(result, 'u')
		case r >= 'g' && r <= 'z':
			result = append(result, 'L')
		case r >= 'G' && r <= 'Z':
			result = append(result, 'U')
		case r < 0x80:
			result = append(result, '?')
		default:
			result = append(result, '*')
		}
	}
	return string(result)
}

func collectLiveFiles(ctx context.Context, t *testing.T, session *Session, root provider.Object, maxDirectories int, maxFileSize int64, maxFiles int) ([]provider.Object, []provider.Object, int) {
	queue := []provider.Object{root}
	var files []provider.Object
	listed := 0
	for len(queue) > 0 && len(files) < maxFiles && maxDirectories > 0 {
		parent := queue[0]
		queue = queue[1:]
		maxDirectories--
		objects, err := session.List(ctx, parent)
		if err != nil {
			if errors.Is(err, provider.ErrPermission) || errors.Is(err, provider.ErrNotFound) {
				continue
			}
			t.Fatalf("live list failed: %v", err)
		}
		listed += len(objects)
		for _, object := range objects {
			if object.IsDir() {
				queue = append(queue, object)
				continue
			}
			if object.Size > 0 && object.Size <= maxFileSize {
				files = append(files, object)
			}
		}
	}
	return files, queue, listed
}

func readWhole(ctx context.Context, session *Session, object provider.Object) ([]byte, error) {
	reader, err := session.OpenRead(ctx, object)
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	data := make([]byte, object.Size)
	for offset := int64(0); offset < object.Size; {
		n, err := reader.ReadAt(ctx, data[offset:], offset)
		if n < 0 || n > len(data)-int(offset) {
			return nil, errors.New("invalid read count")
		}
		offset += int64(n)
		if err != nil {
			if errors.Is(err, io.EOF) && offset == object.Size {
				break
			}
			return nil, err
		}
		if n == 0 {
			return nil, io.ErrUnexpectedEOF
		}
	}
	return data, nil
}

func checkRange(ctx context.Context, session *Session, object provider.Object, whole []byte, offset, length int64) error {
	reader, err := session.OpenRead(ctx, object)
	if err != nil {
		return err
	}
	defer reader.Close()
	data := make([]byte, length)
	n, err := reader.ReadAt(ctx, data, offset)
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if n != len(data) {
		return io.ErrUnexpectedEOF
	}
	if string(data) != string(whole[offset:offset+length]) {
		return errors.New("range bytes do not match whole-file bytes")
	}
	return nil
}

func clearBytes(values map[string][]byte) {
	for key, value := range values {
		clear(value)
		delete(values, key)
	}
}
