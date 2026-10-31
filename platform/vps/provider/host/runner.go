package host

import (
	"context"
	"encoding/json"
	"slices"
	"strconv"
	"strings"

	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/machine"
	"github.com/ocelhq/ocel/platform/vps/provider/shell"
	"github.com/ocelhq/ocel/platform/vps/provider/ssh"
)

const (
	runnerSliceFile  = "/etc/systemd/system/" + boxapi.RunnerSlice
	KindJobImage     = "docker:image"
	sudoersRunner    = machine.SudoersRoot + "/ocel-runner"
	systemdCgroups   = "systemd"
	releaseDownloads = "https://github.com/ocelhq/ocel/releases/download"
	jobDockerfile    = "dist/jobimage/Dockerfile"
	runnerJobLabel   = "dev.ocel.box.runner-job"

	runnerJobStopSeconds = 60

	jobsDaemonImage    = "docker:29.9.1-dind@sha256:3e5756e2b6dad6a5f1f5deacf0a19f9eaa2581b76ae092b2d4a118162e1d4d4e"
	jobsDaemonRunLabel = "dev.ocel.box.jobs-docker.run"
	jobsDaemonRising   = 30

	jobsDaemonFactsTemplate = "image={{.Config.Image}}\n" +
		"cgroup={{.HostConfig.CgroupParent}}\n" +
		"privileged={{.HostConfig.Privileged}}\n" +
		"run={{index .Config.Labels \"" + jobsDaemonRunLabel + "\"}}\n" +
		"state={{.State.Status}}"
)

type RunnerSettings struct {
	URL     string
	Version provider.WrittenBy
	Pulled  string
	CPUs    string
	Memory  string
	Arch    string
}

func (s RunnerSettings) Image() string {
	if s.Pulled != "" {
		return s.Pulled
	}
	return boxapi.RunnerImage + ":" + strings.TrimPrefix(s.Version.String(), "v")
}

func RefuseUnbuildableJobImage(settings RunnerSettings) error {
	if settings.Pulled != "" || settings.Version.Release() {
		return nil
	}
	return refusal.Refuse(refusal.CodeInvalid,
		"this ocel (%s) is no release, so there is no ocel binary to build the job image from\nName a job image with `ocel connector add --runner --image`, or add the connector with a released ocel",
		settings.Version)
}

func runnerDocker() []byte {
	return []byte(strings.Join([]string{
		"#!/bin/sh",
		"if [ \"$1\" = run ]; then",
		"  shift",
		"  exec docker run --cgroup-parent=" + boxapi.RunnerSlice + " --label " + runnerJobLabel + " \"$@\"",
		"fi",
		"exec docker \"$@\"",
		"",
	}, "\n"))
}

func runnerSlice(settings RunnerSettings) ([]byte, error) {
	quota, err := cpuQuota(settings.CPUs)
	if err != nil {
		return nil, err
	}
	memory, err := memoryBytes(settings.Memory)
	if err != nil {
		return nil, err
	}
	return []byte(strings.Join([]string{
		"[Unit]",
		"Description=every job the console runs on this box, under one cap",
		"",
		"[Slice]",
		"CPUQuota=" + quota,
		"MemoryMax=" + strconv.FormatInt(memory, 10),
		"",
	}, "\n")), nil
}

func cpuQuota(cpus string) (string, error) {
	parsed, err := strconv.ParseFloat(cpus, 64)
	if err != nil || parsed <= 0 {
		return "", refusal.Refuse(refusal.CodeInvalid, "%q is no count of CPUs: name one like 2 or 1.5", cpus)
	}
	return strconv.FormatFloat(parsed*100, 'f', 0, 64) + "%", nil
}

var memoryUnits = map[byte]int64{'b': 1, 'k': 1 << 10, 'm': 1 << 20, 'g': 1 << 30}

func memoryBytes(memory string) (int64, error) {
	digits, unit := memory, int64(1)
	if last := len(memory) - 1; last >= 0 {
		if scale, named := memoryUnits[memory[last]]; named {
			digits, unit = memory[:last], scale
		}
	}
	parsed, err := strconv.ParseInt(digits, 10, 64)
	if err != nil || parsed <= 0 {
		return 0, refusal.Refuse(refusal.CodeInvalid, "%q is no amount of memory: name one like 4g or 512m", memory)
	}
	return parsed * unit, nil
}

func runnerSudoers() []byte {
	return []byte("Defaults!" + boxapi.RunnerDocker + " env_keep += \"OCEL_JOB_TOKEN\"\n" +
		deployUser + " ALL=(root) NOPASSWD: " + boxapi.RunnerDocker + "\n")
}

