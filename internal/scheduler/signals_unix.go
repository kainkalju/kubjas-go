//go:build !windows

package scheduler

import (
	"os"
	"strings"
	"syscall"
)

func signalFromInt(n int) os.Signal {
	return syscall.Signal(n)
}

func signalByName(name string) os.Signal {
	name = strings.ToUpper(name)
	m := map[string]syscall.Signal{
		"HUP":  syscall.SIGHUP,
		"INT":  syscall.SIGINT,
		"QUIT": syscall.SIGQUIT,
		"KILL": syscall.SIGKILL,
		"TERM": syscall.SIGTERM,
		"USR1": syscall.SIGUSR1,
		"USR2": syscall.SIGUSR2,
	}
	if sig, ok := m[name]; ok {
		return sig
	}
	return syscall.SIGTERM
}
