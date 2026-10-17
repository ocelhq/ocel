package cloud

import (
	"context"
	"time"
)

func SkipPauses(c *Cloud) *Cloud {
	c.pause = func(context.Context, time.Duration) error { return nil }
	return c
}
