package host

import (
	"context"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/bootstrapplan"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/docker"
	"github.com/ocelhq/ocel/platform/vps/provider/machine"
	"github.com/ocelhq/ocel/platform/vps/provider/shell"
	"github.com/ocelhq/ocel/platform/vps/provider/ssh"
)

type Bootstrap struct {
	host    *Host
	vendor  provider.Vendor
	project string
}

func NewBootstrap(h *Host, vendor provider.Vendor, project string) Bootstrap {
	return Bootstrap{host: h, vendor: vendor, project: project}
}

func (b Bootstrap) Parts() []provider.Part { return nil }

func (b Bootstrap) Describe(ctx context.Context, tier environment.Tier) (provider.BootstrapDescription, error) {
	read, err := b.host.Observe(ctx, tier)
	if err != nil {
		return provider.BootstrapDescription{}, err
	}
	return b.described(ctx, read)
}

func (b Bootstrap) described(ctx context.Context, read Reading) (provider.BootstrapDescription, error) {
	principal, err := b.host.Principal(ctx)
	if err != nil {
		return provider.BootstrapDescription{}, err
	}
	if read, err = b.recorded(ctx, read); err != nil {
		return provider.BootstrapDescription{}, err
	}
	if read.observed(machine.KindRoutingTable, boxapi.RoutingTablePath) || read.observed(machine.KindProxyConfig, ProxyConfig) {
		if read.rerendering, err = b.host.routing.ProxyInspected(ctx, read.Tier); err != nil {
			return provider.BootstrapDescription{}, err
		}
	}
	b.host.rememberReading(read)
	return provider.BootstrapDescription{
		Tier:       read.Tier,
		Present:    read.Present,
		Unfinished: read.unfinished(),
		Stacks: []provider.BootstrapStack{{
			Name:          principal,
			Present:       read.Present,
			DigestCurrent: read.upToDate(),
			WrittenBy:     read.Stamp.Writer,
		}},
	}, nil
}

func (b Bootstrap) DescribeParts(ctx context.Context, described provider.BootstrapDescription) ([]provider.BootstrapPart, error) {
	read, remembered := b.host.recallReading(described.Tier)
	if !remembered {
		var err error
		if described, err = b.Describe(ctx, described.Tier); err != nil {
			return nil, err
		}
		read, _ = b.host.recallReading(described.Tier)
	}
	if !read.Present {
		return nil, nil
	}
	base := provider.BootstrapPart{Name: provider.BasePart, Summary: machine.BaseSummary}
	for _, stack := range described.Stacks {
		base.Current = stack.DigestCurrent
	}
	for _, item := range read.Items() {
		base.Resources = append(base.Resources, provider.PhysicalResource{Kind: item.Kind, ID: item.Name})
	}
	return []provider.BootstrapPart{base}, nil
}

func (b Bootstrap) Plan(ctx context.Context, req provider.BootstrapRequest) (provider.Plan, error) {
	read, err := b.reading(ctx, req.Tier)
	if err != nil {
		return provider.Plan{}, err
	}
	described, err := b.described(ctx, read)
	if err != nil {
		return provider.Plan{}, err
	}
	read, _ = b.host.recallReading(req.Tier)
	if err := read.runnableEngine(b.host.Named()); err != nil {
		return provider.Plan{}, err
	}
	if err := b.host.refuseServingPortsHeld(ctx, read); err != nil {
		return provider.Plan{}, err
	}
	groups := bootstrapplan.ChangeGroups(described, b.Parts(), req)
	groups[0].Changes = planned(read)
	if read.rerendering && groups[0].Action == provider.ActionKeep {
		groups[0].Action, groups[0].Reason = provider.ActionUpdate, ""
	}
	if groups[0].Reason == "" {
		groups[0].Reason = machine.BootstrapDocs
	}
	if read.move != nil {
		var move provider.ChangeGroup
		move, groups[0].Changes = read.move.splitOffGroup(groups[0].Changes)
		groups = slices.Insert(groups, 1, move)
	}
	return provider.Plan{Groups: bootstrapplan.PrefixWithVendor(b.vendor, groups)}, nil
}

