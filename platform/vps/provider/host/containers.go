package host

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"

	"github.com/ocelhq/ocel/pkg/naming"
)

const (
	LabelApp = "ocel.app"
	LabelRef = "ocel.ref"
)

const (
	appRestart  = "unless-stopped"
	appLogTail  = "200"
	nameShort   = 12
	noLogOutput = "(no output)"
)

var stateFields = []string{"Status", "ExitCode", "OOMKilled", "Error", "StartedAt", "FinishedAt", "RestartCount"}

type Container struct {
	Name  string
	App   string
	Image string
}

func ContainerName(stack, app, deployment, image string) string {
	identity := deployment
	if identity == "" {
		sum := sha256.Sum256([]byte(image))
		identity = hex.EncodeToString(sum[:])
	}
	return naming.Sanitize(stack) + "-" + naming.Sanitize(app) + "-" + naming.Sanitize(identity[:min(len(identity), nameShort)])
}

func containerRun(spec Container) []string {
	return []string{"docker", "run", "--detach",
		"--name", spec.Name,
		"--restart", appRestart,
		"--network", ProxyNetwork,
		"--label", LabelApp + "=" + spec.App,
		"--label", LabelRef + "=" + spec.Image,
		"--env", "PORT=" + AppPort,
		spec.Image,
	}
}

func servingCommand(name string) string {
	return "docker inspect --type container --format " +
		quoted("{{.State.Running}} {{index .Config.Labels "+strconv.Quote(LabelRef)+"}}") + " " +
		quoted(name) + " 2>/dev/null || true"
}

func (h *Host) StandUp(ctx context.Context, spec Container) error {
	elevation, err := h.reachDocker(ctx)
	if err != nil {
		return err
	}
	if h.said(ctx, servingCommand(spec.Name), elevation) == "true "+spec.Image {
		return nil
	}
	if _, err := h.ran(ctx, "clear the name "+spec.Name,
		"docker rm --force "+quoted(spec.Name)+" >/dev/null 2>&1 || true", nil, elevation); err != nil {
		return err
	}
	_, err = h.ran(ctx, "stand "+spec.App+" up as "+spec.Name, words(containerRun(spec))+" >/dev/null", nil, elevation)
	return err
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

func stateCommand(name string) string {
	selectors := make([]string, 0, len(stateFields))
	for _, field := range stateFields {
		selectors = append(selectors, field+"={{.State."+field+"}}")
	}
	return "docker inspect --type container --format " + quoted(strings.Join(selectors, " ")) + " " + quoted(name) + " 2>&1 || true"
}

func (h *Host) said(ctx context.Context, command string, elevation string) string {
	result, err := h.stream(ctx, command, nil, elevation)
	if err != nil {
		return err.Error()
	}
	return strings.TrimSpace(result.Stdout + result.Stderr)
}
