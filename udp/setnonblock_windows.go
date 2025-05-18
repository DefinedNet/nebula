//go:build windows
// +build windows

package udp

import "syscall"

// setUDPSocketNonblock sets a socket to non-blocking mode on Windows.
func setUDPSocketNonblock(fd uintptr) error {
	return syscall.SetNonblock(syscall.Handle(fd), true)
}
