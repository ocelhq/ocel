package host

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/platform/vps/provider/shell"
	"github.com/ocelhq/ocel/platform/vps/provider/ssh"
)

func swapping(t *testing.T, restoreFails bool) (string, int) {
	t.Helper()
	bin := t.TempDir()
	log := filepath.Join(bin, "log")
	docker := "#!/bin/sh\nprintf 'docker %s\\n' \"$*\" >>" + log + "\n"
	helper := "#!/bin/sh\nprintf 'backups %s\\n' \"$*\" >>" + log + "\n"
	if restoreFails {
		helper += "echo 'pg_restore: role \"reporting\" does not exist' >&2\nexit 1\n"
	}
	for name, body := range map[string]string{"docker": docker, "backups": helper} {
		executable(t, filepath.Join(bin, name), body)
	}
	spec := resourced()
	spec.Volume.Generation = "17"
	spec.Ready = []string{"pg_isready"}
	swap, _ := swapCommand(spec, "16", "0123456789ab", "/tmp/handed.env",
		upgradeSteps{after: restoreCommand(spec.Tier, spec.Name, "/var/lib/ocel/production/backups/x/1.dump") + "\n"}, "secret")
	script := strings.ReplaceAll(swap, BackupsHelper, filepath.Join(bin, "backups"))
	run := exec.Command("/bin/sh", "-c", script)
	run.Env = []string{"PATH=" + bin + ":" + os.Getenv("PATH")}
	code := 0
	if err := run.Run(); err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatal(err)
		}
		code = exit.ExitCode()
	}
	ran, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	return string(ran), code
}

func ordered(t *testing.T, ran string, steps ...string) {
	t.Helper()
	at := 0
	for _, step := range steps {
		found := strings.Index(ran[at:], step)
		if found < 0 {
			t.Fatalf("the swap never ran %q after what came before it:\n%s", step, ran)
		}
		at += found + len(step)
	}
}

func TestASwapThatLandsRetiresTheOldServerOnlyOnceTheNewOneHasItsData(t *testing.T) {
	t.Parallel()

	name := resourced().Name
	ran, code := swapping(t, false)
	if code != 0 {
		t.Fatalf("the swap exited %d:\n%s", code, ran)
	}
	ordered(t, ran,
		"docker stop --time "+fmt.Sprint(resourceStopSeconds)+" "+name,
		"docker rename "+name+" "+name+"-retired",
		"docker volume create",
		"docker run --detach --name "+name,
		"docker exec "+name+" pg_isready",
		"backups production restore "+name+" /var/lib/ocel/production/backups/x/1.dump",
		"docker rm --force "+name+"-retired",
		"docker volume rm "+name+"-g16",
	)
	if strings.Contains(ran, "docker start") {
		t.Errorf("a swap that landed started the old server back up:\n%s", ran)
	}
}

func TestASwapWhoseRestoreFailsPutsTheOldServerBackOnTheDataItHad(t *testing.T) {
	t.Parallel()

	name := resourced().Name
	ran, code := swapping(t, true)
	if code == 0 {
		t.Fatalf("a swap whose restore failed was reported as landed:\n%s", ran)
	}
	ordered(t, ran,
		"backups production restore "+name,
		"docker rm --force "+name,
		"docker volume rm "+name+"-g17",
		"docker rename "+name+"-retired "+name,
		"docker start "+name,
	)
	if strings.Contains(ran, "docker volume rm "+name+"-g16") {
		t.Errorf("a swap that failed removed the only copy of the data the old server had:\n%s", ran)
	}
}

