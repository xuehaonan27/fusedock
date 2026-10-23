package cli

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"runtime"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/gofrs/flock"
	"github.com/spf13/pflag"
	"golang.org/x/term"

	"github.com/xuehaonan/fusedock/internal/accounts"
	"github.com/xuehaonan/fusedock/internal/broker"
	"github.com/xuehaonan/fusedock/internal/credentials"
	"github.com/xuehaonan/fusedock/internal/fusefs"
	"github.com/xuehaonan/fusedock/internal/providers"
	"github.com/xuehaonan/fusedock/internal/vfs"
	"github.com/xuehaonan/fusedock/pkg/provider"
)

var Version = "0.1.0-dev"

type IO struct {
	In       io.Reader
	Out      io.Writer
	Err      io.Writer
	Registry *provider.Registry
	Accounts *accounts.Store
}

func Run(ctx context.Context, args []string, streams IO) int {
	if streams.In == nil {
		streams.In = strings.NewReader("")
	}
	if streams.Out == nil {
		streams.Out = io.Discard
	}
	if streams.Err == nil {
		streams.Err = io.Discard
	}
	if streams.Registry == nil {
		registry, err := providers.Builtin()
		if err != nil {
			fmt.Fprintln(streams.Err, "fusedock:", err)
			return 1
		}
		streams.Registry = registry
	}
	if len(args) == 0 {
		printUsage(streams.Out)
		return 0
	}
	switch args[0] {
	case "help", "--help", "-h":
		printUsage(streams.Out)
		return 0
	case "version", "--version":
		fmt.Fprintf(streams.Out, "fusedock %s\n", Version)
		return 0
	case "providers":
		return runProviders(args[1:], streams)
	case "account":
		return runAccount(ctx, args[1:], streams)
	case "broker":
		return runBroker(ctx, args[1:], streams)
	case "ls":
		return runLS(ctx, args[1:], streams)
	case "mount":
		return runMount(ctx, args[1:], streams)
	default:
		fmt.Fprintf(streams.Err, "fusedock: unknown command %q\n\n", args[0])
		printUsage(streams.Err)
		return 2
	}
}

func printUsage(out io.Writer) {
	fmt.Fprint(out, `FuseDock mounts cloud storage through a provider-neutral contract.

Usage:
  fusedock providers
  fusedock account add [flags]
  fusedock account ls [--json]
  fusedock account default <name>
  fusedock account remove <name>
  fusedock broker start --account NAME --config PATH
  fusedock ls [flags] [provider]
  fusedock mount [flags] [provider] <mountpoint>
  fusedock version

Saved accounts keep their password and renewable provider state in the system
credential store. One-off commands can instead pass --credential-file,
--credential-env, or an interactive prompt.

Run 'fusedock providers' for provider IDs and their options.
`)
}

