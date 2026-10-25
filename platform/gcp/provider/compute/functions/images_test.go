package functions

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"

	"github.com/ocelhq/ocel/pkg/arch"
	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/run"
)

func basedOn(t *testing.T, config v1.Config) (Images, *string) {
	t.Helper()
	image, err := mutate.Config(empty.Image, config)
	if err != nil {
		t.Fatal(err)
	}
	var asked string
	pulled := &run.Images{Pull: func(_ context.Context, ref string) (v1.Image, error) {
		asked = ref
		return image, nil
	}}
	p := NewImages(pulled.Based)
	return p, &asked
}

func TestTheBaseAFunctionRunsOnIsPinnedByDigestPerRuntime(t *testing.T) {
	for _, runtime := range []string{
		buildoutput.FrameworkNode,
		buildoutput.FrameworkGo,
		buildoutput.FrameworkPython,
		buildoutput.FrameworkRust,
	} {
		t.Run(runtime, func(t *testing.T) {
			p, asked := basedOn(t, v1.Config{})

			if _, err := p.ResolveBase(context.Background(), buildoutput.Framework{Name: runtime}); err != nil {
				t.Fatalf("FunctionBase(%s) = %v", runtime, err)
			}
			if !strings.HasPrefix(*asked, "gcr.io/distroless/") {
				t.Errorf("FunctionBase(%s) read %q, want a base from Google's own registry", runtime, *asked)
			}
			if !strings.Contains(*asked, "@sha256:") {
				t.Errorf("FunctionBase(%s) read %q, want a base pinned by digest", runtime, *asked)
			}
		})
	}
}

