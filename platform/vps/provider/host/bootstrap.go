package host

import (
	"context"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
	"github.com/ocelhq/ocel/platform/vps/provider/live"
)

const (
	reasonStanding = "already current"
	bootstrapDocs  = "https://ocel.dev/docs/providers/vps#bootstrap"
)

type Bootstrap struct {
	host    *Host
	vendor  provider.Vendor
	project string
}

func NewBootstrap(h *Host, vendor provider.Vendor, project string) Bootstrap {
	return Bootstrap{host: h, vendor: vendor, project: project}
}

func (b Bootstrap) Catalogue() []provider.Feature { return nil }

func (b Bootstrap) Describe(ctx context.Context, class edge.Class) (provider.BootstrapReading, error) {
	read, err := b.host.Observe(ctx, class)
	if err != nil {
		return provider.BootstrapReading{}, err
	}
	return b.described(ctx, read)
}

func (b Bootstrap) described(ctx context.Context, read Reading) (provider.BootstrapReading, error) {
	principal, err := b.host.Principal(ctx)
	if err != nil {
		return provider.BootstrapReading{}, err
	}
	if read, err = b.recorded(ctx, read); err != nil {
		return provider.BootstrapReading{}, err
	}
	if read.standing(KindRoutingTable, live.RoutingTable) || read.standing(KindProxyConfig, ProxyConfig) {
		if read.rerendering, err = b.host.proxyInspected(ctx, read.Class); err != nil {
			return provider.BootstrapReading{}, err
		}
	}
	return provider.BootstrapReading{
		Class:      read.Class,
		Present:    read.Present,
		Unfinished: read.unfinished(),
		Reading:    read,
		Stacks: []provider.BootstrapStack{{
			Name:          principal,
			Present:       read.Present,
			Schema:        uint32(read.Stamp.Schema),
			DigestCurrent: read.settled(),
			WrittenBy:     read.Stamp.Writer,
		}},
	}, nil
}