func runBroker(ctx context.Context, args []string, streams IO) int {
	if len(args) == 0 || helpRequested(args) {
		fmt.Fprintln(streams.Out, "usage: fusedock broker start --account NAME --config PATH [-o key=value]")
		return 0
	}
	if args[0] != "start" {
		fmt.Fprintf(streams.Err, "fusedock broker: unknown command %q\n", args[0])
		return 2
	}
	flags := pflag.NewFlagSet("broker start", pflag.ContinueOnError)
	flags.SetOutput(streams.Err)
	accountName := flags.String("account", "", "saved account to serve")
	configPath := flags.String("config", "", "owner-only broker capability file to create")
	var optionValues []string
	flags.StringArrayVarP(&optionValues, "option", "o", nil, "provider option override key=value")
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, pflag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 || *accountName == "" || *configPath == "" {
		fmt.Fprintln(streams.Err, "usage: fusedock broker start --account NAME --config PATH [-o key=value]")
		return 2
	}
	store, err := accountStore(streams)
	if err != nil {
		fmt.Fprintln(streams.Err, "fusedock:", err)
		return 1
	}
	config, err := store.Load(ctx)
	if err != nil {
		fmt.Fprintln(streams.Err, "fusedock:", err)
		return 1
	}
	account, ok := config.Find(*accountName)
	if !ok {
		fmt.Fprintf(streams.Err, "fusedock: saved account %q was not found\n", *accountName)
		return 1
	}
	factory, ok := streams.Registry.Get(account.Provider)
	if !ok {
		fmt.Fprintf(streams.Err, "fusedock: account %q uses unknown provider %q\n", account.Name, account.Provider)
		return 1
	}
	if err := credentials.EnsurePrivateParent(*configPath); err != nil {
		fmt.Fprintln(streams.Err, "fusedock: prepare broker capability directory:", err)
		return 1
	}
	lock := flock.New(*configPath + ".lock")
	locked, err := lock.TryLockContext(ctx, 25*time.Millisecond)
	if err != nil || !locked {
		fmt.Fprintln(streams.Err, "fusedock: another broker is already using this config path")
		return 1
	}
	defer lock.Close()
	common := providerFlags{account: *accountName, accountStore: store, options: optionValues}
	session, err := common.open(ctx, factory, promptFunc(streams.In, streams.Out, streams.Err))
	if err != nil {
		fmt.Fprintln(streams.Err, "fusedock:", err)
		return 1
	}
	defer session.Close()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		fmt.Fprintln(streams.Err, "fusedock: create broker listener:", err)
		return 1
	}
	defer listener.Close()
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		fmt.Fprintln(streams.Err, "fusedock: generate broker capability:", err)
		return 1
	}
	bootstrap := base64.RawURLEncoding.EncodeToString(random[:])
	clear(random[:])
	server, err := broker.NewServer(session, bootstrap)
	if err != nil {
		fmt.Fprintln(streams.Err, "fusedock:", err)
		return 1
	}
	server.SetOnClaim(func() { _ = os.Remove(*configPath) })
	endpoint := "http://" + listener.Addr().String()
	server.SetExpectedHost(listener.Addr().String())
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(ctx, listener) }()
	if err := credentials.WritePrivate(*configPath, map[string][]byte{
		"provider": []byte("broker"),
		"endpoint": []byte(endpoint),
		"token":    []byte(bootstrap),
	}); err != nil {
		_ = listener.Close()
		<-serveDone
		fmt.Fprintln(streams.Err, "fusedock: write broker capability file:", err)
		return 1
	}
	defer func() { _ = os.Remove(*configPath) }()
	fmt.Fprintf(streams.Out, "Broker ready for account %s at %s\n", account.Name, endpoint)
	fmt.Fprintf(streams.Out, "Capability file: %s\n", *configPath)
	if runtime.GOOS == "darwin" {
		fmt.Fprintf(streams.Out, "Mount with: mount -t fusedock %s MOUNTPOINT\n", *configPath)
	}
	fmt.Fprintln(streams.Out, "The capability file is removed after mount claims it. Press Ctrl-C to stop the broker.")
	if err := <-serveDone; err != nil {
		fmt.Fprintln(streams.Err, "fusedock broker:", err)
		return 1
	}
	return 0
}

type providerFlags struct {
	options        []string
	credentials    []string
	credentialEnv  []string
	credentialFile string
	username       string
	account        string
	accountStore   *accounts.Store
}

func (f *providerFlags) bind(flags *pflag.FlagSet) {
	flags.StringArrayVarP(&f.options, "option", "o", nil, "provider option key=value (repeatable)")
	flags.StringArrayVar(&f.credentials, "credential", nil, "credential key=value (repeatable)")
	flags.StringArrayVar(&f.credentialEnv, "credential-env", nil, "read credential from environment, key=ENV (repeatable)")
	flags.StringVar(&f.credentialFile, "credential-file", "", "read credentials from an owner-only key=value file")
	flags.StringVar(&f.username, "username", "", "shortcut for --credential username=VALUE")
	flags.StringVar(&f.account, "account", "", "use a saved account")
}

