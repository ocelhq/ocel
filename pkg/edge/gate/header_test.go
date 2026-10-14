package gate_test

import (
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge/gate"
)

func TestAHeaderFindsAValueUnderAnyCapitalisationOfItsName(t *testing.T) {
	t.Parallel()

	header := gate.Header{}
	header.Add("x-ocel-bypass", "one")
	header.Add("X-OCEL-BYPASS", "two")

	if got := header.Values("X-Ocel-Bypass"); !slices.Equal(got, []string{"one", "two"}) {
		t.Errorf("Values = %q, want both values", got)
	}
	header.Del("x-ocel-bypass")
	if got := header.Get("X-Ocel-Bypass"); got != "" {
		t.Errorf("Get after Del = %q, want empty", got)
	}
}

func TestAHeaderCloneSharesNothingWithItsOriginal(t *testing.T) {
	t.Parallel()

	header := gate.Header{"Cookie": {"a=b"}}
	cloned := header.Clone()
	cloned.Set("Cookie", "c=d")
	cloned.Values("Cookie")[0] = "e=f"

	if got := header.Get("Cookie"); got != "a=b" {
		t.Errorf("the original Cookie = %q after changing the clone, want a=b", got)
	}
}

func TestTheGateLinksNoNetworkingPackageSoItFitsAWasmPlugin(t *testing.T) {
	t.Parallel()

	out, err := exec.Command("go", "list", "-deps", ".").Output()
	if err != nil {
		t.Fatal(err)
	}
	for _, dep := range strings.Fields(string(out)) {
		if dep == "net/http" || dep == "net" {
			t.Errorf("the gate links %s, which a proxy-wasm plugin cannot afford", dep)
		}
	}
}
