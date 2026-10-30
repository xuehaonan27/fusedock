package platform

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"github.com/xuehaonan27/fusedock/internal/credentials"
	"os"
	"os/exec"
	"path/filepath"
)

func xmlValue(value string) string {
	var b bytes.Buffer
	xml.EscapeText(&b, []byte(value))
	return b.String()
}
func InstallService(executable, directory string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	path := filepath.Join(home, "Library", "LaunchAgents", "dev.fusedock.daemon.plist")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	if err := credentials.EnsurePrivateParent(filepath.Join(directory, "daemon.log")); err != nil {
		return err
	}
	data := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict><key>Label</key><string>dev.fusedock.daemon</string><key>ProgramArguments</key><array><string>%s</string><string>daemon</string><string>run</string><string>--directory</string><string>%s</string></array><key>RunAtLoad</key><true/><key>KeepAlive</key><dict><key>SuccessfulExit</key><false/></dict><key>ProcessType</key><string>Background</string><key>StandardOutPath</key><string>%s</string><key>StandardErrorPath</key><string>%s</string></dict></plist>`, xmlValue(executable), xmlValue(directory), xmlValue(filepath.Join(directory, "daemon.log")), xmlValue(filepath.Join(directory, "daemon.log")))
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		return err
	}
	output, err := exec.Command("launchctl", "bootstrap", fmt.Sprintf("gui/%d", os.Getuid()), path).CombinedOutput()
	if err != nil {
		return fmt.Errorf("register launch agent: %w: %s", err, output)
	}
	return nil
}
func UninstallService() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	output, err := exec.Command("launchctl", "bootout", fmt.Sprintf("gui/%d/dev.fusedock.daemon", os.Getuid())).CombinedOutput()
	if err != nil {
		return fmt.Errorf("unregister launch agent: %w: %s", err, output)
	}
	return os.Remove(filepath.Join(home, "Library", "LaunchAgents", "dev.fusedock.daemon.plist"))
}
