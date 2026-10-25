package images

import (
	"context"
	"slices"
	"testing"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
)

func TestTheNodeBaseIsPulledByItsPinnedDigestForTheArchitectureAsked(t *testing.T) {
	var asked []string
	pull := func(_ context.Context, ref, goarch string) (v1.Image, error) {
		asked = append(asked, ref, goarch)
		return mutate.Config(empty.Image, v1.Config{Entrypoint: []string{"/nodejs/bin/node"}})
	}

	image, err := PullNodeBase(context.Background(), pull, "arm64")
	if err != nil {
		t.Fatalf("PullNodeBase() = %v", err)
	}
	if want := []string{NodeBase, "arm64"}; !slices.Equal(asked, want) {
		t.Errorf("pulled %v, want %v", asked, want)
	}
	file, err := image.ConfigFile()
	if err != nil {
		t.Fatal(err)
	}
	if len(file.Config.Entrypoint) != 0 || !slices.Contains(file.Config.Env, "PATH="+NodeBinDir) {
		t.Errorf("config = %+v, want no entrypoint and node's directory on PATH", file.Config)
	}
}

func TestABaseRunsItsFunctionsCommandWithTheBaseBinariesOnItsPath(t *testing.T) {
	base, err := mutate.Config(empty.Image, v1.Config{Entrypoint: []string{"/nodejs/bin/node"}, Env: []string{"PATH=/usr/bin", "TZ=UTC"}})
	if err != nil {
		t.Fatal(err)
	}

	command, err := NewCommandImage(base, []string{NodeBinDir})
	if err != nil {
		t.Fatalf("NewCommandImage() = %v", err)
	}
	file, err := command.ConfigFile()
	if err != nil {
		t.Fatal(err)
	}
	if len(file.Config.Entrypoint) != 0 {
		t.Errorf("Entrypoint = %v, want none: the function's image names its command", file.Config.Entrypoint)
	}
	if want := []string{"TZ=UTC", "PATH=" + NodeBinDir + ":/usr/bin"}; !slices.Equal(file.Config.Env, want) {
		t.Errorf("Env = %v, want %v", file.Config.Env, want)
	}
}
