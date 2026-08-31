package host

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit"
)

const AppPort = "8080"

const (
	LabelApp = "ocel.app"
	LabelRef = "ocel.ref"
	LabelEnv = "ocel.env"
)

const (
	appRestart = "unless-stopped"
	nameShort  = 12
)

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
	Name  string
	App   string
	Image string
	Class providerkit.Class

	Env      map[string]string
	Resolved bool
	Declared []string
}

func ContainerName(stack, app, deployment, image string) string {
	identity := deployment
	if identity == "" {
		sum := sha256.Sum256([]byte(image))
		identity = hex.EncodeToString(sum[:])
	}
	return naming.Sanitize(stack) + "-" + naming.Sanitize(app) + "-" + naming.Sanitize(identity[:min(len(identity), nameShort)])
}

func containerRun(spec Container, held handoff) []string {
	argv := []string{"docker", "run", "--detach",
		"--name", spec.Name,
		"--restart", appRestart,
		"--network", ProxyNetwork,
		"--label", LabelApp + "=" + spec.App,
		"--label", LabelRef + "=" + spec.Image,
		"--label", LabelEnv + "=" + held.digest,
	}
	if held.path != "" {
		argv = append(argv, "--env-file", held.path)
	}
	return append(argv, "--env", "PORT="+AppPort, spec.Image)
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
	held, err := handing(spec)
	if err != nil {
		return err
	}
	said := h.said(ctx, servingCommand(spec.Name), elevation)
	if spec.Resolved {
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
		if len(spec.Declared) > 0 {
			return providerkit.Refuse(providerkit.CodeNotReady,
				"%s no longer stands on this box, and %s declares %s: a container is handed the values its own deploy resolved, and a promotion carries none of them, so putting one back here would serve %s with an empty environment. Run `ocel deploy` to stand it up with its values",
				spec.Name, spec.App, strings.Join(spec.Declared, ", "), spec.App)
		}
	}
	if _, err := h.ran(ctx, "clear the name "+spec.Name,
		"docker rm --force "+quoted(spec.Name)+" >/dev/null 2>&1 || true", nil, elevation); err != nil {
		return err
	}
	defer func() { err = errors.Join(err, h.forget(ctx, held)) }()
	if err := h.hand(ctx, held, spec); err != nil {
		return err
	}
	_, stood := h.ran(ctx, "stand "+spec.App+" up as "+spec.Name,
		words(containerRun(spec, held))+" >/dev/null", nil, elevation)
	return stood
}

func (h *Host) TakeDown(ctx context.Context, name string) error {
	elevation, err := h.reachDocker(ctx)
	if err != nil {
		return err
	}
	_, err = h.ran(ctx, "take down "+name,
		"docker stop "+quoted(name)+" >/dev/null 2>&1 || true\n"+
			"docker rm --force "+quoted(name)+" >/dev/null 2>&1 || true", nil, elevation)
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
