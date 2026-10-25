package images

import (
	"slices"
	"testing"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
)

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
