package docker

import (
	"cmp"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/shell"
)

const (
	ContainerRising = 30
	ContainerPulls  = 5
)

const (
	MigrateSysctl = "net.ipv4.tcp_migrate_req"
	MigrateKnob   = "/proc/sys/net/ipv4/tcp_migrate_req"
	MigrateFact   = "migrate="
	MigrateSet    = "set"
	MigrateUnset  = "unset"
	MountsFact    = "mounts="
	MountsIntact  = "intact"
	MountsMoved   = "moved"
	ExecUnstarted = "126|127"
)

const ContainerFactTemplate = `image={{.Config.Image}}
command={{index .Config.Labels "` + ContainerCmdLabel + `"}}
restart={{.HostConfig.RestartPolicy.Name}}
network={{if index .NetworkSettings.Networks "` + boxapi.ProxyNetwork + `"}}` + NetworkJoined + `{{else}}` + NetworkLeft + `{{end}}
{{range .HostConfig.Binds}}bind={{.}}
{{end}}ports={{json .HostConfig.PortBindings}}
config={{index .Config.Labels "` + ConfigLabel + `"}}
memory={{.HostConfig.Memory}}
state={{.State.Status}}`

func NotAFile(name string) string {
	return "printf '%s\\n' " + shell.Quote(name+" is not a regular file") + " >&2; exit 1"
}

func bindsPresent(files []string) string {
	var written string
	for _, name := range files {
		written += "if [ ! -f " + shell.Quote(name) + " ]; then " + NotAFile(name) + "; fi\n"
	}
	return written
}

type Publish struct {
	Addr   string
	Port   string
	Target string
}

func (p Publish) String() string {
	if p.Addr == "" {
		return p.Port
	}
	return p.Addr + ":" + p.Port
}

type Service struct {
	Name     string
	Image    string
	Command  []string
	Config   string
	Binds    []string
	Ports    []Publish
	Env      []string
	Caps     []string
	FileCaps bool
	Files    []string
	Ready    []string
	Unready  string
	Inodes   []string
	Joins    bool
	Migrates bool
	Restored bool
	Memory   int64
	Network  string
	Networks []Network

	User string
}

func (s Service) Facts() []byte { return s.FactsOver(s.Binds) }

func (s Service) FactsOver(binds []string) []byte {
	stated := []string{
		"image=" + s.Image,
		"command=" + strings.Join(s.Command, " "),
		"restart=" + ContainerRestart,
		"network=" + NetworkJoined,
		"ports=" + Marshalled(Published(s.Ports)),
		"config=" + s.Config,
		"memory=" + strconv.FormatInt(s.Memory, 10),
		"state=running",
		MountsFact + MountsIntact,
	}
	if s.Migrates {
		stated = append(stated, MigrateFact+MigrateSet)
	}
	for _, joined := range s.Networks {
		stated = append(stated, JoinedFact(joined)+NetworkJoined)
	}
	for _, bind := range binds {
		stated = append(stated, "bind="+bind)
	}
	slices.Sort(stated)
	return []byte(strings.Join(stated, "\n") + "\n")
}

func Published(ports []Publish) map[string][]map[string]string {
	bindings := map[string][]map[string]string{}
	for _, port := range ports {
		bindings[port.Target+"/tcp"] = []map[string]string{{"HostIp": port.Addr, "HostPort": port.Port}}
	}
	return bindings
}

func Marshalled(value any) string {
	written, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(written)
}

func (s Service) Run(sysctls ...string) []string {
	return s.composeArgv([]string{"docker", "run", "--detach"}, sysctls)
}

func (s Service) Create() []string { return s.composeArgv([]string{"docker", "create"}, nil) }

