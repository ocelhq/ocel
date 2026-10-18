package host

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/shell"
	"github.com/ocelhq/ocel/platform/vps/provider/ssh"
)

func TestWhatAnInterruptedDeployLeftIsSweptBeforeAValueIsWritten(t *testing.T) {
	t.Parallel()

	spec := valued()
	rig := runningWith(t, spec)
	swept := rig.at(sweepCommand())
	wrote := rig.at("install -m 0600 /dev/stdin " + shell.Quote(EnvFile(spec.Tier, spec.Name)))
	if swept < 0 || wrote < 0 || swept > wrote {
		t.Fatalf("the sweep ran at %d and the env file was written at %d: a sweep after the write is a sweep of nothing, and a SIGKILL between the write and the forget leaves plaintext nothing but the next deploy's sweep takes", swept, wrote)
	}
	command := sweepCommand()
	for what, wanted := range map[string]string{
		"the production records it sweeps":       shell.Quote(StateDir(environment.TierProduction)),
		"the preview records it sweeps":          shell.Quote(StateDir(environment.TierPreview)),
		"the env files a deploy hands over":      shell.Quote("*" + envFileSuffix),
		"an age that spares a deploy in flight":  "-mmin +" + orphanMinutes,
		"a depth that never reaches the records": "-maxdepth 1",
	} {
		if !strings.Contains(command, wanted) {
			t.Errorf("the sweep runs %q, which names no %s (%s)", command, what, wanted)
		}
	}
	if filepath.Dir(EnvFile(environment.TierProduction, "x")) != StateDir(environment.TierProduction) {
		t.Errorf("the env file sits at %s, which is not the depth the sweep reads", EnvFile(environment.TierProduction, "x"))
	}
}

func TestARegistryLoginIsHandedToTheCheckedDockerAndNeverToAConfigTheDeployLoginWrites(t *testing.T) {
	t.Parallel()

	command, err := pull(provider.RegistryTarget{Server: "ghcr.io", Username: "ada", Password: "hunter2"}, "ghcr.io/shop/web:one", "sha256:0000")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(command, "docker "+boxapi.DockerPullAs+" "+shell.Words([]string{"ghcr.io", "ada", "ghcr.io/shop/web@sha256:0000"})) {
		t.Errorf("the pull logs in with %q, want the checked docker to log in and pull under a config only root writes", command)
	}
	for _, refused := range []string{"DOCKER_CONFIG", "docker login", "hunter2"} {
		if strings.Contains(command, refused) {
			t.Errorf("the pull carries %q: %s", refused, command)
		}
	}
	rig := machine(nil)
	rig.answer = func(said string) (ssh.Result, bool) {
		if strings.Contains(said, "docker image ls -q") {
			return ssh.Result{Stdout: "abc\n"}, true
		}
		return ssh.Result{}, false
	}
	if _, err := rig.host().PullImage(context.Background(), provider.RegistryTarget{Server: "ghcr.io"}, "ghcr.io/shop/web:one", "sha256:0000"); err != nil {
		t.Fatalf("PullImage() = %v", err)
	}
	swept, pulled := rig.at(sweepCommand()), rig.at("docker pull")
	if swept < 0 || pulled < 0 || swept > pulled {
		t.Errorf("the sweep ran at %d and the pull at %d, so a login a killed pull left is swept by nothing until the next deploy runs a container", swept, pulled)
	}
}
