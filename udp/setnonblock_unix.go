//go:build !windows
// +build !windows

package udp

import "syscall"

// setUDPSocketNonblock sets a socket to non-blocking mode on Unix-like systems.
func setUDPSocketNonblock(fd uintptr) error {
	return syscall.SetNonblock(int(fd), true)
}
