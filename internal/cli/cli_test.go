package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xuehaonan/fusedock/internal/providers"
	"github.com/xuehaonan/fusedock/pkg/provider"
)

func TestProvidersCommand(t *testing.T) {
	registry, err := providers.Builtin()
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{"providers"}, IO{Out: &stdout, Err: &stderr, Registry: registry})
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	for _, expected := range []string{"local", "pku", "PKU NetDisk", "read"} {
		if !strings.Contains(stdout.String(), expected) {
			t.Errorf("providers output omitted %q:\n%s", expected, stdout.String())
		}
	}
}

func TestLSJSONForLocalProvider(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "report.txt"), []byte("report"), 0600); err != nil {
		t.Fatal(err)
	}
	registry, err := providers.Builtin()
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{
		"ls", "local", "--json", "--option", "root=" + root,
	}, IO{Out: &stdout, Err: &stderr, Registry: registry})
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	var objects []provider.Object
	if err := json.Unmarshal(stdout.Bytes(), &objects); err != nil {
		t.Fatalf("invalid JSON %q: %v", stdout.String(), err)
	}
	if len(objects) != 1 || objects[0].Name != "report.txt" {
		t.Fatalf("unexpected listing: %+v", objects)
	}
}

func TestParsePairsRejectsMissingEquals(t *testing.T) {
	if _, err := parsePairs([]string{"root"}); err == nil {
		t.Fatal("invalid option was accepted")
	}
}

func TestUnknownProviderInputIsRejected(t *testing.T) {
	descriptor := provider.Descriptor{
		ID: "test", Options: []provider.OptionSpec{{Key: "root", Description: "root", Required: true}},
		Credentials: []provider.CredentialSpec{{Key: "username", Description: "user"}},
	}
	if err := validateInputs(descriptor, map[string]string{"rooot": "x"}, nil, ""); err == nil {
		t.Fatal("unknown option was accepted")
	}
	if err := validateInputs(descriptor, map[string]string{}, nil, ""); err == nil {
		t.Fatal("missing required option was accepted")
	}
	if err := validateInputs(descriptor, map[string]string{"root": "/"}, map[string][]byte{"password": []byte("x")}, ""); err == nil {
		t.Fatal("unknown credential was accepted")
	}
	if err := validateInputs(provider.Descriptor{ID: "local"}, nil, nil, "user"); err == nil {
		t.Fatal("unsupported username shortcut was accepted")
	}
}

func TestSubcommandHelpExitsSuccessfully(t *testing.T) {
	registry, err := providers.Builtin()
	if err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{"providers", "ls", "mount"} {
		var stdout, stderr bytes.Buffer
		code := Run(context.Background(), []string{command, "--help"}, IO{Out: &stdout, Err: &stderr, Registry: registry})
		if code != 0 {
			t.Errorf("%s --help exited %d: %s", command, code, stderr.String())
		}
		if !strings.Contains(stdout.String(), "usage: fusedock "+command) {
			t.Errorf("%s --help did not write usage to stdout: %q", command, stdout.String())
		}
	}
}
