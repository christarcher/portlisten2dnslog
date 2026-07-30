package client

import (
	"errors"
	"syscall"
)

// Go's syscall package does not export WSAEADDRINUSE.
const wsaAddressInUse syscall.Errno = 10048

func isAddressInUse(err error) bool {
	return errors.Is(err, wsaAddressInUse)
}