func runnerConfig(settings RunnerSettings) []byte {
	written, err := json.Marshal(boxapi.RunnerConfig{URL: settings.URL, Image: settings.Image()})
	if err != nil {
		panic(err)
	}
	return append(written, '\n')
}

func jobImage(assets Assets, settings RunnerSettings) machine.Item {
	script := func(i machine.Item) string { return buildJobImage(assets, settings) }
	if settings.Pulled != "" {
		script = func(i machine.Item) string { return "docker pull " + shell.Quote(settings.Pulled) + " >/dev/null" }
	}
	return machine.Item{Kind: KindJobImage, Name: settings.Image(), Owner: machine.RootOwner, Slow: true,
		Note: "the image each job runs in", Script: script, Survey: jobImageSurvey}
}

func jobImageSurvey(i machine.Item) string {
	return "if docker image inspect --format '{{.Id}}' " + shell.Quote(i.Name) + " >/dev/null 2>&1; then " +
		machine.Reports(shell.Quote(i.Kind), shell.Quote(i.Name), "0", shell.Quote(machine.RootOwner), `''`) + "; fi"
}

func buildJobImage(assets Assets, settings RunnerSettings) string {
	version := strings.TrimPrefix(settings.Version.String(), "v")
	base := releaseDownloads + "/v" + version
	dockerfile, err := assets.read(jobDockerfile)
	if err != nil {
		panic(err)
	}
	lines := []string{
		"set -e",
		"work=$(mktemp -d)",
		"trap 'rm -rf \"$work\"' EXIT",
		"curl -fsSL --retry 3 -o \"$work/checksums.txt\" " + shell.Quote(base+"/checksums.txt"),
		"mkdir \"$work/context\"",
	}
	for _, binary := range []string{"ocel", "ocel-job"} {
		archive := binary + "_" + version + "_linux_" + settings.Arch + ".tar.gz"
		lines = append(lines,
			"curl -fsSL --retry 3 -o \"$work/archive\" "+shell.Quote(base+"/"+archive),
			"want=$(awk -v name="+shell.Quote(archive)+" '$2 == name { print $1 }' \"$work/checksums.txt\")",
			"[ -n \"$want\" ] || { echo "+shell.Quote("checksums.txt of v"+version+" names no "+archive)+" >&2; exit 1; }",
			"[ \"$(sha256sum \"$work/archive\" | cut -d' ' -f1)\" = \"$want\" ] || { echo "+shell.Quote(archive+" does not match its checksum")+" >&2; exit 1; }",
			"tar -xzf \"$work/archive\" -C \"$work/context\" "+binary,
		)
	}
	return strings.Join(append(lines,
		"printf '%s' "+shell.Quote(string(dockerfile))+" > \"$work/Dockerfile\"",
		"docker build --quiet -f \"$work/Dockerfile\" -t "+shell.Quote(settings.Image())+" \"$work/context\" >/dev/null",
	), "\n")
}

func jobsDaemonRun() []string {
	return []string{"docker", "run", "--detach", "--name", boxapi.RunnerJobsDaemon,
		"--privileged", "--cgroup-parent=" + boxapi.RunnerSlice, "--restart", "always",
		"--mount", "type=volume,source=" + boxapi.RunnerJobsDaemon + ",target=/var/lib/docker",
		"--mount", "type=bind,source=" + boxapi.RunnerJobsDaemonDir + ",target=" + boxapi.RunnerJobsDaemonDir,
		jobsDaemonImage, "dockerd", "--host=unix://" + boxapi.RunnerJobsDaemonSocket,
	}
}

func jobsDaemonRunSum() string {
	return machine.ContentSum([]byte(strings.Join(jobsDaemonRun(), "\n")))
}

func jobsDaemonFacts() []byte {
	return []byte("image=" + jobsDaemonImage + "\n" +
		"cgroup=" + boxapi.RunnerSlice + "\n" +
		"privileged=true\n" +
		"run=" + jobsDaemonRunSum() + "\n" +
		"state=running\n")
}

