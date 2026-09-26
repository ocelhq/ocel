package providerkit

import (
	"context"

	"github.com/ocelhq/ocel/pkg/providerkit/records"
)

type Vars struct {
	Records      records.Store
	Cipher       records.Cipher
	VerifyGrants func(ctx context.Context, binding Binding) error
}

type VarsSource interface {
	Read() (Vars, error)
}

type VarsService struct {
	Source VarsSource
}

type FixedVars Vars

func (s FixedVars) Read() (Vars, error) { return Vars(s), nil }

type sessionVars struct {
	session *session
}

func (s sessionVars) Read() (Vars, error) {
	provider, err := s.session.use()
	if err != nil {
		return Vars{}, err
	}
	return Vars{Records: provider.Records(), Cipher: provider.Cipher(), VerifyGrants: provider.Hooks().VerifyGrants}, nil
}
