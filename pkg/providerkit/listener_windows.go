//go:build windows

package providerkit

import (
	"fmt"
	"net"

	"github.com/ocelhq/ocel/pkg/channel"
)

func listen() (net.Listener, string, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, "", fmt.Errorf("listen on 127.0.0.1: %w", err)
	}

	port := ln.Addr().(*net.TCPAddr).Port
	return ln, channel.FormatTCPAddr(port), nil
}