func (s Service) composeArgv(leading, sysctls []string) []string {
	argv := append(slices.Clone(leading),
		"--name", s.Name,
		"--restart", ContainerRestart,
		"--network", cmp.Or(s.Network, boxapi.ProxyNetwork),
	)
	for _, joined := range s.Networks {
		argv = append(argv, "--network", joined.Name)
	}
	argv = append(argv, "--label", ContainerCmdLabel+"="+strings.Join(s.Command, " "))
	if s.Config != "" {
		argv = append(argv, "--label", ConfigLabel+"="+s.Config)
	}
	if s.Restored {
		argv = append(argv, "--label", RestoredLabel+"="+RestoredBy)
	}
	if s.User != "" {
		argv = append(argv, "--user", s.User)
	}
	for _, env := range s.Env {
		argv = append(argv, "--env", env)
	}
	argv = append(argv, Logging()...)
	argv = append(argv, Confined(s.Caps, s.FileCaps)...)
	if s.Memory > 0 {
		argv = append(argv, "--memory", strconv.FormatInt(s.Memory, 10))
	}
	for _, port := range s.Ports {
		argv = append(argv, "--publish", port.String()+":"+port.Target)
	}
	for _, bind := range s.Binds {
		argv = append(argv, "--volume", bind)
	}
	for _, sysctl := range sysctls {
		argv = append(argv, "--sysctl", sysctl)
	}
	return append(append(argv, s.Image), s.Command...)
}

func (s Service) Writing(attempts int) string {
	return s.prepared() + s.replaced() + s.Rising(attempts)
}

func (s Service) WritingUnderLock(attempts int, locked string, guard []string) string {
	return s.prepared() + locked + strings.Join(guard, "\n") + "\n" + s.replaced() + "flock -u 9\n" + s.Rising(attempts)
}

func (s Service) prepared() string {
	return "set -e\n" +
		s.NetworksPresent() +
		bindsPresent(s.Files) +
		Pulled(s.Image, ContainerPulls, shell.PullAttemptSeconds, shell.PullBudgetSeconds)
}

func (s Service) replaced() string {
	return "docker stop " + shell.Quote(s.Name) + " >/dev/null 2>&1 || true\n" +
		"docker rm --force " + shell.Quote(s.Name) + " >/dev/null 2>&1 || true\n" +
		s.started()
}

func (s Service) started() string {
	if s.Joins {
		return shell.Words(s.Create()) + " >/dev/null\n" + rejoining(s.Name) + "docker start " + shell.Quote(s.Name) + " >/dev/null\n"
	}
	run := shell.Words(s.Run()) + " >/dev/null\n"
	if !s.Migrates {
		return run
	}
	return "if [ -e " + shell.Quote(MigrateKnob) + " ]; then\n" +
		shell.Words(s.Run(MigrateSysctl+"=1")) + " >/dev/null\n" +
		"else\n" + run + "fi\n"
}

func (s Service) NetworksPresent() string {
	var checks string
	for _, joined := range s.Networks {
		if joined.CreatedByOcel {
			checks += EnsuringNetwork(joined.Name) + "\n"
			continue
		}
		missing := fmt.Sprintf("option %q names the docker network %s, which this box does not have: start the proxy that creates it, or create it with docker network create %s",
			joined.Option, joined.Name, joined.Name)
		checks += "if ! docker network inspect " + shell.Quote(joined.Name) + " >/dev/null 2>&1; then\n" +
			"printf '%s\\n' " + shell.Quote(missing) + " >&2\n" +
			"exit 1\n" +
			"fi\n"
	}
	return checks
}

const PullAttempts = 5

func Pulled(imageRef string, attempts, attemptSeconds, budgetSeconds int) string {
	image := shell.Quote(imageRef)
	spent := "{ printf '%s\\n' " + shell.Quote(fmt.Sprintf("%s was not pulled within %s", imageRef, shell.Seconds(budgetSeconds))) + " >&2; exit 1; }"
	return "at=0\n" + ScriptPullBackoff.Start() +
		"pull_deadline=$(($(date +%s) + " + fmt.Sprint(budgetSeconds) + "))\n" +
		"pull_left() { left=$((pull_deadline - $(date +%s))); [ \"$left\" -gt " + fmt.Sprint(attemptSeconds) + " ] && left=" + fmt.Sprint(attemptSeconds) + "; [ \"$left\" -gt 0 ]; }\n" +
		"until " + shell.Within(shell.ProbeSeconds, "docker image inspect "+image) + " >/dev/null 2>&1 || " +
		"{ pull_left || " + spent + "; timeout -k " + fmt.Sprint(shell.KillGraceSeconds) + " \"$left\" docker pull " + image + " >/dev/null; }; do\n" +
		"at=$((at + 1))\n" +
		"if [ \"$at\" -ge " + fmt.Sprint(attempts) + " ]; then\n" +
		"printf '%s\\n' " + shell.Quote(fmt.Sprintf("%s was not pulled in %d attempts of at most %s each", imageRef, attempts, shell.Seconds(attemptSeconds))) + " >&2\n" +
		"exit 1\n" +
		"fi\n" +
		ScriptPullBackoff.Again() +
		"pull_left || " + spent + "\n" +
		"done\n"
}

