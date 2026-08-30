package imagebuild

import (
	"runtime"
	"testing"
)

func TestWithNoDockerHostSetTheDaemonIsThePlatformsOwnSocket(t *testing.T) {
	t.Setenv(DockerHostEnv, "")

	d, err := openDaemon()
	if err != nil {
		t.Fatalf("openDaemon() with no %s set = %v", DockerHostEnv, err)
	}
	want := "unix:///var/run/docker.sock"
	if runtime.GOOS == "windows" {
		want = "npipe:////./pipe/docker_engine"
	}
	if d.address != want {
		t.Errorf("openDaemon() falls back to %q, want %q, the socket a default install of docker listens on", d.address, want)
	}
}

func TestDockerHostBeatsThePlatformSocket(t *testing.T) {
	t.Setenv(DockerHostEnv, "tcp://10.0.0.4:2375")

	d, err := openDaemon()
	if err != nil {
		t.Fatalf("openDaemon() = %v", err)
	}
	if d.network != "tcp" || d.target != "10.0.0.4:2375" {
		t.Errorf("openDaemon() dials %s %q, want the tcp daemon %s names", d.network, d.target, DockerHostEnv)
	}
}
