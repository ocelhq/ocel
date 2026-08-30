//go:build !windows

package providerkit

import (
	"context"
	"net"
)

func platformDockerAddress() string { return "unix:///var/run/docker.sock" }

func pipeDockerHost(string, string) (DockerHost, bool) { return DockerHost{}, false }

func dialDocker(ctx context.Context, network, target string) (net.Conn, error) {
	return (&net.Dialer{}).DialContext(ctx, network, target)
}
