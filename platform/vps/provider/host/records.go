package host

import (
	"context"
	_ "embed"
	"io"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/platform/vps/provider/boxstore"
)

//go:embed records.sh
var recordsScript []byte

func NewRecords(h *Host) *boxstore.Records { return boxstore.NewRecords(sshRecords{host: h}) }

type sshRecords struct{ host *Host }

func (s sshRecords) HasStore(ctx context.Context, tier environment.Tier) (bool, error) {
	return s.host.hasStore(ctx, tier)
}

func (s sshRecords) Records(ctx context.Context, tier environment.Tier, stdin io.Reader, argv ...string) (string, error) {
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
		return "", records.ErrNotFound
	case boxstore.ExitStale:
		return "", records.ErrStale
	default:
		return "", unelevated(refused, s.host.refuse("records "+argv[0], result, elevation))
	}
}
