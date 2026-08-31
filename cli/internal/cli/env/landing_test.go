package env

import (
	"bytes"
	"context"
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

func envRm(t *testing.T, root, key string, opts envOptions) string {
	t.Helper()
	var stdout, stderr bytes.Buffer
	if err := runEnvRm(context.Background(), clitest.NewDeps(), root, key, opts, &stdout, &stderr); err != nil {
		t.Fatalf("runEnvRm(%s) err = %v; stdout=%s stderr=%s", key, err, stdout.String(), stderr.String())
	}
	return stdout.String()
}

func TestRemovingAValueForAContainerAppSaysWhatGoesOnBeingServed(t *testing.T) {
	root := setUpEnvFixture(t)
	t.Setenv(clitest.FakeComputesEnvVar, "container")
	envSet(t, root, "API_TOKEN", "sk-live", envOptions{})

	said := envRm(t, root, "API_TOKEN", envOptions{})
	if !strings.Contains(said, "Removed") {
		t.Fatalf("`ocel env rm` said\n%s\nand never removed the value, so what it says about the container proves nothing", said)
	}
	for _, want := range []string{"next deploy", "ocel deploy"} {
		if !strings.Contains(said, want) {
			t.Errorf("`ocel env rm` against a container app said\n%s\nwhich never says %q: the container serving now was handed the value at deploy time and goes on serving it until the next one, so a removal that says nothing reads as a revocation that took effect", said, want)
		}
	}
}

func TestRemovingAValueForAServerlessAppPromisesNoDeploy(t *testing.T) {
	root := setUpEnvFixture(t)
	t.Setenv(clitest.FakeComputesEnvVar, "serverless")
	envSet(t, root, "API_TOKEN", "sk-live", envOptions{})

	said := envRm(t, root, "API_TOKEN", envOptions{})
	if strings.Contains(said, "next deploy") {
		t.Errorf("`ocel env rm` against a serverless app said\n%s\nand a removed value stops being read there without one", said)
	}
}
