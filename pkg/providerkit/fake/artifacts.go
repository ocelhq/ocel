package fake

import (
	"bytes"
	"context"
	"io"
	"maps"
	"slices"
	"strings"
	"sync"

	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type Artifacts struct {
	journal *Journal

	mu      sync.Mutex
	objects map[provider.ArtifactRef][]byte
}

func NewArtifacts() *Artifacts {
	return &Artifacts{objects: map[provider.ArtifactRef][]byte{}}
}

func (a *Artifacts) Put(_ context.Context, ref provider.ArtifactRef, body io.Reader) error {
	blob, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.objects[ref] = blob
	return nil
}

func (a *Artifacts) Keys() []provider.ArtifactRef {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Collect(maps.Keys(a.objects))
}

func (a *Artifacts) Has(_ context.Context, ref provider.ArtifactRef) (bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	_, held := a.objects[ref]
	return held, nil
}

func (a *Artifacts) Open(_ context.Context, ref provider.ArtifactRef) (io.ReadCloser, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	blob, ok := a.objects[ref]
	if !ok {
		return nil, refusal.Refuse(refusal.CodeInvalid, "no artifact at %s", ref.Key)
	}
	return io.NopCloser(bytes.NewReader(slices.Clone(blob))), nil
}

func (a *Artifacts) RemovePrefix(_ context.Context, class edge.Class, prefix string, progress edge.Progress) error {
	a.journal.note("remove-prefix " + prefix)
	a.mu.Lock()
	defer a.mu.Unlock()
	for ref := range maps.Keys(a.objects) {
		if ref.Class == class && strings.HasPrefix(ref.Key, prefix) {
			delete(a.objects, ref)
		}
	}
	if progress != nil {
		progress.Detail("removed " + prefix)
	}
	return nil
}
