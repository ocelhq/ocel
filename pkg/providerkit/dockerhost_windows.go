package providerkit

import (
	"context"
	"net"

	"github.com/Microsoft/go-winio"
)

func platformDockerAddress() string { return "npipe:////./pipe/docker_engine" }

func pipeDockerHost(host, rest string) (DockerHost, bool) {
	return DockerHost{Address: host, Network: PipeNetwork, Target: rest}, true
}

func dialDocker(ctx context.Context, network, target string) (net.Conn, error) {
	if network == PipeNetwork {
		return winio.DialPipeContext(ctx, target)
	}
	return (&net.Dialer{}).DialContext(ctx, network, target)
}
