package deploy

import (
	"context"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/clitest"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/variablestore"
)

func storedClass(t *testing.T, fixture clitest.FakeProject, key string) variablestore.Class {
	t.Helper()
	scope := variablestore.Scope{Project: clitest.FixtureSlug, Tier: environment.TierProduction}
	value, err := valueStore(fixture).Get(context.Background(), scope, variablestore.Coordinate{Cell: variablestore.Cell{Key: key}}, false)
	if err != nil {
		t.Fatal(err)
	}
	return value.Class
}

func TestADeployRecordsTheClassItsAppsDeclareForEachValueTheyRead(t *testing.T) {
	fixture := setUpVariablesProject(t, `[{"key":"LIVE_KEY","class":"VARIABLE_CLASS_SECRET","required":true},{"key":"BAKED_KEY","class":"VARIABLE_CLASS_PLAIN","required":true}]`)
	envSet(t, fixture, "LIVE_KEY", "sk_live_value", envOptions{})
	envSet(t, fixture, "BAKED_KEY", "baked_value", envOptions{})
	writeRootApp(t, fixture.Root)
	dependencies := newTestDependencies()
	stubBuild(&dependencies, nil)

	if out, err := deployWith(t, dependencies, fixture, deployOptions{yes: true}); err != nil {
		t.Fatalf("runDeploy err = %v; out=%s", err, out)
	}

	for key, want := range map[string]variablestore.Class{"LIVE_KEY": variablestore.ClassSecret, "BAKED_KEY": variablestore.ClassPlain} {
		if got := storedClass(t, fixture, key); got != want {
			t.Errorf("%s reads as %s after the deploy, want %s as its app declares it", key, got, want)
		}
	}
}

func TestADryRunRecordsNoClass(t *testing.T) {
	fixture := setUpVariablesProject(t, `[{"key":"LIVE_KEY","class":"VARIABLE_CLASS_SECRET","required":true}]`)
	envSet(t, fixture, "LIVE_KEY", "sk_live_value", envOptions{})
	writeRootApp(t, fixture.Root)
	dependencies := newTestDependencies()
	stubBuild(&dependencies, nil)

	if out, err := deployWith(t, dependencies, fixture, deployOptions{yes: true, dry: true}); err != nil {
		t.Fatalf("runDeploy --dry-run err = %v; out=%s", err, out)
	}
	if got := storedClass(t, fixture, "LIVE_KEY"); got != variablestore.ClassSensitive {
		t.Errorf("LIVE_KEY reads as %s after a dry run, want it left as it was written", got)
	}
}
