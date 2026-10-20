package function

import (
	"errors"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/arch"
	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/containerimage"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/aws/provider/component"
	"github.com/ocelhq/ocel/platform/aws/provider/component/componenttest"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

func TestTranslateFunctionSpec(t *testing.T) {
	t.Run("passes runtime and entrypoint", func(t *testing.T) {
		t.Parallel()
		got, err := NewArgs("", provider.FunctionSpec{
			Framework: buildoutput.Framework{Name: buildoutput.FrameworkNode},
			EntryFile: "src/server.js",
		})
		if err != nil {
			t.Fatalf("translateFunctionSpec: %v", err)
		}
		if got.Runtime != "nodejs24.x" {
			t.Errorf("Runtime = %q, want nodejs24.x", got.Runtime)
		}
		if got.Handler != "src/server.js" {
			t.Errorf("Handler = %q, want src/server.js", got.Handler)
		}
	})

	t.Run("empty falls back to pinned defaults", func(t *testing.T) {
		t.Parallel()
		got, err := NewArgs("", provider.FunctionSpec{})
		if err != nil {
			t.Fatalf("translateFunctionSpec: %v", err)
		}
		if got.Runtime != defaultRuntime {
			t.Errorf("Runtime = %q, want default %q", got.Runtime, defaultRuntime)
		}
		if got.Handler != defaultEntry {
			t.Errorf("Handler = %q, want default %q", got.Handler, defaultEntry)
		}
	})

	t.Run("defaults size the function for SSR", func(t *testing.T) {
		t.Parallel()
		got, err := NewArgs("", provider.FunctionSpec{})
		if err != nil {
			t.Fatalf("translateFunctionSpec: %v", err)
		}
		if got.MemorySizeMB != defaultMemoryMB {
			t.Errorf("MemorySizeMB = %d, want default %d", got.MemorySizeMB, defaultMemoryMB)
		}
		if got.TimeoutSeconds != defaultTimeoutSeconds {
			t.Errorf("TimeoutSeconds = %d, want default %d", got.TimeoutSeconds, defaultTimeoutSeconds)
		}
	})

	t.Run("an app still answering a request keeps answering for a minute", func(t *testing.T) {
		t.Parallel()
		got, err := NewArgs("", provider.FunctionSpec{})
		if err != nil {
			t.Fatalf("translateFunctionSpec: %v", err)
		}
		if got.TimeoutSeconds != 60 {
			t.Errorf("TimeoutSeconds = %d, want 60, the minute every AWS edge waits on the function", got.TimeoutSeconds)
		}
	})

	t.Run("Next gets the bundle memory default", func(t *testing.T) {
		t.Parallel()
		got, err := NewArgs(buildoutput.FrameworkNext, provider.FunctionSpec{})
		if err != nil {
			t.Fatalf("translateFunctionSpec: %v", err)
		}
		if got.MemorySizeMB != nextBundleMemoryMB {
			t.Errorf("MemorySizeMB = %d, want the Next default %d", got.MemorySizeMB, nextBundleMemoryMB)
		}
	})

	t.Run("non-Next keeps the flat default", func(t *testing.T) {
		t.Parallel()
		got, err := NewArgs(buildoutput.FrameworkNode, provider.FunctionSpec{})
		if err != nil {
			t.Fatalf("translateFunctionSpec: %v", err)
		}
		if got.MemorySizeMB != defaultMemoryMB {
			t.Errorf("MemorySizeMB = %d, want default %d", got.MemorySizeMB, defaultMemoryMB)
		}
	})

	t.Run("the memory the spec asks for wins over both defaults", func(t *testing.T) {
		t.Parallel()
		got, err := NewArgs(buildoutput.FrameworkNext, provider.FunctionSpec{Memory: 3008})
		if err != nil {
			t.Fatalf("translateFunctionSpec: %v", err)
		}
		if got.MemorySizeMB != 3008 {
			t.Errorf("MemorySizeMB = %d, want the spec's own 3008", got.MemorySizeMB)
		}
	})
}

func TestExecutionFor(t *testing.T) {
	t.Parallel()

	t.Run("every runtime lands on a managed runtime its language runs on, on the architecture it named", func(t *testing.T) {
		t.Parallel()
		for _, name := range append([]string{""}, buildoutput.Frameworks()...) {
			for declared, want := range map[string]string{
				"":         arch.X8664,
				arch.X8664: arch.X8664,
				arch.ARM64: arch.ARM64,
			} {
				got, err := executionFor(buildoutput.Framework{Name: name, Arch: declared})
				if err != nil {
					t.Fatalf("executionFor(%q, %q): %v", name, declared, err)
				}
				if got != (execution{Runtime: managedRuntime(name), Arch: want}) {
					t.Errorf("executionFor(%q, %q) = %+v, want %q on %s", name, declared, got, managedRuntime(name), want)
				}
			}
		}
	})

	t.Run("an architecture nothing runs on is refused by name", func(t *testing.T) {
		t.Parallel()
		_, err := executionFor(buildoutput.Framework{Name: buildoutput.FrameworkGo, Arch: "riscv"})
		var refused refusal.Refusal
		if !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid {
			t.Fatalf("executionFor(riscv) = %v, want a %s refusal", err, refusal.CodeInvalid)
		}
		if !strings.Contains(refused.Error(), "riscv") {
			t.Errorf("refusal %q does not name riscv", refused.Error())
		}
	})

	t.Run("a runtime this provider does not have is refused", func(t *testing.T) {
		t.Parallel()
		_, err := executionFor(buildoutput.Framework{Name: "deno"})
		var refused refusal.Refusal
		if !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid {
			t.Fatalf("executionFor(deno) = %v, want a %s refusal", err, refusal.CodeInvalid)
		}
	})
}

func TestAManagedRuntimeFunctionKeepsItsOwnEntryAsTheHandler(t *testing.T) {
	t.Parallel()

	args, err := NewArgs(buildoutput.FrameworkNode, provider.FunctionSpec{
		Framework: buildoutput.Framework{Name: buildoutput.FrameworkNode},
		EntryFile: "src/server.js",
	})
	if err != nil {
		t.Fatalf("translateFunctionSpec: %v", err)
	}
	if got := args.LambdaHandler(); got != "src/server.js" {
		t.Errorf("handler = %q, want the app's own entry", got)
	}
	if env := Env(nil, args, nil, nil); env["OCEL_HANDLER"] != "/var/task/src/server.js" {
		t.Errorf("OCEL_HANDLER = %q, want the app's own entry", env["OCEL_HANDLER"])
	}
}

func TestACommandFunctionKeepsItsOwnEntryInTheEnvironment(t *testing.T) {
	t.Parallel()

	args, err := NewArgs(buildoutput.FrameworkGo, provider.FunctionSpec{
		Framework: buildoutput.Framework{Name: buildoutput.FrameworkGo},
		EntryFile: "web",
	})
	if err != nil {
		t.Fatalf("translateFunctionSpec: %v", err)
	}
	if got := args.LambdaHandler(); got != providedHandler {
		t.Errorf("handler = %q, want %q", got, providedHandler)
	}
	if env := Env(nil, args, nil, nil); env["OCEL_HANDLER"] != "/var/task/web" {
		t.Errorf("OCEL_HANDLER = %q, want the app's own entry rather than the runtime's", env["OCEL_HANDLER"])
	}
}

func TestARustFunctionBootsItsOwnBinaryOnTheProvidedRuntime(t *testing.T) {
	t.Parallel()

	args, err := NewArgs(buildoutput.FrameworkRust, provider.FunctionSpec{
		Framework: buildoutput.Framework{Name: buildoutput.FrameworkRust, Arch: arch.ARM64},
		EntryFile: "web",
	})
	if err != nil {
		t.Fatalf("translateFunctionSpec: %v", err)
	}
	if args.Runtime != "provided.al2023" {
		t.Errorf("runtime = %q, want provided.al2023: a rust binary needs no managed language runtime", args.Runtime)
	}
	if args.Arch != arch.ARM64 {
		t.Errorf("arch = %q, want %q", args.Arch, arch.ARM64)
	}
	if got := args.LambdaHandler(); got != "bootstrap" {
		t.Errorf("handler = %q, want bootstrap", got)
	}
	if env := Env(nil, args, nil, nil); env["OCEL_HANDLER"] != "/var/task/web" {
		t.Errorf("OCEL_HANDLER = %q, want the app's own binary", env["OCEL_HANDLER"])
	}
}

func TestEveryFunctionBootsTheRuntimeWhateverRuntimeItServes(t *testing.T) {
	t.Parallel()

	for _, name := range buildoutput.Frameworks() {
		args, err := NewArgs(name, provider.FunctionSpec{
			Framework: buildoutput.Framework{Name: name},
			EntryFile: "web",
		})
		if err != nil {
			t.Fatalf("translateFunctionSpec(%q): %v", name, err)
		}
		env := Env(nil, args, nil, nil)
		wrapper, wrapped := env["AWS_LAMBDA_EXEC_WRAPPER"]
		if args.Runtime == providedRuntime {
			if wrapped {
				t.Errorf("%q is handed %s = %q; on %s Lambda runs the layer's own bootstrap and there is nothing to wrap", name, "AWS_LAMBDA_EXEC_WRAPPER", wrapper, providedRuntime)
			}
		} else if wrapper != execWrapper {
			t.Errorf("%q boots through %q, want the runtime at %q", name, wrapper, execWrapper)
		}
		if want := "/var/task/web"; env["OCEL_HANDLER"] != want {
			t.Errorf("%q hands the runtime %q, want %q", name, env["OCEL_HANDLER"], want)
		}
		if _, injected := env[containerimage.PortEnvVar]; injected {
			t.Errorf("%q is handed %s by the provider; the runtime tells its child which port to bind", name, containerimage.PortEnvVar)
		}
	}
}

func TestAnAppThatShipsItsOwnBinaryRunsOnTheProvidedRuntime(t *testing.T) {
	t.Parallel()

	for name, want := range map[string]string{
		buildoutput.FrameworkGo:     providedRuntime,
		"rust":                      providedRuntime,
		buildoutput.FrameworkNode:   defaultRuntime,
		buildoutput.FrameworkNext:   defaultRuntime,
		"":                          defaultRuntime,
		buildoutput.FrameworkPython: pythonRuntime,
	} {
		if got := managedRuntime(name); got != want {
			t.Errorf("a %q function runs on %q, want %q — a language this provider ships no interpreter for runs the binary the app ships", name, got, want)
		}
	}
}

func TestAFunctionRunsOnTheManagedRuntimeItsLanguageNeedsAnInterpreterFrom(t *testing.T) {
	t.Parallel()

	for name, want := range map[string]string{
		buildoutput.FrameworkNode:   defaultRuntime,
		buildoutput.FrameworkNext:   defaultRuntime,
		buildoutput.FrameworkGo:     providedRuntime,
		buildoutput.FrameworkPython: pythonRuntime,
	} {
		args, err := NewArgs(name, provider.FunctionSpec{Framework: buildoutput.Framework{Name: name}})
		if err != nil {
			t.Fatalf("translateFunctionSpec(%q): %v", name, err)
		}
		if args.Runtime != want {
			t.Errorf("a %q function runs on %q, want %q — the runtime execs the app's command, and the sandbox has to contain what that command is", name, args.Runtime, want)
		}
	}
}

func TestAFunctionIsToldTheFileItBootsFrom(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		runtime string
		handler string
		want    string
	}{
		{"an exec artifact takes the bootstrap the layer ships", buildoutput.FrameworkGo, "web", providedHandler},
		{"a bundled node artifact takes its bundle", buildoutput.FrameworkNode, "index.mjs", "index.mjs"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			rec := &inputRecorder{}
			stack := testStack(t, "prod", "api")
			program := func(pctx *pulumi.Context) error {
				role, _, err := NewRole(pctx, component.AppRoleCoordinate("shop", stack), ExecutionRole{Namespace: testNamespace, Boundary: componenttest.BoundaryARN})
				if err != nil {
					return err
				}
				args, err := NewArgs(tc.runtime, provider.FunctionSpec{
					Framework: buildoutput.Framework{Name: tc.runtime},
					EntryFile: tc.handler,
				})
				if err != nil {
					return err
				}
				return AppStack{
					Namespace: testNamespace,
					Project:   "shop",
					Stack:     stack,
					Functions: []AppFunction{{Logical: "fn--api--web", RouteID: "/"}},
					Args:      func(AppFunction) Args { return args },
					Layers: map[string]string{
						arch.X8664: testRuntimeLayerARN(arch.X8664),
					},
					Artifacts: map[string]Artifact{"fn--api--web": {Bucket: "artifacts", Key: "fn.zip"}},
					RoleArn:   role.Arn,
				}.Declare(pctx)
			}
			if err := pulumi.RunErr(program, pulumi.WithMocks("shop", "prod--api", rec)); err != nil {
				t.Fatalf("run program: %v", err)
			}

			inputs := rec.inputs("aws:lambda/function:Function", namedFunction(stack, "fn--api--web").Pulumi)
			got, ok := inputs[resource.PropertyKey("handler")]
			if !ok || !got.IsString() {
				t.Fatalf("handler = %v, want a string", got)
			}
			if got.StringValue() != tc.want {
				t.Errorf("handler = %q, want %q: Lambda refuses a package whose handler names no file it can boot", got.StringValue(), tc.want)
			}
		})
	}
}

