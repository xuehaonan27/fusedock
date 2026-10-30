package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/spf13/pflag"
	"github.com/xuehaonan27/fusedock/internal/blockcache"
	"github.com/xuehaonan27/fusedock/internal/credentials"
	"github.com/xuehaonan27/fusedock/internal/daemon"
	"github.com/xuehaonan27/fusedock/internal/namespace"
	"github.com/xuehaonan27/fusedock/internal/platform"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

func runDaemon(ctx context.Context, args []string, streams IO) int {
	if len(args) == 0 || helpRequested(args) {
		fmt.Fprintln(streams.Out, "usage: fusedock daemon <run|start|install|uninstall|status|mount|unmount|ls|cat|refresh|reauthenticate|stop|cache-info|cache-clear|cache-prune|cache-verify> [flags]")
		return 0
	}
	flags := pflag.NewFlagSet("daemon", pflag.ContinueOnError)
	flags.SetOutput(streams.Err)
	base, err := os.UserConfigDir()
	if err != nil {
		fmt.Fprintln(streams.Err, err)
		return 1
	}
	dir := flags.String("directory", filepath.Join(base, "fusedock", "service"), "daemon state directory")
	account := flags.String("account", "", "saved account alias")
	id := flags.String("id", "", "mount ID")
	path := flags.String("mountpoint", "", "native mountpoint")
	scope := flags.String("scope", "/", "remote root path")
	persist := flags.Bool("persistent-cache", true, "retain eligible content across restarts")
	ttl := flags.Duration("metadata-ttl", 2*time.Second, "metadata freshness interval")
	memory := flags.Int64("memory-bytes", 64<<20, "shared memory block budget")
	disk := flags.Int64("disk-bytes", 4<<30, "persistent block budget")
	prune := flags.Int64("bytes", 0, "retained byte budget after pruning")
	remotePath := flags.String("path", "/", "path within the selected mount")
	offset := flags.Int64("offset", 0, "first byte to read")
	length := flags.Int64("length", -1, "bytes to read; -1 reads through EOF")
	if err := flags.Parse(args[1:]); err != nil {
		return 2
	}
	fail := func(err error) int { fmt.Fprintln(streams.Err, "fusedock:", err); return 1 }
	if flags.NArg() != 0 {
		return fail(errors.New("unexpected daemon arguments"))
	}
	switch args[0] {
	case "ls", "cat":
		if err := readDaemon(ctx, *dir, args[0], *id, *remotePath, *offset, *length, streams.Out); err != nil {
			return fail(err)
		}
		return 0
	case "install":
		exe, err := os.Executable()
		if err != nil {
			return fail(err)
		}
		if err := platform.InstallService(exe, *dir); err != nil {
			return fail(err)
		}
		fmt.Fprintln(streams.Out, "FuseDock service registered for login and failure recovery.")
		return 0
	case "uninstall":
		if err := platform.UninstallService(); err != nil {
			return fail(err)
		}
		fmt.Fprintln(streams.Out, "FuseDock service registration removed.")
		return 0
	case "run":
		store, err := accountStore(streams)
		if err != nil {
			return fail(err)
		}
		d, err := daemon.New(daemon.Options{Directory: *dir, Accounts: store, Registry: streams.Registry, Mount: platform.Mount, Cache: blockcache.Options{MemoryBytes: *memory, DiskBytes: *disk}})
		if err != nil {
			return fail(err)
		}
		defer d.Close()
		if err := d.Serve(ctx); err != nil {
			return fail(err)
		}
		return 0
	case "start":
		var status any
		if daemon.Call(ctx, *dir, "status", struct{}{}, &status) == nil {
			fmt.Fprintln(streams.Out, "FuseDock daemon is already running.")
			return 0
		}
		if err := credentials.EnsurePrivateParent(filepath.Join(*dir, "daemon.log")); err != nil {
			return fail(err)
		}
		log, err := os.OpenFile(filepath.Join(*dir, "daemon.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			return fail(err)
		}
		defer log.Close()
		exe, err := os.Executable()
		if err != nil {
			return fail(err)
		}
		command := exec.Command(exe, "daemon", "run", "--directory", *dir, "--memory-bytes", fmt.Sprint(*memory), "--disk-bytes", fmt.Sprint(*disk))
		detachDaemon(command)
		command.Stdout = log
		command.Stderr = log
		if err := command.Start(); err != nil {
			return fail(err)
		}
		command.Process.Release()
		timeout, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			if daemon.Call(timeout, *dir, "status", struct{}{}, &status) == nil {
				fmt.Fprintln(streams.Out, "FuseDock daemon started.")
				return 0
			}
			select {
			case <-timeout.Done():
				return fail(errors.New("daemon did not become ready; inspect daemon.log"))
			case <-ticker.C:
			}
		}
	}
	operation := args[0]
	input := map[string]any{}
	switch operation {
	case "stop":
		operation = "shutdown"
	case "mount":
		input["mount"] = daemon.MountSpec{ID: *id, Account: *account, Path: *path, Scope: *scope, Persistent: *persist, MetadataTTL: *ttl}
	case "unmount":
		input["id"] = *id
	case "refresh", "reauthenticate":
		input["account"] = *account
	case "status":
	case "cache-clear":
		store, err := accountStore(streams)
		if err != nil {
			return fail(err)
		}
		config, err := store.Load(ctx)
		if err != nil {
			return fail(err)
		}
		a, ok := config.Find(*account)
		if !ok {
			return fail(errors.New("account not found"))
		}
		input["namespace"] = namespace.Namespace{AccountID: a.AccountID, ProviderID: a.Provider}
		operation = "cache/clear"
	case "cache-info":
		operation = "cache/info"
	case "cache-verify":
		operation = "cache/verify"
	case "cache-prune":
		operation = "cache/prune"
		input["bytes"] = *prune
	default:
		return fail(errors.New("unknown daemon operation"))
	}
	var output any
	if err := daemon.Call(ctx, *dir, operation, input, &output); err != nil {
		return fail(err)
	}
	encoder := json.NewEncoder(streams.Out)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(output); err != nil {
		return fail(err)
	}
	return 0
}
