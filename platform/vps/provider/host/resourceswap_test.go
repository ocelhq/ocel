package host

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
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
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	spec := resourced()
	spec.Volume.Generation, spec.Database = "17", "main"
	spec.Ready = []string{"pg_isready"}
	script := strings.ReplaceAll(
		swapCommand(spec, "16", "0123456789ab", "/tmp/handed.env", "/var/lib/ocel/production/backups/x/1.dump"),
		BackupsHelper, filepath.Join(bin, "backups"))
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
		"docker stop "+name,
		"docker rename "+name+" "+name+"-retired",
		"docker volume create",
		"docker run --detach --name "+name,
		"docker exec "+name+" pg_isready",
		"backups production restore "+name+" main",
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