func TestASwapWhoseNewServerAnswersButRefusesTheCredentialPutsTheOldServerBackOnItsData(t *testing.T) {
	t.Parallel()

	bin := t.TempDir()
	log := filepath.Join(bin, "log")
	executable(t, filepath.Join(bin, "docker"), "#!/bin/sh\n"+
		"printf 'docker %s\\n' \"$*\" >>"+log+"\n"+
		"case \"$*\" in *--askpass*) printf 'fed %s\\n' \"$(cat)\" >>"+log+"; echo 'NOAUTH Authentication required.'; exit 1;; esac\n")
	spec := resourced()
	spec.User, spec.Volume.Generation = "valkey:valkey", "8"
	spec.Ready = []string{"valkey-cli", "ping"}
	spec.Credential = Credential{Reassert: func(secret string) ([]string, string) {
		return []string{"valkey-cli", "--user", "ocel", "--askpass", "ping"}, secret + "\n"
	}}
	script, stdin := swapCommand(spec, "9", "0123456789ab", "/tmp/handed.env", volumeCopy(spec, "9"), "the-password")
	run := exec.Command("/bin/sh", "-c", script)
	run.Env = []string{"PATH=" + bin + ":" + os.Getenv("PATH")}
	run.Stdin = strings.NewReader(stdin)
	if err := run.Run(); err == nil {
		t.Error("a swap whose new server refused the credential was reported as landed")
	}
	logged, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	ran := string(logged)
	ordered(t, ran,
		"docker exec "+spec.Name+" valkey-cli ping",
		"fed the-password",
		"docker rm --force "+spec.Name,
		"docker volume rm "+spec.Name+"-g8",
		"docker rename "+spec.Name+"-retired "+spec.Name,
		"docker start "+spec.Name,
	)
	if strings.Contains(ran, "docker volume rm "+spec.Name+"-g9") {
		t.Errorf("a swap whose new server refused the credential removed the only copy of the data the old server had:\n%s", ran)
	}
}

func TestAnUpgradeOverAStoppedServerSaysWhichServerToStartAndWhichVersionToSetBack(t *testing.T) {
	t.Parallel()

	spec := resourced()
	spec.Volume.Generation = "17"
	box := machine(nil)
	box.answer = func(command string) (ssh.Result, bool) {
		switch {
		case strings.Contains(command, shell.Quote(servingSelectors())):
			return ssh.Result{Stdout: "exited " + spec.Image + "\n"}, true
		case strings.Contains(command, LabelGeneration):
			return ssh.Result{Stdout: "16\n"}, true
		}
		return ssh.Result{}, false
	}
	err := box.host().RunResource(context.Background(), spec, "secret")
	if err == nil {
		t.Fatal("RunResource() over a stopped server with version 16 data = nil, want it refused until the server can dump that data")
	}
	for _, want := range []string{"Run `docker start " + spec.Name + "`", "set the version back to 16"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("RunResource() = %q, want it to say %q", err, want)
		}
	}
}

func TestAResourceWhoseSpecChangedIsReplacedOnlyOnceItsSuccessorAnswers(t *testing.T) {
	t.Parallel()

	spec := resourced()
	spec.Ready = []string{"pg_isready"}
	box := machine(nil)
	box.answer = func(command string) (ssh.Result, bool) {
		if strings.Contains(command, shell.Quote(servingSelectors())) {
			return ssh.Result{Stdout: "running " + spec.Image + " 000000000000\n"}, true
		}
		return ssh.Result{}, false
	}
	if err := box.host().RunResource(context.Background(), spec, "secret"); err != nil {
		t.Fatalf("RunResource() = %v", err)
	}
	var replaced string
	for _, command := range box.commands() {
		if strings.Contains(command, "docker rm --force "+shell.Quote(spec.Name)+" ") && !strings.Contains(command, "trap back EXIT") {
			t.Fatalf("%s was removed before its successor was proven to answer, so a successor that fails leaves the resource down:\n%s", spec.Name, command)
		}
		if strings.Contains(command, "docker rename "+shell.Quote(spec.Name)) {
			replaced = command
		}
	}
	if replaced == "" {
		t.Fatalf("a changed spec never set the serving %s aside, so nothing can put it back:\n%s", spec.Name, strings.Join(box.commands(), "\n"))
	}
	ordered(t, replaced,
		"docker stop --time "+fmt.Sprint(resourceStopSeconds)+" "+shell.Quote(spec.Name),
		"docker rename "+shell.Quote(spec.Name)+" "+shell.Quote(spec.Name+retiredSuffix),
		shell.Words([]string{"docker", "run", "--detach", "--name", spec.Name}),
		"pg_isready",
		"docker rm --force "+shell.Quote(spec.Name+retiredSuffix),
	)
	if strings.Contains(replaced, "docker volume rm") {
		t.Errorf("replacing %s on its own volume removes a volume:\n%s", spec.Name, replaced)
	}
}

