package vps_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"strings"
	"testing"
	"time"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/types"

	"github.com/ocelhq/ocel/pkg/provider"
	vps "github.com/ocelhq/ocel/platform/vps/provider"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
)

func timedImagesOn(t *testing.T, machine *box, step time.Duration) provider.ImageStore {
	t.Helper()
	p := vps.ProviderOver(
		vps.Options{SSH: vps.Target{Host: "box.invalid", User: "ada"}},
		func(context.Context) (host.Conn, error) { return machine, nil },
	)
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	p.Timing(func() time.Time {
		at = at.Add(step)
		return at
	})
	store, err := p.OpenDirectImages(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return store
}

type zeroLayer struct{ size int64 }

func (z zeroLayer) Digest() (v1.Hash, error) {
	sum := sha256.Sum256([]byte(strings.Repeat("0", int(z.size%97))))
	return v1.Hash{Algorithm: "sha256", Hex: hex.EncodeToString(sum[:])}, nil
}

func (z zeroLayer) DiffID() (v1.Hash, error) { return z.Digest() }

func (z zeroLayer) Compressed() (io.ReadCloser, error) {
	return io.NopCloser(io.LimitReader(zeros{}, z.size)), nil
}

func (z zeroLayer) Uncompressed() (io.ReadCloser, error) { return z.Compressed() }

func (z zeroLayer) Size() (int64, error) { return z.size, nil }

func (z zeroLayer) MediaType() (types.MediaType, error) { return types.DockerLayer, nil }

type zeros struct{}

func (zeros) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

func imageOf(t *testing.T, size int64) v1.Image {
	t.Helper()
	built, err := mutate.AppendLayers(empty.Image, zeroLayer{size: size})
	if err != nil {
		t.Fatal(err)
	}
	return built
}

func TestASlowTransferWarnsWithItsDurationAndPointsAtTheRegistryDocs(t *testing.T) {
	daemonServing(t, "tar-bytes")
	store := timedImagesOn(t, &box{drains: true}, 48*time.Second)
	log := &warned{}
	push := aPush(t)
	push.Built = imageOf(t, 120_000_000)

	if err := store.Push(context.Background(), push, log); err != nil {
		t.Fatalf("Push() = %v", err)
	}
	if len(log.lines) != 1 {
		t.Fatalf("warnings = %q, want one for a 48s transfer", log.lines)
	}
	want := "Sending web's image to box.invalid took 48s for 120 MB. With no registry every deploy sends the whole image over SSH: " +
		"name a `registry` and the box pulls only the layers that changed. https://ocel.dev/docs/providers/vps#images"
	if log.lines[0] != want {
		t.Errorf("warning = %q, want %q", log.lines[0], want)
	}
}

func TestAPullFromARegistryDoesNotWarnHoweverLongItTakes(t *testing.T) {
	aDaemon(t)
	server, _ := aRegistry(t)
	target := aTarget(server)
	p := provisioning(t, &box{})
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	p.Timing(func() time.Time {
		at = at.Add(10 * time.Minute)
		return at
	})
	store, err := p.OpenRegistryImages(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	log := &warned{}

	if err := store.Push(context.Background(), aPull(target), log); err != nil {
		t.Fatalf("Push() = %v", err)
	}
	if len(log.lines) != 0 {
		t.Errorf("warnings = %q, want none: a registry is already named", log.lines)
	}
}

func TestALargeTransferWarnsWithItsSizeEvenWhenItWasQuick(t *testing.T) {
	daemonServing(t, "tar-bytes")
	store := timedImagesOn(t, &box{drains: true}, time.Second)
	log := &warned{}
	push := aPush(t)
	push.Built = imageOf(t, 320_000_000)

	if err := store.Push(context.Background(), push, log); err != nil {
		t.Fatalf("Push() = %v", err)
	}
	if len(log.lines) != 1 {
		t.Fatalf("warnings = %q, want one for a 320 MB transfer", log.lines)
	}
	for _, want := range []string{"320 MB", "https://ocel.dev/docs/providers/vps#images"} {
		if !strings.Contains(log.lines[0], want) {
			t.Errorf("warning = %q, want it to mention %q", log.lines[0], want)
		}
	}
}

func TestAQuickSmallTransferDoesNotWarn(t *testing.T) {
	daemonServing(t, "tar-bytes")
	store := timedImagesOn(t, &box{}, time.Second)
	log := &warned{}

	if err := store.Push(context.Background(), aPush(t), log); err != nil {
		t.Fatalf("Push() = %v", err)
	}
	if len(log.lines) != 0 {
		t.Errorf("warnings = %q, want none for a small image sent in a second", log.lines)
	}
}

func TestAProviderBuiltForADeployTimesTheImagesItSends(t *testing.T) {
	p := vps.NewProvider(vps.Options{SSH: vps.Target{Host: "203.0.113.10"}})
	if p.Clock() == nil {
		t.Fatal("NewProvider() left the clock unset, so the first image sent over ssh panics timing itself")
	}
}
