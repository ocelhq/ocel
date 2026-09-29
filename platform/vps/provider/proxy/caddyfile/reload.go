package caddyfile

import (
	"context"

	"github.com/ocelhq/ocel/pkg/refusal"
)

func (c Caddyfile) validate(ctx context.Context, rendered []byte) error {
	if len(rendered) == 0 {
		return nil
	}
	if _, err := c.Box.Fed(ctx, "adapt "+FileName+" with "+c.running(), c.adapting(), rendered); err != nil {
		return refusal.Refuse(refusal.CodeInvalid,
			"%s cannot adapt the %s ocel rendered, so it was not placed: %v", c.running(), FileName, err)
	}
	return nil
}

func (c Caddyfile) reload(ctx context.Context) error {
	if _, err := c.Box.Ran(ctx, "reload "+c.running(), c.Reloading()); err != nil {
		return refusal.Refuse(refusal.CodeNotReady,
			"%s refused the reload and keeps serving the config it had: %v\nThe error can be in your own Caddyfile as well as in %s",
			c.running(), err, FileName)
	}
	return nil
}