func TestAReplacementWhoseSuccessorNeverAnswersPutsTheServingResourceBack(t *testing.T) {
	t.Parallel()

	bin := t.TempDir()
	log := filepath.Join(bin, "log")
	executable(t, filepath.Join(bin, "docker"), "#!/bin/sh\n"+
		"printf 'docker %s\\n' \"$*\" >>"+log+"\n"+
		"case \"$*\" in *pg_isready*) exit 1;; esac\n")
	executable(t, filepath.Join(bin, "sleep"), "#!/bin/sh\n")
	spec := resourced()
	spec.Ready = []string{"pg_isready"}
	script, _ := swapCommand(spec, spec.Volume.Generation, "0123456789ab", "/tmp/handed.env", upgradeSteps{}, "secret")
	run := exec.Command("/bin/sh", "-c", strings.ReplaceAll(script, "+ "+fmt.Sprint(shell.ReadySeconds)+"))", "+ 1))"))
	run.Env = []string{"PATH=" + bin + ":" + os.Getenv("PATH")}
	if err := run.Run(); err == nil {
		t.Fatal("a replacement whose successor never answered was reported as landed")
	}
	logged, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	ran := string(logged)
	ordered(t, ran,
		"docker rename "+spec.Name+" "+spec.Name+retiredSuffix,
		"docker run --detach --name "+spec.Name,
		"docker rm --force "+spec.Name,
		"docker rename "+spec.Name+retiredSuffix+" "+spec.Name,
		"docker start "+spec.Name,
	)
	if strings.Contains(ran, "docker volume rm") {
		t.Errorf("a failed replacement on the resource's own volume removed a volume, the only copy of its data:\n%s", ran)
	}
}

func TestAConnectionThatDropsWhileAskingAfterAResourceRemovesNothing(t *testing.T) {
	t.Parallel()

	spec := resourced()
	box := machine(nil)
	dropped := errors.New("ssh: connection reset by peer")
	box.broke = func(command string) error {
		if strings.Contains(command, shell.Quote(servingSelectors())) {
			return dropped
		}
		return nil
	}
	if err := box.host().RunResource(context.Background(), spec, "secret"); !errors.Is(err, dropped) {
		t.Fatalf("RunResource() = %v, want the dropped connection it met reading whether %s serves", err, spec.Name)
	}
	if at := box.took("docker rm --force " + shell.Quote(spec.Name)); at >= 0 {
		t.Errorf("a dropped connection was read as a container to clear, and %s was removed:\n%s", spec.Name, strings.Join(box.taking(), "\n"))
	}
}

func TestADaemonThatCannotBeAskedAfterAResourceIsAnErrorAndNotAnAbsence(t *testing.T) {
	t.Parallel()

	spec := resourced()
	box := machine(nil)
	box.answer = func(command string) (ssh.Result, bool) {
		if strings.Contains(command, shell.Quote(servingSelectors())) {
			return ssh.Result{Stderr: "Cannot connect to the Docker daemon at unix:///var/run/docker.sock", Code: 1}, true
		}
		return ssh.Result{}, false
	}
	if err := box.host().RunResource(context.Background(), spec, "secret"); err == nil {
		t.Fatal("RunResource() = nil over a daemon that could not be asked, so it went on to run a second server beside the one it could not see")
	}
	for _, command := range box.commands() {
		if strings.Contains(command, "docker run") {
			t.Errorf("a daemon that could not be asked was read as one with no %s, and a new one was run:\n%s", spec.Name, command)
		}
	}
}

func TestAConnectionThatDropsWhileAskingAfterAnAppRemovesNothing(t *testing.T) {
	t.Parallel()

	spec := valued()
	box := machine(nil)
	dropped := errors.New("ssh: connection reset by peer")
	box.broke = func(command string) error {
		if strings.Contains(command, shell.Quote(servingSelectors())) {
			return dropped
		}
		return nil
	}
	if _, err := box.host().RunContainer(context.Background(), spec); !errors.Is(err, dropped) {
		t.Fatalf("RunContainer() = %v, want the dropped connection it met reading whether %s serves", err, spec.Name)
	}
	if at := box.took("docker rm --force " + shell.Quote(spec.Name)); at >= 0 {
		t.Errorf("a dropped connection was read as a container to clear, and %s was removed:\n%s", spec.Name, strings.Join(box.taking(), "\n"))
	}
}
