package provider_test

import (
	"context"
	"slices"
	"testing"

	v1 "github.com/google/go-containerregistry/pkg/v1"

	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
)

type holdingStore struct{ held []string }

func (holdingStore) Destination() string { return "ghcr.io/acme" }

func (s holdingStore) Has(_ context.Context, push provider.ImagePush) (bool, error) {
	return slices.Contains(s.held, push.App), nil
}

func (holdingStore) Push(context.Context, provider.ImagePush, progress.Progress) error { return nil }

func TestEachImageSentNamesItsAppWhereItGoesAndHowManyAreSent(t *testing.T) {
	t.Parallel()

	wrap := func(context.Context) (v1.Image, func(), error) { return nil, func() {}, nil }
	pushes := provider.ImagePushes{
		Store: holdingStore{held: []string{"api"}},
		Pushes: []provider.ImagePush{
			{App: "web"},
			{App: "api"},
			{App: "worker", Wrap: wrap},
		},
	}
	progress := &fake.Progress{}

	if err := pushes.PushMissing(context.Background(), progress); err != nil {
		t.Fatalf("PushMissing() = %v", err)
	}
	want := []string{
		"INFO Sending web's image to ghcr.io/acme (1 of 2)",
		"INFO Sending worker's image to ghcr.io/acme (2 of 2)",
		"INFO Wrapping worker's image in the ocel runtime",
	}
	if got := progress.Lines(); !slices.Equal(got, want) {
		t.Errorf("PushMissing() said %q, want %q: the image already in the store is not sent, and the wrap is a sentence about one app's image, not tool output", got, want)
	}
}
