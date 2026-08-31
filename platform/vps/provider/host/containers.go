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
	Env   map[string]string
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

func stillServing(said string, spec Container, digest string) bool {
	fields := strings.Fields(said)
	if len(fields) < 2 || fields[0] != "running" || fields[1] != spec.Image {
		return false
	}
	if len(spec.Env) == 0 {
		return true
	}
	return len(fields) > 2 && fields[2] == digest
}

func servingCommand(name string) string {
	return "docker inspect --type container --format " +
		quoted(servingSelectors()) + " " +
		quoted(name) + " 2>/dev/null || true"
}

func (h *Host) StandUp(ctx context.Context, spec Container) error {
	elevation, err := h.reachDocker(ctx)
	if err != nil {
		return err
	}
	digest, err := envDigest(spec.Env)
	if err != nil {
		return err
	}
	if stillServing(h.said(ctx, servingCommand(spec.Name), elevation), spec, digest) {
		return nil
	}
	if _, err := h.ran(ctx, "clear the name "+spec.Name,
		"docker rm --force "+quoted(spec.Name)+" >/dev/null 2>&1 || true", nil, elevation); err != nil {
		return err
	}
	held, err := h.hand(ctx, spec)
	if err != nil {
		return err
	}
	_, stood := h.ran(ctx, "stand "+spec.App+" up as "+spec.Name,
		words(containerRun(spec, held))+" >/dev/null", nil, elevation)
	return errors.Join(stood, h.forget(ctx, held))
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