func argsFor(functions []*contractv1.ManifestFunction) func(AppFunction) Args {
	args := make(map[string]Args, len(functions))
	for _, fn := range functions {
		translated, err := NewArgs(fn.GetFramework().GetName(), provider.FunctionSpec{
			Name:      fn.GetLogicalName(),
			Framework: buildoutput.Framework{Name: fn.GetFramework().GetName(), Arch: fn.GetFramework().GetArch()},
			EntryFile: fn.GetEntryFile(),
		})
		if err != nil {
			panic(err)
		}
		args[fn.GetLogicalName()] = translated
	}
	return func(fn AppFunction) Args { return args[fn.Logical] }
}

func TestFunctionDefaults(t *testing.T) {
	t.Run("clear AWS's implicit ceilings", func(t *testing.T) {
		const (
			awsDefaultTimeoutSeconds = 3
			awsDefaultMemoryMB       = 128
		)
		if defaultTimeoutSeconds <= awsDefaultTimeoutSeconds {
			t.Errorf("defaultFunctionTimeoutSeconds = %d, must exceed AWS's implicit %ds",
				defaultTimeoutSeconds, awsDefaultTimeoutSeconds)
		}
		if defaultMemoryMB <= awsDefaultMemoryMB {
			t.Errorf("defaultFunctionMemoryMB = %d, must exceed AWS's implicit %dMB",
				defaultMemoryMB, awsDefaultMemoryMB)
		}
	})
}
