package localtransport

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
	"net"
)

func Endpoint(dir string) string {
	return fmt.Sprintf(`\\.\pipe\fusedock-%x`, sha256.Sum256([]byte(dir)))
}
func Listen(dir string) (net.Listener, error) {
	token := windows.GetCurrentProcessToken()
	user, err := token.GetTokenUser()
	if err != nil {
		return nil, err
	}
	return winio.ListenPipe(Endpoint(dir), &winio.PipeConfig{SecurityDescriptor: "D:P(A;;GA;;;" + user.User.Sid.String() + ")", InputBufferSize: 65536, OutputBufferSize: 65536})
}
func Dial(ctx context.Context, dir string) (net.Conn, error) {
	c, err := winio.DialPipeContext(ctx, Endpoint(dir))
	if err != nil {
		return nil, err
	}
	file := c.(interface{ Fd() uintptr })
	var pid uint32
	if err := windows.GetNamedPipeServerProcessId(windows.Handle(file.Fd()), &pid); err != nil {
		c.Close()
		return nil, err
	}
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		c.Close()
		return nil, err
	}
	defer windows.CloseHandle(process)
	var token windows.Token
	if err := windows.OpenProcessToken(process, windows.TOKEN_QUERY, &token); err != nil {
		c.Close()
		return nil, err
	}
	defer token.Close()
	peer, err := token.GetTokenUser()
	if err != nil {
		c.Close()
		return nil, err
	}
	self, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		c.Close()
		return nil, err
	}
	if !peer.User.Sid.Equals(self.User.Sid) {
		c.Close()
		return nil, errors.New("daemon peer belongs to another user")
	}
	return c, nil
}
