package bucket

import (
	"context"
	"fmt"
	"net/url"
	"slices"

	s3store "github.com/ocelhq/ocel/platform/s3"
)

type callbacks struct {
	allowed []string
}

func (c callbacks) Post(ctx context.Context, target string, body []byte) error {
	parsed, err := url.Parse(target)
	if err != nil {
		return fmt.Errorf("read the upload callback address: %w", err)
	}
	origin := parsed.Scheme + "://" + parsed.Host
	if !slices.Contains(c.allowed, origin) {
		return nil
	}
	return s3store.HTTPPoster{}.Post(ctx, target, body)
}
