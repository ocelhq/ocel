package env

import (
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/cli/clitest"
)

func TestSettingAValueForAContainerAppSaysWhenItLands(t *testing.T) {
	root := setUpEnvFixture(t)
	t.Setenv(clitest.FakeComputesEnvVar, "container")

	said := envSet(t, root, "API_TOKEN", "sk-live", envOptions{})
	for _, want := range []string{"next deploy", "ocel deploy"} {
		if !strings.Contains(said, want) {
			t.Errorf("`ocel env set` against a container app said\n%s\nwhich never says %q: a container carries nothing of ocel's to refresh a value, so the value it holds is the one its last deploy handed it", said, want)
		}
	}
}

func TestSettingAValueForAServerlessAppPromisesNoDeploy(t *testing.T) {
	root := setUpEnvFixture(t)
	t.Setenv(clitest.FakeComputesEnvVar, "serverless")

	said := envSet(t, root, "API_TOKEN", "sk-live", envOptions{})
	if strings.Contains(said, "next deploy") {
		t.Errorf("`ocel env set` against a serverless app said\n%s\nand a live value there is picked up without one", said)
	}
}
