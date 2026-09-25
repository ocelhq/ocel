package env

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/cli/clitest"
)

func TestSettingAValueForAnAppOnALiveComputePromisesNoDeploy(t *testing.T) {
	root := setUpEnvFixture(t)
	t.Setenv(clitest.FakeComputesEnvVar, "serverless")

	said := envSet(t, root, "API_TOKEN", "sk-live", envOptions{})
	if strings.Contains(said, "next deploy") {
		t.Errorf("`ocel env set` against an app on a compute the provider bakes nothing into said\n%s\nand a live value there is picked up without one", said)
	}
}

func TestSettingAValueForAContainerAppTheProviderReadsLivePromisesNoDeploy(t *testing.T) {
	root := setUpEnvFixture(t)
	t.Setenv(clitest.FakeComputesEnvVar, "container")

	said := envSet(t, root, "API_TOKEN", "sk-live", envOptions{})
	if strings.Contains(said, "next deploy") {
		t.Errorf("`ocel env set` against a container app said\n%s\nand this provider names no compute it bakes values into, so the running container reads this one without a deploy", said)
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

func TestRemovingAValueForAnAppOnALiveComputePromisesNoDeploy(t *testing.T) {
	root := setUpEnvFixture(t)
	t.Setenv(clitest.FakeComputesEnvVar, "serverless")
	envSet(t, root, "API_TOKEN", "sk-live", envOptions{})

	said := envRm(t, root, "API_TOKEN", envOptions{})
	if strings.Contains(said, "next deploy") {
		t.Errorf("`ocel env rm` against an app on a compute the provider bakes nothing into said\n%s\nand a removed value stops being read there without one", said)
	}
}

func TestRemovingAValueForAContainerAppTheProviderReadsLivePromisesNoDeploy(t *testing.T) {
	root := setUpEnvFixture(t)
	t.Setenv(clitest.FakeComputesEnvVar, "container")
	envSet(t, root, "API_TOKEN", "sk-live", envOptions{})

	said := envRm(t, root, "API_TOKEN", envOptions{})
	if strings.Contains(said, "next deploy") {
		t.Errorf("`ocel env rm` against a container app said\n%s\nand this provider names no compute it bakes values into, so the running container stops reading this one without a deploy", said)
	}
}
