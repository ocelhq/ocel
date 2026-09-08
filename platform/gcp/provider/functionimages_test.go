package gcp

import (
	"context"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"

	"github.com/ocelhq/ocel/pkg/providerkit"
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
		providerkit.RuntimeNode,
		providerkit.RuntimeGo,
		providerkit.RuntimePython,
	} {
		t.Run(runtime, func(t *testing.T) {
			p, asked := basedOn(t, v1.Config{})

			if _, err := p.FunctionBase(context.Background(), providerkit.Runtime{Name: runtime}); err != nil {
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

	if _, err := p.FunctionBase(context.Background(), providerkit.Runtime{Name: providerkit.RuntimePython}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(*asked, "debian13") {
		t.Errorf("a python function is built on %q, and only debian 13 carries python %s, which the cli vendors wheels for",
			*asked, providerkit.PythonVersion)
	}
}

func TestABaseDropsTheEntrypointTheFunctionsCommandWouldBeAppendedTo(t *testing.T) {
	p, _ := basedOn(t, v1.Config{Entrypoint: []string{"/nodejs/bin/node"}})

	base, err := p.FunctionBase(context.Background(), providerkit.Runtime{Name: providerkit.RuntimeNode})
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

	base, err := p.FunctionBase(context.Background(), providerkit.Runtime{Name: providerkit.RuntimeNode})
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

func TestARuntimeNoBaseIsCarriedForIsRefused(t *testing.T) {
	p, _ := basedOn(t, v1.Config{})

	_, err := p.FunctionBase(context.Background(), providerkit.Runtime{Name: "deno"})
	if err == nil {
		t.Fatal("FunctionBase(deno) built an image on a base nothing names")
	}
	if code, refused := providerkit.RefusedCode(err); !refused || code != providerkit.CodeInvalid {
		t.Errorf("FunctionBase(deno) code = %v, want %v", code, providerkit.CodeInvalid)
	}
	if !strings.Contains(err.Error(), "deno") {
		t.Errorf("FunctionBase(deno) = %v, want the runtime named", err)
	}
}

func TestANextFunctionIsRefusedLikeAnyOtherRuntimeNoBaseIsCarriedFor(t *testing.T) {
	p, _ := basedOn(t, v1.Config{})

	_, err := p.FunctionBase(context.Background(), providerkit.Runtime{Name: providerkit.RuntimeNext})
	if err == nil {
		t.Fatal("FunctionBase(next) built an image, and Next on Cloud Run is not something this provider serves")
	}
	if code, refused := providerkit.RefusedCode(err); !refused || code != providerkit.CodeInvalid {
		t.Errorf("FunctionBase(next) code = %v, want %v", code, providerkit.CodeInvalid)
	}
	if !strings.Contains(err.Error(), providerkit.RuntimeNext) {
		t.Errorf("FunctionBase(next) = %v, want the runtime named", err)
	}
}

func TestAFunctionBuiltForArm64IsRefused(t *testing.T) {
	p, _ := basedOn(t, v1.Config{})

	_, err := p.FunctionBase(context.Background(),
		providerkit.Runtime{Name: providerkit.RuntimeNode, Arch: providerkit.ArchARM64})
	if err == nil {
		t.Fatal("FunctionBase() built an arm64 function, and Cloud Run runs x86_64 alone")
	}
	if code, refused := providerkit.RefusedCode(err); !refused || code != providerkit.CodeInvalid {
		t.Errorf("code = %v, want %v", code, providerkit.CodeInvalid)
	}
	for _, said := range []string{providerkit.ArchARM64, providerkit.ArchX8664} {
		if !strings.Contains(err.Error(), said) {
			t.Errorf("FunctionBase() = %v, want %q named", err, said)
		}
	}
}

func TestTheMembraneIsCarriedForTheRuntimeThatBootsThroughOne(t *testing.T) {
	p, _ := basedOn(t, v1.Config{})
	ctx := context.Background()

	body, err := p.FunctionMembrane(ctx, providerkit.Runtime{Name: providerkit.RuntimeNode})
	if err != nil {
		t.Fatalf("FunctionMembrane(node) = %v", err)
	}
	if len(body) == 0 {
		t.Fatal("FunctionMembrane(node) carried nothing, and a node function boots through it")
	}
	carried, err := p.FunctionMembrane(ctx, providerkit.Runtime{Name: providerkit.RuntimeGo})
	if err != nil {
		t.Fatalf("FunctionMembrane(go) = %v", err)
	}
	if len(carried) != 0 {
		t.Error("FunctionMembrane(go) carried a node membrane into an image that runs a compiled binary")
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
		if _, err := p.FunctionBase(ctx, providerkit.Runtime{Name: providerkit.RuntimeNode}); err != nil {
			t.Fatalf("FunctionBase() = %v", err)
		}
	}

	if got := fetches.Load(); got != 1 {
		t.Errorf("the base was fetched %d times for 2 functions, want once: an app with many functions pays a registry round trip for each", got)
	}
}

func TestEachFunctionIsReachedAtAURLOfItsOwn(t *testing.T) {
	if !(&Provider{}).ServesFunctionURLs() {
		t.Error("ServesFunctionURLs() = false, and every function on Cloud Run is a service with a url of its own")
	}
}
