// Package credentials reads provider secrets from an owner-only local file.
package credentials

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

const maxFileBytes = 64 << 10

// Read parses key=value lines from a regular, owner-only file. A duplicate key
// or a malformed line is an error. Blank lines and lines beginning with # are
// ignored; password values keep their exact bytes.
func Read(path string) (map[string][]byte, error) {
	if path == "" {
		return nil, errors.New("credential file path is required")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open credential file: %w", err)
	}
	defer file.Close()
	linkInfo, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("inspect credential file: %w", err)
	}
	if linkInfo.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("credential file cannot be a symbolic link")
	}
	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("inspect credential file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("credential file must be a regular file")
	}
	if info.Size() > maxFileBytes {
		return nil, errors.New("credential file exceeds 64 KiB")
	}
	if err := checkOwnerAndMode(path, info); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(file, maxFileBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read credential file: %w", err)
	}
	defer clear(data)
	if len(data) > maxFileBytes {
		return nil, errors.New("credential file exceeds 64 KiB")
	}
	result := make(map[string][]byte)
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 1024), maxFileBytes)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := strings.TrimSuffix(scanner.Text(), "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if !ok || !validKey(key) {
			return nil, fmt.Errorf("invalid credential file line %d; expected key=value", lineNumber)
		}
		if strings.ContainsRune(value, 0) || strings.IndexFunc(value, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
			return nil, fmt.Errorf("credential %q contains a control character", key)
		}
		if value == "" {
			return nil, fmt.Errorf("credential %q is empty", key)
		}
		if _, exists := result[key]; exists {
			return nil, fmt.Errorf("credential %q is repeated", key)
		}
		result[key] = []byte(value)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("parse credential file: %w", err)
	}
	if len(result) == 0 {
		return nil, errors.New("credential file contains no credentials")
	}
	return result, nil
}

func validKey(key string) bool {
	if key == "" || len(key) > 64 {
		return false
	}
	for _, r := range key {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' && r != '_' {
			return false
		}
	}
	return true
}
