package host

import (
	"context"
	_ "embed"
	"io"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/platform/vps/provider/boxstore"
)

//go:embed records.sh
var recordsScript []byte

func NewKeyValues(h *Host) *boxstore.KeyValues { return boxstore.NewKeyValues(sshKeyValues{host: h}) }

type sshKeyValues struct{ host *Host }

func (s sshKeyValues) HasStore(ctx context.Context, tier environment.Tier) (bool, error) {
	return s.host.hasStore(ctx, tier)
}

func (s sshKeyValues) KeyValues(ctx context.Context, tier environment.Tier, stdin io.Reader, argv ...string) (string, error) {
	command := quoted(boxstore.RecordsHelper) + " " + quoted(string(tier))
	for _, arg := range argv {
		command += " " + quoted(arg)
	}
	elevation, refused := s.host.elevate(ctx)
	result, err := s.host.stream(ctx, command, stdin, elevation)
	if err != nil {
		return "", err
	}
	switch result.Code {
	case 0:
		return result.Stdout, nil
	case boxstore.ExitNoRecord:
		return "", keyvalue.ErrNotFound
	case boxstore.ExitStale:
		return "", keyvalue.ErrStale
	default:
		return "", unelevated(refused, s.host.refuse("records "+argv[0], result, elevation))
	}
}
