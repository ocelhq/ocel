package previewgate_test

import (
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge/previewgate"
)

func TestAHeaderFindsAValueUnderAnyCapitalisationOfItsName(t *testing.T) {
	t.Parallel()

	header := previewgate.Header{}
	header.Add("x-ocel-preview-bypass", "one")
	header.Add("X-OCEL-PREVIEW-BYPASS", "two")

	if got := header.Values("X-Ocel-Preview-Bypass"); !slices.Equal(got, []string{"one", "two"}) {
		t.Errorf("Values = %q, want both values", got)
	}
	header.Del("x-ocel-preview-bypass")
	if got := header.Get("X-Ocel-Preview-Bypass"); got != "" {
		t.Errorf("Get after Del = %q, want empty", got)
	}
}

func TestAHeaderCloneSharesNothingWithItsOriginal(t *testing.T) {
	t.Parallel()

	header := previewgate.Header{"Cookie": {"a=b"}}
	cloned := header.Clone()
	cloned.Set("Cookie", "c=d")
	cloned.Values("Cookie")[0] = "e=f"

	if got := header.Get("Cookie"); got != "a=b" {
		t.Errorf("the original Cookie = %q after changing the clone, want a=b", got)
	}
}

func TestThePreviewGateLinksNoNetworkingPackageSoItFitsAWasmPlugin(t *testing.T) {
	t.Parallel()

	out, err := exec.Command("go", "list", "-deps", ".").Output()
	if err != nil {
		t.Fatal(err)
	}
	for _, dep := range strings.Fields(string(out)) {
		if dep == "net/http" || dep == "net" {
			t.Errorf("the preview gate links %s, which a proxy-wasm plugin cannot afford", dep)
		}
	}
}
