package providerkit

import "context"

type Vars struct {
	Records      RecordStore
	Sealer       Sealer
	VerifyGrants func(ctx context.Context, binding Binding) error
}

type VarsSource interface {
	Vars() (Vars, error)
}

type VarsHandler struct {
	Source VarsSource
}

type StandingVars Vars

func (s StandingVars) Vars() (Vars, error) { return Vars(s), nil }

type sessionVars struct {
	session *session
}

func (s sessionVars) Vars() (Vars, error) {
	provider, err := s.session.use()
	if err != nil {
		return Vars{}, err
	}
	return Vars{Records: provider.Records(), Sealer: provider.Sealer(), VerifyGrants: provider.Hooks().VerifyGrants}, nil
}
