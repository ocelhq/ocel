package fake

import (
	"context"
	"sync"

	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
)

type Images struct {
	mu     sync.Mutex
	stored map[string]bool
	asked  []provider.ImagePush
	pushed []provider.ImagePush
	failed error
	denied error
	opened []provider.RegistryTarget
	probed []string
}

func NewImages() *Images { return &Images{stored: map[string]bool{}} }

func (i *Images) Preload(imageRef string) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.stored[imageRef] = true
}

func (i *Images) FailPushes(err error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.failed = err
}

func (i *Images) DenyPushes(err error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.denied = err
}

func (i *Images) Probed() []string {
	i.mu.Lock()
	defer i.mu.Unlock()
	return append([]string(nil), i.probed...)
}

func (i *Images) Asked() []provider.ImagePush {
	i.mu.Lock()
	defer i.mu.Unlock()
	return append([]provider.ImagePush(nil), i.asked...)
}

func (i *Images) Pushed() []provider.ImagePush {
	i.mu.Lock()
	defer i.mu.Unlock()
	return append([]provider.ImagePush(nil), i.pushed...)
}

func (i *Images) Opened() []provider.RegistryTarget {
	i.mu.Lock()
	defer i.mu.Unlock()
	return append([]provider.RegistryTarget(nil), i.opened...)
}

func (i *Images) Destination() string { return RegistryServer }

func (i *Images) CheckPush(_ context.Context, repository string) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.probed = append(i.probed, repository)
	return i.denied
}

func (i *Images) Has(_ context.Context, push provider.ImagePush) (bool, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.asked = append(i.asked, push)
	if i.failed != nil {
		return false, i.failed
	}
	return i.stored[push.ImageRef], nil
}

func (i *Images) Push(_ context.Context, push provider.ImagePush, _ progress.Log) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.failed != nil {
		return i.failed
	}
	i.pushed = append(i.pushed, push)
	i.stored[push.ImageRef] = true
	return nil
}

func (i *Images) open(target provider.RegistryTarget) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.opened = append(i.opened, target)
}

func (p *Provider) OpenRegistryImages(_ context.Context, target provider.RegistryTarget) (provider.ImageStore, error) {
	p.images.open(target)
	return p.images, nil
}
