package host

import (
	"context"
	"errors"

	"github.com/ocelhq/ocel/pkg/refusal"
)

func (h *Host) actAsStateOwner(ctx context.Context) (string, bool) {
	live, err := h.dial(ctx)
	if err != nil || live.Destination().User == stateOwner {
		return "", false
	}
	result, err := h.stream(ctx, "id -u "+quoted(stateOwner)+" >/dev/null 2>&1", nil, "")
	if err != nil || result.Code != 0 {
		return "", false
	}
	acting, err := h.reachStateRoot(ctx)
	if err != nil {
		return "", false
	}
	return acting, true
}

func (h *Host) reachStateRoot(ctx context.Context) (string, error) {
	live, err := h.dial(ctx)
	if err != nil {
		return "", err
	}
	login := live.Destination().User
	if login == stateOwner {
		return "", nil
	}
	elevation, err := h.elevate(ctx)
	if err != nil {
		var refused refusal.Refusal
		if !errors.As(err, &refused) {
			return "", err
		}
		return "", refusal.Refuse(refusal.CodeDenied,
			"%s belongs to %s, and %s cannot act as it: %s\nDeploy as %s (set `user` under `ssh` to it), or as a login with sudo without a password",
			stateRoot, stateOwner, login, refused.Message, stateOwner)
	}
	if elevation == "" {
		return "runuser -u " + stateOwner + " -- ", nil
	}
	return elevation + "-u " + stateOwner + " ", nil
}