func planned(read Reading) []provider.Change {
	items := read.Items()
	changes := make([]provider.Change, 0, len(items))
	for _, item := range items {
		change := provider.Change{
			Kind:   item.Kind,
			Name:   item.Name,
			Action: provider.ActionCreate,
			Reason: item.Note,
			Slow:   item.Slow,
		}
		switch {
		case read.rerendering && item.Kind == machine.KindProxyConfig:
			change.Action, change.Reason = provider.ActionUpdate, "rendered again from "+boxapi.RoutingTablePath
		case item.Kind == machine.KindEngine && read.current(item):
			change.Action, change.Reason, change.Slow = provider.ActionAdopt, adoptedEngine(read.Engine.Version), false
		case read.current(item):
			change.Action, change.Reason = provider.ActionKeep, machine.ReasonCurrent
		case read.observed(item.Kind, item.Name):
			change.Action = provider.ActionUpdate
			if change.Reason == "" {
				change.Reason = "drifted"
			}
		}
		changes = append(changes, change)
	}
	return slowLast(changes)
}

func itemPlan(read Reading) provider.Plan {
	return provider.Plan{Groups: []provider.ChangeGroup{{
		Kind:    provider.StackGroupKind,
		Name:    string(read.Tier),
		Changes: planned(read),
	}}}
}

func slowLast(changes []provider.Change) []provider.Change {
	slices.SortStableFunc(changes, func(a, b provider.Change) int {
		switch {
		case a.Slow == b.Slow:
			return 0
		case a.Slow:
			return 1
		default:
			return -1
		}
	})
	return changes
}

func (b Bootstrap) reading(ctx context.Context, tier environment.Tier) (Reading, error) {
	if remembered, ok := b.host.recallReading(tier); ok {
		return remembered, nil
	}
	return b.read(ctx, tier)
}

func (b Bootstrap) read(ctx context.Context, tier environment.Tier) (Reading, error) {
	read, err := b.host.Read(ctx, tier)
	if err != nil {
		return Reading{}, err
	}
	return b.recorded(ctx, read)
}

func (b Bootstrap) Apply(ctx context.Context, req provider.BootstrapRequest, progress progress.Log) error {
	progress = orDiscard(progress)
	shown, err := b.reading(ctx, req.Tier)
	if err != nil {
		return err
	}
	defer b.host.forgetReading(req.Tier)
	current, err := b.read(ctx, req.Tier)
	if err != nil {
		return err
	}
	if err := current.adopting(); err != nil {
		return err
	}
	if err := current.runnableEngine(b.host.Named()); err != nil {
		return err
	}
	if err := b.host.refuseServingPortsHeld(ctx, current); err != nil {
		return err
	}
	items := current.Items()
	if err := provider.RefuseUnconsentedChanges(itemPlan(shown), itemPlan(current)); err != nil {
		return err
	}

	if req.RefuseReplacements {
		if err := refuseReplacements(current, items); err != nil {
			return err
		}
	}

	stamp := machine.Stamp{
		State:   machine.StateApplying,
		Writer:  req.WrittenBy.String(),
		Digests: machine.DigestsOf(items),
	}
	step := &Step{bootstrap: b, Read: current, Recorded: current.Stamp, Applying: stamp, progress: progress}
	for _, part := range b.host.parts() {
		if err := part.Apply(ctx, step); err != nil {
			return err
		}
	}
	if _, err := b.host.routing.EnsureOriginID(ctx); err != nil {
		return err
	}
	b.host.warnOfKernelSettings(ctx, progress)
	stamp.State, stamp.Seal = machine.StateComplete, step.Read.Seal
	return b.host.Stamp(ctx, req.Tier, stamp)
}

func orDiscard(log progress.Log) progress.Log {
	if log == nil {
		return progress.Discard()
	}
	return log
}

func beneath(root, name string) bool {
	return name == root || strings.HasPrefix(name, root+"/")
}

func replacing(item machine.Item) bool {
	switch item.Kind {
	case machine.KindDir, machine.KindUnit, machine.KindNetwork, machine.KindContainer, machine.KindProxyConfig, machine.KindRoutingTable:
		return false
	default:
		return true
	}
}

