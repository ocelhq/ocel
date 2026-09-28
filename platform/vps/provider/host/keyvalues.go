package host

import (
	"context"
	_ "embed"
	"io"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/platform/vps/provider/boxstore"
)

//go:embed keyvalues.sh
var keyValuesScript []byte

func NewKeyValues(h *Host) *boxstore.KeyValues { return boxstore.NewKeyValues(sshKeyValues{host: h}) }

type sshKeyValues struct{ host *Host }

func (s sshKeyValues) HasStore(ctx context.Context, tier environment.Tier) (bool, error) {
	return s.host.hasStore(ctx, tier)
}

func (s sshKeyValues) Run(ctx context.Context, tier environment.Tier, stdin io.Reader, argv ...string) (string, error) {
	command := quoted(boxstore.KeyValuesHelper) + " " + quoted(string(tier))
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
	case boxstore.ExitNotFound:
		return "", keyvalue.ErrNotFound
	case boxstore.ExitStale:
		return "", keyvalue.ErrStale
	default:
		return "", unelevated(refused, s.host.refuse("keyvalues "+argv[0], result, elevation))
	}
}
