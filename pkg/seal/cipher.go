package seal

import (
	"context"

	"github.com/ocelhq/ocel/pkg/environment"
)

type Cipher interface {
	Seal(ctx context.Context, tier environment.Tier, bound AssociatedData, plaintext []byte) ([]byte, error)

	Open(ctx context.Context, tier environment.Tier, bound AssociatedData, sealed []byte) ([]byte, error)
}