func startJobsDaemon(machine.Item) string {
	run := jobsDaemonRun()
	labelled := append(append(slices.Clone(run[:2]), "--label", jobsDaemonRunLabel+"="+jobsDaemonRunSum()), run[2:]...)
	answers := shell.Words([]string{"docker", "exec", boxapi.RunnerJobsDaemon, "docker", "--host", "unix://" + boxapi.RunnerJobsDaemonSocket, "version"})
	return strings.Join([]string{
		"systemctl daemon-reload",
		"docker rm --force " + shell.Quote(boxapi.RunnerJobsDaemon) + " >/dev/null 2>&1 || true",
		shell.Words(labelled) + " >/dev/null",
		"for attempt in $(seq " + strconv.Itoa(jobsDaemonRising) + "); do",
		"  if " + answers + " >/dev/null 2>&1; then exit 0; fi",
		"  sleep 1",
		"done",
		"echo " + shell.Quote("the jobs' Docker daemon "+boxapi.RunnerJobsDaemon+" did not answer within "+strconv.Itoa(jobsDaemonRising)+" seconds: read `docker logs "+boxapi.RunnerJobsDaemon+"`, then add the connector again") + " >&2",
		"exit 1",
	}, "\n")
}

func jobsDaemonSurvey(i machine.Item) string {
	return "if command -v " + shell.Quote(dockerEngine) + " >/dev/null 2>&1 && " +
		"facts=$(docker inspect --type container --format " + shell.Quote(jobsDaemonFactsTemplate) + " " + shell.Quote(i.Name) + " 2>/dev/null); then\n" +
		machine.Reports(shell.Quote(i.Kind), shell.Quote(i.Name), "0", shell.Quote(machine.RootOwner),
			`"$(printf '%s\n' "$facts" | sha256sum | cut -d' ' -f1)"`) + "\nfi"
}

func jobsDaemon() machine.Item {
	return machine.Item{Kind: machine.KindContainer, Name: boxapi.RunnerJobsDaemon, Owner: machine.RootOwner, Content: jobsDaemonFacts(), Slow: true,
		Note: "the Docker daemon jobs build and run containers in, under " + boxapi.RunnerSlice, Script: startJobsDaemon, Survey: jobsDaemonSurvey}
}

func RunnerItems(assets Assets, settings RunnerSettings) ([]machine.Item, error) {
	slice, err := runnerSlice(settings)
	if err != nil {
		return nil, err
	}
	return []machine.Item{
		{Kind: machine.KindFile, Name: runnerSliceFile, Mode: 0o644, Owner: machine.RootOwner, Content: slice,
			Note: "caps every job on the box together"},
		{Kind: machine.KindFile, Name: boxapi.RunnerDocker, Mode: 0o755, Owner: machine.RootOwner, Content: runnerDocker(),
			Note: "runs docker as root for the connector's jobs, each under " + boxapi.RunnerSlice},
		{Kind: machine.KindFile, Name: sudoersRunner, Mode: 0o440, Owner: machine.RootOwner, Content: runnerSudoers(),
			Note: "the connector starts jobs as root"},
		machine.Dir(boxapi.RunnerDir, 0o700, stateOwner, "the runner's jobs, sources and uploads"),
		machine.Dir(boxapi.RunnerJobsDaemonDir, 0o700, machine.RootOwner, "the socket of the Docker daemon jobs use"),
		jobsDaemon(),
		jobImage(assets, settings),
		{Kind: machine.KindFile, Name: boxapi.RunnerConfigPath, Mode: 0o644, Owner: machine.RootOwner, Content: runnerConfig(settings),
			Note: "the connector runs the console's jobs"},
	}, nil
}

func surveyedRunnerItems(assets Assets, settings RunnerSettings) []machine.Item {
	return []machine.Item{
		{Kind: machine.KindFile, Name: runnerSliceFile},
		{Kind: machine.KindFile, Name: boxapi.RunnerDocker},
		{Kind: machine.KindFile, Name: sudoersRunner},
		machine.Dir(boxapi.RunnerDir, 0o700, stateOwner, ""),
		machine.Dir(boxapi.RunnerJobsDaemonDir, 0o700, machine.RootOwner, ""),
		jobsDaemon(),
		jobImage(assets, settings),
		{Kind: machine.KindFile, Name: boxapi.RunnerConfigPath},
	}
}

type Runner struct{ host *Host }

func (h *Host) Runner() *Runner { return &Runner{host: h} }

type RunnerReading struct {
	Observed map[string]string
	CPUs     int
	MemoryKB int64
}

func (r RunnerReading) HasRunner() bool {
	_, configured := r.Observed[machine.KindFile+" "+boxapi.RunnerConfigPath]
	return configured
}

func (r RunnerReading) ChooseCaps(cpus, memory string) (string, string) {
	if cpus == "" {
		cpus = strconv.Itoa(max(r.CPUs-1, 1))
	}
	if memory == "" && r.MemoryKB > 0 {
		memory = strconv.FormatInt(r.MemoryKB*3/4/1024, 10) + "m"
	}
	return cpus, memory
}

