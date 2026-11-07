//go:build unix

package regularfile

import "syscall"

const nonblock = syscall.O_NONBLOCK
