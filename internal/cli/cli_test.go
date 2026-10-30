package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zalando/go-keyring"

	"github.com/xuehaonan27/fusedock/internal/accounts"
	"github.com/xuehaonan27/fusedock/internal/credentials"
	"github.com/xuehaonan27/fusedock/internal/providers"
	brokerprovider "github.com/xuehaonan27/fusedock/internal/providers/broker"
	"github.com/xuehaonan27/fusedock/pkg/provider"
)

type testSecrets map[string]string

func (m testSecrets) Get(service, user string) (string, error) {
	value, ok := m[service+"/"+user]
	if !ok {
		return "", keyring.ErrNotFound
	}
	return value, nil
}
func (m testSecrets) Set(service, user, secret string) error {
	m[service+"/"+user] = secret
	return nil
}
func (m testSecrets) Delete(service, user string) error {
	delete(m, service+"/"+user)
	return nil
}

func testAccountStore(t *testing.T) *accounts.Store {
	t.Helper()
	return &accounts.Store{Path: filepath.Join(t.TempDir(), "fusedock", "accounts.json"), Secrets: testSecrets{}}
}

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

func TestCredentialFileCarriesProviderOptions(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "file.txt"), []byte("data"), 0600); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(t.TempDir(), "local.credentials")
	if err := os.WriteFile(config, []byte("provider=local\nroot="+root+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	registry, err := providers.Builtin()
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{
		"ls", "local", "--json", "--credential-file", config,
	}, IO{Out: &stdout, Err: &stderr, Registry: registry})
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "file.txt") {
		t.Fatalf("unexpected listing: %s", stdout.String())
	}
}

