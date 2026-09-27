package gcp

import (
	"slices"
	"testing"

	"github.com/google/go-containerregistry/pkg/v1/empty"
)

func TestABinaryImageRunsTheBinaryItContains(t *testing.T) {
	t.Parallel()

	built, err := binaryImage(empty.Image, []byte("connector"), connectorImagePath)
	if err != nil {
		t.Fatalf("binaryImage: %v", err)
	}
	file, err := built.ConfigFile()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(file.Config.Entrypoint, []string{connectorImagePath}) {
		t.Errorf("the image starts %v, want %s", file.Config.Entrypoint, connectorImagePath)
	}
	if len(file.Config.Cmd) != 0 {
		t.Errorf("the image has the base's command %v, and a static base's command is not the binary", file.Config.Cmd)
	}
	layers, err := built.Layers()
	if err != nil {
		t.Fatal(err)
	}
	if len(layers) != 1 {
		t.Errorf("the image has %d layers on an empty base, want the one that contains the binary", len(layers))
	}

	again, err := binaryImage(empty.Image, []byte("connector"), connectorImagePath)
	if err != nil {
		t.Fatal(err)
	}
	first, err := built.Digest()
	if err != nil {
		t.Fatal(err)
	}
	second, err := again.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Error("the same binary built to two image digests, so a re-run would push and release an image nothing changed in")
	}
}
