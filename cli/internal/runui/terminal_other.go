//go:build !windows

package runui

import "io"

func enableVirtualTerminal(io.Writer) error { return nil }
