package readiness

import (
	"reflect"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/project"
)

func TestAPreflightRecordingWantsNamesThePartTheCommandWantsBeyondItsBuild(t *testing.T) {
	t.Parallel()

	sent := newPreflightRequest(&project.Project{}, Request{RecordWants: true, Feature: "variables-key"})

	if !sent.GetRecordWants() || !reflect.DeepEqual(sent.GetWantedParts(), []string{"variables-key"}) {
		t.Errorf("preflight = record_wants %v, wanted_parts %v, want the key recorded as wanted", sent.GetRecordWants(), sent.GetWantedParts())
	}
	if dry := newPreflightRequest(&project.Project{}, Request{RecordWants: true, DryRun: true}); dry.GetRecordWants() {
		t.Errorf("preflight = record_wants %v, want a dry run to record nothing", dry.GetRecordWants())
	}
	if plain := newPreflightRequest(&project.Project{}, Request{}); plain.GetRecordWants() || len(plain.GetWantedParts()) != 0 {
		t.Errorf("preflight = record_wants %v, wanted_parts %v, want a command that wants nothing beyond its build to name nothing", plain.GetRecordWants(), plain.GetWantedParts())
	}
}

func TestOnlyContainerAppsAreNamedEachWithTheArchitectureItDeclares(t *testing.T) {
	t.Parallel()

	cfg := &project.Project{Apps: []project.App{
		{Name: "web", Compute: "container", Arch: "arm64"},
		{Name: "worker", Compute: "container"},
		{Name: "api", Compute: "serverless", Serverless: &project.Serverless{Framework: "node"}, Arch: "arm64"},
		{Name: "site"},
	}}

	named := containers(cfg)
	if len(named) != 2 {
		t.Fatalf("containers() = %v, want web and worker alone: nothing else has an image to build", named)
	}
	if named[0].GetApp() != "web" || named[0].GetArch() != "arm64" {
		t.Errorf("containers()[0] = %v, want web declaring arm64", named[0])
	}
	if named[1].GetApp() != "worker" || named[1].GetArch() != "" {
		t.Errorf("containers()[1] = %v, want worker declaring nothing, so the provider names what it runs on", named[1])
	}
}
