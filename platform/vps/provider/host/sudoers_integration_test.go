//go:build integration

package host

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/enginetest"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/shell"
)

const sudoImage = "ubuntu@sha256:fe81560990fae86dc4743202b2cb4d3df98d0bcf949173cecef15ce1127ef852"

type sudoBox struct {
	t    *testing.T
	name string
}

func newSudoBox(t *testing.T) sudoBox {
	t.Helper()
	enginetest.RequireDocker(t)
	name := "ocel-sudoers-" + strings.ToLower(strings.NewReplacer("/", "-", "_", "-").Replace(t.Name()))
	run := append([]string{"run", "--detach", "--rm", "--name", name, "--network", enginetest.Network(t, "sudoers")}, enginetest.RunLabelArgs(t)...)
	if out, err := exec.Command(dockerEngine, append(run, "--entrypoint", "sleep", sudoImage, "600")...).CombinedOutput(); err != nil {
		t.Fatalf("start a box with sudo: %v\n%s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command(dockerEngine, "rm", "--force", name).Run() })
	box := sudoBox{t: t, name: name}
	box.root("apt-get update -qq >/dev/null && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq sudo >/dev/null")
	box.root("useradd -r -M " + boxapi.DeployLogin)
	return box
}

func (b sudoBox) root(script string) string {
	b.t.Helper()
	out, err := exec.Command(dockerEngine, "exec", b.name, "sh", "-c", script).CombinedOutput()
	if err != nil {
		b.t.Fatalf("%s: %v\n%s", script, err, out)
	}
	return string(out)
}

func (b sudoBox) asDeploy(env []string, argv []string) (string, error) {
	b.t.Helper()
	args := append([]string{"exec", "--user", boxapi.DeployLogin}, append(envFlags(env), b.name)...)
	out, err := exec.Command(dockerEngine, append(args, argv...)...).CombinedOutput()
	return string(out), err
}

func envFlags(env []string) []string {
	var flags []string
	for _, kv := range env {
		flags = append(flags, "--env", kv)
	}
	return flags
}

func (b sudoBox) write(path string, mode string, content []byte) {
	b.t.Helper()
	b.root("mkdir -p $(dirname " + shell.Quote(path) + ") && printf '%s' " + shell.Quote(string(content)) + " > " + shell.Quote(path) + " && chmod " + mode + " " + shell.Quote(path))
}

func TestTheSudoersLinesTheBootstrapWritesLetTheConnectorRunWhatItRuns(t *testing.T) {
	box := newSudoBox(t)
	box.write(sudoersSign, "0440", signSudoers())
	box.write(sudoersRunner, "0440", runnerSudoers())
	box.root("visudo -c")
	box.write(LiveBinary, "0755", []byte("#!/bin/sh\necho \"signed $*\"\n"))
	box.write(boxapi.RunnerDocker, "0755", []byte("#!/bin/sh\necho \"docker $* token=$OCEL_JOB_TOKEN\"\n"))

	said, err := box.asDeploy(nil, boxapi.SignArgv())
	if err != nil || strings.TrimSpace(said) != "signed "+boxapi.SignCommand {
		t.Errorf("%v = %q, %v, want the signer run as root", boxapi.SignArgv(), said, err)
	}

	argv := boxapi.RunnerDockerArgv("run", "--rm", "ocel-job:1.2.3")
	said, err = box.asDeploy([]string{"OCEL_JOB_TOKEN=job-token"}, argv)
	if err != nil || strings.TrimSpace(said) != "docker run --rm ocel-job:1.2.3 token=job-token" {
		t.Errorf("%v = %q, %v, want docker run as root with the job token kept", argv, said, err)
	}

	if said, err := box.asDeploy(nil, []string{"sudo", "-n", "/bin/sh", "-c", "id"}); err == nil {
		t.Errorf("sudo let ocel-deploy run a shell: %q", said)
	}
}
