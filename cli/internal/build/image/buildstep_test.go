package image

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/moby/buildkit/solver/pb"
	digest "github.com/opencontainers/go-digest"
	specs "github.com/opencontainers/image-spec/specs-go/v1"
	railpack "github.com/railwayapp/railpack/buildkit"
	railpackplan "github.com/railwayapp/railpack/core/plan"
)

const hyphenatedKey = "OCEL_RESOURCE_POSTGRES_my-db"

func decodeOps(t *testing.T, def *pb.Definition) map[string]*pb.Op {
	t.Helper()
	ops := map[string]*pb.Op{}
	for _, raw := range def.Def {
		var op pb.Op
		if err := op.UnmarshalVT(raw); err != nil {
			t.Fatal(err)
		}
		ops[digest.FromBytes(raw).String()] = &op
	}
	return ops
}

func defineRailpackBuild(t *testing.T, secretsHash string) (*pb.Definition, *railpackplan.BuildPlan) {
	t.Helper()
	return defineRailpackBuildOf(t, "testdata/nextworkspace/apps/web", secretsHash)
}

func defineRailpackBuildOf(t *testing.T, dir, secretsHash string) (*pb.Definition, *railpackplan.BuildPlan) {
	t.Helper()
	raw, err := Plan(at(t, dir))
	if err != nil {
		t.Fatalf("Plan() = %v", err)
	}
	var plan railpackplan.BuildPlan
	if err := json.Unmarshal(raw, &plan); err != nil {
		t.Fatal(err)
	}
	state, _, err := railpack.ConvertPlanToLLB(&plan, railpack.ConvertPlanOptions{
		BuildPlatform: specs.Platform{OS: "linux", Architecture: "amd64"},
		SecretsHash:   secretsHash,
	})
	if err != nil {
		t.Fatalf("ConvertPlanToLLB() = %v", err)
	}
	def, err := state.Marshal(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return def.ToPB(), &plan
}

func listCommands(plan *railpackplan.BuildPlan, step string) []string {
	var commands []string
	for _, s := range plan.Steps {
		if s.Name != step {
			continue
		}
		for _, command := range s.Commands {
			if exec, ok := command.(railpackplan.ExecCommand); ok {
				commands = append(commands, exec.Cmd)
			}
		}
	}
	return commands
}

func joinArgs(exec *pb.ExecOp) string {
	return strings.Join(exec.Meta.Args, " ")
}

func listSecretMounts(exec *pb.ExecOp) map[string]string {
	mounted := map[string]string{}
	for _, mount := range exec.Mounts {
		if mount.MountType == pb.MountType_SECRET {
			mounted[mount.Dest] = mount.SecretOpt.GetID()
		}
	}
	return mounted
}

func isFromBuildStep(plan *railpackplan.BuildPlan, exec *pb.ExecOp) bool {
	return slices.ContainsFunc(listCommands(plan, "build"), func(command string) bool {
		return strings.Contains(joinArgs(exec), strings.TrimSuffix(strings.TrimPrefix(command, "sh -c '"), "'"))
	})
}

func TestOnlyTheBuildStepsCommandsReadTheLiveValuesAsFilesOnTheHostNetwork(t *testing.T) {
	original, plan := defineRailpackBuild(t, "a live hash")
	if len(listCommands(plan, "build")) == 0 {
		t.Fatal("the plan has no build command to give the live values to")
	}

	rewritten, err := mountLiveValues(original, []string{"OCEL_BINDING_DB", hyphenatedKey})
	if err != nil {
		t.Fatalf("mountLiveValues() = %v", err)
	}

	built := 0
	for _, op := range decodeOps(t, rewritten) {
		exec := op.GetExec()
		if exec == nil {
			continue
		}
		if len(exec.Secretenv) > 0 {
			t.Errorf("%q has the secrets %v in its environment", joinArgs(exec), exec.Secretenv)
		}
		if !isFromBuildStep(plan, exec) {
			if exec.Network == pb.NetMode_HOST || len(listSecretMounts(exec)) > 0 || slices.Contains(exec.Meta.Env, "OCEL_LIVE_DIR=/run/ocel-live") {
				t.Errorf("%q, outside the build step, is given the live values or the host network", joinArgs(exec))
			}
			continue
		}
		built++
		if exec.Network != pb.NetMode_HOST {
			t.Errorf("the build command %q runs on network %v, want the host's, where the forwards are", joinArgs(exec), exec.Network)
		}
		if !slices.Contains(exec.Meta.Env, "OCEL_LIVE_DIR=/run/ocel-live") {
			t.Errorf("the build command %q runs without OCEL_LIVE_DIR, so the app cannot find its bindings", joinArgs(exec))
		}
		want := map[string]string{"/run/ocel-live/OCEL_BINDING_DB": "OCEL_BINDING_DB", "/run/ocel-live/" + hyphenatedKey: hyphenatedKey}
		if got := listSecretMounts(exec); len(got) != len(want) || got["/run/ocel-live/OCEL_BINDING_DB"] != "OCEL_BINDING_DB" || got["/run/ocel-live/"+hyphenatedKey] != hyphenatedKey {
			t.Errorf("the build command %q mounts the secrets %v, want each value as a file named after its key: %v", joinArgs(exec), got, want)
		}
	}
	if built == 0 {
		t.Error("no command of the build step was given the live values")
	}
}

func TestEveryCommandRailpackPlannedRunsUnchanged(t *testing.T) {
	original, _ := defineRailpackBuild(t, "a live hash")
	var before []string
	for _, op := range decodeOps(t, original) {
		if exec := op.GetExec(); exec != nil {
			before = append(before, joinArgs(exec))
		}
	}

	rewritten, err := mountLiveValues(original, []string{"OCEL_BINDING_DB"})
	if err != nil {
		t.Fatalf("mountLiveValues() = %v", err)
	}

	var after []string
	for _, op := range decodeOps(t, rewritten) {
		if exec := op.GetExec(); exec != nil {
			after = append(after, joinArgs(exec))
		}
	}
	slices.Sort(before)
	slices.Sort(after)
	if !slices.Equal(before, after) {
		t.Errorf("the commands changed from\n%v\nto\n%v\nand railpack recognises a mise install by its exact command", before, after)
	}
	if !slices.ContainsFunc(after, func(command string) bool { return strings.Contains(command, "mise install") }) {
		t.Errorf("no command installs the toolchain with mise: %v", after)
	}
}

func TestTheRewrittenDefinitionStaysAGraphWhoseOpsPointAtOpsItHolds(t *testing.T) {
	original, _ := defineRailpackBuild(t, "a live hash")

	rewritten, err := mountLiveValues(original, []string{"OCEL_BINDING_DB"})
	if err != nil {
		t.Fatalf("mountLiveValues() = %v", err)
	}

	ops := decodeOps(t, rewritten)
	for held, op := range ops {
		for _, input := range op.Inputs {
			if _, ok := ops[input.Digest]; !ok {
				t.Errorf("the op %s takes an input %s that the definition no longer holds, so the build cannot be solved", held, input.Digest)
			}
		}
	}
	for held := range rewritten.Metadata {
		if _, ok := ops[held]; !ok {
			t.Errorf("the definition keeps metadata for an op %s it no longer holds", held)
		}
	}
	if len(rewritten.Def) != len(original.Def) {
		t.Errorf("the definition holds %d ops, want the %d it held", len(rewritten.Def), len(original.Def))
	}
}

func TestABuildWithNoLiveValuesIsHandedBackUntouched(t *testing.T) {
	original, _ := defineRailpackBuild(t, "")

	rewritten, err := mountLiveValues(original, []string{"OCEL_BINDING_DB"})
	if err != nil {
		t.Fatalf("mountLiveValues() = %v", err)
	}

	if rewritten != original {
		t.Error("a definition railpack gave no secrets hash was rewritten, which would break its cache")
	}
}

func TestASecretTheAppsRailpackFileDeclaresUnderALiveKeyReachesNoCommandsEnvironment(t *testing.T) {
	original, plan := defineRailpackBuildOf(t, "testdata/declaredsecrets", "a live hash")
	if !slices.Contains(plan.Secrets, hyphenatedKey) {
		t.Fatalf("the plan names the secrets %v, want the %s the app's railpack.json declares, or this test proves nothing", plan.Secrets, hyphenatedKey)
	}

	rewritten, err := mountLiveValues(original, []string{hyphenatedKey})
	if err != nil {
		t.Fatalf("mountLiveValues() = %v", err)
	}

	others := 0
	for _, op := range decodeOps(t, rewritten) {
		exec := op.GetExec()
		if exec == nil {
			continue
		}
		for _, env := range exec.Secretenv {
			switch env.ID {
			case hyphenatedKey:
				t.Errorf("%q has the live value %s in its environment, where every step railpack runs can read it", joinArgs(exec), env.ID)
			case "NPM_TOKEN":
				others++
			}
		}
	}
	if others == 0 {
		t.Error("a secret the app declares that is no live value was dropped, so its build no longer asks for it")
	}
}
