package host

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"maps"
	"strconv"
	"strings"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	"github.com/ocelhq/ocel/pkg/runtimekit/originguard"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
	"github.com/ocelhq/ocel/platform/vps/provider/live"
)

const (
	LabelApp     = "ocel.app"
	LabelProject = "ocel.project"
	LabelRef     = "ocel.ref"
	LabelEnv     = "ocel.env"
	LabelClass   = "ocel.class"

	LabelResource   = "ocel.resource"
	LabelGeneration = "ocel.generation"
)

const (
	appNetworkPrefix = "ocel-"
	poolExhausted    = "address pool"
	membersFormat    = `{{range .Containers}}{{.Name}}{{"\n"}}{{end}}`
)

func AppNetwork(class edge.Class, project string) string {
	return appNetworkPrefix + string(class) + "-" + naming.Sanitize(project)
}

func networkLabels(class edge.Class, project string) []string {
	return []string{
		"--label", LabelClass + "=" + string(class),
		"--label", LabelProject + "=" + naming.Sanitize(project),
	}
}

func networkCreating(class edge.Class, project string) string {
	network := quoted(AppNetwork(class, project))
	create := "docker network create " + words(networkLabels(class, project)) + " " + network
	return "set -e\n" +
		"if ! docker network inspect " + network + " >/dev/null 2>&1; then\n" +
		"if ! said=$(" + create + " 2>&1 >/dev/null) && ! docker network inspect " + network + " >/dev/null 2>&1; then\n" +
		"printf '%s\\n' \"$said\" >&2\n" +
		"exit 1\n" +
		"fi\n" +
		"fi"
}

func networkStanding(class edge.Class, project string) string {
	network := quoted(AppNetwork(class, project))
	return networkCreating(class, project) + "\n" +
		"if ! docker network connect " + network + " " + quoted(SwitchboardContainer) + " >/dev/null 2>&1 && " +
		"! docker network inspect --format " + quoted(membersFormat) + " " + network + " | grep -qx " + quoted(SwitchboardContainer) + "; then\n" +
		"printf '%s\\n' " + quoted(SwitchboardContainer+" could not join "+AppNetwork(class, project)) + " >&2\n" +
		"exit 1\n" +
		"fi"
}

func networkForgetting(class edge.Class, project string) string {
	network := quoted(AppNetwork(class, project))
	return "if docker network inspect " + network + " >/dev/null 2>&1; then\n" +
		"if docker network inspect --format " + quoted(membersFormat) + " " + network +
		" | grep -qvx " + quoted(SwitchboardContainer) + "; then printf '%s\\n' " + quoted(networkHeld) + "; exit 0; fi\n" +
		"docker network disconnect --force " + network + " " + quoted(SwitchboardContainer) + " >/dev/null 2>&1 || true\n" +
		"docker network rm " + network + " >/dev/null\n" +
		"fi"
}

func (h *Host) join(ctx context.Context, spec Container, elevation string) error {
	return h.joining(ctx, spec.App, spec.Class, spec.Project, networkStanding(spec.Class, spec.Project), elevation)
}

func (h *Host) joining(ctx context.Context, who string, class edge.Class, project, command, elevation string) error {
	_, said, err := h.spoke(ctx, "put "+who+" on "+AppNetwork(class, project), command, nil, elevation)
	if err != nil && strings.Contains(said, poolExhausted) {
		return refusal.Refuse(refusal.CodeNotReady,
			"%s has no subnet left for %s: %s\n"+
				"Add `\"default-address-pools\": [{\"base\": \"10.200.0.0/16\", \"size\": 24}]` to /etc/docker/daemon.json and restart docker",
			h.named(), AppNetwork(class, project), said)
	}
	return err
}

func (h *Host) ForgetNetwork(ctx context.Context, class edge.Class, project string) error {
	elevation, err := h.reachDocker(ctx)
	if err != nil {
		return err
	}
	_, err = h.ran(ctx, "take "+AppNetwork(class, project)+" down", networkForgetting(class, project), nil, elevation)
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
	Class   edge.Class

	Env        map[string]string
	HealthPath string
	Manifest   []byte
	Resolved   bool
	Declared   []string
}

