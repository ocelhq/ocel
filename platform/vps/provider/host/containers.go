package host

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ocelhq/ocel/pkg/containerimage"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/runtime/originguard"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/shell"
	"github.com/ocelhq/ocel/platform/vps/provider/ssh"
)

const (
	LabelApp     = "ocel.app"
	LabelProject = "ocel.project"
	LabelRef     = "ocel.ref"
	LabelEnv     = "ocel.env"
	LabelTier    = boxapi.TierLabel

	LabelResource   = "ocel.resource"
	LabelGeneration = "ocel.generation"
	LabelOwner      = "ocel.database-owner"
)

const (
	poolExhausted = "address pool"
	membersFormat = `{{range .Containers}}{{.Name}}{{"\n"}}{{end}}`
)

func networkLabels(tier environment.Tier, project string) []string {
	return []string{
		"--label", LabelTier + "=" + string(tier),
		"--label", LabelProject + "=" + naming.Sanitize(project),
	}
}

func networkCreating(tier environment.Tier, project string) string {
	network := shell.Quote(boxapi.AppNetwork(tier, project))
	create := "docker network create " + shell.Words(networkLabels(tier, project)) + " " + network
	return "set -e\n" +
		"if ! docker network inspect " + network + " >/dev/null 2>&1; then\n" +
		"if ! said=$(" + create + " 2>&1 >/dev/null) && ! docker network inspect " + network + " >/dev/null 2>&1; then\n" +
		"printf '%s\\n' \"$said\" >&2\n" +
		"exit 1\n" +
		"fi\n" +
		"fi"
}

func joinNetworkScript(tier environment.Tier, project string) string {
	network := shell.Quote(boxapi.AppNetwork(tier, project))
	return networkCreating(tier, project) + "\n" +
		"if ! docker network connect " + network + " " + shell.Quote(SwitchboardContainer) + " >/dev/null 2>&1 && " +
		"! docker network inspect --format " + shell.Quote(membersFormat) + " " + network + " | grep -qx " + shell.Quote(SwitchboardContainer) + "; then\n" +
		"printf '%s\\n' " + shell.Quote(SwitchboardContainer+" could not join "+boxapi.AppNetwork(tier, project)) + " >&2\n" +
		"exit 1\n" +
		"fi"
}

func networkForgetting(tier environment.Tier, project string) string {
	network := shell.Quote(boxapi.AppNetwork(tier, project))
	return "if docker network inspect " + network + " >/dev/null 2>&1; then\n" +
		"if docker network inspect --format " + shell.Quote(membersFormat) + " " + network +
		" | grep -qvx " + shell.Quote(SwitchboardContainer) + "; then printf '%s\\n' " + shell.Quote(networkInUse) + "; exit 0; fi\n" +
		"docker network disconnect --force " + network + " " + shell.Quote(SwitchboardContainer) + " >/dev/null 2>&1 || true\n" +
		"docker network rm " + network + " >/dev/null\n" +
		"fi"
}

func (e *Engine) Join(ctx context.Context, who string, tier environment.Tier, project string) error {
	_, said, err := e.box.Attempt(ctx, ssh.Docker, "put "+who+" on "+boxapi.AppNetwork(tier, project), joinNetworkScript(tier, project), nil)
	if err != nil && strings.Contains(said, poolExhausted) {
		return refusal.Refuse(refusal.CodeNotReady,
			"%s has no subnet left for %s: %s\n"+
				"Add `\"default-address-pools\": [{\"base\": \"10.200.0.0/16\", \"size\": 24}]` to /etc/docker/daemon.json and restart docker",
			e.box.Named(), boxapi.AppNetwork(tier, project), said)
	}
	return err
}

func (e *Engine) ForgetNetwork(ctx context.Context, tier environment.Tier, project string) error {
	_, err := e.box.Run(ctx, ssh.Docker, "take "+boxapi.AppNetwork(tier, project)+" down", networkForgetting(tier, project), nil)
	return err
}

const (
	appRestart = "unless-stopped"
	nameShort  = 12
)

const (
	logDriver   = "local"
	logMaxSize  = "20m"
	logMaxFiles = "5"
	LogCeiling  = 20 * 5 << 20
)

const (
	pidsLimit       = "4096"
	noNewPrivileges = "no-new-privileges"
)

var appCapabilities = []string{"CHOWN", "DAC_OVERRIDE", "FOWNER", "FSETID", "KILL", "NET_BIND_SERVICE", "SETGID", "SETUID", "SETPCAP"}

func logging() []string {
	return []string{"--log-driver", logDriver, "--log-opt", "max-size=" + logMaxSize, "--log-opt", "max-file=" + logMaxFiles}
}

