package embedded

import (
	"context"
	"io"
)

type Proxy interface {
	io.Closer
	Reload(ctx context.Context) error
}
