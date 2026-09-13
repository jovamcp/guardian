//go:build !linux

package main

import "os"

func fileOwner(os.FileInfo) (uid, gid int) { return 0, 0 }
