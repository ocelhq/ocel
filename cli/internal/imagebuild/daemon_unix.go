//go:build !windows

package imagebuild

import (
	"context"
	"net"
)

func platformAddress() string { return "unix:///var/run/docker.sock" }

func pipeDaemon(string, string) (daemon, bool) { return daemon{}, false }

func dial(ctx context.Context, network, target string) (net.Conn, error) {
	return (&net.Dialer{}).DialContext(ctx, network, target)
}
