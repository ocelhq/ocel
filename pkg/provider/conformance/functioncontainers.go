package conformance

import (
	"archive/tar"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/mutate"

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
		push, err := functioncode.ContainerImage(ctx, "shop", "web", "", provider.RegistryTarget{}, fn, functioncode.ImageSource{
			Base:    runtime.FunctionBase,
			Runtime: runtime.FunctionFiles,
			Binary:  runtime.Binary,
		})
		if err != nil {
			found = append(found, fmt.Sprintf("a container built from a Next function on %s is refused: %v", declared, err))
			continue
		}
		if fault := findUnrunnableCommand(push.Built); fault != "" {
			found = append(found, fmt.Sprintf("a container built from a Next function on %s, on the base FunctionBase declares, %s", declared, fault))
		}
	}
	if built == 0 {
		found = append(found, "Runtime().Binary ships a container runtime for no architecture, so no container built from a function boots")
	}
	return found
}

func findUnrunnableCommand(image v1.Image) string {
	file, err := image.ConfigFile()
	if err != nil {
		return fmt.Sprintf("has no readable config: %v", err)
	}
	command := file.Config.Cmd
	if len(command) == 0 {
		return "names no command"
	}
	held, err := listImageFiles(image)
	if err != nil {
		return fmt.Sprintf("has no readable filesystem: %v", err)
	}
	program := command[0]
	if path.IsAbs(program) {
		if !held[strings.TrimPrefix(program, "/")] {
			return fmt.Sprintf("runs %s, which the image does not hold", program)
		}
		return ""
	}
	searchPath := ""
	for _, entry := range file.Config.Env {
		if named, value, _ := strings.Cut(entry, "="); named == "PATH" {
			searchPath = value
		}
	}
	for _, dir := range strings.Split(searchPath, ":") {
		if dir != "" && held[strings.TrimPrefix(path.Join(dir, program), "/")] {
			return ""
		}
	}
	return fmt.Sprintf("runs %s, which no directory on its PATH (%q) holds", program, searchPath)
}

func listImageFiles(image v1.Image) (map[string]bool, error) {
	flattened := mutate.Extract(image)
	defer flattened.Close()
	held := map[string]bool{}
	archive := tar.NewReader(flattened)
	for {
		header, err := archive.Next()
		if errors.Is(err, io.EOF) {
			return held, nil
		}
		if err != nil {
			return nil, err
		}
		if header.Typeflag == tar.TypeReg || header.Typeflag == tar.TypeSymlink {
			held[strings.TrimPrefix(path.Clean(header.Name), "/")] = true
		}
	}
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