func (b Bootstrap) Plan(ctx context.Context, req provider.BootstrapRequest) (provider.Plan, error) {
	read, err := b.reading(ctx, req)
	if err != nil {
		return provider.Plan{}, err
	}
	described, err := b.described(ctx, read)
	if err != nil {
		return provider.Plan{}, err
	}
	read = described.Reading.(Reading)
	if err := read.runnableEngine(b.host.named()); err != nil {
		return provider.Plan{}, err
	}
	if err := b.host.servingFree(ctx, read); err != nil {
		return provider.Plan{}, err
	}
	groups := providerkit.DeriveGroups(described, b.Catalogue(), req)
	groups[0].Changes = planned(read)
	if read.rerendering && groups[0].Action == provider.ActionKeep {
		groups[0].Action, groups[0].Reason = provider.ActionUpdate, ""
	}
	if groups[0].Reason == "" {
		groups[0].Reason = bootstrapDocs
	}
	return provider.Plan{Groups: providerkit.Vendored(b.vendor, groups)}, nil
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
		case read.rerendering && item.Kind == KindProxyConfig:
			change.Action, change.Reason = provider.ActionUpdate, "rendered again from "+live.RoutingTable
		case item.Kind == KindEngine && read.current(item):
			change.Action, change.Reason, change.Slow = provider.ActionAdopt, adoptedEngine(read.Engine.Version), false
		case read.current(item):
			change.Action, change.Reason = provider.ActionKeep, reasonStanding
		case read.standing(item.Kind, item.Name):
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
		Name:    string(read.Class),
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

func (b Bootstrap) reading(ctx context.Context, req provider.BootstrapRequest) (Reading, error) {
	if held, carried := req.Reading.(Reading); carried && held.Class == req.Class {
		return held, nil
	}
	return b.read(ctx, req.Class)
}

func (b Bootstrap) read(ctx context.Context, class edge.Class) (Reading, error) {
	read, err := b.host.Read(ctx, class)
	if err != nil {
		return Reading{}, err
	}
	return b.recorded(ctx, read)
}

func (b Bootstrap) Apply(ctx context.Context, req provider.BootstrapRequest, progress edge.Progress) error {
	if req.Heal {
		return b.heal(ctx, req, progress)
	}
	shown, err := b.reading(ctx, req)
	if err != nil {
		return err
	}
	standing, err := b.read(ctx, req.Class)
	if err != nil {
		return err
	}
	if err := standing.adopting(); err != nil {
		return err
	}
	if err := standing.runnableEngine(b.host.named()); err != nil {
		return err
	}
	if err := b.host.servingFree(ctx, standing); err != nil {
		return err
	}
	items := standing.Items()
	if err := providerkit.RefuseGrowth(itemPlan(shown), itemPlan(standing)); err != nil {
		return err
	}

	if req.Unattended {
		if err := refuseReplacements(standing, items); err != nil {
			return err
		}
	}

	stamp := Stamp{
		Schema:  provider.BootstrapSchema,
		State:   StateApplying,
		Writer:  req.WrittenBy.String(),
		Digests: digests(items),
	}
	if err := b.write(ctx, standing, ClassItems(req.Class), progress); err != nil {
		return err
	}
	if err := b.host.Stamp(ctx, req.Class, stamp); err != nil {
		return err
	}
	if err := b.write(ctx, standing, StorageItems(req.Class, standing.Keys), progress); err != nil {
		return err
	}

	minted, err := b.host.Read(ctx, req.Class)
	if err != nil {
		return err
	}
	if minted.Seal.Fingerprint == "" {
		return refusal.Refuse(refusal.CodeDenied,
			"%s has no seal key",
			req.Class)
	}
	held := Reading{Class: req.Class, Present: true, Seal: minted.Seal, Stamp: standing.Stamp}
	if err := held.adopting(); err != nil {
		return err
	}
	if err := b.write(ctx, standing, EngineItems(), progress); err != nil {
		return err
	}
	served, err := b.host.Read(ctx, req.Class)
	if err != nil {
		return err
	}
	if err := b.write(ctx, served, LiveItems(standing.Arch), progress); err != nil {
		return err
	}
	if err := b.write(ctx, served, standing.recorded, progress); err != nil {
		return err
	}
	if err := b.write(ctx, served, ProxyItems(standing.Arch, standing.Front), progress); err != nil {
		return err
	}
	if err := b.host.rerender(ctx); err != nil {
		return err
	}
	if err := b.write(ctx, served, BackupItems(), progress); err != nil {
		return err
	}
	stamp.State, stamp.Seal = StateComplete, minted.Seal
	return b.host.Stamp(ctx, req.Class, stamp)
}

func (b Bootstrap) heal(ctx context.Context, req provider.BootstrapRequest, progress edge.Progress) error {
	read, err := b.host.Own(ctx, req.Class)
	if err != nil {
		return err
	}
	work, left, err := healing(read, req.Unattended)
	if err != nil {
		return err
	}
	for _, id := range left {
		say(progress, id+": left as is")
	}
	return b.writing(ctx, read, work, progress, b.host.Reassert)
}

func healing(read Reading, unattended bool) ([]Item, []string, error) {
	work, left, err := healable(read)
	if err != nil {
		return nil, nil, err
	}
	if unattended {
		if err := refuseReplacements(read, work); err != nil {
			return nil, nil, err
		}
	}
	return work, left, nil
}

func healable(read Reading) ([]Item, []string, error) {
	command := provider.BootstrapCommand(read.Class)
	if !read.Present {
		return nil, nil, refusal.Refuse(refusal.CodeDenied,
			"the %s class is not bootstrapped on this host\nRun `%s`",
			read.Class, command)
	}
	if read.unfinished() {
		return nil, nil, refusal.Refuse(refusal.CodeDenied,
			"%s records an unfinished apply\nRun `%s` to finish it",
			StampPath(read.Class), command)
	}
	if err := read.adopting(); err != nil {
		return nil, nil, err
	}
	var work []Item
	var left, denied []string
	for _, item := range read.Items() {
		if read.current(item) {
			continue
		}
		if deployOwned(item) {
			work = append(work, item)
			continue
		}
		if daemonHeld(item) || routingHeld(item) || rewrittenByDeploys(item) || !read.standing(item.Kind, item.Name) {
			left = append(left, item.ID())
			continue
		}
		denied = append(denied, item.ID())
	}
	if len(denied) > 0 {
		return nil, nil, refusal.Refuse(refusal.CodeDenied,
			"heal cannot write %s\nRun `%s` as the login that bootstrapped this host",
			strings.Join(denied, ", "), command)
	}
	return work, left, nil
}

func daemonHeld(item Item) bool {
	switch item.Kind {
	case KindEngine, KindUnit, KindNetwork, KindContainer:
		return true
	default:
		return false
	}
}

func deployOwned(item Item) bool {
	if item.Kind != KindDir && item.Kind != KindFile {
		return false
	}
	if beneath(sshDir, item.Name) || routingHeld(item) {
		return false
	}
	return item.Owner == stateOwner && beneath(stateRoot, item.Name)
}

func routingHeld(item Item) bool {
	return beneath(proxyRoot, item.Name) || beneath(live.RoutingDir, item.Name)
}

func beneath(root, name string) bool {
	return name == root || strings.HasPrefix(name, root+"/")
}

func replacing(item Item) bool {
	switch item.Kind {
	case KindDir, KindUnit, KindNetwork, KindContainer, KindProxyConfig, KindRoutingTable:
		return false
	default:
		return true
	}
}

func refuseReplacements(standing Reading, items []Item) error {
	var over []string
	for _, item := range items {
		if !standing.current(item) && standing.standing(item.Kind, item.Name) && replacing(item) {
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
	standing := r.Seal.Fingerprint
	if standing == "" {
		if r.standing(KindSealKey, SealKeyPath(r.Class)) {
			return nil
		}
		standing = "no key at all"
	}
	return refusal.Refuse(refusal.CodeInvalid,
		"%s records seal key %s, but %s holds %s\nRestore the recorded key, or `ocel destroy` the class",
		StampPath(r.Class), recorded, SealKeyPath(r.Class), standing)
}

func (b Bootstrap) write(ctx context.Context, standing Reading, items []Item, progress edge.Progress) error {
	return b.writing(ctx, standing, items, progress, func(ctx context.Context, item Item) error {
		if item.Kind == KindEngine {
			return b.host.installEngine(ctx, progress)
		}
		return b.host.Install(ctx, item)
	})
}

func (b Bootstrap) writing(ctx context.Context, standing Reading, items []Item, progress edge.Progress,
	install func(context.Context, Item) error) error {
	for _, item := range items {
		if standing.current(item) {
			say(progress, item.ID()+": "+reasonStanding)
			continue
		}
		if err := install(ctx, item); err != nil {
			return err
		}
		say(progress, "wrote "+item.ID())
	}
	return nil
}

func say(progress edge.Progress, message string) {
	if progress != nil {
		progress.Say(message)
	}
}

func (b Bootstrap) PlanRemove(ctx context.Context, class edge.Class) (provider.Plan, error) {
	removals, err := b.removals(ctx, class)
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
			Kind:   removal.kind,
			Name:   removal.path,
			Action: removal.action,
			Reason: removal.reason,
		})
	}
	group := provider.ChangeGroup{
		Kind:    provider.StackGroupKind,
		Name:    principal,
		Action:  provider.ActionDelete,
		Changes: changes,
	}
	return provider.Plan{Groups: providerkit.Vendored(b.vendor, []provider.ChangeGroup{group})}, nil
}

