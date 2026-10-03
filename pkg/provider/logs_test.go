package provider_test

import (
	"testing"

	"github.com/ocelhq/ocel/pkg/provider"
)

func TestALogTargetNamesThePhysicalResourceAndRevisionOfWhatItReads(t *testing.T) {
	t.Parallel()

	for name, test := range map[string]struct {
		target             provider.LogTarget
		physical, revision string
	}{
		"a function":  {provider.LogTarget{Function: &provider.Function{Physical: "web-fn", Revision: "web-fn-2"}}, "web-fn", "web-fn-2"},
		"a container": {provider.LogTarget{Container: &provider.AppContainer{Physical: "web-box", Revision: "web-box-3"}}, "web-box", "web-box-3"},
		"neither":     {provider.LogTarget{App: "web"}, "", ""},
	} {
		if got := test.target.Physical(); got != test.physical {
			t.Errorf("Physical() of %s = %q, want %q", name, got, test.physical)
		}
		if got := test.target.Revision(); got != test.revision {
			t.Errorf("Revision() of %s = %q, want %q", name, got, test.revision)
		}
	}
}
