package providerkit_test

import (
	"runtime"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

func TestWithNoDockerHostSetTheDaemonIsThePlatformsOwnSocket(t *testing.T) {
	t.Setenv(providerkit.DockerHostEnv, "")

	d, err := providerkit.OpenDockerHost()
	if err != nil {
		t.Fatalf("providerkit.OpenDockerHost() with no %s set = %v, so ocel rejects the address it chose for itself", providerkit.DockerHostEnv, err)
	}
	want, network := "unix:///var/run/docker.sock", "unix"
	if runtime.GOOS == "windows" {
		want, network = "npipe:////./pipe/docker_engine", providerkit.PipeNetwork
	}
	if d.Address != want {
		t.Errorf("providerkit.OpenDockerHost() falls back to %q, want %q, the socket a default install of docker listens on", d.Address, want)
	}
	if d.Network != network {
		t.Errorf("providerkit.OpenDockerHost() dials its own default over %q, want %q", d.Network, network)
	}
}

func TestAWindowsPipeIsDialledOnWindowsAndRefusedWhereThereIsNoPipe(t *testing.T) {
	t.Setenv(providerkit.DockerHostEnv, "npipe:////./pipe/docker_engine")

	d, err := providerkit.OpenDockerHost()
	if runtime.GOOS != "windows" {
		if err == nil {
			t.Fatalf("providerkit.OpenDockerHost() accepted a named pipe on %s, where nothing can dial one", runtime.GOOS)
		}
		return
	}
	if err != nil {
		t.Fatalf("providerkit.OpenDockerHost() = %v, want the pipe docker for windows listens on", err)
	}
	if d.Network != providerkit.PipeNetwork || d.Target != "//./pipe/docker_engine" {
		t.Errorf("providerkit.OpenDockerHost() dials %s %q, want the pipe %s names", d.Network, d.Target, providerkit.DockerHostEnv)
	}
}

func TestDockerHostBeatsThePlatformSocket(t *testing.T) {
	t.Setenv(providerkit.DockerHostEnv, "tcp://10.0.0.4:2375")

	d, err := providerkit.OpenDockerHost()
	if err != nil {
		t.Fatalf("providerkit.OpenDockerHost() = %v", err)
	}
	if d.Network != "tcp" || d.Target != "10.0.0.4:2375" {
		t.Errorf("providerkit.OpenDockerHost() dials %s %q, want the tcp daemon %s names", d.Network, d.Target, providerkit.DockerHostEnv)
	}
}

func TestATLSPostureOnARemoteDaemonIsRefusedRatherThanDowngraded(t *testing.T) {
	for _, stated := range []string{providerkit.DockerTLSVerifyEnv, providerkit.DockerCertPathEnv} {
		t.Run(stated, func(t *testing.T) {
			t.Setenv(providerkit.DockerHostEnv, "tcp://build-box:2376")
			t.Setenv(providerkit.DockerTLSVerifyEnv, "")
			t.Setenv(providerkit.DockerCertPathEnv, "")
			t.Setenv(stated, "1")

			_, err := providerkit.OpenDockerHost()
			if err == nil {
				t.Fatal("providerkit.OpenDockerHost() dialled a daemon the user asked to be reached over tls, so the build context crosses the network in the clear")
			}
			if !strings.Contains(err.Error(), stated) {
				t.Errorf("providerkit.OpenDockerHost() = %v, and the reader is never told which variable ocel cannot honour", err)
			}
		})
	}
}

func TestARemoteDaemonWithNoTLSAskedForIsDialledAsGiven(t *testing.T) {
	t.Setenv(providerkit.DockerHostEnv, "tcp://build-box:2375")
	t.Setenv(providerkit.DockerTLSVerifyEnv, "")
	t.Setenv(providerkit.DockerCertPathEnv, "")

	d, err := providerkit.OpenDockerHost()
	if err != nil {
		t.Fatalf("providerkit.OpenDockerHost() = %v, want the plain tcp daemon nothing asked to be secured", err)
	}
	if d.Network != "tcp" || d.Target != "build-box:2375" {
		t.Errorf("providerkit.OpenDockerHost() dials %s %q, want the tcp daemon %s names", d.Network, d.Target, providerkit.DockerHostEnv)
	}
}
