package host

import (
	"context"
	_ "embed"
	"strconv"
	"strings"
	"time"

	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/vps/provider/session"
)

//go:embed containerlogs.sh
var containerLogsScript string

func (h *Host) FollowContainerLogs(ctx context.Context, names []string, since time.Time, each func(container int, line Line) error, gone func(container int) error) error {
	elevation, err := h.reachDocker(ctx)
	if err != nil {
		return err
	}
	newest := make([][2]time.Time, len(names))
	reported := make([]bool, len(names))
	return h.streamLines(ctx, followLogsCommand(names, since), elevation, func(raw session.Line) error {
		stderr := raw.Pipe == session.Stderr
		container, text, tagged := cutContainerTag(raw.Text, len(names))
		if !tagged {
			if stderr {
				return refusal.Refuse(refusal.CodeNotReady, "%s cannot follow container logs: %s", h.named(), raw.Text)
			}
			return nil
		}
		line, stamped := parseLogLine(text, stderr)
		switch {
		case !stamped && !stderr:
			return nil
		case !stamped && strings.Contains(strings.ToLower(text), "no such container"):
			if reported[container] {
				return nil
			}
			reported[container] = true
			return gone(container)
		case !stamped:
			return refusal.Refuse(refusal.CodeNotReady, "%s cannot follow the logs of %s: %s", h.named(), names[container], text)
		}
		stream := 0
		if stderr {
			stream = 1
		}
		if line.Time.Before(since) || !line.Time.After(newest[container][stream]) {
			return nil
		}
		newest[container][stream] = line.Time
		return each(container, line)
	})
}

func followLogsCommand(names []string, since time.Time) string {
	command := "sh -c " + quoted(containerLogsScript) + " containerlogs " + quoted(since.UTC().Format(time.RFC3339Nano))
	for _, name := range names {
		command += " " + quoted(name)
	}
	return command
}

func cutContainerTag(raw string, containers int) (int, string, bool) {
	tag, text, cut := strings.Cut(raw, " ")
	container, err := strconv.Atoi(tag)
	if !cut || err != nil || container < 0 || container >= containers {
		return 0, "", false
	}
	return container, text, true
}
