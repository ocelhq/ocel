//go:build integration

package image_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/cli/internal/build/image"
	"github.com/ocelhq/ocel/cli/internal/incustest"
)

const leaked = "a value the build must never see"

func TestLiveARailpackBuildLandsAWorkingImageInTheDaemon(t *testing.T) {
	vm := incustest.Require(t)
	vm.Engine(t)
	vm.Forward(t)
	t.Setenv("OCEL_LIVE_LEAK", leaked)

	built, err := image.Build(context.Background(), image.App{Slug: "Shop Live", Name: "Web API", Workspace: located(t, "testdata/plainserver")}, "", image.LiveValues{}, incustest.Progress{T: t})
	if err != nil {
		t.Fatalf("Build() against a real daemon = %v", err)
	}

	addresses(t, vm, built, "ocel/shop-live/web-api")

	if pulled := vm.SSH(t, "docker image ls --format '{{.Repository}}'"); strings.Contains(pulled, "railpack-frontend") {
		t.Errorf("the daemon pulled a railpack frontend image, so the build was not in-process:\n%s", pulled)
	}
	for _, where := range []string{
		"docker image inspect " + built.Ref,
		"docker image history --no-trunc --format '{{.CreatedBy}}' " + built.Ref,
	} {
		said := vm.SSH(t, where)
		for _, secret := range []string{"OCEL_LIVE_LEAK", leaked} {
			if strings.Contains(said, secret) {
				t.Errorf("`%s` shows %q from ocel's own environment, so the build was not bare:\n%s", where, secret, said)
			}
		}
	}

	if said := serves(t, vm, built, 18080); said != "plainserver" {
		t.Errorf("the running image answered %q, want the app's own response", said)
	}
}

func addresses(t *testing.T, vm incustest.Machine, built image.Image, repository string) {
	t.Helper()
	if built.Repository != repository {
		t.Errorf("the image's repository is %q, want %q, derived from the app's name", built.Repository, repository)
	}
	if want := built.Repository + "@" + built.Digest; built.Ref != want {
		t.Errorf("the image's ref is %q, want %q", built.Ref, want)
	}
	for _, coordinate := range []string{built.Ref, built.Repository + ":" + built.Tag} {
		if _, err := vm.Attempt("docker image inspect " + coordinate); err != nil {
			t.Errorf("the daemon has no image at %s, so the coordinate ocel hands a provider names nothing: %v", coordinate, err)
		}
	}
	repoDigests := vm.SSH(t, "docker image inspect --format '{{json .RepoDigests}}' "+built.Repository+":"+built.Tag)
	if !strings.Contains(repoDigests, built.Ref) {
		t.Errorf("the daemon addresses the image it built by %s, and %s is not among them: the digest ocel hands a provider is not the one the daemon answers to", repoDigests, built.Ref)
	}
}

func serves(t *testing.T, vm incustest.Machine, built image.Image, port int) string {
	t.Helper()
	name := fmt.Sprintf("ocel-live-%d", port)
	vm.SSH(t, "docker rm -f "+name+" >/dev/null 2>&1 || true")
	t.Cleanup(func() { _, _ = vm.Attempt("docker rm -f " + name) })
	vm.SSH(t, fmt.Sprintf("docker run -d --name %s -e PORT=8080 -p 127.0.0.1:%d:8080 %s", name, port, built.Ref))

	deadline := time.Now().Add(60 * time.Second)
	for {
		said, err := vm.Attempt(fmt.Sprintf("curl -sf -m 2 http://127.0.0.1:%d/", port))
		if err == nil {
			return said
		}
		if time.Now().After(deadline) {
			t.Fatalf("the image built for %s never served on the injected PORT: %v\n%s", built.Ref, err, vm.SSH(t, "docker logs "+name+" 2>&1 | tail -30"))
		}
		time.Sleep(2 * time.Second)
	}
}

func TestLiveADockerfileBuildLandsTheSameCoordinateAsARailpackOne(t *testing.T) {
	vm := incustest.Require(t)
	vm.Engine(t)
	vm.Forward(t)

	built, err := image.Build(context.Background(), image.App{Slug: "Shop Live", Name: "Docs Site", Workspace: located(t, "testdata/dockerfileapp")}, "", image.LiveValues{}, incustest.Progress{T: t})
	if err != nil {
		t.Fatalf("Build() of an app with a Dockerfile against a real daemon = %v", err)
	}

	addresses(t, vm, built, "ocel/shop-live/docs-site")

	if said := serves(t, vm, built, 18081); said != "dockerfile" {
		t.Errorf("the running image answered %q: the app's Dockerfile is what sets that, so %q was built by railpack instead", said, built.Ref)
	}
}

func TestLiveAnImageGainsFilesOverWhatItWasBuiltFromAndStillServes(t *testing.T) {
	vm := incustest.Require(t)
	vm.Engine(t)
	vm.Forward(t)

	built, err := image.Build(context.Background(), image.App{Slug: "Shop Live", Name: "Worker Host", Workspace: located(t, "testdata/dockerfileapp")}, "", image.LiveValues{}, incustest.Progress{T: t})
	if err != nil {
		t.Fatalf("Build() = %v", err)
	}
	files := t.TempDir()
	if err := os.WriteFile(filepath.Join(files, "entry.txt"), []byte("the worker entry"), 0o644); err != nil {
		t.Fatal(err)
	}
	extended, err := image.AddFiles(context.Background(), built, "Shop Live", "Worker Host", files, "/ocel/worker", "", incustest.Progress{T: t})
	if err != nil {
		t.Fatalf("AddFiles() = %v", err)
	}
	if extended.Digest == built.Digest {
		t.Fatalf("AddFiles() answered the image it was handed, %s", built.Ref)
	}
	addresses(t, vm, extended, "ocel/shop-live/worker-host")
	if said := vm.SSH(t, "docker run --rm --entrypoint cat "+extended.Ref+" /ocel/worker/entry.txt"); strings.TrimSpace(said) != "the worker entry" {
		t.Errorf("the extended image holds %q at /ocel/worker/entry.txt", said)
	}
	if said := serves(t, vm, extended, 18082); said != "dockerfile" {
		t.Errorf("the extended image answered %q, want what the app it was built from serves", said)
	}
}
