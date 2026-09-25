package host

import (
	"context"
	_ "embed"
	"io"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/vps/provider/helpers"
)

//go:embed records.sh
var recordsScript []byte

func NewRecords(h *Host) *helpers.Records { return helpers.RecordsOver(sshRecords{host: h}) }

type sshRecords struct{ host *Host }

func (s sshRecords) Holds(ctx context.Context, class providerkit.Class) (bool, error) {
	return s.host.holds(ctx, class)
}

func (s sshRecords) Records(ctx context.Context, class providerkit.Class, stdin io.Reader, argv ...string) (string, error) {
	command := quoted(recordsHelper) + " " + quoted(string(class))
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
	case helpers.ExitNoRecord:
		return "", providerkit.ErrNoRecord
	case helpers.ExitStale:
		return "", providerkit.ErrStale
	default:
		return "", unelevated(refused, s.host.refuse("records "+argv[0], result))
	}
}
