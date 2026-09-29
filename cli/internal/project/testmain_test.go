package project

import (
	"os"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/configdoc"
	"github.com/ocelhq/ocel/pkg/provider/fake"
)

func TestMain(m *testing.M) {
	configdoc.AddKnownIDs(string(fake.Vendor), []string{string(fake.KindRelay), string(fake.KindDirect)}, []string{string(fake.KindZone)})
	os.Exit(m.Run())
}

func providerNamedAlone() string {
	ids := configdoc.ProviderIDs()
	i := slices.IndexFunc(ids, configdoc.ProviderNamedAlone)
	if i < 0 {
		panic("no shipped provider may be named alone")
	}
	return ids[i]
}
