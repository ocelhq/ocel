package imagebuild

import (
	"runtime"
	"strings"
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

func TestATLSPostureOnARemoteDaemonIsRefusedRatherThanDowngraded(t *testing.T) {
	for _, stated := range []string{DockerTLSVerifyEnv, DockerCertPathEnv} {
		t.Run(stated, func(t *testing.T) {
			t.Setenv(DockerHostEnv, "tcp://build-box:2376")
			t.Setenv(DockerTLSVerifyEnv, "")
			t.Setenv(DockerCertPathEnv, "")
			t.Setenv(stated, "1")

			_, err := openDaemon()
			if err == nil {
				t.Fatal("openDaemon() dialled a daemon the user asked to be reached over tls, so the build context crosses the network in the clear")
			}
			if !strings.Contains(err.Error(), stated) {
				t.Errorf("openDaemon() = %v, and the reader is never told which variable ocel cannot honour", err)
			}
		})
	}
}

func TestARemoteDaemonWithNoTLSAskedForIsDialledAsGiven(t *testing.T) {
	t.Setenv(DockerHostEnv, "tcp://build-box:2375")
	t.Setenv(DockerTLSVerifyEnv, "")
	t.Setenv(DockerCertPathEnv, "")

	d, err := openDaemon()
	if err != nil {
		t.Fatalf("openDaemon() = %v, want the plain tcp daemon nothing asked to be secured", err)
	}
	if d.network != "tcp" || d.target != "build-box:2375" {
		t.Errorf("openDaemon() dials %s %q, want the tcp daemon %s names", d.network, d.target, DockerHostEnv)
	}
}
