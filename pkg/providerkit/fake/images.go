package fake

import (
	"context"
	"sync"

	"github.com/ocelhq/ocel/pkg/providerkit"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type Images struct {
	mu     sync.Mutex
	held   map[string]bool
	asked  []providerkit.ImagePush
	pushed []providerkit.ImagePush
	failed error
	opened []providerkit.RegistryTarget
}

func NewImages() *Images { return &Images{held: map[string]bool{}} }

func (i *Images) Holds(imageRef string) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.held[imageRef] = true
}

func (i *Images) Refusing(err error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.failed = err
}

func (i *Images) Asked() []providerkit.ImagePush {
	i.mu.Lock()
	defer i.mu.Unlock()
	return append([]providerkit.ImagePush(nil), i.asked...)
}

func (i *Images) Pushed() []providerkit.ImagePush {
	i.mu.Lock()
	defer i.mu.Unlock()
	return append([]providerkit.ImagePush(nil), i.pushed...)
}

func (i *Images) Opened() []providerkit.RegistryTarget {
	i.mu.Lock()
	defer i.mu.Unlock()
	return append([]providerkit.RegistryTarget(nil), i.opened...)
}

func (i *Images) Destination() string { return RegistryServer }

func (i *Images) Has(_ context.Context, push providerkit.ImagePush) (bool, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.asked = append(i.asked, push)
	if i.failed != nil {
		return false, i.failed
	}
	return i.held[push.ImageRef], nil
}

func (i *Images) Push(_ context.Context, push providerkit.ImagePush, _ edge.Progress) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.failed != nil {
		return i.failed
	}
	i.pushed = append(i.pushed, push)
	i.held[push.ImageRef] = true
	return nil
}

func (i *Images) open(target providerkit.RegistryTarget) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.opened = append(i.opened, target)
}

func (p *Provider) OpenRegistryImages(_ context.Context, target providerkit.RegistryTarget) (providerkit.ImageStore, error) {
	p.images.open(target)
	return p.images, nil
}