func refuseReplacements(read Reading, items []machine.Item) error {
	var over []string
	for _, item := range items {
		if !read.current(item) && read.observed(item.Kind, item.Name) && replacing(item) {
			over = append(over, item.ID())
		}
	}
	if len(over) == 0 {
		return nil
	}
	return refusal.Refuse(refusal.CodeNotReady,
		"%s would be overwritten\nRe-run with --yes to overwrite",
		strings.Join(over, ", "))
}

func (r Reading) adopting() error {
	recorded := r.Stamp.Seal.Fingerprint
	if !r.Present || recorded == "" || r.Seal.Fingerprint == recorded {
		return nil
	}
	present := r.Seal.Fingerprint
	if present == "" {
		if r.observed(machine.KindSealKey, SealKeyPath(r.Tier)) {
			return nil
		}
		present = "no key at all"
	}
	return refusal.Refuse(refusal.CodeInvalid,
		"%s records seal key %s, but %s contains %s\nRestore the recorded key, or remove the tier with `ocel bootstrap destroy %s`",
		machine.StampPath(r.Tier), recorded, SealKeyPath(r.Tier), present, r.Tier)
}

func (b Bootstrap) write(ctx context.Context, read Reading, items []machine.Item, progress progress.Log) error {
	return b.host.InstallChanged(ctx, items, read.current, progress)
}

func (b Bootstrap) PlanRemove(ctx context.Context, tier environment.Tier) (provider.Plan, error) {
	removals, err := b.removals(ctx, tier)
	if err != nil || len(removals) == 0 {
		return provider.Plan{}, err
	}
	principal, err := b.host.Principal(ctx)
	if err != nil {
		return provider.Plan{}, err
	}
	changes := make([]provider.Change, 0, len(removals))
	for _, removal := range removals {
		changes = append(changes, provider.Change{
			Kind:   removal.Kind,
			Name:   removal.Path,
			Action: removal.Action,
			Reason: removal.Reason,
		})
	}
	group := provider.ChangeGroup{
		Kind:    provider.StackGroupKind,
		Name:    principal,
		Action:  provider.ActionDelete,
		Changes: changes,
	}
	return provider.Plan{Groups: bootstrapplan.PrefixWithVendor(b.vendor, []provider.ChangeGroup{group})}, nil
}

func (b Bootstrap) Remove(ctx context.Context, tier environment.Tier, progress progress.Log) error {
	progress = orDiscard(progress)
	defer b.host.forgetReading(tier)
	forget, err := b.host.KnownHostRemoval(ctx)
	if err != nil {
		return err
	}
	removals, err := b.removals(ctx, tier)
	if err != nil {
		return err
	}
	for _, removal := range removals {
		if removal.Action != provider.ActionDelete {
			progress.Say(removal.Kept())
			continue
		}
		taken, err := b.host.remove(ctx, removal)
		if err != nil {
			return err
		}
		if !taken {
			progress.Say("Kept " + removal.Phrase() + ": something else on this box still uses it")
			continue
		}
		progress.Say("Removed " + removal.Phrase())
	}
	progress.Say(leavingKnownHosts(forget))
	return nil
}

func leavingKnownHosts(forget string) string {
	return "Your known_hosts still trusts this box: to drop it, run `" + forget + "`"
}

func (b Bootstrap) removals(ctx context.Context, tier environment.Tier) ([]machine.Removal, error) {
	read, err := b.host.Survey(ctx, tier)
	if err != nil {
		return nil, err
	}
	sibling, err := b.host.Survey(ctx, tier.Sibling())
	if err != nil {
		return nil, err
	}
	apps, err := b.host.presentApps(ctx, tier)
	if err != nil {
		return nil, err
	}
	return removing(read, sibling, apps), nil
}

type appsPresent struct{ containers, networks, volumes bool }

func tierSelector(tier environment.Tier) string { return docker.LabelTier + "=" + string(tier) }

func appsProbe(tier environment.Tier) string {
	filter := shell.Quote("label=" + tierSelector(tier))
	return "if command -v " + shell.Quote(dockerEngine) + " >/dev/null 2>&1; then\n" +
		"if [ -n \"$(docker ps --all --quiet --filter " + filter + " 2>/dev/null)\" ]; then echo containers; fi\n" +
		"if [ -n \"$(docker network ls --quiet --filter " + filter + " 2>/dev/null)\" ]; then echo networks; fi\n" +
		"if [ -n \"$(docker volume ls --quiet --filter " + filter + " 2>/dev/null)\" ]; then echo volumes; fi\n" +
		"fi"
}