func TestCredentialFileRejectsWrongProvider(t *testing.T) {
	descriptor := provider.Descriptor{ID: "local"}
	if err := mergeCredentialFile(descriptor, map[string][]byte{"provider": []byte("pku")}, nil, nil); err == nil {
		t.Fatal("credential file for another provider was accepted")
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

func TestAccountAddAndUse(t *testing.T) {
	store := testAccountStore(t)
	registry := provider.NewRegistry()
	factory := &fixtureFactory{}
	if err := registry.Register(factory); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(t.TempDir(), "fake.credentials")
	if err := os.WriteFile(config, []byte("provider=fake\nusername=fixture\npassword=secret\nendpoint=https://example.test\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{
		"account", "add", "--credential-file", config, "--name", "fixture", "--default",
	}, IO{Out: &stdout, Err: &stderr, Registry: registry, Accounts: store})
	if code != 0 {
		t.Fatalf("add exit=%d stderr=%s", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	code = Run(context.Background(), []string{"ls", "--account", "fixture", "--json"}, IO{
		Out: &stdout, Err: &stderr, Registry: registry, Accounts: store,
	})
	if code != 0 {
		t.Fatalf("ls exit=%d stderr=%s", code, stderr.String())
	}
	if factory.username != "fixture" || factory.password != "secret" || factory.endpoint != "https://example.test" {
		t.Fatalf("unexpected open request: user=%q password=%q endpoint=%q", factory.username, factory.password, factory.endpoint)
	}
	stdout.Reset()
	stderr.Reset()
	code = Run(context.Background(), []string{"account", "ls", "--json"}, IO{
		Out: &stdout, Err: &stderr, Registry: registry, Accounts: store,
	})
	if code != 0 {
		t.Fatalf("account ls exit=%d stderr=%s", code, stderr.String())
	}
	if strings.Contains(stdout.String(), "secret") || strings.Contains(stdout.String(), "password") {
		t.Fatalf("account listing exposed secret material: %s", stdout.String())
	}
}

func TestBrokerCommandCreatesOneUseCapabilityAndCleansUp(t *testing.T) {
	store := testAccountStore(t)
	registry := provider.NewRegistry()
	factory := &fixtureFactory{}
	if err := registry.Register(factory); err != nil {
		t.Fatal(err)
	}
	account := accounts.Account{Name: "fixture", Provider: "fake", Username: "fixture"}
	if err := store.Add(context.Background(), account, map[string][]byte{"password": []byte("secret")}, true, false); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(t.TempDir(), "private", "broker.credentials")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var stdout, stderr bytes.Buffer
	done := make(chan int, 1)
	go func() {
		done <- Run(ctx, []string{"broker", "start", "--account", "fixture", "--config", configPath}, IO{
			Out: &stdout, Err: &stderr, Registry: registry, Accounts: store,
		})
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(configPath); err == nil {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatalf("broker capability was not created; stderr=%s", stderr.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	values, err := credentials.Read(configPath)
	if err != nil {
		t.Fatal(err)
	}
	defer clearSecrets(values)
	if string(values["provider"]) != "broker" || !strings.HasPrefix(string(values["endpoint"]), "http://127.0.0.1:") || len(values["token"]) < 32 {
		t.Fatalf("invalid broker capability file: %#v", values)
	}
	if strings.Contains(string(values["token"]), "secret") {
		t.Fatal("broker capability contains the account password")
	}
	brokerSession, err := (brokerprovider.Factory{}).Open(ctx, provider.OpenRequest{
		Options: map[string]string{"endpoint": string(values["endpoint"])},
		Secrets: map[string][]byte{"token": append([]byte(nil), values["token"]...)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(configPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("bootstrap capability file survived claim: %v", err)
	}
	if err := brokerSession.Close(); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("broker exit=%d stderr=%s", code, stderr.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("broker command did not stop")
	}
	if strings.Contains(stdout.String(), string(values["token"])) {
		t.Fatal("broker command printed the bootstrap capability")
	}
	if _, err := os.Stat(configPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("capability file was not removed: %v", err)
	}
}

func TestAccountAddUsesProviderAuthorizer(t *testing.T) {
	store := testAccountStore(t)
	registry := provider.NewRegistry()
	factory := &oauthFactory{}
	if err := registry.Register(factory); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(t.TempDir(), "oauth.credentials")
	if err := os.WriteFile(config, []byte("provider=oauth\napp_key=public\nsecret_key=private\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{"account", "add", "--credential-file", config, "--name", "oauth"}, IO{
		Out: &stdout, Err: &stderr, Registry: registry, Accounts: store,
		OpenURL: func(target string) error {
			if target != "https://example.test/authorize" {
				t.Errorf("authorization URL = %q", target)
			}
			return nil
		},
	})
	if code != 0 {
		t.Fatalf("add exit=%d stderr=%s", code, stderr.String())
	}
	account, secrets, state, err := store.Credentials(context.Background(), "oauth")
	if err != nil {
		t.Fatal(err)
	}
	defer clearSecrets(secrets)
	if account.Username != "oauth-user" || string(secrets["app_key"]) != "public" || string(secrets["secret_key"]) != "private" || string(state) != `{"access":"session"}` {
		t.Fatalf("unexpected account=%+v secrets=%q state=%q", account, secrets, state)
	}
}

func TestBrokerListenAddressValidation(t *testing.T) {
	valid := []string{"127.0.0.1:0", "127.0.0.1:17893"}
	for _, address := range valid {
		if err := validateListenAddress(address); err != nil {
			t.Errorf("valid address %q: %v", address, err)
		}
	}
	invalid := []string{"0.0.0.0:17893", "192.168.1.10:17893", "localhost:17893", "[::1]:17893", "127.0.0.1", "127.0.0.1:99999"}
	for _, address := range invalid {
		if err := validateListenAddress(address); err == nil {
			t.Errorf("invalid address %q was accepted", address)
		}
	}
}

type oauthFactory struct{}

func (*oauthFactory) Descriptor() provider.Descriptor {
	return provider.Descriptor{
		ContractVersion: provider.ContractVersion, ID: "oauth", Name: "OAuth fixture", Capabilities: provider.CapRead,
		Credentials: []provider.CredentialSpec{
			{Key: "app_key", Description: "app key"},
			{Key: "secret_key", Description: "secret key", Secret: true, Required: true},
		},
	}
}
func (*oauthFactory) Open(_ context.Context, req provider.OpenRequest) (provider.Session, error) {
	if string(req.State) != `{"access":"session"}` {
		return nil, errors.New("missing state")
	}
	return fixtureSession{}, nil
}
func (*oauthFactory) Authorize(_ context.Context, req provider.AuthorizeRequest) (provider.Authorization, error) {
	if string(req.Secrets["secret_key"]) != "private" {
		return provider.Authorization{}, errors.New("missing secret")
	}
	if err := req.OpenURL("https://example.test/authorize"); err != nil {
		return provider.Authorization{}, err
	}
	return provider.Authorization{
		Username: "oauth-user",
		Secrets:  map[string][]byte{"app_key": req.Secrets["app_key"], "secret_key": req.Secrets["secret_key"]},
		State:    []byte(`{"access":"session"}`),
	}, nil
}

type fixtureFactory struct {
	username string
	password string
	endpoint string
}

func (*fixtureFactory) Descriptor() provider.Descriptor {
	return provider.Descriptor{
		ContractVersion: provider.ContractVersion, ID: "fake", Name: "Fixture", Capabilities: provider.CapRead,
		Options: []provider.OptionSpec{{Key: "endpoint", Description: "endpoint"}},
		Credentials: []provider.CredentialSpec{
			{Key: "username", Description: "username"},
			{Key: "password", Description: "password", Secret: true},
		},
	}
}
func (f *fixtureFactory) Open(_ context.Context, request provider.OpenRequest) (provider.Session, error) {
	f.username = request.Username
	f.password = string(request.Secrets["password"])
	f.endpoint = request.Options["endpoint"]
	return fixtureSession{}, nil
}

type fixtureSession struct{}

func (fixtureSession) Root(context.Context) (provider.Object, error) {
	return provider.Object{ID: "root", Name: "root", Kind: provider.KindDirectory}, nil
}
func (fixtureSession) Stat(context.Context, provider.Object) (provider.Object, error) {
	return provider.Object{}, nil
}
func (fixtureSession) List(context.Context, provider.Object) ([]provider.Object, error) {
	return []provider.Object{{ID: "file", ParentID: "root", Name: "file", Kind: provider.KindFile, Size: 1}}, nil
}
func (fixtureSession) OpenRead(context.Context, provider.Object) (provider.Reader, error) {
	return nil, provider.ErrUnsupported
}
func (fixtureSession) Close() error { return nil }
