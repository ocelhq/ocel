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
	f, err := os.CreateTemp("", "ocel-provider-*.sock")
	if err != nil {
		return nil, "", fmt.Errorf("reserve socket path: %w", err)
	}
	path := f.Name()
	f.Close()
	if err := os.Remove(path); err != nil {
		return nil, "", fmt.Errorf("clear reserved socket path: %w", err)
	}

	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, "", fmt.Errorf("listen on %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
		return nil, "", fmt.Errorf("restrict socket permissions: %w", err)
	}

	return ln, providerv1.FormatUnixAddr(path), nil
}
