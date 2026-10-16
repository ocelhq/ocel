package variablestoreserver

import (
	"context"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/seal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

type wantRecordingCipher struct {
	seal.Cipher
	keyValues keyvalue.Store
	project   string
}

func (c wantRecordingCipher) Seal(ctx context.Context, tier environment.Tier, bound seal.AssociatedData, plaintext []byte) ([]byte, error) {
	sealed, err := c.Cipher.Seal(ctx, tier, bound, plaintext)
	return sealed, stackrecords.RecordWantedPart(ctx, c.keyValues, tier, c.project, err)
}

func (c wantRecordingCipher) Open(ctx context.Context, tier environment.Tier, bound seal.AssociatedData, sealed []byte) ([]byte, error) {
	opened, err := c.Cipher.Open(ctx, tier, bound, sealed)
	return opened, stackrecords.RecordWantedPart(ctx, c.keyValues, tier, c.project, err)
}