func (s Service) Readiness() []string {
	return append([]string{"docker", "exec", s.Name}, s.Ready...)
}

func (s Service) MountInodesCommand() []string {
	command := slices.Clone(s.Inodes)
	for _, bind := range s.Binds {
		_, dest, _ := strings.Cut(bind, ":")
		dest, _, _ = strings.Cut(dest, ":")
		command = append(command, dest)
	}
	return command
}

func (s Service) Rising(attempts int) string {
	name := shell.Quote(s.Name)
	inspect := shell.Within(shell.ProbeSeconds, "docker inspect --type container --format ")
	answering := shell.Within(shell.ProbeSeconds, shell.Words(s.Readiness())) + " >/dev/null 2>&1"
	return "at=0\n" +
		"while :; do\n" +
		"if [ \"$(" + inspect + shell.Quote("{{.State.Status}}") + " " + name + " 2>/dev/null)\" = running ] && " + answering + "; then exit 0; fi\n" +
		"at=$((at + 1))\n" +
		"[ \"$at\" -lt " + fmt.Sprint(attempts) + " ] || break\n" +
		"sleep 1\n" +
		"done\n" +
		"printf '%s\\n' " + shell.Quote(fmt.Sprintf("%s was created and %s within %ds", s.Name, s.Unready, attempts)) + " >&2\n" +
		inspect + shell.Quote("status={{.State.Status}} exit={{.State.ExitCode}} error={{.State.Error}}") +
		" " + name + " >&2 2>&1 || true\n" +
		shell.Within(shell.ProbeSeconds, "docker logs --tail 2 "+name) + " >&2 2>&1 || true\n" +
		"exit 1"
}

func BoundMount(source, target string, options ...string) string {
	var written strings.Builder
	fields := csv.NewWriter(&written)
	if err := fields.Write(append([]string{"type=bind", "source=" + source, "target=" + target}, options...)); err != nil {
		panic(err)
	}
	fields.Flush()
	return strings.TrimSuffix(written.String(), "\n")
}

func (s Service) Sources() []string {
	paths := make([]string, 0, len(s.Binds))
	for _, bind := range s.Binds {
		source, _, _ := strings.Cut(bind, ":")
		paths = append(paths, source)
	}
	return paths
}

func (s Service) Restoring(attempts int, locked string) string {
	return "set -e\n" +
		Pulled(s.Image, ContainerPulls, shell.PullAttemptSeconds, shell.PullBudgetSeconds) +
		locked +
		"if [ \"$(docker inspect --type container --format " + shell.Quote("{{.State.Running}}") + " " + shell.Quote(s.Name) + " 2>/dev/null)\" != true ]; then\n" +
		"docker rm --force " + shell.Quote(s.Name) + " >/dev/null 2>&1 || true\n" +
		s.NetworksPresent() +
		EnsuringNetwork(boxapi.ProxyNetwork) + "\n" +
		bindsPresent(s.Files) +
		s.started() +
		"fi\n" +
		"flock -u 9\n" +
		s.Rising(attempts)
}

const StaticImage = "gcr.io/distroless/static-debian12@sha256:d75cdd72874d4790092fcb1b058493ecf6bb5bf2b2b897045b00ff01d91843f2"

var ScriptPullBackoff = shell.Backoff{Counter: "at", Base: 2, Ceiling: 30, Spread: 5}