func (b Bootstrap) Remove(ctx context.Context, class edge.Class, progress edge.Progress) error {
	defer b.host.forgetTiers()
	forget, err := b.host.forgetting(ctx)
	if err != nil {
		return err
	}
	removals, err := b.removals(ctx, class)
	if err != nil {
		return err
	}
	for _, removal := range removals {
		if removal.action != provider.ActionDelete {
			say(progress, "kept "+removal.kind+" "+removal.path)
			continue
		}
		taken, err := b.host.remove(ctx, removal)
		if err != nil {
			return err
		}
		if !taken {
			say(progress, "kept "+removal.kind+" "+removal.path+", still in use")
			continue
		}
		say(progress, "removed "+removal.kind+" "+removal.path)
	}
	say(progress, leavingKnownHosts(forget))
	return nil
}

func leavingKnownHosts(forget string) string {
	return "to drop this host from known_hosts: " + forget
}

const dirHeld = "dir=held"

type removal struct {
	kind   string
	path   string
	reason string
	action provider.ChangeAction
	shared bool
}

func taking(kind, path, reason string) removal {
	return removal{kind: kind, path: path, reason: reason, action: provider.ActionDelete}
}

func sharing(path, reason string) removal {
	return removal{kind: KindDir, path: path, reason: reason, action: provider.ActionDelete, shared: true}
}

func (r removal) command() string {
	switch {
	case r.kind == KindContainer:
		return "docker rm --force " + quoted(r.path)
	case r.kind == KindUnit:
		return unitRemoval(r.path)
	case r.kind == KindApps:
		return "docker ps --all --quiet --filter " + quoted("label="+r.path) + " | xargs -r docker rm --force >/dev/null"
	case r.kind == KindResourceVolumes:
		return "docker volume ls --quiet --filter " + quoted("label="+r.path) + " | xargs -r docker volume rm >/dev/null"
	case r.kind == KindAppNetworks:
		return "for net in $(docker network ls --quiet --filter " + quoted("label="+r.path) + "); do\n" +
			"docker network disconnect --force \"$net\" " + quoted(SwitchboardContainer) + " >/dev/null 2>&1 || true\n" +
			"docker network rm \"$net\" >/dev/null\n" +
			"done"
	case r.kind == KindNetwork:
		return "if ! docker network rm " + quoted(r.path) + " >/dev/null 2>&1 && " +
			"docker network inspect " + quoted(r.path) + " >/dev/null 2>&1; then printf '%s\\n' " + quoted(networkHeld) + "; fi"
	case r.kind == KindRoutingTable || r.kind == KindProxyConfig:
		return routingLocked("-x") + "rm -f " + quoted(r.path)
	case r.shared:
		return "rmdir " + quoted(r.path) + " 2>/dev/null || printf '%s\\n' " + quoted(dirHeld)
	default:
		return "rm -rf " + quoted(r.path)
	}
}

