//go:build !windows

package agent

import (
	"fmt"
	"os"
	"syscall"
)

func validateCursorPrivateTreeOwnership(path string, info os.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("cursor private tree entry %q has unavailable ownership metadata", path)
	}
	if stat.Uid != uint32(os.Geteuid()) {
		return fmt.Errorf("cursor private tree entry %q is foreign-owned", path)
	}
	if info.Mode().IsRegular() && stat.Nlink != 1 {
		return fmt.Errorf("cursor private tree file %q is hard-linked", path)
	}
	return nil
}