func TestThePythonBaseRunsTheVersionTheWheelsAreVendoredFor(t *testing.T) {
	p, asked := basedOn(t, v1.Config{})

	if _, err := p.ResolveBase(context.Background(), buildoutput.Framework{Name: buildoutput.FrameworkPython}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(*asked, "debian13") {
		t.Errorf("a python function is built on %q, and only debian 13 ships python %s, which the cli vendors wheels for",
			*asked, arch.PythonVersion)
	}
}

func TestABaseDropsTheEntrypointTheFunctionsCommandWouldBeAppendedTo(t *testing.T) {
	p, _ := basedOn(t, v1.Config{Entrypoint: []string{"/nodejs/bin/node"}})

	base, err := p.ResolveBase(context.Background(), buildoutput.Framework{Name: buildoutput.FrameworkNode})
	if err != nil {
		t.Fatal(err)
	}
	file, err := base.ConfigFile()
	if err != nil {
		t.Fatal(err)
	}
	if got := file.Config.Entrypoint; len(got) != 0 {
		t.Errorf("the base keeps the entrypoint %v, and the image's command is the whole command: what it names would run as an argument to it", got)
	}
}

func TestANodeFunctionFindsNodeOnTheBasesPath(t *testing.T) {
	p, _ := basedOn(t, v1.Config{Env: []string{"PATH=/usr/bin:/bin"}})

	base, err := p.ResolveBase(context.Background(), buildoutput.Framework{Name: buildoutput.FrameworkNode})
	if err != nil {
		t.Fatal(err)
	}
	file, err := base.ConfigFile()
	if err != nil {
		t.Fatal(err)
	}
	path := ""
	for _, entry := range file.Config.Env {
		if name, value, _ := strings.Cut(entry, "="); name == "PATH" {
			path = value
		}
	}
	if !slices.Contains(strings.Split(path, ":"), nodeBinDir) {
		t.Errorf("the base a node function runs on has PATH=%q, and its command is `node`, which is only at %s", path, nodeBinDir)
	}
	if !strings.Contains(path, "/usr/bin") {
		t.Errorf("PATH=%q, want what the base already named kept", path)
	}
}

func TestARuntimeNoBaseIsShippedForIsRefused(t *testing.T) {
	p, _ := basedOn(t, v1.Config{})

	_, err := p.ResolveBase(context.Background(), buildoutput.Framework{Name: "deno"})
	if err == nil {
		t.Fatal("FunctionBase(deno) built an image on a base nothing names")
	}
	if code, refused := provider.RefusedCode(err); !refused || code != refusal.CodeInvalid {
		t.Errorf("FunctionBase(deno) code = %v, want %v", code, refusal.CodeInvalid)
	}
	if !strings.Contains(err.Error(), "deno") {
		t.Errorf("FunctionBase(deno) = %v, want the runtime named", err)
	}
}

func TestANextFunctionRunsOnThePlainNodeBase(t *testing.T) {
	p, asked := basedOn(t, v1.Config{Env: []string{"PATH=/usr/bin"}})

	base, err := p.ResolveBase(context.Background(), buildoutput.Framework{Name: buildoutput.FrameworkNext})
	if err != nil {
		t.Fatalf("FunctionBase(next) = %v", err)
	}
	if *asked != nodeImage {
		t.Errorf("FunctionBase(next) read %q, want the node base %q: a Next function runs on node", *asked, nodeImage)
	}
	if files := filesIn(t, base); len(files) != 0 {
		t.Errorf("the Next base holds %v, want the node base alone: the Next runtime goes in a layer every function shares", files)
	}
	file, err := base.ConfigFile()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(file.Config.Env, func(entry string) bool { return strings.HasPrefix(entry, "PATH="+nodeBinDir) }) {
		t.Errorf("the Next base has env %v, and its command is `node`, which is only at %s", file.Config.Env, nodeBinDir)
	}
}

type unreadStore struct{ provider.ImageStore }

func containerSpec(t *testing.T, functions ...provider.FunctionSpec) provider.StackSpec {
	t.Helper()
	return provider.StackSpec{
		Ref:    provider.StackRef{Project: "shop"},
		Images: provider.ImagePushes{Store: unreadStore{}},
		App: &provider.AppSpec{
			App:       "web",
			Framework: buildoutput.FrameworkNext,
			Compute:   provider.ComputeContainer,
			Functions: functions,
		},
	}
}

func builtFunction(t *testing.T) provider.FunctionSpec {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.mjs"), []byte("export {}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, buildoutput.FunctionConfigFile), []byte(`{"entryFile":"index.mjs"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	return provider.FunctionSpec{
		Name:        "web",
		Framework:   buildoutput.Framework{Name: buildoutput.FrameworkNext, Arch: string(arch.X8664)},
		ArtifactDir: dir,
	}
}

func TestANextContainerBuiltFromItsFunctionIsHandedThatFunctionsImage(t *testing.T) {
	p, _ := basedOn(t, v1.Config{})

	imaged, err := p.WithImages(context.Background(), containerSpec(t, builtFunction(t)))
	if err != nil {
		t.Fatalf("WithImages() = %v", err)
	}
	if pushes := imaged.Images.Pushes; len(pushes) != 1 || !pushes[0].Function || pushes[0].App != "web" || pushes[0].Built == nil {
		t.Errorf("WithImages() pushes %+v, want the one image built from the container's function", pushes)
	}
}

func TestAContainerRunFromAnImageIsHandedNoFunctionImage(t *testing.T) {
	p, _ := basedOn(t, v1.Config{})
	spec := containerSpec(t)
	spec.App.Image = "europe-west1-docker.pkg.dev/acme/ocel/web@sha256:abc"

	imaged, err := p.WithImages(context.Background(), spec)
	if err != nil {
		t.Fatalf("WithImages() = %v", err)
	}
	if len(imaged.Images.Pushes) != 0 {
		t.Errorf("WithImages() pushes %+v, want none: the container runs the image it names", imaged.Images.Pushes)
	}
}

func filesIn(t *testing.T, image v1.Image) []string {
	t.Helper()
	reader := tar.NewReader(mutate.Extract(image))
	var files []string
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return files
		}
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, header.Name)
	}
}

func TestAFunctionBuiltForArm64IsRefused(t *testing.T) {
	p, _ := basedOn(t, v1.Config{})

	_, err := p.ResolveBase(context.Background(),
		buildoutput.Framework{Name: buildoutput.FrameworkNode, Arch: arch.ARM64})
	if err == nil {
		t.Fatal("FunctionBase() built an arm64 function, and Cloud Run runs x86_64 alone")
	}
	if code, refused := provider.RefusedCode(err); !refused || code != refusal.CodeInvalid {
		t.Errorf("code = %v, want %v", code, refusal.CodeInvalid)
	}
	for _, said := range []string{arch.ARM64, arch.X8664} {
		if !strings.Contains(err.Error(), said) {
			t.Errorf("FunctionBase() = %v, want %q named", err, said)
		}
	}
}

func TestTheRuntimeIsShippedForTheRuntimeThatBootsThroughOne(t *testing.T) {
	p, _ := basedOn(t, v1.Config{})
	ctx := context.Background()

	body, err := p.ReadRuntime(ctx, buildoutput.Framework{Name: buildoutput.FrameworkNode})
	if err != nil {
		t.Fatalf("ReadFunctionRuntime(node) = %v", err)
	}
	if len(body["entrypoint.mjs"]) == 0 || len(body) != 1 {
		t.Fatalf("ReadFunctionRuntime(node) returned %d files, want the entrypoint a node function boots through", len(body))
	}
	compiled, err := p.ReadRuntime(ctx, buildoutput.Framework{Name: buildoutput.FrameworkGo})
	if err != nil {
		t.Fatalf("ReadFunctionRuntime(go) = %v", err)
	}
	if len(compiled) != 0 {
		t.Error("ReadFunctionRuntime(go) returned a node runtime for an image that runs a compiled binary")
	}
	next, err := p.ReadRuntime(ctx, buildoutput.Framework{Name: buildoutput.FrameworkNext})
	if err != nil {
		t.Fatalf("ReadFunctionRuntime(next) = %v", err)
	}
	if len(next["entrypoint.mjs"]) == 0 || len(next["node_modules/sharp/package.json"]) == 0 {
		t.Errorf("ReadFunctionRuntime(next) returned %d files, want the Next entrypoint and the sharp it loads", len(next))
	}
	if bytes.Equal(next["entrypoint.mjs"], body["entrypoint.mjs"]) {
		t.Error("ReadFunctionRuntime(next) returned the node runtime for an image that boots the Next runtime")
	}
}

func TestOneBaseIsFetchedOnceHoweverManyFunctionsRunOnIt(t *testing.T) {
	var fetches atomic.Int64
	image, err := mutate.Config(empty.Image, v1.Config{})
	if err != nil {
		t.Fatal(err)
	}
	pulled := &run.Images{Pull: func(context.Context, string) (v1.Image, error) {
		fetches.Add(1)
		return image, nil
	}}
	p := NewImages(pulled.Based)

	ctx := context.Background()
	for range 2 {
		if _, err := p.ResolveBase(ctx, buildoutput.Framework{Name: buildoutput.FrameworkNode}); err != nil {
			t.Fatalf("FunctionBase() = %v", err)
		}
	}

	if got := fetches.Load(); got != 1 {
		t.Errorf("the base was fetched %d times for 2 functions, want once: an app with many functions pays a registry round trip for each", got)
	}
}

func TestABaseFetchedForOneRequestStillReadsItsLayersAfterThatRequestEnds(t *testing.T) {
	var fetchedWith context.Context
	image, err := mutate.Config(empty.Image, v1.Config{})
	if err != nil {
		t.Fatal(err)
	}
	pulled := &run.Images{Pull: func(ctx context.Context, _ string) (v1.Image, error) {
		fetchedWith = ctx
		return image, nil
	}}
	p := NewImages(pulled.Based)

	infra, endInfra := context.WithCancel(context.Background())
	if _, err := pulled.Based(infra, run.StaticImage); err != nil {
		t.Fatalf("based() = %v", err)
	}
	endInfra()
	if _, err := p.ResolveBase(context.Background(), buildoutput.Framework{Name: buildoutput.FrameworkGo}); err != nil {
		t.Fatalf("ResolveFunctionBase() = %v", err)
	}

	if err := fetchedWith.Err(); err != nil {
		t.Errorf("the shared base reads its layers with a context that is %v once the request that first pulled it ended, so a later deploy's image load fails", err)
	}
}
