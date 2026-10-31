package host

import (
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/images"
)

func TestTheNextRuntimeAContainerBootsCarriesSharpBuiltForTheBoxsArchitecture(t *testing.T) {
	for goarch, cpu := range map[string]string{"amd64": "x64", "arm64": "arm64"} {
		files, err := NextContainerRuntime(testAssets, goarch)
		if err != nil {
			t.Fatalf("NextContainerRuntime(testAssets, %s) = %v", goarch, err)
		}
		if _, ok := files[images.RuntimeEntrypointFile]; !ok {
			t.Errorf("NextContainerRuntime(testAssets, %s) holds no %s", goarch, images.RuntimeEntrypointFile)
		}
		if _, ok := files["THIRD_PARTY_NOTICES"]; !ok {
			t.Errorf("NextContainerRuntime(testAssets, %s) holds no THIRD_PARTY_NOTICES for the libvips it ships", goarch)
		}
		own, other := false, false
		for name := range files {
			own = own || strings.HasPrefix(name, "node_modules/@img/sharp-linux-"+cpu+"/")
			other = other || strings.HasPrefix(name, "node_modules/@img/sharp-linux-") && !strings.HasPrefix(name, "node_modules/@img/sharp-linux-"+cpu+"/")
		}
		if !own || other {
			t.Errorf("NextContainerRuntime(testAssets, %s) carries sharp for its own cpu = %v and for another = %v, want only %s's: an image optimizes on the box it runs on", goarch, own, other, cpu)
		}
	}
}

func TestTheNextRuntimeForAnArchitectureNoBoxRunsIsRefused(t *testing.T) {
	if _, err := NextContainerRuntime(testAssets, "riscv64"); err == nil {
		t.Error("NextContainerRuntime(testAssets, riscv64) = nil error, want a refusal")
	}
}
