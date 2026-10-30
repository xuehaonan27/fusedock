package platform

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func systemdArgument(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "%", "%%", "\n", `\n`).Replace(s) + `"`
}
func InstallService(executable, directory string) error {
	base, err := os.UserConfigDir()
	if err != nil {
		return err
	}
	path := filepath.Join(base, "systemd", "user", "fusedock.service")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	data := fmt.Sprintf("[Unit]\nDescription=FuseDock account and filesystem service\nAfter=network-online.target\n[Service]\nExecStart=%s daemon run --directory %s\nRestart=on-failure\nRestartSec=2\n[Install]\nWantedBy=default.target\n", systemdArgument(executable), systemdArgument(directory))
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		return err
	}
	if out, err := exec.Command("systemctl", "--user", "daemon-reload").CombinedOutput(); err != nil {
		return fmt.Errorf("reload user services: %w: %s", err, out)
	}
	out, err := exec.Command("systemctl", "--user", "enable", "--now", "fusedock.service").CombinedOutput()
	if err != nil {
		return fmt.Errorf("start user service: %w: %s", err, out)
	}
	return nil
}
func UninstallService() error {
	out, err := exec.Command("systemctl", "--user", "disable", "--now", "fusedock.service").CombinedOutput()
	if err != nil {
		return fmt.Errorf("stop user service: %w: %s", err, out)
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(base, "systemd", "user", "fusedock.service")); err != nil {
		return err
	}
	return exec.Command("systemctl", "--user", "daemon-reload").Run()
}
