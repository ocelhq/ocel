package host

import (
	"context"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit/provider"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
	"github.com/ocelhq/ocel/platform/vps/provider/session"
)

func TestWhatAnInterruptedDeployLeftIsSweptBeforeAValueIsWritten(t *testing.T) {
	t.Parallel()

	spec := valued()
	rig := runningWith(t, spec)
	swept := rig.at(sweepCommand())
	wrote := rig.at("install -m 0600 /dev/stdin " + quoted(EnvFile(spec.Class, spec.Name)))
	if swept < 0 || wrote < 0 || swept > wrote {
		t.Fatalf("the sweep ran at %d and the env file was written at %d: a sweep after the write is a sweep of nothing, and a SIGKILL between the write and the forget leaves plaintext nothing but the next deploy's sweep takes", swept, wrote)
	}
	command := sweepCommand()
	for what, wanted := range map[string]string{
		"the root it sweeps under":               "find " + quoted(stateRoot),
		"the env files a deploy hands over":      quoted("*" + envFileSuffix),
		"the registry configs a pull writes":     quoted(registryPrefix + "*"),
		"an age that spares a deploy in flight":  "-mmin +" + orphanMinutes,
		"a depth that never reaches the records": "-maxdepth 2",
	} {
		if !strings.Contains(command, wanted) {
			t.Errorf("the sweep runs %q, which names no %s (%s)", command, what, wanted)
		}
	}
	if !strings.HasPrefix(EnvFile(edge.ClassProduction, "x"), stateRoot+"/") || strings.Count(strings.TrimPrefix(EnvFile(edge.ClassProduction, "x"), stateRoot+"/"), "/") != 1 {
		t.Errorf("the env file sits at %s, which is not the depth the sweep reads", EnvFile(edge.ClassProduction, "x"))
	}
}

func TestARegistryLoginIsWrittenWhereTheSweepReadsAndSweptBeforeThePull(t *testing.T) {
	t.Parallel()

	command, err := pull(provider.RegistryTarget{Server: "ghcr.io", Username: "ada", Password: "hunter2"}, "ghcr.io/shop/web:one", "sha256:0000")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(command, "mktemp -d "+quoted(stateRoot+"/"+registryPrefix+"XXXXXX")) {
		t.Errorf("the pull writes its login with %q, and a config under /tmp is one a killed session leaves for nothing to sweep", command)
	}
	rig := machine(nil)
	rig.answer = func(said string) (session.Result, bool) {
		if strings.Contains(said, "docker image ls -q") {
			return session.Result{Stdout: "abc\n"}, true
		}
		return session.Result{}, false
	}
	if _, err := rig.host().PullImage(context.Background(), provider.RegistryTarget{Server: "ghcr.io"}, "ghcr.io/shop/web:one", "sha256:0000"); err != nil {
		t.Fatalf("PullImage() = %v", err)
	}
	swept, pulled := rig.at(sweepCommand()), rig.at("docker pull")
	if swept < 0 || pulled < 0 || swept > pulled {
		t.Errorf("the sweep ran at %d and the pull at %d, so a login a killed pull left is swept by nothing until the next deploy runs a container", swept, pulled)
	}
}
