//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package slicer

import (
	"os"
	"syscall"
	"time"
)

func setOpenFileTimes(file *os.File, _ *os.Root, _ string, modified time.Time) error {
	times := []syscall.Timeval{
		syscall.NsecToTimeval(modified.UnixNano()),
		syscall.NsecToTimeval(modified.UnixNano()),
	}
	return syscall.Futimes(int(file.Fd()), times)
}
