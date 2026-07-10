//go:build windows

package main

import (
	"fmt"
	"net"

	providerv1 "github.com/ocelhq/ocel/pkg/proto/provider/v1"
)

// listen binds the runtime's private local channel. Unix domain sockets aren't
// a viable transport on Windows, so this fallback binds explicitly to
// 127.0.0.1 (never 0.0.0.0) on an ephemeral port. Loopback TCP is a weaker
// isolation posture than the Unix socket and must be revisited before the
// runtime handles real cloud credentials.
func listen() (net.Listener, string, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, "", fmt.Errorf("listen on 127.0.0.1: %w", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	return ln, providerv1.FormatTCPAddr(port), nil
}
