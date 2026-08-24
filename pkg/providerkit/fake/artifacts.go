package fake

import (
	"bytes"
	"context"
	"io"
	"maps"
	"slices"
	"strings"
	"sync"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

type Artifacts struct {
	mu      sync.Mutex
	objects map[providerkit.ArtifactRef][]byte
}

func NewArtifacts() *Artifacts {
	return &Artifacts{objects: map[providerkit.ArtifactRef][]byte{}}
}

func (a *Artifacts) Put(_ context.Context, ref providerkit.ArtifactRef, body io.Reader) error {
	blob, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.objects[ref] = blob
	return nil
}

func (a *Artifacts) Open(_ context.Context, ref providerkit.ArtifactRef) (io.ReadCloser, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	blob, ok := a.objects[ref]
	if !ok {
		return nil, providerkit.Refuse(providerkit.CodeInvalid, "no artifact at %s", ref.Key)
	}
	return io.NopCloser(bytes.NewReader(slices.Clone(blob))), nil
}

func (a *Artifacts) RemovePrefix(_ context.Context, class providerkit.Class, prefix string, report providerkit.Reporter) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	for ref := range maps.Keys(a.objects) {
		if ref.Class == class && strings.HasPrefix(ref.Key, prefix) {
			delete(a.objects, ref)
		}
	}
	if report != nil {
		report.Detail("removed " + prefix)
	}
	return nil
}
