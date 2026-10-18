package docker

import (
	"context"
	"strings"

	"github.com/ocelhq/ocel/platform/vps/provider/shell"
	"github.com/ocelhq/ocel/platform/vps/provider/ssh"
)

var StateFields = []struct{ Label, selector string }{
	{"Status", ".State.Status"},
	{"ExitCode", ".State.ExitCode"},
	{"OOMKilled", ".State.OOMKilled"},
	{"Error", ".State.Error"},
	{"StartedAt", ".State.StartedAt"},
	{"FinishedAt", ".State.FinishedAt"},
	{"RestartCount", ".RestartCount"},
}

func ServingSelectors() string {
	return "{{.State.Status}} " + LabelSelector(LabelRef) + " " + LabelSelector(LabelEnv)
}

func IsRunningImage(said, image string) bool {
	fields := strings.Fields(said)
	return len(fields) >= 2 && fields[0] == "running" && fields[1] == image
}

func IsServing(said, image, digest string) bool {
	if !IsRunningImage(said, image) {
		return false
	}
	fields := strings.Fields(said)
	return len(fields) > 2 && fields[2] == digest
}

func IsStartableImage(said, image string) bool {
	fields := strings.Fields(said)
	return len(fields) >= 2 && fields[1] == image &&
		(fields[0] == "exited" || fields[0] == "created" || fields[0] == "paused")
}

func RestartCommand(said, name string) string {
	if strings.HasPrefix(strings.TrimSpace(said), "paused") {
		return "docker unpause " + shell.Quote(name) + " >/dev/null"
	}
	return "docker start " + shell.Quote(name) + " >/dev/null"
}

func ServingCommand(name string) string {
	return "if said=$(docker inspect --type container --format " + shell.Quote(ServingSelectors()) + " " + shell.Quote(name) + " 2>&1); then printf '%s\\n' \"$said\"\n" +
		"elif printf '%s' \"$said\" | grep -qi 'no such'; then :\n" +
		"else printf '%s\\n' \"$said\" >&2; exit 1\n" +
		"fi"
}

func StateSelectors() []string {
	selectors := make([]string, 0, len(StateFields))
	for _, field := range StateFields {
		selectors = append(selectors, field.Label+"={{"+field.selector+"}}")
	}
	return selectors
}

func StateCommand(name string) string {
	return "docker inspect --type container --format " + shell.Quote(strings.Join(StateSelectors(), " ")) + " " + shell.Quote(name) + " 2>&1 || true"
}

func (e *Engine) ReadServing(ctx context.Context, name string) (string, error) {
	said, err := e.box.Run(ctx, ssh.Docker, "read whether "+name+" is serving", ServingCommand(name), nil)
	return strings.TrimSpace(said), err
}

func StateField(state, label string) string {
	for _, field := range strings.Fields(state) {
		if name, value, split := strings.Cut(field, "="); split && name == label {
			return value
		}
	}
	return ""
}
