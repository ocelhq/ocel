package host

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/platform/vps/provider/machine"
	"github.com/ocelhq/ocel/platform/vps/provider/shell"
)

type daemonBench struct {
	bin    string
	config string
}

func daemonOn(t *testing.T, swarm string, config *string) daemonBench {
	t.Helper()
	bench := daemonBench{bin: t.TempDir(), config: filepath.Join(t.TempDir(), "daemon.json")}
	log := filepath.Join(bench.bin, "log")
	executable(t, filepath.Join(bench.bin, "docker"), "#!/bin/sh\nprintf '%s\\n' "+shell.Quote(swarm)+"\n")
	executable(t, filepath.Join(bench.bin, "dockerd"), "#!/bin/sh\nprintf 'dockerd %s\\n' \"$*\" >>"+log+"\n"+
		"for f; do :; done\npython3 -I -c 'import json,sys; assert isinstance(json.load(open(sys.argv[1])), dict)' \"$f\"\n")
	executable(t, filepath.Join(bench.bin, "systemctl"), "#!/bin/sh\nprintf 'systemctl %s\\n' \"$*\" >>"+log+"\n")
	if config != nil {
		if err := os.WriteFile(bench.config, []byte(*config), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return bench
}

func (b daemonBench) apply(t *testing.T) (string, error) {
	t.Helper()
	run := exec.Command("/bin/sh", "-c", liveRestoreCommand(b.config))
	run.Env = []string{"PATH=" + b.bin + ":" + os.Getenv("PATH")}
	out, err := run.CombinedOutput()
	logged, _ := os.ReadFile(filepath.Join(b.bin, "log"))
	if err != nil {
		return string(logged), fmt.Errorf("%w: %s", err, out)
	}
	return string(logged), nil
}

func (b daemonBench) settings(t *testing.T) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(b.config)
	if err != nil {
		t.Fatal(err)
	}
	var settings map[string]any
	if err := json.Unmarshal(raw, &settings); err != nil {
		t.Fatalf("daemon.json is %q, which no daemon reads: %v", raw, err)
	}
	return settings
}

func TestEveryBootstrappedEngineKeepsItsContainersRunningWhileTheDaemonRestarts(t *testing.T) {
	t.Parallel()

	if !slices.ContainsFunc(EngineItems(), func(item machine.Item) bool { return item.Kind == machine.KindLiveRestore }) {
		t.Fatal("bootstrap leaves live-restore off, so every daemon upgrade or restart stops every app and resource on the box")
	}
}

func TestLiveRestoreIsWrittenIntoADaemonConfigThatDoesNotExistAndTheDaemonReloaded(t *testing.T) {
	t.Parallel()

	bench := daemonOn(t, "inactive", nil)
	logged, err := bench.apply(t)
	if err != nil {
		t.Fatalf("the command = %v", err)
	}
	if settings := bench.settings(t); settings["live-restore"] != true {
		t.Errorf("daemon.json holds %v, want live-restore on", settings)
	}
	if !strings.Contains(logged, "systemctl reload docker") {
		t.Errorf("the daemon was never reloaded, so it runs without live-restore until it next restarts:\n%s", logged)
	}
	if strings.Contains(logged, "systemctl restart") {
		t.Errorf("the daemon was restarted, stopping every container live-restore is there to keep:\n%s", logged)
	}
}

func TestLiveRestoreJoinsTheSettingsADaemonConfigAlreadyHolds(t *testing.T) {
	t.Parallel()

	for name, config := range map[string]string{
		"settings":        "{\n  \"default-address-pools\": [{\"base\": \"10.200.0.0/16\", \"size\": 24}]\n}\n",
		"an empty object": "{}\n",
	} {
		bench := daemonOn(t, "inactive", &config)
		if _, err := bench.apply(t); err != nil {
			t.Fatalf("over %s, the command = %v", name, err)
		}
		settings := bench.settings(t)
		if settings["live-restore"] != true {
			t.Errorf("over %s, daemon.json holds %v, want live-restore on", name, settings)
		}
		if name == "settings" && settings["default-address-pools"] == nil {
			t.Errorf("daemon.json lost the settings it held: %v", settings)
		}
	}
}

func TestADaemonConfigThatAlreadyDecidesLiveRestoreOrRunsSwarmIsLeftAsItIs(t *testing.T) {
	t.Parallel()

	decided := "{\"live-restore\": false}\n"
	for name, bench := range map[string]daemonBench{
		"decided":    daemonOn(t, "inactive", &decided),
		"in a swarm": daemonOn(t, "active", nil),
	} {
		logged, err := bench.apply(t)
		if err != nil {
			t.Fatalf("%s, the command = %v", name, err)
		}
		if strings.Contains(logged, "systemctl") {
			t.Errorf("%s, the daemon was reloaded:\n%s", name, logged)
		}
	}
	if raw, _ := os.ReadFile(daemonOn(t, "inactive", &decided).config); string(raw) != decided {
		t.Errorf("a config that set live-restore itself became %q", raw)
	}
}

func TestADaemonConfigTheMergeWouldBreakIsLeftAsItWasAndTheReloadNeverRuns(t *testing.T) {
	t.Parallel()

	broken := "[\"not an object\"]\n"
	bench := daemonOn(t, "inactive", &broken)
	logged, err := bench.apply(t)
	if err == nil {
		t.Error("a merge the daemon refuses to read was reported as applied")
	}
	if raw, _ := os.ReadFile(bench.config); string(raw) != broken {
		t.Errorf("daemon.json became %q, and a daemon that cannot read its config does not start", raw)
	}
	if strings.Contains(logged, "systemctl") {
		t.Errorf("the daemon was reloaded over a config it cannot read:\n%s", logged)
	}
}
