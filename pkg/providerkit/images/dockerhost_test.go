package images_test

import (
	"runtime"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit/images"
)

func TestWithNoDockerHostSetTheDaemonIsThePlatformsOwnSocket(t *testing.T) {
	t.Setenv(images.DockerHostEnv, "")

	d, err := images.DockerHostFromEnv()
	if err != nil {
		t.Fatalf("images.DockerHostFromEnv() with no %s set = %v, so ocel rejects the address it chose for itself", images.DockerHostEnv, err)
	}
	want, network := "unix:///var/run/docker.sock", "unix"
	if runtime.GOOS == "windows" {
		want, network = "npipe:////./pipe/docker_engine", images.PipeNetwork
	}
	if d.Address != want {
		t.Errorf("images.DockerHostFromEnv() falls back to %q, want %q, the socket a default install of docker listens on", d.Address, want)
	}
	if d.Network != network {
		t.Errorf("images.DockerHostFromEnv() dials its own default over %q, want %q", d.Network, network)
	}
}

func TestAWindowsPipeIsDialledOnWindowsAndRefusedWhereThereIsNoPipe(t *testing.T) {
	t.Setenv(images.DockerHostEnv, "npipe:////./pipe/docker_engine")

	d, err := images.DockerHostFromEnv()
	if runtime.GOOS != "windows" {
		if err == nil {
			t.Fatalf("images.DockerHostFromEnv() accepted a named pipe on %s, where nothing can dial one", runtime.GOOS)
		}
		return
	}
	if err != nil {
		t.Fatalf("images.DockerHostFromEnv() = %v, want the pipe docker for windows listens on", err)
	}
	if d.Network != images.PipeNetwork || d.Target != "//./pipe/docker_engine" {
		t.Errorf("images.DockerHostFromEnv() dials %s %q, want the pipe %s names", d.Network, d.Target, images.DockerHostEnv)
	}
}

func TestDockerHostBeatsThePlatformSocket(t *testing.T) {
	t.Setenv(images.DockerHostEnv, "tcp://10.0.0.4:2375")

	d, err := images.DockerHostFromEnv()
	if err != nil {
		t.Fatalf("images.DockerHostFromEnv() = %v", err)
	}
	if d.Network != "tcp" || d.Target != "10.0.0.4:2375" {
		t.Errorf("images.DockerHostFromEnv() dials %s %q, want the tcp daemon %s names", d.Network, d.Target, images.DockerHostEnv)
	}
}

func TestATLSPostureOnARemoteDaemonIsRefusedRatherThanDowngraded(t *testing.T) {
	for _, stated := range []string{images.DockerTLSVerifyEnv, images.DockerCertPathEnv} {
		t.Run(stated, func(t *testing.T) {
			t.Setenv(images.DockerHostEnv, "tcp://build-box:2376")
			t.Setenv(images.DockerTLSVerifyEnv, "")
			t.Setenv(images.DockerCertPathEnv, "")
			t.Setenv(stated, "1")

			_, err := images.DockerHostFromEnv()
			if err == nil {
				t.Fatal("images.DockerHostFromEnv() dialled a daemon the user asked to be reached over tls, so the build context crosses the network in the clear")
			}
			if !strings.Contains(err.Error(), stated) {
				t.Errorf("images.DockerHostFromEnv() = %v, and the reader is never told which variable ocel cannot honour", err)
			}
		})
	}
}

func TestARemoteDaemonWithNoTLSAskedForIsDialledAsGiven(t *testing.T) {
	t.Setenv(images.DockerHostEnv, "tcp://build-box:2375")
	t.Setenv(images.DockerTLSVerifyEnv, "")
	t.Setenv(images.DockerCertPathEnv, "")

	d, err := images.DockerHostFromEnv()
	if err != nil {
		t.Fatalf("images.DockerHostFromEnv() = %v, want the plain tcp daemon nothing asked to be secured", err)
	}
	if d.Network != "tcp" || d.Target != "build-box:2375" {
		t.Errorf("images.DockerHostFromEnv() dials %s %q, want the tcp daemon %s names", d.Network, d.Target, images.DockerHostEnv)
	}
}
