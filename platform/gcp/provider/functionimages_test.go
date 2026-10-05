package gcp

import (
	"archive/tar"
	"context"
	"errors"
	"io"
	"path"
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
)

func basedOn(t *testing.T, config v1.Config) (*Provider, *string) {
	t.Helper()
	image, err := mutate.Config(empty.Image, config)
	if err != nil {
		t.Fatal(err)
	}
	var asked string
	p := &Provider{bases: functionBases(), pull: func(_ context.Context, ref string) (v1.Image, error) {
		asked = ref
		return image, nil
	}}
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

			if _, err := p.ResolveFunctionBase(context.Background(), buildoutput.Framework{Name: runtime}); err != nil {
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

	if _, err := p.ResolveFunctionBase(context.Background(), buildoutput.Framework{Name: buildoutput.FrameworkPython}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(*asked, "debian13") {
		t.Errorf("a python function is built on %q, and only debian 13 ships python %s, which the cli vendors wheels for",
			*asked, arch.PythonVersion)
	}
}

func TestABaseDropsTheEntrypointTheFunctionsCommandWouldBeAppendedTo(t *testing.T) {
	p, _ := basedOn(t, v1.Config{Entrypoint: []string{"/nodejs/bin/node"}})

	base, err := p.ResolveFunctionBase(context.Background(), buildoutput.Framework{Name: buildoutput.FrameworkNode})
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

	base, err := p.ResolveFunctionBase(context.Background(), buildoutput.Framework{Name: buildoutput.FrameworkNode})
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

	_, err := p.ResolveFunctionBase(context.Background(), buildoutput.Framework{Name: "deno"})
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

func TestANextFunctionRunsOnTheNodeBaseWithTheNextRuntimeInTheDirectoryItsFactsName(t *testing.T) {
	p, asked := basedOn(t, v1.Config{Env: []string{"PATH=/usr/bin"}})

	base, err := p.ResolveFunctionBase(context.Background(), buildoutput.Framework{Name: buildoutput.FrameworkNext})
	if err != nil {
		t.Fatalf("FunctionBase(next) = %v", err)
	}
	if *asked != nodeImage {
		t.Errorf("FunctionBase(next) read %q, want the node base %q: a Next function runs on node", *asked, nodeImage)
	}
	dir := strings.TrimPrefix(p.Facts().NextRuntimeDir, "/")
	files := filesIn(t, base)
	for _, want := range []string{"entrypoint.mjs", "cache-handler.cjs", "use-cache-default.cjs", "use-cache-remote.cjs"} {
		if !slices.Contains(files, dir+"/"+want) {
			t.Errorf("the Next base holds %v, want %s in %s, where the image boots the Next runtime from and the build points Next's cache handlers", files, want, dir)
		}
	}
	file, err := base.ConfigFile()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(file.Config.Env, func(entry string) bool { return strings.HasPrefix(entry, "PATH="+nodeBinDir) }) {
		t.Errorf("the Next base has env %v, and its command is `node`, which is only at %s", file.Config.Env, nodeBinDir)
	}
}

func TestCloudRunNamesAnAbsoluteDirectoryForTheNextRuntime(t *testing.T) {
	if dir := pushing(t, "").Facts().NextRuntimeDir; !path.IsAbs(dir) {
		t.Errorf("Facts().NextRuntimeDir = %q, want the absolute dir the Next base holds the runtime in", dir)
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

	_, err := p.ResolveFunctionBase(context.Background(),
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

	body, err := p.ReadFunctionRuntime(ctx, buildoutput.Framework{Name: buildoutput.FrameworkNode})
	if err != nil {
		t.Fatalf("ReadFunctionRuntime(node) = %v", err)
	}
	if len(body) == 0 {
		t.Fatal("ReadFunctionRuntime(node) returned nothing, and a node function boots through it")
	}
	compiled, err := p.ReadFunctionRuntime(ctx, buildoutput.Framework{Name: buildoutput.FrameworkGo})
	if err != nil {
		t.Fatalf("ReadFunctionRuntime(go) = %v", err)
	}
	if len(compiled) != 0 {
		t.Error("ReadFunctionRuntime(go) returned a node runtime for an image that runs a compiled binary")
	}
	next, err := p.ReadFunctionRuntime(ctx, buildoutput.Framework{Name: buildoutput.FrameworkNext})
	if err != nil {
		t.Fatalf("ReadFunctionRuntime(next) = %v", err)
	}
	if len(next) != 0 {
		t.Error("ReadFunctionRuntime(next) returned the node runtime for an image that boots the Next runtime")
	}
}

func TestOneBaseIsFetchedOnceHoweverManyFunctionsRunOnIt(t *testing.T) {
	var fetches atomic.Int64
	image, err := mutate.Config(empty.Image, v1.Config{})
	if err != nil {
		t.Fatal(err)
	}
	p := &Provider{bases: functionBases(), pull: func(context.Context, string) (v1.Image, error) {
		fetches.Add(1)
		return image, nil
	}}

	ctx := context.Background()
	for range 2 {
		if _, err := p.ResolveFunctionBase(ctx, buildoutput.Framework{Name: buildoutput.FrameworkNode}); err != nil {
			t.Fatalf("FunctionBase() = %v", err)
		}
	}

	if got := fetches.Load(); got != 1 {
		t.Errorf("the base was fetched %d times for 2 functions, want once: an app with many functions pays a registry round trip for each", got)
	}
}
