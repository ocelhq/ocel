package host

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"text/template"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/machine"
	"github.com/ocelhq/ocel/platform/vps/provider/shell"
	"github.com/ocelhq/ocel/platform/vps/provider/ssh"
)

var released = RunnerSettings{URL: "https://box.example.com" + boxapi.ConnectorPath, Version: "v1.2.3", CPUs: "3", Memory: "6144m", Arch: ssh.AMD64}

func runnerItems(t *testing.T, settings RunnerSettings) []machine.Item {
	t.Helper()
	items, err := RunnerItems(testAssets, settings)
	if err != nil {
		t.Fatal(err)
	}
	return items
}

func TestEveryJobOnTheBoxSharesOneCappedSlice(t *testing.T) {
	t.Parallel()

	slice := string(itemNamed(t, runnerItems(t, released), runnerSliceFile).Content)
	for _, want := range []string{"[Slice]", "CPUQuota=300%", "MemoryMax=6442450944"} {
		if !strings.Contains(slice, want) {
			t.Errorf("slice = %q, want %q", slice, want)
		}
	}
	helper := string(itemNamed(t, runnerItems(t, released), boxapi.RunnerDocker).Content)
	if !strings.Contains(helper, "exec docker run --cgroup-parent="+boxapi.RunnerSlice+" --label "+runnerJobLabel+` "$@"`) {
		t.Errorf("helper = %q, want every job it runs put under %s", helper, boxapi.RunnerSlice)
	}
}

func TestTheHelperPutsEveryRunUnderTheSliceAndPassesOtherCommandsThrough(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	log := filepath.Join(dir, "docker.log")
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte("#!/bin/sh\nprintf '%s\\n' \"$*\" >> "+shell.Quote(log)+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join(dir, "runner-docker")
	if err := os.WriteFile(helper, runnerDocker(), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"run", "--rm", "ocel-job:1.2.3"}, {"container", "rm", "--force", "ocel-job-1"}} {
		cmd := exec.Command(helper, args...)
		cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"))
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %v %s", args, err, out)
		}
	}
	ran, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if want := "run --cgroup-parent=" + boxapi.RunnerSlice + " --label " + runnerJobLabel + " --rm ocel-job:1.2.3\ncontainer rm --force ocel-job-1\n"; string(ran) != want {
		t.Errorf("docker ran %q, want %q", ran, want)
	}
}

func fakeJobDocker(t *testing.T, listed string, stubborn bool) (dir string, log string) {
	t.Helper()
	dir = t.TempDir()
	log = filepath.Join(dir, "docker.log")
	state := filepath.Join(dir, "listed")
	if err := os.WriteFile(state, []byte(listed), 0o644); err != nil {
		t.Fatal(err)
	}
	clears := ": > " + shell.Quote(state)
	if stubborn {
		clears = ":"
	}
	docker := strings.Join([]string{
		"#!/bin/sh",
		"printf '%s\\n' \"$*\" >> " + shell.Quote(log),
		"case \"$1\" in",
		"ps) cat " + shell.Quote(state) + " ;;",
		"stop|rm) " + clears + " ;;",
		"esac",
		"",
	}, "\n")
	for name, script := range map[string]string{"docker": docker, "sleep": "#!/bin/sh\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir, log
}

func runStopRunnerJobs(t *testing.T, dir string) (string, error) {
	t.Helper()
	cmd := exec.Command("sh", "-c", stopRunnerJobs())
	cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestRemovingTheRunnerStopsItsRunningJobsWithinTheTimeoutAndKillsWhatOutlastsIt(t *testing.T) {
	t.Parallel()

	dir, log := fakeJobDocker(t, "job-a\njob-b\n", false)
	if out, err := runStopRunnerJobs(t, dir); err != nil {
		t.Fatalf("stopping the jobs failed: %v %s", err, out)
	}
	ran, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"ps --all --quiet --filter label=" + runnerJobLabel + "\n",
		"stop --time " + strconv.Itoa(runnerJobStopSeconds) + " job-a job-b\n",
	} {
		if !strings.Contains(string(ran), want) {
			t.Errorf("docker ran %q, want %q", ran, want)
		}
	}
}

func TestRemovingTheRunnerFailsWhileAJobOutlivesTheStopAndTheKill(t *testing.T) {
	t.Parallel()

	dir, log := fakeJobDocker(t, "job-a\n", true)
	out, err := runStopRunnerJobs(t, dir)
	if err == nil {
		t.Fatalf("stopping the jobs succeeded with job-a still on the box: %s", out)
	}
	ran, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(ran), "rm --force job-a\n") {
		t.Errorf("docker ran %q, want job-a killed once the stop's timeout passed", ran)
	}
}

