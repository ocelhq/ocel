package envsource

import (
	"testing"

	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/googletest"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/run"
)

func syncComposed(b bootstrap.Engine) bootstrap.Engine {
	b.Removable = []bootstrap.RemovablePart{Part}
	b.Accounts = []bootstrap.Account{Account}
	return b
}

func syncOpened(s *googletest.Sync, t *testing.T) bootstrap.Engine {
	t.Helper()
	c := s.Open(t)
	services := &run.Services{OpenClients: googletest.Opener(c), Records: fake.NewKeyValues()}
	return syncComposed(bootstrap.Engine{ServiceStates: &bootstrap.ServiceStates{}, Clients: c, DeployAndRoute: services.DeployAndRoute, TearDown: services.TearDown, PushBinary: s.Push})
}