func confined(capabilities []string, fileCapabilities bool) []string {
	argv := []string{"--pids-limit", pidsLimit}
	if !fileCapabilities {
		argv = append(argv, "--security-opt", noNewPrivileges)
	}
	argv = append(argv, "--cap-drop", "ALL")
	for _, capability := range capabilities {
		argv = append(argv, "--cap-add", capability)
	}
	return argv
}

var stateFields = []struct{ label, selector string }{
	{"Status", ".State.Status"},
	{"ExitCode", ".State.ExitCode"},
	{"OOMKilled", ".State.OOMKilled"},
	{"Error", ".State.Error"},
	{"StartedAt", ".State.StartedAt"},
	{"FinishedAt", ".State.FinishedAt"},
	{"RestartCount", ".RestartCount"},
}

type Container struct {
	Name    string
	Project string
	App     string
	Image   string
	Tier    environment.Tier

	Env        map[string]string
	HealthPath string
	Manifest   []byte
	Resolved   bool
	Declared   []string

	Limits Limits
}

func (c Container) delivered() map[string]string {
	env := make(map[string]string, len(c.Env)+2)
	maps.Copy(env, c.Env)
	if c.HealthPath != "" {
		env[originguard.HealthPathVar] = c.HealthPath
	}
	if len(c.Manifest) > 0 {
		env[boxapi.EnvVar] = string(c.Manifest)
	}
	return env
}

func ContainerName(stack, app, deployment, image string) string {
	identity := deployment
	if identity == "" {
		sum := sha256.Sum256([]byte(image))
		identity = hex.EncodeToString(sum[:])
	}
	return naming.Sanitize(stack) + "-" + naming.Sanitize(app) + "-" + naming.Sanitize(identity[:min(len(identity), nameShort)])
}

const appPulls = 5

func runContainerScript(spec Container, env handoff) string {
	return imagePulled(spec.Image, appPulls, shell.PullAttemptSeconds, shell.PullBudgetSeconds) +
		shell.Bounded(shell.DockerStepSeconds, "docker run of "+spec.Name, shell.Words(containerRun(spec, env))+" >/dev/null")
}

func containerRun(spec Container, env handoff) []string {
	argv := []string{"docker", "run", "--detach",
		"--name", spec.Name,
		"--restart", appRestart,
		"--init=false",
		"--network", boxapi.AppNetwork(spec.Tier, spec.Project),
		"--label", LabelTier + "=" + string(spec.Tier),
		"--label", LabelProject + "=" + naming.Sanitize(spec.Project),
		"--label", LabelApp + "=" + spec.App,
		"--label", LabelRef + "=" + spec.Image,
		"--label", LabelEnv + "=" + env.digest,
	}
	argv = append(argv, logging()...)
	argv = append(argv, confined(appCapabilities, false)...)
	argv = append(argv, spec.Limits.argv()...)
	if env.path != "" {
		argv = append(argv, "--env-file", env.path)
	}
	if len(spec.Manifest) > 0 {
		argv = append(argv, "--mount", "type=bind,src="+LiveSocketDir+",dst="+LiveSocketDir+",readonly",
			"--tmpfs", containerimage.LivePath+":"+liveDirTmpfs)
	}
	return append(argv, "--env", containerimage.PortEnvVar+"="+containerimage.PortText, spec.Image)
}

func LabelSelector(label string) string {
	return "{{index .Config.Labels " + strconv.Quote(label) + "}}"
}

func servingSelectors() string {
	return "{{.State.Status}} " + LabelSelector(LabelRef) + " " + LabelSelector(LabelEnv)
}

func runningImage(said, image string) bool {
	fields := strings.Fields(said)
	return len(fields) >= 2 && fields[0] == "running" && fields[1] == image
}

func stillServing(said, image, digest string) bool {
	if !runningImage(said, image) {
		return false
	}
	fields := strings.Fields(said)
	return len(fields) > 2 && fields[2] == digest
}

func startableImage(said, image string) bool {
	fields := strings.Fields(said)
	return len(fields) >= 2 && fields[1] == image &&
		(fields[0] == "exited" || fields[0] == "created" || fields[0] == "paused")
}

func restartCommand(said, name string) string {
	if strings.HasPrefix(strings.TrimSpace(said), "paused") {
		return "docker unpause " + shell.Quote(name) + " >/dev/null"
	}
	return "docker start " + shell.Quote(name) + " >/dev/null"
}

