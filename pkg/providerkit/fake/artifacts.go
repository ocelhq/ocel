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
	objects map[string][]byte
}

func NewArtifacts() *Artifacts {
	return &Artifacts{objects: map[string][]byte{}}
}

func (a *Artifacts) Put(_ context.Context, ref providerkit.ArtifactRef, body io.Reader) error {
	blob, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.objects[artifactKey(ref)] = blob
	return nil
}

func (a *Artifacts) Open(_ context.Context, ref providerkit.ArtifactRef) (io.ReadCloser, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	blob, ok := a.objects[artifactKey(ref)]
	if !ok {
		return nil, providerkit.Refuse(providerkit.CodeInvalid, "no artifact at %s", artifactKey(ref))
	}
	return io.NopCloser(bytes.NewReader(slices.Clone(blob))), nil
}

func (a *Artifacts) RemovePrefix(_ context.Context, prefix string, report providerkit.Reporter) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	for key := range maps.Keys(a.objects) {
		if strings.HasPrefix(key, prefix) {
			delete(a.objects, key)
		}
	}
	if report != nil {
		report.Detail("removed " + prefix)
	}
	return nil
}

func artifactKey(ref providerkit.ArtifactRef) string {
	return ref.Bucket + "/" + ref.Key
}
