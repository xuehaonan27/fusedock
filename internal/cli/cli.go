package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/pflag"
	"golang.org/x/term"

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
  fusedock ls [flags] <provider>
  fusedock mount [flags] <provider> <mountpoint>
  fusedock version

Provider configuration is expressed with --option key=value. Credentials are
passed with --credential, --credential-env, or an interactive prompt; they are
never written by this initial version.

Run 'fusedock providers' for provider IDs and their options.
`)
}

type providerFlags struct {
	options       []string
	credentials   []string
	credentialEnv []string
	username      string
}

func (f *providerFlags) bind(flags *pflag.FlagSet) {
	flags.StringArrayVarP(&f.options, "option", "o", nil, "provider option key=value (repeatable)")
	flags.StringArrayVar(&f.credentials, "credential", nil, "credential key=value (repeatable)")
	flags.StringArrayVar(&f.credentialEnv, "credential-env", nil, "read credential from environment, key=ENV (repeatable)")
	flags.StringVar(&f.username, "username", "", "shortcut for --credential username=VALUE")
}

func (f *providerFlags) open(ctx context.Context, factory provider.Factory, prompt provider.PromptFunc) (provider.Session, error) {
	options, err := parsePairs(f.options)
	if err != nil {
		return nil, fmt.Errorf("invalid provider option: %w", err)
	}
	secrets, err := parseSecrets(f.credentials, f.credentialEnv)
	if err != nil {
		return nil, err
	}
	defer clearSecrets(secrets)
	if err := validateInputs(factory.Descriptor(), options, secrets, f.username); err != nil {
		return nil, err
	}
	request := provider.OpenRequest{Username: f.username, Secrets: secrets, Options: options, Prompt: prompt}
	return factory.Open(ctx, request)
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
	path := flags.StringP("path", "p", "/", "provider path to list")
	jsonOutput := flags.Bool("json", false, "write a JSON array")
	showAll := flags.BoolP("all", "a", false, "include hidden local entries")
	flags.Usage = func() {
		fmt.Fprintln(flags.Output(), "usage: fusedock ls [flags] <provider>")
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
	if flags.NArg() != 1 {
		fmt.Fprintln(streams.Err, "usage: fusedock ls [flags] <provider>")
		return 2
	}
	factory, ok := streams.Registry.Get(flags.Arg(0))
	if !ok {
		fmt.Fprintf(streams.Err, "fusedock: unknown provider %q\n", flags.Arg(0))
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
	attrTTL := flags.Duration("attr-ttl", 2*time.Second, "kernel attribute cache duration")
	entryTTL := flags.Duration("entry-ttl", 2*time.Second, "kernel entry cache duration")
	cacheTTL := flags.Duration("metadata-cache", 2*time.Second, "provider metadata cache duration")
	debug := flags.Bool("debug", false, "enable FUSE protocol diagnostics")
	volumeName := flags.String("volume-name", "FuseDock", "filesystem display name")
	readOnly := flags.Bool("read-only", true, "mount read-only (the only mode in this release)")
	flags.Usage = func() {
		fmt.Fprintln(flags.Output(), "usage: fusedock mount [flags] <provider> <mountpoint>")
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
	if flags.NArg() != 2 {
		fmt.Fprintln(streams.Err, "usage: fusedock mount [flags] <provider> <mountpoint>")
		return 2
	}
	if !*readOnly {
		fmt.Fprintln(streams.Err, "fusedock: writable mounts are not implemented; only --read-only=true is supported")
		return 2
	}
	if *attrTTL <= 0 || *entryTTL <= 0 || *cacheTTL < 0 {
		fmt.Fprintln(streams.Err, "fusedock: TTL values must be positive; metadata cache may be zero")
		return 2
	}
	providerID, mountpoint := flags.Arg(0), flags.Arg(1)
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
