//go:build !windows

package providerkit

import (
	"fmt"
	"net"
	"os"
	"path/filepath"

	"github.com/ocelhq/ocel/pkg/channel"
)

func listen() (net.Listener, string, error) {
	dir, err := os.MkdirTemp("", "ocel-provider-*")
	if err != nil {
		return nil, "", fmt.Errorf("reserve socket dir: %w", err)
	}
	path := filepath.Join(dir, "provider.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, "", fmt.Errorf("listen on %s: %w", path, err)
	}

	return ln, channel.FormatUnixAddr(path), nil
}
