package imagebuild_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/cli/internal/imagebuild"
	"github.com/ocelhq/ocel/cli/internal/livemachine"
)

const leaked = "a value the build must never see"

func TestLiveARailpackBuildLandsAWorkingImageInTheDaemon(t *testing.T) {
	vm := livemachine.Require(t)
	vm.Engine(t)
	vm.Forward(t)
	t.Setenv("OCEL_LIVE_LEAK", leaked)

	image, err := imagebuild.Builder{Progress: livemachine.Progress{T: t}}.Build(context.Background(), imagebuild.App{Name: "Web API", Dir: "testdata/plainserver"})
	if err != nil {
		t.Fatalf("Build() against a real daemon = %v", err)
	}

	addresses(t, vm, image, "ocel/web-api")

	if pulled := vm.SSH(t, "docker image ls --format '{{.Repository}}'"); strings.Contains(pulled, "railpack-frontend") {
		t.Errorf("the daemon pulled a railpack frontend image, so the build was not in-process:\n%s", pulled)
	}
	for _, where := range []string{
		"docker image inspect " + image.Ref,
		"docker image history --no-trunc --format '{{.CreatedBy}}' " + image.Ref,
	} {
		said := vm.SSH(t, where)
		for _, secret := range []string{"OCEL_LIVE_LEAK", leaked} {
			if strings.Contains(said, secret) {
				t.Errorf("`%s` carries %q from ocel's own environment, so the build was not bare:\n%s", where, secret, said)
			}
		}
	}

	if said := serves(t, vm, image, 18080); said != "plainserver" {
		t.Errorf("the running image answered %q, want the app's own response", said)
	}
}

func addresses(t *testing.T, vm livemachine.Machine, image imagebuild.Image, repository string) {
	t.Helper()
	if image.Repository != repository {
		t.Errorf("the image's repository is %q, want %q, derived from the app's name", image.Repository, repository)
	}
	if want := image.Repository + "@" + image.Digest; image.Ref != want {
		t.Errorf("the image's ref is %q, want %q", image.Ref, want)
	}
	for _, coordinate := range []string{image.Ref, image.Repository + ":" + image.Tag} {
		if _, err := vm.Attempt("docker image inspect " + coordinate); err != nil {
			t.Errorf("the daemon holds no image at %s, so the coordinate ocel hands a provider names nothing: %v", coordinate, err)
		}
	}
	repoDigests := vm.SSH(t, "docker image inspect --format '{{json .RepoDigests}}' "+image.Repository+":"+image.Tag)
	if !strings.Contains(repoDigests, image.Ref) {
		t.Errorf("the daemon addresses the image it built by %s, and %s is not among them: the digest ocel hands a provider is not the one the daemon answers to", repoDigests, image.Ref)
	}
}

func serves(t *testing.T, vm livemachine.Machine, image imagebuild.Image, port int) string {
	t.Helper()
	name := fmt.Sprintf("ocel-live-%d", port)
	vm.SSH(t, "docker rm -f "+name+" >/dev/null 2>&1 || true")
	t.Cleanup(func() { _, _ = vm.Attempt("docker rm -f " + name) })
	vm.SSH(t, fmt.Sprintf("docker run -d --name %s -e PORT=8080 -p 127.0.0.1:%d:8080 %s", name, port, image.Ref))

	deadline := time.Now().Add(60 * time.Second)
	for {
		said, err := vm.Attempt(fmt.Sprintf("curl -sf -m 2 http://127.0.0.1:%d/", port))
		if err == nil {
			return said
		}
		if time.Now().After(deadline) {
			t.Fatalf("the image built for %s never served on the injected PORT: %v\n%s", image.Ref, err, vm.SSH(t, "docker logs "+name+" 2>&1 | tail -30"))
		}
		time.Sleep(2 * time.Second)
	}
}

func TestLiveADockerfileBuildLandsTheSameCoordinateAsARailpackOne(t *testing.T) {
	vm := livemachine.Require(t)
	vm.Engine(t)
	vm.Forward(t)

	image, err := imagebuild.Builder{Progress: livemachine.Progress{T: t}}.Build(context.Background(), imagebuild.App{Name: "Docs Site", Dir: "testdata/dockerfileapp"})
	if err != nil {
		t.Fatalf("Build() of an app with a Dockerfile against a real daemon = %v", err)
	}

	addresses(t, vm, image, "ocel/docs-site")

	if said := serves(t, vm, image, 18081); said != "dockerfile" {
		t.Errorf("the running image answered %q: the app's Dockerfile is what sets that, so %q was built by railpack instead", said, image.Ref)
	}
}
