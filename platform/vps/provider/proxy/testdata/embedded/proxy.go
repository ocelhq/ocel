package embedded

import (
	"context"
	"io"
)

type Proxy interface {
	io.Closer
	Admit(ctx context.Context) error
}
