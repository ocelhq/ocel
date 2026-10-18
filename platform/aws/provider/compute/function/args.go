package function

import (
	"github.com/ocelhq/ocel/pkg/arch"
	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
)

func NewArgs(appFramework string, spec provider.FunctionSpec) (Args, error) {
	execution, err := executionFor(spec.Framework)
	if err != nil {
		return Args{}, err
	}
	handler := defaultEntry
	if spec.EntryFile != "" {
		handler = spec.EntryFile
	}
	memoryMB := defaultMemoryMB
	if appFramework == buildoutput.FrameworkNext {
		memoryMB = nextBundleMemoryMB
	}
	if spec.Memory > 0 {
		memoryMB = spec.Memory
	}
	return Args{
		Runtime:        execution.Runtime,
		Handler:        handler,
		Arch:           execution.Arch,
		MemorySizeMB:   memoryMB,
		TimeoutSeconds: defaultTimeoutSeconds,
		InvokeMode:     invokeModeStream,
	}, nil
}

type execution struct {
	Runtime string
	Arch    string
}

func executionFor(framework buildoutput.Framework) (execution, error) {
	if framework.Name != "" && !buildoutput.IsKnownFramework(framework.Name) {
		return execution{}, refusal.Refuse(refusal.CodeInvalid, "this provider has no framework named %q", framework.Name)
	}
	architecture := arch.Architecture(framework.Arch)
	if architecture != arch.X8664 && architecture != arch.ARM64 {
		return execution{}, refusal.Refuse(refusal.CodeInvalid,
			"this provider runs functions on %s and %s, and %q asks for %s",
			arch.X8664, arch.ARM64, framework.Name, framework.Arch)
	}
	return execution{Runtime: managedRuntime(framework.Name), Arch: architecture}, nil
}

func managedRuntime(name string) string {
	switch name {
	case buildoutput.FrameworkPython:
		return pythonRuntime
	case "":
		return defaultRuntime
	}
	if buildoutput.RunsOnNode(name) {
		return defaultRuntime
	}
	return providedRuntime
}
