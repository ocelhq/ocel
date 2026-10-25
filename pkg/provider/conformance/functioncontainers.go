package conformance

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"

	"github.com/ocelhq/ocel/pkg/arch"
	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/images"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/functioncode"
)

func RunFunctionContainers(t *testing.T, facts provider.Facts, runtime provider.Runtime) {
	t.Helper()

	if !slices.Contains(facts.Computes, provider.ComputeContainer) {
		t.Skip("this provider runs no container, so no container is built from a function")
	}
	for _, fault := range findFunctionContainerFaults(context.Background(), runtime, stagedNextFunction(t)) {
		t.Error(fault)
	}
}

func findFunctionContainerFaults(ctx context.Context, runtime provider.Runtime, staged string) []string {
	var found []string
	built := 0
	for _, declared := range []string{arch.X8664, arch.ARM64} {
		goarch, _ := arch.GoArch(declared)
		if binary, err := runtime.Binary(ctx, goarch); err != nil || len(binary) == 0 {
			continue
		}
		built++
		framework := buildoutput.Framework{Name: buildoutput.FrameworkNext, Arch: declared}
		files, err := runtime.FunctionFiles(ctx, framework)
		if err != nil {
			found = append(found, fmt.Sprintf("FunctionFiles(%s on %s) error = %v, want the runtime a container built from a Next function boots through", framework.Name, declared, err))
			continue
		}
		if _, ok := files[images.RuntimeEntrypointFile]; !ok {
			found = append(found, fmt.Sprintf("FunctionFiles(%s on %s) has no %s, so a container built from a Next function boots node over a file that is not there", framework.Name, declared, images.RuntimeEntrypointFile))
			continue
		}
		fn := provider.FunctionSpec{Name: "server", Framework: framework, ArtifactDir: staged}
		if _, err := functioncode.ContainerImage(ctx, "shop", "web", provider.RegistryTarget{}, fn, functioncode.ImageSource{
			Base:    func(context.Context, buildoutput.Framework) (v1.Image, error) { return empty.Image, nil },
			Runtime: runtime.FunctionFiles,
			Binary:  runtime.Binary,
		}); err != nil {
			found = append(found, fmt.Sprintf("a container built from a Next function on %s is refused: %v", declared, err))
		}
	}
	if built == 0 {
		found = append(found, "Runtime().Binary ships a container runtime for no architecture, so no container built from a function boots")
	}
	return found
}

func stagedNextFunction(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	staged, err := json.Marshal(buildoutput.FunctionConfig{EntryFile: "index.mjs"})
	if err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string][]byte{"index.mjs": []byte("export {}"), buildoutput.FunctionConfigFile: staged} {
		if err := os.WriteFile(filepath.Join(dir, name), body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}
