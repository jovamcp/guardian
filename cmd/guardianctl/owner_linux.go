//go:build linux

package main

import (
	"os"
	"syscall"
)

func fileOwner(st os.FileInfo) (uid, gid int) {
	if s, ok := st.Sys().(*syscall.Stat_t); ok {
		return int(s.Uid), int(s.Gid)
	}
	return -1, -1
}
