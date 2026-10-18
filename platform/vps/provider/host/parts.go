package host

import (
	"context"
	"slices"

	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/refusal"
)

type Part interface {
	Items(read Reading) []Item
	Apply(ctx context.Context, step *Step) error
}

type Step struct {
	bootstrap Bootstrap
	Read      Reading
	Recorded  Stamp
	Applying  Stamp
	progress  progress.Log
}

func (s *Step) Write(ctx context.Context, items []Item) error {
	return s.bootstrap.write(ctx, s.Read, items, s.progress)
}

func (s *Step) Reread(ctx context.Context) error {
	read, err := s.bootstrap.host.Read(ctx, s.Read.Tier)
	if err != nil {
		return err
	}
	read.recorded, read.move, read.rerendering = s.Read.recorded, s.Read.move, s.Read.rerendering
	s.Read = read
	return nil
}

type itemsPart func(read Reading) []Item

func (p itemsPart) Items(read Reading) []Item { return p(read) }

func (p itemsPart) Apply(ctx context.Context, step *Step) error { return step.Write(ctx, p(step.Read)) }

func (h *Host) parts() []Part {
	return []Part{
		tierPart{host: h},
		storagePart{},
		enginePart{host: h},
		itemsPart(func(read Reading) []Item { return LiveItems(read.Arch) }),
		itemsPart(func(read Reading) []Item { return DockerAccessItems(read.Front) }),
		itemsPart(func(read Reading) []Item { return EnvSourceSyncItems(read.Tier, read.Arch) }),
		proxyPart{host: h},
		itemsPart(func(read Reading) []Item { return RealtimeItems(read.Arch) }),
		itemsPart(func(Reading) []Item { return BackupItems() }),
	}
}

func partItems(parts []Part, read Reading) []Item {
	var items []Item
	for _, part := range parts {
		items = slices.Concat(items, part.Items(read))
	}
	return items
}

type tierPart struct{ host *Host }

func (tierPart) Items(read Reading) []Item { return TierItems(read.Tier) }

func (p tierPart) Apply(ctx context.Context, step *Step) error {
	if err := step.Write(ctx, p.Items(step.Read)); err != nil {
		return err
	}
	return p.host.Stamp(ctx, step.Read.Tier, step.Applying)
}

type storagePart struct{}

func (storagePart) Items(read Reading) []Item { return StorageItems(read.Tier, read.Keys, read.Arch) }

func (p storagePart) Apply(ctx context.Context, step *Step) error {
	if err := step.Write(ctx, p.Items(step.Read)); err != nil {
		return err
	}
	if err := step.Reread(ctx); err != nil {
		return err
	}
	if step.Read.Seal.Fingerprint == "" {
		return refusal.Refuse(refusal.CodeDenied, "%s has no seal key", step.Read.Tier)
	}
	sealed := Reading{Tier: step.Read.Tier, Present: true, Seal: step.Read.Seal, Stamp: step.Recorded}
	return sealed.adopting()
}

type enginePart struct{ host *Host }

func (enginePart) Items(Reading) []Item { return EngineItems() }

func (p enginePart) Apply(ctx context.Context, step *Step) error {
	for _, item := range p.Items(step.Read) {
		if step.Read.current(item) {
			step.progress.Debug(capitalized(item.phrase()) + " is " + reasonCurrent)
			continue
		}
		install := func() error { return p.host.Install(ctx, item) }
		if item.Kind == KindEngine {
			install = func() error { return p.host.installEngine(ctx, step.progress) }
		}
		if err := install(); err != nil {
			return err
		}
		step.progress.Say("Installed " + item.phrase())
	}
	return step.Reread(ctx)
}

type proxyPart struct{ host *Host }

func (proxyPart) Items(read Reading) []Item {
	return append(ProxyItems(read.Arch, read.Front), read.recorded...)
}

func (p proxyPart) Apply(ctx context.Context, step *Step) error {
	read := step.Read
	if err := p.host.refuseDirectoryMissing(ctx, read.Front); err != nil {
		return err
	}
	if err := step.Write(ctx, read.move.recordUnfinished()); err != nil {
		return err
	}
	if err := read.move.removeOldFront(ctx, step.progress); err != nil {
		return err
	}
	if err := step.Write(ctx, ProxyItems(read.Arch, read.Front)); err != nil {
		return err
	}
	placeRoutes := p.host.routing.Rerender
	if read.move != nil {
		placeRoutes = read.move.placeRoutes
	}
	if err := placeRoutes(ctx); err != nil {
		return err
	}
	if err := read.move.awaitFront(ctx, read.Tier, step.progress); err != nil {
		return err
	}
	return step.Write(ctx, read.recorded)
}