func servingCommand(name string) string {
	return "if said=$(docker inspect --type container --format " + shell.Quote(servingSelectors()) + " " + shell.Quote(name) + " 2>&1); then printf '%s\\n' \"$said\"\n" +
		"elif printf '%s' \"$said\" | grep -qi 'no such'; then :\n" +
		"else printf '%s\\n' \"$said\" >&2; exit 1\n" +
		"fi"
}

type Run uint8

const (
	RunKept Run = iota
	RunCreated
	RunRestarted
)

func (h *Host) RunContainer(ctx context.Context, spec Container) (ran Run, err error) {
	elevation, err := ssh.Docker, h.Assume(ctx, ssh.Docker)
	if err != nil {
		return RunKept, err
	}
	if spec.Limits, err = h.engine.Limits(ctx, spec.Tier); err != nil {
		return RunKept, err
	}
	if err := h.engine.Join(ctx, spec.App, spec.Tier, spec.Project); err != nil {
		return RunKept, err
	}
	said, err := h.engine.ReadServing(ctx, spec.Name)
	if err != nil {
		return RunKept, err
	}
	if spec.Resolved {
		env, err := handing(spec)
		if err != nil {
			return RunKept, err
		}
		if stillServing(said, spec.Image, env.digest) {
			return RunKept, nil
		}
	} else {
		if runningImage(said, spec.Image) {
			return RunKept, nil
		}
		if startableImage(said, spec.Image) {
			_, err := h.Run(ctx, elevation, "start "+spec.App+" back up as "+spec.Name, restartCommand(said, spec.Name), nil)
			return RunRestarted, err
		}
		note, known, err := h.handed(ctx, spec)
		if err != nil {
			return RunKept, err
		}
		switch {
		case known && len(note.Handed) > 0:
			return RunKept, refusal.Refuse(refusal.CodeNotReady,
				"%s is gone from this box and was handed %s, which a promotion cannot pass on\nRun `ocel deploy`",
				spec.Name, strings.Join(note.Handed, ", "))
		case known:
			spec.Manifest = note.Live
		case len(spec.Declared) > 0:
			return RunKept, refusal.Refuse(refusal.CodeNotReady,
				"%s is gone from this box, %s declares %s, and no note of its values remains\nRun `ocel deploy`",
				spec.Name, spec.App, strings.Join(spec.Declared, ", "))
		default:
			return RunKept, refusal.Refuse(refusal.CodeNotReady,
				"%s is gone from this box and no note of its values remains\nRun `ocel deploy`",
				spec.Name)
		}
	}
	env, err := handing(spec)
	if err != nil {
		return RunKept, err
	}
	if _, err := h.Run(ctx, elevation, "clear the name "+spec.Name, "docker rm --force "+shell.Quote(spec.Name)+" >/dev/null 2>&1 || true", nil); err != nil {
		return RunCreated, err
	}
	if err := h.sweep(ctx, elevation); err != nil {
		return RunCreated, err
	}
	defer func() { err = errors.Join(err, h.forget(ctx, env)) }()
	if err := h.hand(ctx, env, spec); err != nil {
		return RunCreated, err
	}
	if spec.Resolved {
		if err := h.note(ctx, spec); err != nil {
			return RunCreated, err
		}
	}
	_, err = h.Run(ctx, elevation, "run "+spec.App+" as "+spec.Name, runContainerScript(spec, env), nil)
	return RunCreated, err
}

func (h *Host) TakeDown(ctx context.Context, tier environment.Tier, name string) error {
	elevation, err := ssh.Docker, h.Assume(ctx, ssh.Docker)
	if err != nil {
		return err
	}
	_, err = h.Run(ctx, elevation, "take down "+name, shell.Within(shell.DockerStepSeconds, "docker stop "+shell.Quote(name))+" >/dev/null 2>&1 || true\n"+
		shell.BoundedTolerating(shell.DockerStepSeconds, "docker rm of "+name, "docker rm --force "+shell.Quote(name)+" >/dev/null 2>&1"), nil)
	if err != nil {
		return err
	}
	acting, err := ssh.Deploy, h.Assume(ctx, ssh.Deploy)
	if err != nil {
		return err
	}
	_, err = h.Run(ctx, acting, "forget what "+name+" was handed", "rm -f "+shell.Quote(HandedNote(tier, name)), nil)
	return err
}

func releaseSelectors() string {
	return LabelSelector(LabelProject) + " " + LabelSelector(LabelApp) + " " + LabelSelector(LabelRef)
}

func releaseCommand(name string) string {
	return "docker inspect --type container --format " + shell.Quote(releaseSelectors()) + " " + shell.Quote(name)
}