func (c *Runner) Read(ctx context.Context, settings RunnerSettings) (RunnerReading, error) {
	rendered, err := c.host.Run(ctx, ssh.RootWhenAble, "survey the runner this box has", machine.SurveyScript(surveyedRunnerItems(c.host.assets, settings)), nil)
	if err != nil {
		return RunnerReading{}, err
	}
	observed, _, err := machine.ReadSurvey(rendered)
	if err != nil {
		return RunnerReading{}, err
	}
	said, err := c.host.Run(ctx, ssh.Login, "ask this box how many CPUs and how much memory it has", "nproc; awk '/^MemTotal:/ { print $2 }' /proc/meminfo", nil)
	if err != nil {
		return RunnerReading{}, err
	}
	reading := RunnerReading{Observed: observed}
	if fields := strings.Fields(said); len(fields) == 2 {
		reading.CPUs, _ = strconv.Atoi(fields[0])
		reading.MemoryKB, _ = strconv.ParseInt(fields[1], 10, 64)
	}
	return reading, nil
}

func (c *Runner) Apply(ctx context.Context, settings RunnerSettings, read RunnerReading, progress progress.Log) error {
	items, err := RunnerItems(c.host.assets, settings)
	if err != nil {
		return err
	}
	driver, err := c.host.Run(ctx, ssh.Root, "ask which cgroup driver Docker uses", "docker info --format '{{.CgroupDriver}}'", nil)
	if err != nil {
		return err
	}
	if driver = strings.TrimSpace(driver); driver != systemdCgroups {
		return refusal.Refuse(refusal.CodeNotReady,
			"Docker on %s uses the %s cgroup driver, so no cap holds the runner's jobs together\nSet \"exec-opts\": [\"native.cgroupdriver=systemd\"] in /etc/docker/daemon.json, restart Docker, then add the connector again",
			c.host.Named(), driver)
	}
	if err := c.host.InstallChanged(ctx, items, CurrentIn(read.Observed), progress); err != nil {
		return err
	}
	if _, err := c.host.Run(ctx, ssh.Root, "load the cap on the runner's jobs", "systemctl daemon-reload", nil); err != nil {
		return err
	}
	return c.host.ConsoleTrust().restartConnector(ctx, progress, "runs the console's jobs")
}

func (c *Runner) Remove(ctx context.Context, progress progress.Log) error {
	if _, err := c.host.Run(ctx, ssh.Root, "take the runner off this box", runnerRemoval(), nil); err != nil {
		return err
	}
	progress.Say("Stopped the runner's jobs and removed the runner: " + boxapi.RunnerConfigPath + ", " + boxapi.RunnerDir + ", the container " + boxapi.RunnerJobsDaemon + " and the volumes " + boxapi.RunnerCache + " and " + boxapi.RunnerJobsDaemon + "; job images stay")
	return c.host.ConsoleTrust().restartConnector(ctx, progress, "")
}

func runnerRemoval() string {
	return strings.Join([]string{
		"rm -f " + shell.Quote(sudoersRunner),
		stopRunnerJobs(),
		"docker rm --force " + shell.Quote(boxapi.RunnerJobsDaemon) + " >/dev/null 2>&1 || true",
		"rm -f " + shell.Quote(boxapi.RunnerConfigPath) + " " + shell.Quote(boxapi.RunnerDocker) + " " + shell.Quote(runnerSliceFile),
		"systemctl daemon-reload",
		"rm -rf " + shell.Quote(boxapi.RunnerDir) + " " + shell.Quote(boxapi.RunnerJobsDaemonDir),
		"docker volume rm --force " + shell.Quote(boxapi.RunnerCache) + " " + shell.Quote(boxapi.RunnerJobsDaemon) + " >/dev/null 2>&1 || true",
	}, "\n")
}

func stopRunnerJobs() string {
	listed := "docker ps --all --quiet --filter " + shell.Quote("label="+runnerJobLabel)
	return strings.Join([]string{
		"jobs=$(" + listed + ")",
		"if [ -n \"$jobs\" ]; then",
		"  docker stop --time " + strconv.Itoa(runnerJobStopSeconds) + " $jobs >/dev/null 2>&1 || true",
		"  docker rm --force $jobs >/dev/null 2>&1 || true",
		"fi",
		"for attempt in 1 2 3 4 5 6 7 8 9 10; do",
		"  [ -z \"$(" + listed + ")\" ] && break",
		"  sleep 1",
		"done",
		"[ -z \"$(" + listed + ")\" ] || { echo " + shell.Quote("jobs still run on this box: remove the containers labelled "+runnerJobLabel+", then remove the runner again") + " >&2; exit 1; }",
	}, "\n")
}
