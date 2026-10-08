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
	configdoc.AddKnownIDs(twoOptionProvider, nil, nil)
	configdoc.AddKnownProviderOptions(twoOptionProvider, []configdoc.ProviderOption{
		{Name: "host", Doc: "The host to deploy onto.", Required: true},
		{Name: "zone", Doc: "The zone to deploy into.", Required: true},
	})
	if clitest.IsFakeSession() {
		os.Exit(clitest.RunFakeSession())
	}
	clitest.UnsetColorEnv()
	done := clitest.IsolateConfigHome()
	code := m.Run()
	done()
	os.Exit(code)
}

const twoOptionProvider = "two-option"

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
