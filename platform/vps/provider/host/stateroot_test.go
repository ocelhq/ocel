package host

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/envfile"
	"github.com/ocelhq/ocel/platform/vps/provider/release"
	"github.com/ocelhq/ocel/platform/vps/provider/shell"
	"github.com/ocelhq/ocel/platform/vps/provider/ssh"
)

func loggedInAs(user string, facts ssh.Facts) *bench {
	rig := newBench(nil)
	rig.dest.User = user
	rig.facts = facts
	imaged(rig, false)
	return rig
}

func commandWith(t *testing.T, rig *bench, marker string) string {
	t.Helper()
	for _, command := range rig.commands() {
		if strings.Contains(command, marker) {
			return command
		}
	}
	t.Fatalf("nothing ran %q: %v", marker, rig.commands())
	return ""
}

func TestALoginWithSudoLoadsImagesAsTheDeployLogin(t *testing.T) {
	rig := loggedInAs("ubuntu", ssh.Facts{Sudo: true, Systemd: true})

	if _, err := rig.host().images.Load(context.Background(), imageRef, strings.NewReader("tar-bytes")); err != nil {
		t.Fatalf("LoadImage() as a login with sudo = %v", err)
	}
	if loaded := commandWith(t, rig, "docker load"); !strings.HasPrefix(loaded, "sudo -n -u "+deployUser+" env PATH="+deployPath+" sh -c "+strings.TrimSuffix(shell.Quote("flock -x "+shell.Quote(boxapi.ImagesLock)), "'")) {
		t.Errorf("the load ran as %q: %s is %s's and closed to every other login, so the lock is taken as %s", loaded, boxapi.StateRoot, deployUser, deployUser)
	}
}

func TestARootLoginLoadsImagesAsTheDeployLogin(t *testing.T) {
	rig := loggedInAs("root", ssh.Facts{Root: true, Systemd: true})

	if _, err := rig.host().images.Load(context.Background(), imageRef, strings.NewReader("tar-bytes")); err != nil {
		t.Fatalf("LoadImage() as root = %v", err)
	}
	if loaded := commandWith(t, rig, "docker load"); !strings.HasPrefix(loaded, "runuser -u "+deployUser+" -- env PATH="+deployPath+" sh -c "+strings.TrimSuffix(shell.Quote("flock -x "+shell.Quote(boxapi.ImagesLock)), "'")) {
		t.Errorf("the load ran as %q: a lock root creates under %s is one %s cannot open on its next deploy", loaded, boxapi.StateRoot, deployUser)
	}
}

func TestTheDeployLoginLoadsImagesAsItself(t *testing.T) {
	rig := loggedInAs(deployUser, ssh.Facts{Systemd: true})
	rig.floor = refusal.Refuse(refusal.CodeDenied, "%s can neither act as root nor run sudo without a password", deployUser)

	if _, err := rig.host().images.Load(context.Background(), imageRef, strings.NewReader("tar-bytes")); err != nil {
		t.Fatalf("LoadImage() as %s = %v", deployUser, err)
	}
	if loaded := commandWith(t, rig, "docker load"); !strings.HasPrefix(loaded, "flock -x "+shell.Quote(boxapi.ImagesLock)) {
		t.Errorf("the load ran as %q, want no elevation: %s owns %s and can sudo nothing", loaded, deployUser, boxapi.StateRoot)
	}
}

func TestALoginWithSudoRecordsReleasesAsTheDeployLogin(t *testing.T) {
	rig := loggedInAs("ubuntu", ssh.Facts{Sudo: true, Systemd: true})

	if err := rig.host().images.Promote(context.Background(), environment.TierProduction, "shop", "web", imageRef); err != nil {
		t.Fatalf("Promote() as a login with sudo = %v", err)
	}
	if recorded := commandWith(t, rig, shell.Quote(boxapi.ReleasesHelper)); !strings.HasPrefix(recorded, "sudo -n -u "+deployUser+" env PATH="+deployPath+" sh -c "+strings.TrimSuffix(shell.Quote(shell.Quote(boxapi.ReleasesHelper)), "'")) {
		t.Errorf("the release was recorded as %q, want it written as %s, who owns %s", recorded, deployUser, boxapi.ReleasesDir)
	}
}

func TestALoginWithoutSudoIsToldToDeployAsTheDeployLogin(t *testing.T) {
	rig := loggedInAs("ubuntu", ssh.Facts{Systemd: true})
	rig.floor = refusal.Refuse(refusal.CodeDenied, "ubuntu@ocelbox can neither act as root nor run sudo without a password")

	_, err := rig.host().images.Load(context.Background(), imageRef, strings.NewReader("tar-bytes"))
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeDenied {
		t.Fatalf("LoadImage() as a login that cannot sudo = %v, want a denial", err)
	}
	if !strings.Contains(err.Error(), deployUser) || !strings.Contains(err.Error(), boxapi.StateRoot) {
		t.Errorf("the denial reads %q, and never names %s or the login that can reach it", err, boxapi.StateRoot)
	}
	for _, command := range rig.commands() {
		if strings.Contains(command, "docker load") {
			t.Errorf("the load ran as %q where it can only be refused", command)
		}
	}
}