func (h *Host) presentApps(ctx context.Context, tier environment.Tier) (appsPresent, error) {
	said, err := h.Run(ctx, ssh.Root, "ask what "+string(tier)+" still runs", appsProbe(tier), nil)
	if err != nil {
		return appsPresent{}, err
	}
	return appsPresent{
		containers: strings.Contains(said, "containers"),
		networks:   strings.Contains(said, "networks"),
		volumes:    strings.Contains(said, "volumes"),
	}, nil
}

func appsRemoving(tier environment.Tier, apps appsPresent) []machine.Removal {
	var taken []machine.Removal
	if apps.containers {
		taken = append(taken, machine.Taking(machine.KindApps, tierSelector(tier),
			""))
	}
	if apps.volumes {
		taken = append(taken, machine.Taking(machine.KindResourceVolumes, tierSelector(tier),
			"resource data, not recoverable"))
	}
	if apps.networks {
		taken = append(taken, machine.Taking(machine.KindAppNetworks, tierSelector(tier),
			""))
	}
	return taken
}

func removing(read, sibling Reading, apps appsPresent) []machine.Removal {
	beside := sibling.Tier
	last := !sibling.observed(machine.KindDir, boxapi.TierDir(beside)) && !sibling.observed(machine.KindDir, boxapi.StateDir(beside))

	beneath := append([]machine.Removal{machine.Taking(machine.KindUnit, EnvSourceSyncService(read.Tier), "")}, appsRemoving(read.Tier, apps)...)
	beneath = append(beneath,
		machine.Taking(machine.KindDir, boxapi.StateDir(read.Tier), "deploy records"),
		machine.Taking(machine.KindSealKey, SealKeyPath(read.Tier), "sealed values become unreadable"),
		machine.Taking(machine.KindFile, sudoersSeal(read.Tier), ""),
	)
	stamp := []machine.Removal{machine.Taking(machine.KindDir, boxapi.TierDir(read.Tier),
		"")}
	var above []machine.Removal
	if last {
		beneath = append(beneath, placedRemovals(read.Front)...)
		beneath = append(beneath, proxyRemovals()...)
		beneath = append(beneath, liveRemovals()...)
		beneath = append(beneath, envSourceSyncRemovals()...)
		beneath = append(beneath, backupRemovals()...)
		beneath = append(beneath, dockerAccessRemovals()...)
		beneath = append(beneath,
			machine.Taking(machine.KindDir, sshDir, ""),
			machine.Taking(machine.KindDir, boxapi.ReleasesDir, "images stay"),
			machine.Taking(machine.KindFile, boxapi.ImagesLock, ""),
			machine.Sharing(boxapi.StateRoot, ""),
			machine.Taking(machine.KindUser, deployUser, ""),
			machine.Taking(machine.KindFile, boxapi.ReleasesHelper, ""),
			machine.Taking(machine.KindFile, boxapi.SwitchboardBinary, ""),
			machine.Taking(machine.KindDir, boxapi.SwitchboardDir, ""),
			machine.Taking(machine.KindFile, RealtimeBinary, ""),
			machine.Taking(machine.KindDir, RealtimeDir, ""),
			machine.Sharing(boxapi.LibDir, ""),
		)
		above = []machine.Removal{machine.Sharing(boxapi.TierRoot, "")}
	}
	ordered := slices.Concat(beneath, stamp, above)

	present := make([]machine.Removal, 0, len(ordered))
	for _, candidate := range ordered {
		if candidate.Kind == machine.KindApps || candidate.Kind == machine.KindResourceVolumes || candidate.Kind == machine.KindAppNetworks || candidate.Kind == machine.KindPlaced ||
			read.observed(candidate.Kind, candidate.Path) || sibling.observed(candidate.Kind, candidate.Path) {
			present = append(present, candidate)
		}
	}
	if len(present) == 0 {
		return nil
	}
	if read.observed(machine.KindEngine, dockerEngine) || sibling.observed(machine.KindEngine, dockerEngine) {
		return append([]machine.Removal{keptEngine()}, present...)
	}
	return present
}