func (f *providerFlags) open(ctx context.Context, factory provider.Factory, prompt provider.PromptFunc) (provider.Session, error) {
	if f.account != "" {
		return f.openAccount(ctx, factory, prompt)
	}
	options, err := parsePairs(f.options)
	if err != nil {
		return nil, fmt.Errorf("invalid provider option: %w", err)
	}
	secrets, err := parseSecrets(f.credentials, f.credentialEnv)
	if err != nil {
		return nil, err
	}
	defer clearSecrets(secrets)
	if f.credentialFile != "" {
		fromFile, err := credentials.Read(f.credentialFile)
		if err != nil {
			return nil, err
		}
		defer clearSecrets(fromFile)
		if err := mergeCredentialFile(factory.Descriptor(), fromFile, options, secrets); err != nil {
			return nil, err
		}
	}
	if err := validateInputs(factory.Descriptor(), options, secrets, f.username); err != nil {
		return nil, err
	}
	request := provider.OpenRequest{Username: f.username, Secrets: secrets, Options: options, Prompt: prompt}
	return factory.Open(ctx, request)
}

func (f *providerFlags) openAccount(ctx context.Context, factory provider.Factory, prompt provider.PromptFunc) (provider.Session, error) {
	if f.credentialFile != "" || len(f.credentials) > 0 || len(f.credentialEnv) > 0 || f.username != "" {
		return nil, errors.New("--account cannot be combined with explicit credentials")
	}
	store := f.accountStore
	if store == nil {
		var err error
		store, err = accounts.Default()
		if err != nil {
			return nil, err
		}
	}
	account, storedSecrets, state, err := store.Credentials(ctx, f.account)
	if err != nil {
		return nil, err
	}
	defer clearSecrets(storedSecrets)
	defer clear(state)
	descriptor := factory.Descriptor()
	if account.Provider != descriptor.ID {
		return nil, fmt.Errorf("account %q belongs to provider %s, not %s", account.Name, account.Provider, descriptor.ID)
	}
	options := cloneStringMap(account.Options)
	overrides, err := parsePairs(f.options)
	if err != nil {
		return nil, fmt.Errorf("invalid provider option: %w", err)
	}
	for key, value := range overrides {
		options[key] = value
	}
	if err := validateInputs(descriptor, options, storedSecrets, account.Username); err != nil {
		return nil, err
	}
	request := provider.OpenRequest{
		Username: account.Username, Secrets: storedSecrets, Options: options, State: state, Prompt: prompt,
		SaveState: func(ctx context.Context, data []byte) error {
			return store.SaveState(ctx, account.Name, account.Provider, account.Username, data)
		},
	}
	return factory.Open(ctx, request)
}

func cloneStringMap(input map[string]string) map[string]string {
	result := make(map[string]string, len(input))
	for key, value := range input {
		result[key] = value
	}
	return result
}

func accountStore(streams IO) (*accounts.Store, error) {
	if streams.Accounts != nil {
		return streams.Accounts, nil
	}
	return accounts.Default()
}

func resolveProviderID(ctx context.Context, streams IO, accountName string, args []string) (string, error) {
	if accountName == "" {
		if len(args) != 1 {
			return "", errors.New("provider is required")
		}
		return args[0], nil
	}
	if len(args) > 1 {
		return "", errors.New("too many provider arguments")
	}
	store, err := accountStore(streams)
	if err != nil {
		return "", err
	}
	config, err := store.Load(ctx)
	if err != nil {
		return "", err
	}
	account, ok := config.Find(accountName)
	if !ok {
		return "", fmt.Errorf("%w: %s", accounts.ErrNotFound, accountName)
	}
	if len(args) == 1 && args[0] != account.Provider {
		return "", fmt.Errorf("account %q belongs to provider %s, not %s", accountName, account.Provider, args[0])
	}
	return account.Provider, nil
}

func mergeCredentialFile(descriptor provider.Descriptor, fileValues map[string][]byte, options map[string]string, secrets map[string][]byte) error {
	if options == nil {
		options = make(map[string]string)
	}
	if secrets == nil {
		secrets = make(map[string][]byte)
	}
	knownOptions := make(map[string]bool, len(descriptor.Options))
	knownCredentials := make(map[string]bool, len(descriptor.Credentials))
	for _, option := range descriptor.Options {
		knownOptions[option.Key] = true
	}
	for _, credential := range descriptor.Credentials {
		knownCredentials[credential.Key] = true
	}
	for key, value := range fileValues {
		if key == "provider" {
			continue
		}
		switch {
		case knownOptions[key]:
			if _, exists := options[key]; exists {
				return fmt.Errorf("option %q is supplied both by flag and credential file", key)
			}
			options[key] = string(value)
		case knownCredentials[key]:
			if _, exists := secrets[key]; exists {
				return fmt.Errorf("credential %q is supplied both by flag and credential file", key)
			}
			secrets[key] = append([]byte(nil), value...)
		default:
			return fmt.Errorf("provider %s does not define key %q in its credential file", descriptor.ID, key)
		}
	}
	if providerID := strings.TrimSpace(string(fileValues["provider"])); providerID != "" && providerID != descriptor.ID {
		return fmt.Errorf("credential file is for provider %q, not %q", providerID, descriptor.ID)
	}
	return nil
}

