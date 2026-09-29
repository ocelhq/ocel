package docker

import (
	"testing"

	"github.com/ocelhq/ocel/pkg/images"
)

func TestRefuseRemoteDaemon(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		host    images.DockerHost
		refused bool
	}{
		{"a local socket is used", images.DockerHost{Network: "unix", Target: "/var/run/docker.sock"}, false},
		{"a local pipe is used", images.DockerHost{Network: images.PipeNetwork, Target: `\\.\pipe\docker_engine`}, false},
		{"tcp to localhost is used", images.DockerHost{Network: "tcp", Target: "localhost:2375"}, false},
		{"tcp to loopback v4 is used", images.DockerHost{Network: "tcp", Target: "127.0.0.1:2375"}, false},
		{"tcp to loopback v6 is used", images.DockerHost{Network: "tcp", Target: "[::1]:2375"}, false},
		{"tcp to another machine by name is refused", images.DockerHost{Network: "tcp", Target: "build-box.internal:2375"}, true},
		{"tcp to another address is refused", images.DockerHost{Network: "tcp", Target: "10.0.0.7:2375"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if err := refuseRemoteDaemon(tc.host); (err != nil) != tc.refused {
				t.Fatalf("refuseRemoteDaemon(%+v) = %v, want refused %v", tc.host, err, tc.refused)
			}
		})
	}
}