func (b Bootstrap) removals(ctx context.Context, class edge.Class) ([]removal, error) {
	read, err := b.host.Survey(ctx, class)
	if err != nil {
		return nil, err
	}
	sibling, err := b.host.Survey(ctx, other(class))
	if err != nil {
		return nil, err
	}
	apps, err := b.host.appsStanding(ctx, class)
	if err != nil {
		return nil, err
	}
	return removing(read, sibling, apps), nil
}

const (
	KindApps        = "docker:app-containers"
	KindAppNetworks = "docker:app-networks"

	KindResourceVolumes = "docker:resource-volumes"
)

type appsStanding struct{ containers, networks, volumes bool }

func classSelector(class edge.Class) string { return LabelClass + "=" + string(class) }

func appsProbe(class edge.Class) string {
	filter := quoted("label=" + classSelector(class))
	return "if command -v " + quoted(dockerEngine) + " >/dev/null 2>&1; then\n" +
		"if [ -n \"$(docker ps --all --quiet --filter " + filter + " 2>/dev/null)\" ]; then echo containers; fi\n" +
		"if [ -n \"$(docker network ls --quiet --filter " + filter + " 2>/dev/null)\" ]; then echo networks; fi\n" +
		"if [ -n \"$(docker volume ls --quiet --filter " + filter + " 2>/dev/null)\" ]; then echo volumes; fi\n" +
		"fi"
}

func (h *Host) appsStanding(ctx context.Context, class edge.Class) (appsStanding, error) {
	said, err := h.run(ctx, "ask what "+string(class)+" still runs", appsProbe(class), nil)
	if err != nil {
		return appsStanding{}, err
	}
	return appsStanding{
		containers: strings.Contains(said, "containers"),
		networks:   strings.Contains(said, "networks"),
		volumes:    strings.Contains(said, "volumes"),
	}, nil
}

func appsRemoving(class edge.Class, apps appsStanding) []removal {
	var taken []removal
	if apps.containers {
		taken = append(taken, taking(KindApps, classSelector(class),
			""))
	}
	if apps.volumes {
		taken = append(taken, taking(KindResourceVolumes, classSelector(class),
			"resource data, not recoverable"))
	}
	if apps.networks {
		taken = append(taken, taking(KindAppNetworks, classSelector(class),
			""))
	}
	return taken
}

func removing(read, sibling Reading, apps appsStanding) []removal {
	beside := sibling.Class
	last := !sibling.standing(KindDir, ClassDir(beside)) && !sibling.standing(KindDir, StateDir(beside))

	beneath := append(appsRemoving(read.Class, apps),
		taking(KindDir, StateDir(read.Class), "deploy records"),
		taking(KindSealKey, SealKeyPath(read.Class), "sealed values become unreadable"),
		taking(KindFile, sudoersSeal(read.Class), ""),
	)
	stamp := []removal{taking(KindDir, ClassDir(read.Class),
		"")}
	var above []removal
	if last {
		beneath = append(beneath, proxyRemovals()...)
		beneath = append(beneath, liveRemovals()...)
		beneath = append(beneath, backupRemovals()...)
		beneath = append(beneath,
			taking(KindDir, sshDir, ""),
			taking(KindDir, releasesRoot, "images stay"),
			sharing(stateRoot, ""),
			taking(KindUser, deployUser, ""),
			taking(KindFile, recordsHelper, ""),
			taking(KindFile, releasesHelper, ""),
			taking(KindFile, SealHelper, ""),
			taking(KindFile, SwitchboardBinary, ""),
			taking(KindDir, SwitchboardDir, ""),
			sharing(helperRoot, ""),
		)
		above = []removal{sharing(classRoot, "")}
	}
	ordered := slices.Concat(beneath, stamp, above)

	standing := make([]removal, 0, len(ordered))
	for _, candidate := range ordered {
		if candidate.kind == KindApps || candidate.kind == KindResourceVolumes || candidate.kind == KindAppNetworks ||
			read.standing(candidate.kind, candidate.path) || sibling.standing(candidate.kind, candidate.path) {
			standing = append(standing, candidate)
		}
	}
	if len(standing) == 0 {
		return nil
	}
	if read.standing(KindEngine, dockerEngine) || sibling.standing(KindEngine, dockerEngine) {
		return append([]removal{keptEngine()}, standing...)
	}
	return standing
}

func other(class edge.Class) edge.Class {
	if class == edge.ClassProduction {
		return edge.ClassPreview
	}
	return edge.ClassProduction
}
