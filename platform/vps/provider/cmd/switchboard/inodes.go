package main

import (
	"fmt"
	"io"
	"strings"
	"syscall"
)

func inodes(paths []string, out, errs io.Writer) int {
	if len(paths) == 0 {
		return usage(errs)
	}
	var said strings.Builder
	for _, path := range paths {
		var info syscall.Stat_t
		if err := syscall.Stat(path, &info); err != nil {
			return refuse(errs, fmt.Errorf("stat %s: %w", path, err))
		}
		fmt.Fprintf(&said, "%s %d:%d\n", path, info.Dev, info.Ino)
	}
	_, _ = io.WriteString(out, said.String())
	return 0
}
