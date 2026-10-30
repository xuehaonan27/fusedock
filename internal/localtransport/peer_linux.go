package localtransport

import (
	"golang.org/x/sys/unix"
	"net"
	"os"
)

func sameUser(c *net.UnixConn) bool {
	raw, err := c.SyscallConn()
	if err != nil {
		return false
	}
	ok := false
	err = raw.Control(func(fd uintptr) {
		u, e := unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
		ok = e == nil && u.Uid == uint32(os.Getuid())
	})
	return err == nil && ok
}