func (c Container) delivered() map[string]string {
	env := make(map[string]string, len(c.Env)+2)
	maps.Copy(env, c.Env)
	if c.HealthPath != "" {
		env[originguard.HealthPathVar] = c.HealthPath
	}
	if len(c.Manifest) > 0 {
		env[live.EnvVar] = string(c.Manifest)
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

func containerStanding(spec Container, held handoff) string {
	return imageHeld(spec.Image, appPulls) + words(containerRun(spec, held)) + " >/dev/null"
}

func containerRun(spec Container, held handoff) []string {
	argv := []string{"docker", "run", "--detach",
		"--name", spec.Name,
		"--restart", appRestart,
		"--network", AppNetwork(spec.Class, spec.Project),
		"--label", LabelClass + "=" + string(spec.Class),
		"--label", LabelProject + "=" + naming.Sanitize(spec.Project),
		"--label", LabelApp + "=" + spec.App,
		"--label", LabelRef + "=" + spec.Image,
		"--label", LabelEnv + "=" + held.digest,
	}
	argv = append(argv, logging()...)
	argv = append(argv, confined(appCapabilities, false)...)
	if held.path != "" {
		argv = append(argv, "--env-file", held.path)
	}
	if len(spec.Manifest) > 0 {
		argv = append(argv, "--mount", "type=bind,src="+LiveSocketDir+",dst="+LiveSocketDir+",readonly",
			"--tmpfs", LiveDir+":"+liveDirTmpfs)
	}
	return append(argv, "--env", providerkit.InjectedPortName+"="+providerkit.InjectedPortText, spec.Image)
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
		return "docker unpause " + quoted(name) + " >/dev/null"
	}
	return "docker start " + quoted(name) + " >/dev/null"
}

func servingCommand(name string) string {
	return "docker inspect --type container --format " +
		quoted(servingSelectors()) + " " +
		quoted(name) + " 2>/dev/null || true"
}

func (h *Host) StandUp(ctx context.Context, spec Container) (err error) {
	elevation, err := h.reachDocker(ctx)
	if err != nil {
		return err
	}
	if err := h.join(ctx, spec, elevation); err != nil {
		return err
	}
	said := h.said(ctx, servingCommand(spec.Name), elevation)
	if spec.Resolved {
		held, err := handing(spec)
		if err != nil {
			return err
		}
		if stillServing(said, spec.Image, held.digest) {
			return nil
		}
	} else {
		if runningImage(said, spec.Image) {
			return nil
		}
		if startableImage(said, spec.Image) {
			_, err := h.ran(ctx, "start "+spec.App+" back up as "+spec.Name,
				restartCommand(said, spec.Name), nil, elevation)
			return err
		}
		note, known, err := h.handed(ctx, spec)
		if err != nil {
			return err
		}
		switch {
		case known && len(note.Handed) > 0:
			return refusal.Refuse(refusal.CodeNotReady,
				"%s is gone from this box and was handed %s, which a promotion cannot carry\nRun `ocel deploy`",
				spec.Name, strings.Join(note.Handed, ", "))
		case known:
			spec.Manifest = note.Live
		case len(spec.Declared) > 0:
			return refusal.Refuse(refusal.CodeNotReady,
				"%s is gone from this box, %s declares %s, and no note of its values remains\nRun `ocel deploy`",
				spec.Name, spec.App, strings.Join(spec.Declared, ", "))
		default:
			return refusal.Refuse(refusal.CodeNotReady,
				"%s is gone from this box and no note of its values remains\nRun `ocel deploy`",
				spec.Name)
		}
	}
	held, err := handing(spec)
	if err != nil {
		return err
	}
	if _, err := h.ran(ctx, "clear the name "+spec.Name,
		"docker rm --force "+quoted(spec.Name)+" >/dev/null 2>&1 || true", nil, elevation); err != nil {
		return err
	}
	if err := h.sweep(ctx, elevation); err != nil {
		return err
	}
	defer func() { err = errors.Join(err, h.forget(ctx, held)) }()
	if err := h.hand(ctx, held, spec); err != nil {
		return err
	}
	if spec.Resolved {
		if err := h.note(ctx, spec); err != nil {
			return err
		}
	}
	_, stood := h.ran(ctx, "stand "+spec.App+" up as "+spec.Name,
		containerStanding(spec, held), nil, elevation)
	return stood
}

func (h *Host) TakeDown(ctx context.Context, class edge.Class, name string) error {
	elevation, err := h.reachDocker(ctx)
	if err != nil {
		return err
	}
	_, err = h.ran(ctx, "take down "+name,
		"docker stop "+quoted(name)+" >/dev/null 2>&1 || true\n"+
			"docker rm --force "+quoted(name)+" >/dev/null 2>&1 || true", nil, elevation)
	if err != nil {
		return err
	}
	_, err = h.ran(ctx, "forget what "+name+" was handed", "rm -f "+quoted(HandedNote(class, name)), nil, "")
	return err
}

func (h *Host) StopContainer(ctx context.Context, name string) error {
	elevation, err := h.reachDocker(ctx)
	if err != nil {
		return err
	}
	_, err = h.ran(ctx, "stop "+name, "docker stop "+quoted(name)+" >/dev/null", nil, elevation)
	return err
}

func (h *Host) RemoveContainer(ctx context.Context, name string) error {
	elevation, err := h.reachDocker(ctx)
	if err != nil {
		return err
	}
	_, err = h.ran(ctx, "remove "+name, "docker rm --force "+quoted(name)+" >/dev/null", nil, elevation)
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
	return "docker inspect --type container --format " + quoted(strings.Join(stateSelectors(), " ")) + " " + quoted(name) + " 2>&1 || true"
}

func logCommand(name string) string {
	return "docker logs --timestamps --tail " + appLogTail + " " + quoted(name) + " 2>&1 || true"
}

func (h *Host) said(ctx context.Context, command string, elevation string) string {
	result, err := h.stream(ctx, command, nil, elevation)
	if err != nil {
		return err.Error()
	}
	return strings.TrimSpace(result.Stdout + result.Stderr)
}
