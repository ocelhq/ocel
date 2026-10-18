package host

import (
	"context"
	_ "embed"
	"io"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/platform/vps/provider/boxstore"
	"github.com/ocelhq/ocel/platform/vps/provider/shell"
	"github.com/ocelhq/ocel/platform/vps/provider/ssh"
)

//go:embed keyvalues.sh
var keyValuesScript []byte

func NewKeyValues(h *Host) *boxstore.KeyValues { return boxstore.NewKeyValues(sshKeyValues{host: h}) }

type sshKeyValues struct{ host *Host }

func (s sshKeyValues) HasStore(ctx context.Context, tier environment.Tier) (bool, error) {
	return s.host.hasStore(ctx, tier)
}

func (s sshKeyValues) Run(ctx context.Context, tier environment.Tier, stdin io.Reader, argv ...string) (string, error) {
	command := shell.Quote(boxstore.KeyValuesHelper) + " " + shell.Quote(string(tier))
	for _, arg := range argv {
		command += " " + shell.Quote(arg)
	}
	elevation := ssh.RootWhenAble
	result, err := s.host.Stream(ctx, elevation, command, stdin)
	if err != nil {
		return "", err
	}
	switch result.Code {
	case 0:
		return result.Stdout, nil
	case boxstore.ExitNotFound:
		return "", records.ErrNotFound
	case boxstore.ExitStale:
		return "", records.ErrStale
	default:
		return "", s.host.Refuse("keyvalues "+argv[0], result)
	}
}
