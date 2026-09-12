package providerkit

type Vars struct {
	Records RecordStore
	Sealer  Sealer
	Grants  GrantVerifier
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
	out := Vars{Records: provider.Records(), Sealer: provider.Sealer()}
	if verifier, vets := provider.(GrantVerifier); vets {
		out.Grants = verifier
	}
	return out, nil
}
