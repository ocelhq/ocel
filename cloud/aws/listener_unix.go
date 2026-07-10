//go:build !windows

package main

import (
	"fmt"
	"net"
	"os"

	providerv1 "github.com/ocelhq/ocel/pkg/proto/provider/v1"
)

// listen binds the provider's private local channel: a Unix domain socket
// at a fresh, uniquely-named path. This is the intended isolation boundary
// (paired with the per-session token); see listener_windows.go for the
// fallback where Unix sockets aren't viable.
func listen() (net.Listener, string, error) {
	dir, err := os.MkdirTemp("", "ocel-provider-*")
	if err != nil {
	    return nil, "", fmt.Errorf("reserve socket dir: %w", err)
	}
	// dir is 0700 by MkdirTemp — only the owner can traverse it,
	// so nothing can reach the socket regardless of the socket's own mode.
	path := filepath.Join(dir, "provider.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
	    return nil, "", fmt.Errorf("listen on %s: %w", path, err)
	}

	return ln, providerv1.FormatUnixAddr(path), nil
}
