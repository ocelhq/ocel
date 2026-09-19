package docker

import (
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

func TestAContainerIsReachedWhereItsDaemonIs(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name      string
		host      providerkit.DockerHost
		publishOn string
		reachedAt string
	}{
		{"a local socket", providerkit.DockerHost{Network: "unix", Target: "/var/run/docker.sock"}, "127.0.0.1", "127.0.0.1"},
		{"a local pipe", providerkit.DockerHost{Network: providerkit.PipeNetwork, Target: `\\.\pipe\docker_engine`}, "127.0.0.1", "127.0.0.1"},
		{"tcp on this machine", providerkit.DockerHost{Network: "tcp", Target: "localhost:2375"}, "127.0.0.1", "127.0.0.1"},
		{"tcp on loopback v6", providerkit.DockerHost{Network: "tcp", Target: "[::1]:2375"}, "127.0.0.1", "127.0.0.1"},
		{"tcp on another machine", providerkit.DockerHost{Network: "tcp", Target: "build-box.internal:2375"}, "0.0.0.0", "build-box.internal"},
		{"tcp on another address", providerkit.DockerHost{Network: "tcp", Target: "10.0.0.7:2375"}, "0.0.0.0", "10.0.0.7"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			publishOn, reachedAt := published(tc.host)
			if publishOn.String() != tc.publishOn || reachedAt != tc.reachedAt {
				t.Fatalf("published = %s, %s, want %s, %s", publishOn, reachedAt, tc.publishOn, tc.reachedAt)
			}
		})
	}
}
