//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd

package slicer

import (
	"os"
	"time"
)

func setOpenFileTimes(_ *os.File, root *os.Root, name string, modified time.Time) error {
	return root.Chtimes(name, modified, modified)
}