func (h *Host) RetireContainer(ctx context.Context, name string) error {
	elevation, err := ssh.Docker, h.Assume(ctx, ssh.Docker)
	if err != nil {
		return err
	}
	said, err := h.Run(ctx, elevation, "read which release "+name+" runs", releaseCommand(name), nil)
	if err != nil {
		return err
	}
	release := strings.Fields(said)
	if len(release) != 3 {
		return refusal.Refuse(refusal.CodeNotReady,
			"%s lacks the %s, %s or %s label every app container carries, so its image cannot be archived for a rollback: %q",
			name, LabelProject, LabelApp, LabelRef, strings.TrimSpace(said))
	}
	if err := h.archive(ctx, release[0], release[1], release[2]); err != nil {
		return err
	}
	_, err = h.Run(ctx, elevation, "stop "+name, shell.Bounded(shell.DockerStepSeconds, "docker stop of "+name, "docker stop "+shell.Quote(name)+" >/dev/null"), nil)
	return err
}

func (h *Host) RemoveContainer(ctx context.Context, name string) error {
	elevation, err := ssh.Docker, h.Assume(ctx, ssh.Docker)
	if err != nil {
		return err
	}
	_, err = h.Run(ctx, elevation, "remove "+name, shell.Bounded(shell.DockerStepSeconds, "docker rm of "+name, "docker rm --force "+shell.Quote(name)+" >/dev/null"), nil)
	return err
}

func stateSelectors() []string {
	selectors := make([]string, 0, len(stateFields))
	for _, field := range stateFields {
		selectors = append(selectors, field.label+"={{"+field.selector+"}}")
	}
	return selectors
}

func stateCommand(name string) string {
	return "docker inspect --type container --format " + shell.Quote(strings.Join(stateSelectors(), " ")) + " " + shell.Quote(name) + " 2>&1 || true"
}

func logCommand(name string) string {
	return "docker logs --timestamps --tail " + appLogTail + " " + shell.Quote(name) + " 2>&1 || true"
}

var ErrContainerMissing = errors.New("the container does not exist")

type Line struct {
	Time   time.Time
	Stderr bool
	Text   string
}

func (h *Host) ReadContainerLogs(ctx context.Context, name string, since, until time.Time, limit int, contains string) ([]Line, error) {
	elevation, err := ssh.Docker, h.Assume(ctx, ssh.Docker)
	if err != nil {
		return nil, err
	}
	result, err := h.Stream(ctx, elevation, logsCommand(name, since, until, limit, contains), nil)
	if err != nil {
		return nil, err
	}
	if result.Code != 0 {
		if strings.Contains(strings.ToLower(result.Stderr), "no such container") {
			return nil, ErrContainerMissing
		}
		return nil, h.Refuse("read the logs of "+name, result)
	}
	lines := slices.DeleteFunc(append(parseLogLines(result.Stdout, false), parseLogLines(result.Stderr, true)...), func(line Line) bool {
		return line.Time.Before(since) || !until.IsZero() && line.Time.After(until) || !strings.Contains(line.Text, contains)
	})
	slices.SortStableFunc(lines, func(a, b Line) int { return a.Time.Compare(b.Time) })
	return lines[len(lines)-min(len(lines), max(limit, 0)):], nil
}

func logsCommand(name string, since, until time.Time, limit int, contains string) string {
	command := "docker logs --timestamps --since " + shell.Quote(since.UTC().Format(time.RFC3339Nano))
	if !until.IsZero() {
		command += " --until " + shell.Quote(until.UTC().Format(time.RFC3339Nano))
	}
	if until.IsZero() && contains == "" {
		command += " --tail " + strconv.Itoa(limit)
	}
	return command + " " + shell.Quote(name)
}

func parseLogLines(output string, stderr bool) []Line {
	var lines []Line
	for raw := range strings.SplitSeq(output, "\n") {
		if line, stamped := parseLogLine(raw, stderr); stamped {
			lines = append(lines, line)
		}
	}
	return lines
}

func parseLogLine(raw string, stderr bool) (Line, bool) {
	stamp, text, _ := strings.Cut(strings.TrimSuffix(raw, "\r"), " ")
	at, err := time.Parse(time.RFC3339Nano, stamp)
	if err != nil {
		return Line{}, false
	}
	return Line{Time: at.UTC(), Stderr: stderr, Text: text}, true
}

func (e *Engine) ReadServing(ctx context.Context, name string) (string, error) {
	said, err := e.box.Run(ctx, ssh.Docker, "read whether "+name+" is serving", servingCommand(name), nil)
	return strings.TrimSpace(said), err
}

func (h *Host) ForgetNetwork(ctx context.Context, tier environment.Tier, project string) error {
	return h.engine.ForgetNetwork(ctx, tier, project)
}
