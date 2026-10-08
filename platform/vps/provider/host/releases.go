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

type Swept struct {
	Removed []string
	Unused  []string
}

func (h *Host) Reconcile(ctx context.Context, project, app, imageRef string) (Swept, error) {
	repository, named := Repository(imageRef)
	if !named {
		return Swept{}, refusal.Refuse(refusal.CodeInvalid,
			"%s runs %s, which names no repository and tag", app, imageRef)
	}
	said, err := h.releases(ctx, "reconcile "+app+"'s images", Scope(project, app), "reconcile", repository)
	if err != nil {
		return Swept{}, err
	}
	var swept Swept
	for line := range strings.Lines(said) {
		kind, image, _ := strings.Cut(strings.TrimSpace(line), " ")
		switch {
		case image == "":
		case kind == "removed":
			swept.Removed = append(swept.Removed, image)
		case kind == "unused":
			swept.Unused = append(swept.Unused, image)
		}
	}
	return swept, nil
}

func (h *Host) Settle(ctx context.Context, project, app string, imageRefs []string) error {
	if len(imageRefs) == 0 {
		return nil
	}
	_, err := h.releases(ctx, "settle "+app+"'s removed images", Scope(project, app), append([]string{"settle"}, imageRefs...)...)
	return err
}

func (h *Host) IsClaimed(ctx context.Context, project, app, imageRef string) (bool, error) {
	said, err := h.releases(ctx, "read whether a release of "+app+" still names "+imageRef, Scope(project, app), "claimed", imageRef)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(said) == imageRef, nil
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