func TestADaemonThatIsDownIsReportedWhenTheDeployLoginIsActedAs(t *testing.T) {
	rig := loggedInAs("ubuntu", ssh.Facts{Sudo: true, Systemd: true})
	rig.answer = func(command string) (ssh.Result, bool) {
		if strings.Contains(command, ssh.DockerReach) {
			return ssh.Result{Code: 1, Stderr: "Cannot connect to the Docker daemon at unix:///var/run/docker.sock. Is the docker daemon running?"}, true
		}
		return ssh.Result{}, false
	}

	err := rig.host().containers.TakeDown(context.Background(), environment.TierProduction, "shop-web")
	if err == nil || !strings.Contains(err.Error(), "Is the docker daemon running?") {
		t.Fatalf("TakeDown() over a daemon that is down = %v, want the probe's reason: take down ignores what docker stop and rm say, so without the probe a container still running reads as taken down", err)
	}
	for _, command := range rig.commands() {
		if strings.Contains(command, "docker stop") {
			t.Errorf("%q ran against a daemon the probe found down", command)
		}
	}
}

func actedAsTheDeployLogin(command string) (string, bool) {
	return strings.CutPrefix(command, "sudo -n -u "+deployUser+" ")
}

func ranHereFromAPipeOnlyAnotherLoginCouldReopen(t *testing.T, script, fed string) error {
	t.Helper()
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = read.Close() }()
	if err := read.Chmod(0); err != nil {
		t.Fatal(err)
	}
	go func() {
		_, _ = io.WriteString(write, fed)
		_ = write.Close()
	}()
	run := exec.Command("sh", "-c", script)
	run.Stdin = read
	said, err := run.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, said)
	}
	return nil
}

func TestEveryFileALoginWithSudoWritesAsTheDeployLoginHoldsWhatItWasFed(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reopens a pipe whatever its mode, so this machine cannot stand in for the deploy login")
	}
	rig := loggedInAs("ubuntu", ssh.Facts{Sudo: true, Systemd: true})
	imaging(rig, "false ")
	served := rig.answer
	rig.answer = func(command string) (ssh.Result, bool) {
		if !strings.HasPrefix(command, "sudo ") && strings.Contains(command, "install ") && strings.Contains(command, boxapi.StateRoot+"/") {
			return ssh.Result{Code: 1, Stderr: "install: cannot create regular file: Permission denied"}, true
		}
		return served(command)
	}
	container, resource := valued(), resourced()
	if _, err := rig.host().containers.Run(context.Background(), container); err != nil {
		t.Fatalf("RunContainer() as a login with sudo = %v", err)
	}
	if err := rig.host().resources.Run(context.Background(), resource, "secret"); err != nil {
		t.Fatalf("RunResource() as a login with sudo = %v", err)
	}

	here := t.TempDir()
	for _, path := range []string{envfile.Path(container.Tier, container.Name), envfile.Path(resource.Tier, resource.Name)} {
		if err := os.MkdirAll(filepath.Dir(here+path), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	written := map[string]string{
		envfile.Path(container.Tier, container.Name):       "",
		release.HandedNote(container.Tier, container.Name): "",
		envfile.Path(resource.Tier, resource.Name):         "",
	}
	rig.mu.Lock()
	ran, fed := append([]string(nil), rig.ran...), append([]string(nil), rig.fed...)
	rig.mu.Unlock()
	for at, command := range ran {
		script, acted := actedAsTheDeployLogin(command)
		if !acted || fed[at] == "" {
			continue
		}
		for path := range written {
			if !strings.Contains(script, path) {
				continue
			}
			written[path] = fed[at]
			if err := ranHereFromAPipeOnlyAnotherLoginCouldReopen(t, strings.ReplaceAll(script, boxapi.StateRoot, here+boxapi.StateRoot), fed[at]); err != nil {
				t.Errorf("writing %s as %s from what ssh feeds it = %v: the pipe ssh feeds is the login's, and %s cannot reopen it by name", path, deployUser, err, deployUser)
				continue
			}
			held, err := os.ReadFile(here + path)
			if err != nil {
				t.Errorf("%s was never written: %v", path, err)
				continue
			}
			if string(held) != fed[at] {
				t.Errorf("%s holds %q, want what it was fed, %q", path, held, fed[at])
			}
		}
	}
	for path, wrote := range written {
		if wrote == "" {
			t.Errorf("nothing wrote %s as %s: %v", path, deployUser, ran)
		}
	}
}