func TestRemovingTheRunnerClosesNewLaunchesThenStopsItsJobsBeforeItDeletesTheRunner(t *testing.T) {
	t.Parallel()

	box := newBench(nil)
	var said []string
	if err := box.host().Runner().Remove(t.Context(), saying(&said)); err != nil {
		t.Fatal(err)
	}
	at := box.at("rm -rf " + shell.Quote(boxapi.RunnerDir))
	if at < 0 {
		t.Fatalf("ran %q, want the runner's directory deleted", box.commands())
	}
	script := box.commands()[at]
	closes := strings.Index(script, "rm -f "+shell.Quote(sudoersRunner))
	stops := strings.Index(script, stopRunnerJobs())
	helper := strings.Index(script, shell.Quote(boxapi.RunnerDocker))
	deletes := strings.Index(script, "rm -rf "+shell.Quote(boxapi.RunnerDir))
	if closes < 0 || stops <= closes || helper <= stops || deletes <= stops {
		t.Errorf("removal = %q, want the connector's sudo rule gone, then the jobs stopped, then the helper and the directory deleted", script)
	}
}

func TestJobsBuildAndRunContainersInADockerDaemonOfTheirOwnUnderTheJobsSlice(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	log := filepath.Join(dir, "ran.log")
	for _, name := range []string{"docker", "systemctl", "sleep"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\nprintf '%s\\n' \""+name+" $*\" >> "+shell.Quote(log)+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("sh", "-c", itemNamed(t, runnerItems(t, released), boxapi.RunnerJobsDaemon).Command())
	cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("starting the jobs' daemon failed: %v %s", err, out)
	}
	raw, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	ran := strings.Split(strings.TrimSpace(string(raw)), "\n")
	run := slices.IndexFunc(ran, func(line string) bool { return strings.HasPrefix(line, "docker run ") })
	if run < 0 || slices.Index(ran, "systemctl daemon-reload") > run || !slices.Contains(ran, "systemctl daemon-reload") {
		t.Fatalf("ran %q, want the slice loaded before the daemon runs in it", ran)
	}
	for _, want := range []string{
		" --privileged ",
		" --cgroup-parent=" + boxapi.RunnerSlice + " ",
		" --restart always ",
		" --mount type=volume,source=" + boxapi.RunnerJobsDaemon + ",target=/var/lib/docker ",
		" --mount type=bind,source=" + boxapi.RunnerJobsDaemonDir + ",target=" + boxapi.RunnerJobsDaemonDir + " ",
		" " + jobsDaemonImage + " dockerd --host=unix://" + boxapi.RunnerJobsDaemonSocket,
	} {
		if !strings.Contains(ran[run], want) {
			t.Errorf("docker %q, want %q", ran[run], want)
		}
	}
}

func TestTheJobsDaemonReadsCurrentWhenItRunsAsItsItemStates(t *testing.T) {
	t.Parallel()

	daemon := itemNamed(t, runnerItems(t, released), boxapi.RunnerJobsDaemon)
	inspected := map[string]any{
		"Config":     map[string]any{"Image": jobsDaemonImage, "Labels": map[string]string{jobsDaemonRunLabel: jobsDaemonRunSum()}},
		"HostConfig": map[string]any{"CgroupParent": boxapi.RunnerSlice, "Privileged": true},
		"State":      map[string]any{"Status": "running"},
	}
	var facts strings.Builder
	if err := template.Must(template.New("facts").Parse(jobsDaemonFactsTemplate)).Execute(&facts, inspected); err != nil {
		t.Fatal(err)
	}
	if got := facts.String() + "\n"; got != string(daemon.Content) {
		t.Errorf("a daemon run as stated reads %q, want %q", got, daemon.Content)
	}
}

func TestCapsAreReadAsDockerSpellsThem(t *testing.T) {
	t.Parallel()

	for memory, want := range map[string]int64{"512m": 512 << 20, "4g": 4 << 30, "1048576": 1 << 20, "64k": 64 << 10} {
		if got, err := memoryBytes(memory); err != nil || got != want {
			t.Errorf("memoryBytes(%q) = %d, %v, want %d", memory, got, err, want)
		}
	}
	if quota, err := cpuQuota("1.5"); err != nil || quota != "150%" {
		t.Errorf("cpuQuota(1.5) = %q, %v, want 150%%", quota, err)
	}
	for _, bad := range []string{"", "four", "0"} {
		if _, err := memoryBytes(bad); err == nil {
			t.Errorf("memoryBytes(%q) accepted it", bad)
		}
		if _, err := cpuQuota(bad); err == nil {
			t.Errorf("cpuQuota(%q) accepted it", bad)
		}
	}
}

