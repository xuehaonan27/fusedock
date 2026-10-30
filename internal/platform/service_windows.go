package platform

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"github.com/xuehaonan27/fusedock/internal/credentials"
	"golang.org/x/sys/windows"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

func xmlValue(value string) string {
	var b bytes.Buffer
	xml.EscapeText(&b, []byte(value))
	return b.String()
}
func InstallService(executable, directory string) error {
	if err := credentials.EnsurePrivateParent(filepath.Join(directory, "service.xml")); err != nil {
		return err
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	sid := xmlValue(user.User.Sid.String())
	data := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?><Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task"><Triggers><LogonTrigger><Enabled>true</Enabled><UserId>%s</UserId></LogonTrigger></Triggers><Principals><Principal id="Author"><UserId>%s</UserId><LogonType>InteractiveToken</LogonType><RunLevel>LeastPrivilege</RunLevel></Principal></Principals><Settings><MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy><DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries><StopIfGoingOnBatteries>false</StopIfGoingOnBatteries><ExecutionTimeLimit>PT0S</ExecutionTimeLimit><RestartOnFailure><Interval>PT1M</Interval><Count>10</Count></RestartOnFailure></Settings><Actions Context="Author"><Exec><Command>%s</Command><Arguments>daemon run --directory %s</Arguments></Exec></Actions></Task>`, sid, sid, xmlValue(executable), xmlValue(syscall.EscapeArg(directory)))
	file := filepath.Join(directory, "service.xml")
	if err := os.WriteFile(file, []byte(data), 0600); err != nil {
		return err
	}
	if out, err := exec.Command("schtasks", "/Create", "/TN", "FuseDock", "/XML", file, "/F").CombinedOutput(); err != nil {
		return fmt.Errorf("register task: %w: %s", err, out)
	}
	return exec.Command("schtasks", "/Run", "/TN", "FuseDock").Run()
}
func UninstallService() error {
	exec.Command("schtasks", "/End", "/TN", "FuseDock").Run()
	out, err := exec.Command("schtasks", "/Delete", "/TN", "FuseDock", "/F").CombinedOutput()
	if err != nil {
		return fmt.Errorf("remove task: %w: %s", err, out)
	}
	return nil
}
