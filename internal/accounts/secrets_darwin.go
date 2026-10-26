//go:build darwin

package accounts

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"

	"al.essio.dev/pkg/shellescape"
	"github.com/zalando/go-keyring"
)

const keychainEncodingPrefix = "go-keyring-base64:"

// macSecrets writes generic-password items through `security -i` so the
// secret never appears in process arguments. Only Apple's security tool is
// trusted by the item ACL. The FSKit extension accesses provider data through
// the broker and never reads this credential directly.
type macSecrets struct{}

func newSystemSecrets() Secrets { return macSecrets{} }

func (macSecrets) Get(service, user string) (string, error) {
	out, err := exec.Command("/usr/bin/security", "find-generic-password", "-s", service, "-wa", user).CombinedOutput()
	if err != nil {
		if strings.Contains(string(out), "could not be found") {
			return "", keyring.ErrNotFound
		}
		return "", fmt.Errorf("read system credential: %w", err)
	}
	value := strings.TrimSpace(string(out))
	if strings.HasPrefix(value, keychainEncodingPrefix) {
		decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(value, keychainEncodingPrefix))
		if err != nil {
			return "", errors.New("system credential has invalid encoding")
		}
		return string(decoded), nil
	}
	return value, nil
}

func (macSecrets) Set(service, user, secret string) error {
	encoded := keychainEncodingPrefix + base64.StdEncoding.EncodeToString([]byte(secret))
	command := fmt.Sprintf("add-generic-password -U -s %s -a %s -w %s",
		shellescape.Quote(service), shellescape.Quote(user), shellescape.Quote(encoded))
	exists, err := genericPasswordExists(service, user)
	if err != nil {
		return err
	}
	if !exists {
		command += " -T " + shellescape.Quote("/usr/bin/security")
	}
	cmd := exec.Command("/usr/bin/security", "-i")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Start(); err != nil {
		return err
	}
	if _, err := io.WriteString(stdin, command+"\n"); err != nil {
		_ = stdin.Close()
		_ = cmd.Wait()
		return err
	}
	if err := stdin.Close(); err != nil {
		_ = cmd.Wait()
		return err
	}
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("write system credential: %w: %s", err, strings.TrimSpace(output.String()))
	}
	return nil
}

func genericPasswordExists(service, user string) (bool, error) {
	out, err := exec.Command("/usr/bin/security", "find-generic-password", "-s", service, "-a", user).CombinedOutput()
	if err == nil {
		return true, nil
	}
	if strings.Contains(string(out), "could not be found") {
		return false, nil
	}
	return false, fmt.Errorf("inspect system credential: %w", err)
}

func (macSecrets) Delete(service, user string) error {
	out, err := exec.Command("/usr/bin/security", "delete-generic-password", "-s", service, "-a", user).CombinedOutput()
	if err != nil {
		if strings.Contains(string(out), "could not be found") {
			return keyring.ErrNotFound
		}
		return fmt.Errorf("delete system credential: %w", err)
	}
	return nil
}