func TestTheConnectorStartsJobsAsRootThroughOneHelperAndPassesOnlyTheJobToken(t *testing.T) {
	t.Parallel()

	sudoers := string(itemNamed(t, runnerItems(t, released), sudoersRunner).Content)
	if !strings.Contains(sudoers, boxapi.DeployLogin+" ALL=(root) NOPASSWD: "+boxapi.RunnerDocker+"\n") {
		t.Errorf("sudoers = %q, want the connector's login allowed the helper alone", sudoers)
	}
	if !strings.Contains(sudoers, `env_keep += "OCEL_JOB_TOKEN"`) {
		t.Errorf("sudoers = %q, want the job token kept across sudo", sudoers)
	}
}

func TestTheRunnersConfigNamesTheJobImageAndTheEndpoint(t *testing.T) {
	t.Parallel()

	var config boxapi.RunnerConfig
	if err := json.Unmarshal(itemNamed(t, runnerItems(t, released), boxapi.RunnerConfigPath).Content, &config); err != nil {
		t.Fatal(err)
	}
	want := boxapi.RunnerConfig{URL: released.URL, Image: "ocel-job:1.2.3"}
	if config != want {
		t.Errorf("config = %+v, want %+v", config, want)
	}
	items := runnerItems(t, released)
	if items[len(items)-1].Name != boxapi.RunnerConfigPath {
		t.Error("the runner's config is written before the image it names, so a connector restarted midway starts jobs in no image")
	}
}

func TestTheJobImageIsBuiltFromTheReleaseItsChecksumNames(t *testing.T) {
	t.Parallel()

	script := itemNamed(t, runnerItems(t, released), "ocel-job:1.2.3").Command()
	for _, want := range []string{
		"https://github.com/ocelhq/ocel/releases/download/v1.2.3/ocel_1.2.3_linux_amd64.tar.gz",
		"https://github.com/ocelhq/ocel/releases/download/v1.2.3/ocel-job_1.2.3_linux_amd64.tar.gz",
		"-C \"$work/context\" ocel-job",
		"checksums.txt",
		"sha256sum",
		"docker build",
		"FROM ",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("the build script lacks %q", want)
		}
	}
}

func TestANamedJobImageIsPulledInsteadOfBuilt(t *testing.T) {
	t.Parallel()

	pulled := released
	pulled.Pulled = "registry.example.com/ocel-job:dev"
	image := itemNamed(t, runnerItems(t, pulled), pulled.Pulled)
	if script := image.Command(); !strings.HasPrefix(script, "docker pull ") {
		t.Errorf("script = %q, want a pull", script)
	}
}

func TestADevelopmentOcelBuildsNoJobImage(t *testing.T) {
	t.Parallel()

	dev := released
	dev.Version = provider.WrittenBy("dev")
	if err := RefuseUnbuildableJobImage(dev); err == nil || !strings.Contains(err.Error(), "--image") {
		t.Errorf("err = %v, want a refusal naming --image", err)
	}
	dev.Pulled = "registry.example.com/ocel-job:dev"
	if err := RefuseUnbuildableJobImage(dev); err != nil {
		t.Errorf("err = %v, want a named image accepted", err)
	}
}

func TestAJobIsCappedBelowTheWholeBoxUnlessTheOptionSaysOtherwise(t *testing.T) {
	t.Parallel()

	box := RunnerReading{CPUs: 4, MemoryKB: 8 << 20}
	if cpus, memory := box.ChooseCaps("", ""); cpus != "3" || memory != "6144m" {
		t.Errorf("caps = %s, %s, want 3 and 6144m", cpus, memory)
	}
	if cpus, _ := (RunnerReading{CPUs: 1}).ChooseCaps("", ""); cpus != "1" {
		t.Errorf("caps on one CPU = %s, want 1", cpus)
	}
	if cpus, memory := box.ChooseCaps("2", "2g"); cpus != "2" || memory != "2g" {
		t.Errorf("caps = %s, %s, want the option's", cpus, memory)
	}
}

func TestARunnerIsPresentWhenItsConfigIs(t *testing.T) {
	t.Parallel()

	if (RunnerReading{Observed: map[string]string{}}).HasRunner() {
		t.Error("an empty box has a runner")
	}
	items := runnerItems(t, released)
	observed := machine.DigestsOf(items[len(items)-1:])
	if !(RunnerReading{Observed: observed}).HasRunner() {
		t.Error("a box with the runner's config has no runner")
	}
}
