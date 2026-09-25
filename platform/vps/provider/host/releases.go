package host

import (
	"context"
	_ "embed"
	"strings"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit"
)

//go:embed releases.sh
var releasesScript []byte

func Repository(coordinate string) (string, bool) {
	if strings.Contains(coordinate, "@") {
		return "", false
	}
	at := strings.LastIndex(coordinate, ":")
	if at <= 0 || strings.Contains(coordinate[at+1:], "/") || at+1 == len(coordinate) {
		return "", false
	}
	return coordinate[:at], true
}

func Scope(project, app string) string { return naming.Sanitize(project) + "/" + app }

func (h *Host) Promote(ctx context.Context, class providerkit.Class, project, app, coordinate string) error {
	_, err := h.releases(ctx, "record "+coordinate+" as "+app+"'s release", "",
		Scope(project, app), "promote", string(class), coordinate)
	return err
}

func (h *Host) Forget(ctx context.Context, class providerkit.Class, project, app string) error {
	_, err := h.releases(ctx, "forget "+app+"'s releases", "",
		Scope(project, app), "forget", string(class))
	return err
}

func (h *Host) Reconcile(ctx context.Context, project, app, coordinate string, progress providerkit.Progress) error {
	repository, named := Repository(coordinate)
	if !named {
		return providerkit.Refuse(providerkit.CodeInvalid,
			"%s runs %s, which names no repository and tag", app, coordinate)
	}
	elevation, err := h.reachDocker(ctx)
	if err != nil {
		return err
	}
	said, err := h.releases(ctx, "reconcile "+app+"'s images", elevation, Scope(project, app), "reconcile", repository)
	if err != nil {
		return err
	}
	if progress == nil {
		return nil
	}
	for line := range strings.Lines(said) {
		removed := strings.TrimSpace(line)
		if removed == "" {
			continue
		}
		progress.Detail("Removed " + removed + ": unused image of " + app)
	}
	return nil
}

func (h *Host) releases(ctx context.Context, what, elevation, scope string, args ...string) (string, error) {
	command := quoted(releasesHelper) + " " + quoted(scope)
	for _, arg := range args {
		command += " " + quoted(arg)
	}
	return h.ran(ctx, what, command, nil, elevation)
}
