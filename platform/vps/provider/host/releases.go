package host

import (
	"context"
	_ "embed"
	"strings"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/refusal"
)

//go:embed releases.sh
var releasesScript []byte

func Repository(imageRef string) (string, bool) {
	if strings.Contains(imageRef, "@") {
		return "", false
	}
	at := strings.LastIndex(imageRef, ":")
	if at <= 0 || strings.Contains(imageRef[at+1:], "/") || at+1 == len(imageRef) {
		return "", false
	}
	return imageRef[:at], true
}

func Scope(project, app string) string { return naming.Sanitize(project) + "/" + app }

func (h *Host) Promote(ctx context.Context, tier environment.Tier, project, app, imageRef string) error {
	_, err := h.releases(ctx, "record "+imageRef+" as "+app+"'s release",
		Scope(project, app), "promote", string(tier), imageRef)
	return err
}

func (h *Host) Forget(ctx context.Context, tier environment.Tier, project, app string) error {
	_, err := h.releases(ctx, "forget "+app+"'s releases",
		Scope(project, app), "forget", string(tier))
	return err
}

func (h *Host) Reconcile(ctx context.Context, project, app, imageRef string) ([]string, error) {
	repository, named := Repository(imageRef)
	if !named {
		return nil, refusal.Refuse(refusal.CodeInvalid,
			"%s runs %s, which names no repository and tag", app, imageRef)
	}
	said, err := h.releases(ctx, "reconcile "+app+"'s images", Scope(project, app), "reconcile", repository)
	if err != nil {
		return nil, err
	}
	var removed []string
	for line := range strings.Lines(said) {
		if image := strings.TrimSpace(line); image != "" {
			removed = append(removed, image)
		}
	}
	return removed, nil
}

func (h *Host) releases(ctx context.Context, what, scope string, args ...string) (string, error) {
	acting, err := h.reachStateRoot(ctx)
	if err != nil {
		return "", err
	}
	command := acting + quoted(releasesHelper) + " " + quoted(scope)
	for _, arg := range args {
		command += " " + quoted(arg)
	}
	return h.ran(ctx, what, command, nil, "")
}
