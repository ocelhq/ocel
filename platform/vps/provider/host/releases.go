package host

import (
	"context"
	_ "embed"
	"strings"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

//go:embed releases.sh
var releasesScript []byte

func Repository(coordinate string) (string, bool) {
	at := strings.LastIndex(coordinate, ":")
	if at <= 0 || strings.ContainsAny(coordinate[at+1:], "/@") || at+1 == len(coordinate) {
		return "", false
	}
	return coordinate[:at], true
}

func (h *Host) Promote(ctx context.Context, class providerkit.Class, app, coordinate string) error {
	_, err := h.releases(ctx, "record "+coordinate+" as what "+app+" most recently served", "",
		app, "promote", string(class), coordinate)
	return err
}

func (h *Host) Forget(ctx context.Context, class providerkit.Class, app string) error {
	_, err := h.releases(ctx, "drop the window "+app+" was served from", "",
		app, "forget", string(class))
	return err
}

func (h *Host) Reconcile(ctx context.Context, app, coordinate string, report providerkit.Reporter) error {
	repository, named := Repository(coordinate)
	if !named {
		return providerkit.Refuse(providerkit.CodeInvalid,
			"%s runs %s, which names no repository this host can list, and a sweep whose filter and desired set disagree on scope removes the wrong thing", app, coordinate)
	}
	elevation, err := h.reachDocker(ctx)
	if err != nil {
		return err
	}
	said, err := h.releases(ctx, "reconcile "+app+"'s images", elevation, app, "reconcile", repository)
	if err != nil {
		return err
	}
	if report == nil {
		return nil
	}
	for line := range strings.Lines(said) {
		removed := strings.TrimSpace(line)
		if removed == "" {
			continue
		}
		report.Detail("Removed " + removed + ": no release of " + app + " this host keeps names it and nothing runs it")
	}
	return nil
}

func (h *Host) releases(ctx context.Context, what, elevation, app string, args ...string) (string, error) {
	command := quoted(releasesHelper) + " " + quoted(app)
	for _, arg := range args {
		command += " " + quoted(arg)
	}
	return h.ran(ctx, what, command, nil, elevation)
}
