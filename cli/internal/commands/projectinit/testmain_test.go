package projectinit

import (
	"os"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/clitest"
	"github.com/ocelhq/ocel/pkg/configdoc"
)

func TestMain(m *testing.M) {
	clitest.AddFakeProviderIDs()
	if clitest.IsFakeSession() {
		os.Exit(clitest.RunFakeSession())
	}
	clitest.UnsetColorEnv()
	done := clitest.IsolateConfigHome()
	code := m.Run()
	done()
	os.Exit(code)
}

func newTestDependencies() Dependencies {
	return Dependencies{Invocation: clitest.NewInvocation()}
}

func providerNamedAlone() string {
	return providerWhere(configdoc.ProviderNamedAlone)
}

func providerKeyed() string {
	return providerWhere(func(id string) bool {
		return !configdoc.ProviderNamedAlone(id) && len(configdoc.RequiredProviderOptions(id)) == 0
	})
}

func providerWhere(matches func(id string) bool) string {
	ids := configdoc.ProviderIDs()
	i := slices.IndexFunc(ids, matches)
	if i < 0 {
		panic("no shipped provider is rendered that way")
	}
	return ids[i]
}
