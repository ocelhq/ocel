package imagebuild

import (
	"context"
	"net"

	"github.com/Microsoft/go-winio"
)

func platformAddress() string { return "npipe:////./pipe/docker_engine" }

func pipeDaemon(host, rest string) (daemon, bool) {
	return daemon{address: host, network: pipeNetwork, target: rest}, true
}

func dial(ctx context.Context, network, target string) (net.Conn, error) {
	if network == pipeNetwork {
		return winio.DialPipeContext(ctx, target)
	}
	return (&net.Dialer{}).DialContext(ctx, network, target)
}
