package image

import (
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/moby/buildkit/solver/pb"
)

var publicValues = map[string]string{"NEXT_PUBLIC_GREETING": "journey-hello"}

func TestADockerfileBuildReadsThePublicValuesAsBuildArgsAndNoLiveValue(t *testing.T) {
	recipe, err := ChooseRecipe(App{Name: "web", Workspace: at(t, "testdata/dockerfileapp"), BuildArgs: publicValues})
	if err != nil {
		t.Fatalf("ChooseRecipe() = %v", err)
	}
	live := NewLiveValues(map[string]string{"STRIPE_API_KEY": "sk-live"}, map[string]string{"OCEL_SESSION_TOKEN": "token-1"}, machineKey)
	opt, done, err := recipe.solve("", live)
	if err != nil {
		t.Fatalf("solve() = %v", err)
	}
	t.Cleanup(done)

	if got := opt.FrontendAttrs[buildArgAttrPrefix+"NEXT_PUBLIC_GREETING"]; got != "journey-hello" {
		t.Errorf("build-arg:NEXT_PUBLIC_GREETING = %q, want journey-hello: a Dockerfile declaring ARG NEXT_PUBLIC_GREETING inlines it as next build does under ocel's adapter", got)
	}
	for key, value := range opt.FrontendAttrs {
		if strings.Contains(value, "sk-live") || strings.Contains(value, "token-1") || strings.HasSuffix(key, "STRIPE_API_KEY") || strings.HasSuffix(key, "OCEL_SESSION_TOKEN") {
			t.Errorf("frontend option %s = %q carries a live value, which a build arg would leave in the image's history", key, value)
		}
	}
}

func TestARailpackBuildWithPublicValuesAloneStillFindsItsBuildStep(t *testing.T) {
	recipe, err := ChooseRecipe(App{Name: "web", Workspace: at(t, "testdata/plainserver"), BuildArgs: publicValues})
	if err != nil {
		t.Fatalf("ChooseRecipe() = %v", err)
	}
	opt, done, err := recipe.solve("", LiveValues{})
	if err != nil {
		t.Fatalf("solve() = %v", err)
	}
	t.Cleanup(done)

	if opt.FrontendAttrs[buildArgAttrPrefix+railpackSecretsHashArg] == "" {
		t.Error("the railpack solve sets no secrets hash, so railpack marks no build step to hand the public values to")
	}
	if _, set := opt.FrontendAttrs[buildArgAttrPrefix+"NEXT_PUBLIC_GREETING"]; set {
		t.Error("the railpack solve passes NEXT_PUBLIC_GREETING as a frontend option, want it in the build step's environment alone")
	}
	if len(opt.Session) != 0 {
		t.Errorf("the solve attaches %d session providers, want none: a public value is no secret", len(opt.Session))
	}
}

func TestOnlyTheBuildStepsCommandsReadThePublicValuesFromTheirEnvironment(t *testing.T) {
	original, plan := defineRailpackBuild(t, "a public hash")

	rewritten, err := giveBuildStepValues(original, buildStepValues{env: publicValues})
	if err != nil {
		t.Fatalf("giveBuildStepValues() = %v", err)
	}

	built := 0
	for _, operation := range decodeOperations(t, rewritten) {
		exec := operation.GetExec()
		if exec == nil {
			continue
		}
		reads := slices.Contains(exec.Meta.Env, "NEXT_PUBLIC_GREETING=journey-hello")
		if !isFromBuildStep(plan, exec) {
			if reads || exec.Network == pb.NetMode_HOST {
				t.Errorf("%q, outside the build step, reads the public value or the host network", joinArgs(exec))
			}
			continue
		}
		built++
		if !reads {
			t.Errorf("the build command %q runs with %v, want NEXT_PUBLIC_GREETING among it", joinArgs(exec), slices.Collect(maps.Keys(publicValues)))
		}
	}
	if built == 0 {
		t.Error("no command of the build step was given the public values")
	}
}