func validateInputs(descriptor provider.Descriptor, options map[string]string, secrets map[string][]byte, username string) error {
	knownOptions := make(map[string]bool, len(descriptor.Options))
	knownCredentials := make(map[string]bool, len(descriptor.Credentials))
	for _, option := range descriptor.Options {
		knownOptions[option.Key] = true
		if option.Required && strings.TrimSpace(options[option.Key]) == "" {
			return fmt.Errorf("provider %s requires --option %s=VALUE", descriptor.ID, option.Key)
		}
	}
	for _, credential := range descriptor.Credentials {
		knownCredentials[credential.Key] = true
	}
	for key := range options {
		if !knownOptions[key] {
			return fmt.Errorf("provider %s does not define option %q", descriptor.ID, key)
		}
	}
	for key := range secrets {
		if !knownCredentials[key] {
			return fmt.Errorf("provider %s does not define credential %q", descriptor.ID, key)
		}
	}
	if username != "" && !knownCredentials["username"] {
		return fmt.Errorf("provider %s does not accept --username", descriptor.ID)
	}
	if username != "" && len(secrets["username"]) > 0 {
		return errors.New("username was supplied both with --username and --credential")
	}
	return nil
}

func parsePairs(values []string) (map[string]string, error) {
	result := make(map[string]string, len(values))
	for _, value := range values {
		key, item, ok := strings.Cut(value, "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" {
			return nil, fmt.Errorf("expected key=value, got %q", value)
		}
		if _, exists := result[key]; exists {
			return nil, fmt.Errorf("duplicate key %q", key)
		}
		result[key] = item
	}
	return result, nil
}

func parseSecrets(values, environments []string) (map[string][]byte, error) {
	result := make(map[string][]byte)
	for _, value := range values {
		key, item, ok := strings.Cut(value, "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" {
			return nil, fmt.Errorf("invalid credential %q; expected key=value", value)
		}
		if _, exists := result[key]; exists {
			return nil, fmt.Errorf("duplicate credential %q", key)
		}
		result[key] = []byte(item)
	}
	for _, value := range environments {
		key, envName, ok := strings.Cut(value, "=")
		key, envName = strings.TrimSpace(key), strings.TrimSpace(envName)
		if !ok || key == "" || envName == "" {
			return nil, fmt.Errorf("invalid credential environment %q; expected key=ENV", value)
		}
		if _, exists := result[key]; exists {
			return nil, fmt.Errorf("duplicate credential %q", key)
		}
		item, exists := os.LookupEnv(envName)
		if !exists {
			return nil, fmt.Errorf("environment variable %s is not set", envName)
		}
		result[key] = []byte(item)
	}
	return result, nil
}

func clearSecrets(values map[string][]byte) {
	for key, value := range values {
		clear(value)
		delete(values, key)
	}
}

func promptFunc(in io.Reader, out, errOut io.Writer) provider.PromptFunc {
	reader := bufio.NewReader(in)
	return func(_ context.Context, prompt provider.Prompt) (string, error) {
		message := prompt.Message
		if message == "" {
			message = string(prompt.Kind)
		}
		if len(prompt.Image) > 0 {
			file, err := os.CreateTemp("", "fusedock-captcha-*")
			if err != nil {
				return "", fmt.Errorf("save CAPTCHA: %w", err)
			}
			path := file.Name()
			if _, err := file.Write(prompt.Image); err != nil {
				file.Close()
				os.Remove(path)
				return "", fmt.Errorf("save CAPTCHA: %w", err)
			}
			file.Close()
			defer os.Remove(path)
			fmt.Fprintf(errOut, "%s. CAPTCHA image: %s\n", message, path)
		} else {
			fmt.Fprintf(errOut, "%s: ", message)
		}
		if prompt.Secret {
			if file, ok := in.(*os.File); ok && term.IsTerminal(int(file.Fd())) {
				value, err := term.ReadPassword(int(file.Fd()))
				fmt.Fprintln(errOut)
				if err != nil {
					return "", err
				}
				return strings.TrimSpace(string(value)), nil
			}
		}
		value, err := reader.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", err
		}
		return strings.TrimSpace(value), nil
	}
}

func runProviders(args []string, streams IO) int {
	flags := pflag.NewFlagSet("providers", pflag.ContinueOnError)
	flags.SetOutput(streams.Err)
	flags.Usage = func() { fmt.Fprintln(flags.Output(), "usage: fusedock providers") }
	if helpRequested(args) {
		flags.SetOutput(streams.Out)
		flags.Usage()
		return 0
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, pflag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(streams.Err, "fusedock providers: unexpected arguments")
		return 2
	}
	table := tabwriter.NewWriter(streams.Out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(table, "ID\tNAME\tCONTRACT\tCAPABILITIES\tDESCRIPTION")
	for _, descriptor := range streams.Registry.Descriptors() {
		fmt.Fprintf(table, "%s\t%s\t%d\t%s\t%s\n", descriptor.ID, descriptor.Name, descriptor.ContractVersion, capabilityText(descriptor.Capabilities), descriptor.Description)
	}
	_ = table.Flush()
	for _, descriptor := range streams.Registry.Descriptors() {
		fmt.Fprintf(streams.Out, "\n%s options:\n", descriptor.ID)
		if len(descriptor.Options) == 0 && len(descriptor.Credentials) == 0 {
			fmt.Fprintln(streams.Out, "  none")
			continue
		}
		for _, option := range descriptor.Options {
			fmt.Fprintf(streams.Out, "  --option %s=VALUE  %s", option.Key, option.Description)
			if option.Default != "" {
				fmt.Fprintf(streams.Out, " (default %s)", option.Default)
			}
			fmt.Fprintln(streams.Out)
		}
		for _, credential := range descriptor.Credentials {
			fmt.Fprintf(streams.Out, "  credential %s  %s\n", credential.Key, credential.Description)
		}
	}
	return 0
}

func runAccount(ctx context.Context, args []string, streams IO) int {
	if len(args) == 0 || helpRequested(args) {
		fmt.Fprintln(streams.Out, "usage: fusedock account <add|ls|default|remove> [flags]")
		return 0
	}
	switch args[0] {
	case "add":
		return runAccountAdd(ctx, args[1:], streams)
	case "ls", "list":
		return runAccountLS(ctx, args[1:], streams)
	case "default":
		return runAccountDefault(ctx, args[1:], streams)
	case "remove", "rm":
		return runAccountRemove(ctx, args[1:], streams)
	default:
		fmt.Fprintf(streams.Err, "fusedock account: unknown command %q\n", args[0])
		return 2
	}
}

func runAccountAdd(ctx context.Context, args []string, streams IO) int {
	flags := pflag.NewFlagSet("account add", pflag.ContinueOnError)
	flags.SetOutput(streams.Err)
	providerID := flags.String("provider", "", "provider ID")
	name := flags.String("name", "", "local account name (defaults to username)")
	username := flags.String("username", "", "provider username")
	credentialFile := flags.String("credential-file", "", "bootstrap from an owner-only key=value file")
	makeDefault := flags.Bool("default", false, "make this the default account")
	replace := flags.Bool("replace", false, "replace an existing account with the same name")
	var optionValues []string
	flags.StringArrayVarP(&optionValues, "option", "o", nil, "provider option key=value (repeatable)")
	flags.Usage = func() {
		fmt.Fprintln(streams.Out, "usage: fusedock account add --provider ID --username USER [--name NAME] [--credential-file PATH] [--default] [--replace] [-o key=value]")
	}
	if helpRequested(args) {
		flags.Usage()
		return 0
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, pflag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(streams.Err, "fusedock account add: unexpected arguments")
		return 2
	}
	options, err := parsePairs(optionValues)
	if err != nil {
		fmt.Fprintln(streams.Err, "fusedock:", err)
		return 2
	}
	secrets := make(map[string][]byte)
	defer clearSecrets(secrets)
	if *credentialFile != "" {
		values, err := credentials.Read(*credentialFile)
		if err != nil {
			fmt.Fprintln(streams.Err, "fusedock:", err)
			return 1
		}
		defer clearSecrets(values)
		if *providerID == "" {
			*providerID = strings.TrimSpace(string(values["provider"]))
		}
		if *username == "" {
			*username = strings.TrimSpace(string(values["username"]))
		}
		for key, value := range values {
			if key == "provider" || key == "username" || key == "password" {
				continue
			}
			if _, exists := options[key]; !exists {
				options[key] = string(value)
			}
		}
		if value := values["password"]; len(value) > 0 {
			secrets["password"] = append([]byte(nil), value...)
		}
	}
	if *providerID == "" {
		fmt.Fprintln(streams.Err, "fusedock: --provider is required")
		return 2
	}
	factory, ok := streams.Registry.Get(*providerID)
	if !ok {
		fmt.Fprintf(streams.Err, "fusedock: unknown provider %q\n", *providerID)
		return 2
	}
	if *username == "" {
		value, err := promptFunc(streams.In, streams.Out, streams.Err)(ctx, provider.Prompt{Kind: "text", Message: "Provider username"})
		if err != nil {
			fmt.Fprintln(streams.Err, "fusedock:", err)
			return 1
		}
		*username = value
	}
	if len(secrets["password"]) == 0 {
		value, err := promptFunc(streams.In, streams.Out, streams.Err)(ctx, provider.Prompt{Kind: provider.PromptPassword, Message: "Password", Secret: true})
		if err != nil {
			fmt.Fprintln(streams.Err, "fusedock:", err)
			return 1
		}
		secrets["password"] = []byte(value)
	}
	if err := validateInputs(factory.Descriptor(), options, secrets, *username); err != nil {
		fmt.Fprintln(streams.Err, "fusedock:", err)
		return 2
	}
	if *name == "" {
		*name = *username
	}
	store, err := accountStore(streams)
	if err != nil {
		fmt.Fprintln(streams.Err, "fusedock:", err)
		return 1
	}
	account := accounts.Account{Name: *name, Provider: *providerID, Username: *username, Options: options}
	if err := store.Add(ctx, account, secrets, *makeDefault, *replace); err != nil {
		fmt.Fprintln(streams.Err, "fusedock:", err)
		return 1
	}
	fmt.Fprintf(streams.Out, "Account %s saved for provider %s.\n", account.Name, account.Provider)
	if *credentialFile != "" {
		fmt.Fprintf(streams.Out, "The password is now in the system credential store. After checkout succeeds, remove the bootstrap file: %s\n", *credentialFile)
	}
	return 0
}

func runAccountLS(ctx context.Context, args []string, streams IO) int {
	flags := pflag.NewFlagSet("account ls", pflag.ContinueOnError)
	flags.SetOutput(streams.Err)
	jsonOutput := flags.Bool("json", false, "write a JSON document")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, pflag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(streams.Err, "fusedock account ls: unexpected arguments")
		return 2
	}
	store, err := accountStore(streams)
	if err != nil {
		fmt.Fprintln(streams.Err, "fusedock:", err)
		return 1
	}
	config, err := store.Load(ctx)
	if err != nil {
		fmt.Fprintln(streams.Err, "fusedock:", err)
		return 1
	}
	if *jsonOutput {
		encoder := json.NewEncoder(streams.Out)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(config); err != nil {
			fmt.Fprintln(streams.Err, "fusedock:", err)
			return 1
		}
		return 0
	}
	table := tabwriter.NewWriter(streams.Out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(table, "DEFAULT\tNAME\tPROVIDER\tUSERNAME")
	for _, account := range config.Accounts {
		marker := ""
		if account.Name == config.Default {
			marker = "*"
		}
		fmt.Fprintf(table, "%s\t%s\t%s\t%s\n", marker, account.Name, account.Provider, account.Username)
	}
	_ = table.Flush()
	return 0
}

func runAccountDefault(ctx context.Context, args []string, streams IO) int {
	if len(args) != 1 {
		fmt.Fprintln(streams.Err, "usage: fusedock account default <name>")
		return 2
	}
	store, err := accountStore(streams)
	if err == nil {
		err = store.SetDefault(ctx, args[0])
	}
	if err != nil {
		fmt.Fprintln(streams.Err, "fusedock:", err)
		return 1
	}
	fmt.Fprintf(streams.Out, "Default account: %s\n", args[0])
	return 0
}

func runAccountRemove(ctx context.Context, args []string, streams IO) int {
	if len(args) != 1 {
		fmt.Fprintln(streams.Err, "usage: fusedock account remove <name>")
		return 2
	}
	store, err := accountStore(streams)
	if err == nil {
		err = store.Remove(ctx, args[0])
	}
	if err != nil {
		fmt.Fprintln(streams.Err, "fusedock:", err)
		return 1
	}
	fmt.Fprintf(streams.Out, "Removed account: %s\n", args[0])
	return 0
}

func capabilityText(capabilities provider.Capabilities) string {
	var values []string
	if capabilities&provider.CapRead != 0 {
		values = append(values, "read")
	}
	if capabilities&provider.CapMutate != 0 {
		values = append(values, "write")
	}
	if capabilities&provider.CapSearch != 0 {
		values = append(values, "search")
	}
	if capabilities&provider.CapShare != 0 {
		values = append(values, "share")
	}
	if len(values) == 0 {
		return "none"
	}
	return strings.Join(values, ",")
}

func runLS(ctx context.Context, args []string, streams IO) int {
	flags := pflag.NewFlagSet("ls", pflag.ContinueOnError)
	flags.SetOutput(streams.Err)
	var common providerFlags
	common.bind(flags)
	common.accountStore = streams.Accounts
	path := flags.StringP("path", "p", "/", "provider path to list")
	jsonOutput := flags.Bool("json", false, "write a JSON array")
	showAll := flags.BoolP("all", "a", false, "include hidden local entries")
	flags.Usage = func() {
		fmt.Fprintln(flags.Output(), "usage: fusedock ls [flags] [provider]")
		flags.PrintDefaults()
	}
	if helpRequested(args) {
		flags.SetOutput(streams.Out)
		flags.Usage()
		return 0
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, pflag.ErrHelp) {
			return 0
		}
		return 2
	}
	providerID, err := resolveProviderID(ctx, streams, common.account, flags.Args())
	if err != nil {
		fmt.Fprintln(streams.Err, "fusedock:", err)
		return 2
	}
	factory, ok := streams.Registry.Get(providerID)
	if !ok {
		fmt.Fprintf(streams.Err, "fusedock: unknown provider %q\n", providerID)
		return 2
	}
	session, err := common.open(ctx, factory, promptFunc(streams.In, streams.Out, streams.Err))
	if err != nil {
		fmt.Fprintln(streams.Err, "fusedock:", err)
		return 1
	}
	defer session.Close()
	object, err := provider.Resolve(ctx, session, *path)
	if err != nil {
		fmt.Fprintln(streams.Err, "fusedock:", err)
		return 1
	}
	if !object.IsDir() {
		fmt.Fprintln(streams.Err, "fusedock: path is not a directory")
		return 1
	}
	objects, err := session.List(ctx, object)
	if err != nil {
		fmt.Fprintln(streams.Err, "fusedock:", err)
		return 1
	}
	visible := objects[:0]
	for _, item := range objects {
		if *showAll || !strings.HasPrefix(item.Name, ".") {
			visible = append(visible, item)
		}
	}
	sort.Slice(visible, func(i, j int) bool { return visible[i].Name < visible[j].Name })
	if *jsonOutput {
		encoder := json.NewEncoder(streams.Out)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(visible); err != nil {
			fmt.Fprintln(streams.Err, "fusedock:", err)
			return 1
		}
		return 0
	}
	table := tabwriter.NewWriter(streams.Out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(table, "TYPE\tSIZE\tMODIFIED\tNAME")
	for _, item := range visible {
		kind, size := "file", formatSize(item.Size)
		if item.IsDir() {
			kind, size = "dir", "-"
		}
		modified := "-"
		if !item.Modified.IsZero() {
			modified = item.Modified.Local().Format("2006-01-02 15:04")
		}
		fmt.Fprintf(table, "%s\t%s\t%s\t%s\n", kind, size, modified, item.Name)
	}
	_ = table.Flush()
	return 0
}

func runMount(ctx context.Context, args []string, streams IO) int {
	flags := pflag.NewFlagSet("mount", pflag.ContinueOnError)
	flags.SetOutput(streams.Err)
	var common providerFlags
	common.bind(flags)
	common.accountStore = streams.Accounts
	attrTTL := flags.Duration("attr-ttl", 2*time.Second, "kernel attribute cache duration")
	entryTTL := flags.Duration("entry-ttl", 2*time.Second, "kernel entry cache duration")
	cacheTTL := flags.Duration("metadata-cache", 2*time.Second, "provider metadata cache duration")
	debug := flags.Bool("debug", false, "enable FUSE protocol diagnostics")
	volumeName := flags.String("volume-name", "FuseDock", "filesystem display name")
	readOnly := flags.Bool("read-only", true, "mount read-only (the only mode in this release)")
	flags.Usage = func() {
		fmt.Fprintln(flags.Output(), "usage: fusedock mount [flags] [provider] <mountpoint>")
		flags.PrintDefaults()
	}
	if helpRequested(args) {
		flags.SetOutput(streams.Out)
		flags.Usage()
		return 0
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, pflag.ErrHelp) {
			return 0
		}
		return 2
	}
	var providerArgs []string
	var mountpoint string
	if common.account != "" {
		if flags.NArg() < 1 || flags.NArg() > 2 {
			fmt.Fprintln(streams.Err, "usage: fusedock mount [flags] --account NAME [provider] <mountpoint>")
			return 2
		}
		mountpoint = flags.Arg(flags.NArg() - 1)
		providerArgs = flags.Args()[:flags.NArg()-1]
	} else {
		if flags.NArg() != 2 {
			fmt.Fprintln(streams.Err, "usage: fusedock mount [flags] <provider> <mountpoint>")
			return 2
		}
		providerArgs = flags.Args()[:1]
		mountpoint = flags.Arg(1)
	}
	if !*readOnly {
		fmt.Fprintln(streams.Err, "fusedock: writable mounts are not implemented; only --read-only=true is supported")
		return 2
	}
	if *attrTTL <= 0 || *entryTTL <= 0 || *cacheTTL < 0 {
		fmt.Fprintln(streams.Err, "fusedock: TTL values must be positive; metadata cache may be zero")
		return 2
	}
	providerID, err := resolveProviderID(ctx, streams, common.account, providerArgs)
	if err != nil {
		fmt.Fprintln(streams.Err, "fusedock:", err)
		return 2
	}
	factory, ok := streams.Registry.Get(providerID)
	if !ok {
		fmt.Fprintf(streams.Err, "fusedock: unknown provider %q\n", providerID)
		return 2
	}
	if err := fusefs.Preflight(mountpoint); err != nil {
		fmt.Fprintln(streams.Err, "fusedock:", err)
		return 1
	}
	session, err := common.open(ctx, factory, promptFunc(streams.In, streams.Out, streams.Err))
	if err != nil {
		fmt.Fprintln(streams.Err, "fusedock:", err)
		return 1
	}
	defer session.Close()
	cached := vfs.New(session, *cacheTTL)
	fmt.Fprintf(streams.Err, "Mounting %s at %s (read-only). Ctrl-C unmounts.\n", factory.Descriptor().Name, mountpoint)
	if err := fusefs.Mount(ctx, cached, mountpoint, fusefs.Options{
		ReadOnly: true, AttrTTL: *attrTTL, EntryTTL: *entryTTL, Debug: *debug, VolumeName: *volumeName,
	}); err != nil {
		fmt.Fprintln(streams.Err, "fusedock:", err)
		return 1
	}
	return 0
}

func formatSize(size int64) string {
	if size < 0 {
		return "-"
	}
	const unit = 1024
	if size < unit {
		return fmt.Sprintf("%d B", size)
	}
	div, exp := int64(unit), 0
	for n := size / unit; n >= unit && exp < 5; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(size)/float64(div), "KMGTPE"[exp])
}

func helpRequested(args []string) bool {
	for _, arg := range args {
		if arg == "--help" || arg == "-h" {
			return true
		}
	}
	return false
}
