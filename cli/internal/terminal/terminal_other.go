//go:build !windows

package terminal

import "io"

func enableVirtualTerminal(io.Writer) error { return nil }
