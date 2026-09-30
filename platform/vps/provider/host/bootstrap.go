package host

import (
	"context"
	"path/filepath"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/bootstrapplan"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/vps/provider/boxstore"
	"github.com/ocelhq/ocel/platform/vps/provider/live"
	"github.com/ocelhq/ocel/platform/vps/provider/switchboard"
)

const (
	reasonCurrent = "already current"
	bootstrapDocs = "https://ocel.dev/docs/providers/vps#bootstrap"
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
	if read.observed(KindRoutingTable, live.RoutingTable) || read.observed(KindProxyConfig, ProxyConfig) {
		if read.rerendering, err = b.host.proxyInspected(ctx, read.Tier); err != nil {
			return provider.BootstrapDescription{}, err
		}
	}
	return provider.BootstrapDescription{
		Tier:        read.Tier,
		Present:     read.Present,
		Unfinished:  read.unfinished(),
		VendorState: read,
		Stacks: []provider.BootstrapStack{{
			Name:          principal,
			Present:       read.Present,
			DigestCurrent: read.upToDate(),
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
	read = described.VendorState.(Reading)
	if err := read.runnableEngine(b.host.named()); err != nil {
		return provider.Plan{}, err
	}
	if err := b.host.refuseServingPortsHeld(ctx, read); err != nil {
		return provider.Plan{}, err
	}
	groups := bootstrapplan.ChangeGroups(described, b.Catalogue(), req)
	groups[0].Changes = planned(read)
	if read.rerendering && groups[0].Action == provider.ActionKeep {
		groups[0].Action, groups[0].Reason = provider.ActionUpdate, ""
	}
	if groups[0].Reason == "" {
		groups[0].Reason = bootstrapDocs
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
		case read.rerendering && item.Kind == KindProxyConfig:
			change.Action, change.Reason = provider.ActionUpdate, "rendered again from "+live.RoutingTable
		case item.Kind == KindEngine && read.current(item):
			change.Action, change.Reason, change.Slow = provider.ActionAdopt, adoptedEngine(read.Engine.Version), false
		case read.current(item):
			change.Action, change.Reason = provider.ActionKeep, reasonCurrent
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

func (b Bootstrap) reading(ctx context.Context, req provider.BootstrapRequest) (Reading, error) {
	if cached, ok := req.VendorState.(Reading); ok && cached.Tier == req.Tier {
		return cached, nil
	}
	return b.read(ctx, req.Tier)
}

func (b Bootstrap) read(ctx context.Context, tier environment.Tier) (Reading, error) {
	read, err := b.host.Read(ctx, tier)
	if err != nil {
		return Reading{}, err
	}
	return b.recorded(ctx, read)
}

func (b Bootstrap) Apply(ctx context.Context, req provider.BootstrapRequest, progress progress.Log) error {
	if req.Repair {
		return b.repair(ctx, req, progress)
	}
	shown, err := b.reading(ctx, req)
	if err != nil {
		return err
	}
	current, err := b.read(ctx, req.Tier)
	if err != nil {
		return err
	}
	if err := current.adopting(); err != nil {
		return err
	}
	if err := current.runnableEngine(b.host.named()); err != nil {
		return err
	}
	if err := b.host.refuseServingPortsHeld(ctx, current); err != nil {
		return err
	}
	items := current.Items()
	if err := bootstrapplan.RefuseUnconsentedChanges(itemPlan(shown), itemPlan(current)); err != nil {
		return err
	}

	if req.RefuseReplacements {
		if err := refuseReplacements(current, items); err != nil {
			return err
		}
	}

	stamp := Stamp{
		State:   StateApplying,
		Writer:  req.WrittenBy.String(),
		Digests: digests(items),
	}
	if err := b.write(ctx, current, TierItems(req.Tier), progress); err != nil {
		return err
	}
	if err := b.host.Stamp(ctx, req.Tier, stamp); err != nil {
		return err
	}
	if err := b.write(ctx, current, StorageItems(req.Tier, current.Keys), progress); err != nil {
		return err
	}

	minted, err := b.host.Read(ctx, req.Tier)
	if err != nil {
		return err
	}
	if minted.Seal.Fingerprint == "" {
		return refusal.Refuse(refusal.CodeDenied,
			"%s has no seal key",
			req.Tier)
	}
	sealed := Reading{Tier: req.Tier, Present: true, Seal: minted.Seal, Stamp: current.Stamp}
	if err := sealed.adopting(); err != nil {
		return err
	}
	if err := b.write(ctx, current, EngineItems(), progress); err != nil {
		return err
	}
	served, err := b.host.Read(ctx, req.Tier)
	if err != nil {
		return err
	}
	if err := b.write(ctx, served, LiveItems(current.Arch), progress); err != nil {
		return err
	}
	if err := b.write(ctx, served, EnvSourceSyncItems(req.Tier, current.Arch), progress); err != nil {
		return err
	}
	if err := b.host.refuseDirectoryMissing(ctx, current.Front); err != nil {
		return err
	}
	if err := b.write(ctx, served, current.move.recordUnfinished(), progress); err != nil {
		return err
	}
	if err := current.move.removeOldFront(ctx, progress); err != nil {
		return err
	}
	if err := b.write(ctx, served, ProxyItems(current.Arch, current.Front), progress); err != nil {
		return err
	}
	placeRoutes := b.host.rerender
	if current.move != nil {
		placeRoutes = current.move.placeRoutes
	}
	if err := placeRoutes(ctx); err != nil {
		return err
	}
	if err := current.move.awaitFront(ctx, req.Tier, progress); err != nil {
		return err
	}
	if err := b.write(ctx, served, current.recorded, progress); err != nil {
		return err
	}
	if err := b.write(ctx, served, BackupItems(), progress); err != nil {
		return err
	}
	stamp.State, stamp.Seal = StateComplete, minted.Seal
	return b.host.Stamp(ctx, req.Tier, stamp)
}

func (b Bootstrap) repair(ctx context.Context, req provider.BootstrapRequest, progress progress.Log) error {
	read, err := b.host.Own(ctx, req.Tier)
	if err != nil {
		return err
	}
	work, left, err := repairing(read, req.RefuseReplacements)
	if err != nil {
		return err
	}
	for _, item := range left {
		say(progress, "Left "+item.phrase()+" as it is: a refresh rewrites only what deploys own")
	}
	return b.writing(ctx, read, work, progress, b.host.Reassert)
}

func repairing(read Reading, refuse bool) ([]Item, []Item, error) {
	work, left, err := repairable(read)
	if err != nil {
		return nil, nil, err
	}
	if refuse {
		if err := refuseReplacements(read, work); err != nil {
			return nil, nil, err
		}
	}
	return work, left, nil
}

func repairable(read Reading) ([]Item, []Item, error) {
	command := provider.BootstrapCommand(read.Tier)
	if !read.Present {
		return nil, nil, refusal.Refuse(refusal.CodeDenied,
			"the %s tier is not bootstrapped on this host\nRun `%s`",
			read.Tier, command)
	}
	if read.unfinished() {
		return nil, nil, refusal.Refuse(refusal.CodeDenied,
			"%s records an unfinished apply\nRun `%s` to finish it",
			StampPath(read.Tier), command)
	}
	if err := read.adopting(); err != nil {
		return nil, nil, err
	}
	var work, left []Item
	var denied []string
	for _, item := range read.Items() {
		if read.current(item) {
			continue
		}
		if deployOwned(item) {
			work = append(work, item)
			continue
		}
		if daemonState(item) || routingState(item) || rewrittenByDeploys(item) || !read.observed(item.Kind, item.Name) {
			left = append(left, item)
			continue
		}
		denied = append(denied, item.ID())
	}
	if len(denied) > 0 {
		return nil, nil, refusal.Refuse(refusal.CodeDenied,
			"repair cannot write %s\nRun `%s` as the login that bootstrapped this host",
			strings.Join(denied, ", "), command)
	}
	return work, left, nil
}

func daemonState(item Item) bool {
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
	if beneath(sshDir, item.Name) || routingState(item) {
		return false
	}
	return item.Owner == stateOwner && beneath(stateRoot, item.Name)
}

func routingState(item Item) bool {
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

func refuseReplacements(read Reading, items []Item) error {
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
		if r.observed(KindSealKey, SealKeyPath(r.Tier)) {
			return nil
		}
		present = "no key at all"
	}
	return refusal.Refuse(refusal.CodeInvalid,
		"%s records seal key %s, but %s contains %s\nRestore the recorded key, or `ocel destroy` the tier",
		StampPath(r.Tier), recorded, SealKeyPath(r.Tier), present)
}

func (b Bootstrap) write(ctx context.Context, read Reading, items []Item, progress progress.Log) error {
	return b.writing(ctx, read, items, progress, func(ctx context.Context, item Item) error {
		if item.Kind == KindEngine {
			return b.host.installEngine(ctx, progress)
		}
		return b.host.Install(ctx, item)
	})
}

func (b Bootstrap) writing(ctx context.Context, read Reading, items []Item, progress progress.Log,
	install func(context.Context, Item) error) error {
	for _, item := range items {
		if read.current(item) {
			debug(progress, capitalized(item.phrase())+" is "+reasonCurrent)
			continue
		}
		if err := install(ctx, item); err != nil {
			return err
		}
		say(progress, "Installed "+item.phrase())
	}
	return nil
}

func say(progress progress.Log, message string) {
	if progress != nil {
		progress.Say(message)
	}
}

func debug(progress progress.Log, line string) {
	if progress != nil {
		progress.Debug(line)
	}
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
	return provider.Plan{Groups: bootstrapplan.PrefixWithVendor(b.vendor, []provider.ChangeGroup{group})}, nil
}

func (b Bootstrap) Remove(ctx context.Context, tier environment.Tier, progress progress.Log) error {
	defer b.host.forgetTiers()
	forget, err := b.host.forgetting(ctx)
	if err != nil {
		return err
	}
	removals, err := b.removals(ctx, tier)
	if err != nil {
		return err
	}
	for _, removal := range removals {
		if removal.action != provider.ActionDelete {
			say(progress, removal.kept())
			continue
		}
		taken, err := b.host.remove(ctx, removal)
		if err != nil {
			return err
		}
		if !taken {
			say(progress, "Kept "+removal.phrase()+": something else on this box still uses it")
			continue
		}
		say(progress, "Removed "+removal.phrase())
	}
	say(progress, leavingKnownHosts(forget))
	return nil
}

func leavingKnownHosts(forget string) string {
	return "Your known_hosts still trusts this box: to drop it, run `" + forget + "`"
}

const dirNonEmpty = "dir=nonempty"

type removal struct {
	kind    string
	path    string
	reason  string
	action  provider.ChangeAction
	shared  bool
	reload  string
	origins string
}

func (r removal) phrase() string { return phrase(r.kind, r.path) }

func (r removal) kept() string {
	if r.reason == "" {
		return "Kept " + r.phrase()
	}
	return "Kept " + r.phrase() + ": " + r.reason
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
			"docker network inspect " + quoted(r.path) + " >/dev/null 2>&1; then printf '%s\\n' " + quoted(networkInUse) + "; fi"
	case r.kind == KindPlaced:
		unplaced := words(placementCommand(filepath.Dir(r.path), "unplace", r.path)) + " 2>/dev/null || rm -f " + quoted(r.path) + "\n"
		if r.reload != "" {
			unplaced += r.reload + "\n"
		}
		placed := "if [ -e " + quoted(r.path) + " ] || [ -L " + quoted(r.path) + " ]; then\n" + unplaced + "fi"
		if r.origins == "" {
			return placed
		}
		return placed + "\n" + words(placementCommand(r.origins, "unplace-origins")) + " 2>/dev/null || rm -f " + quoted(r.origins) + "/" + switchboard.OriginPrefix + "*.pem"
	case r.kind == KindRoutingTable || r.kind == KindProxyConfig:
		return routingLocked("-x") + "rm -f " + quoted(r.path)
	case r.shared:
		return "rmdir " + quoted(r.path) + " 2>/dev/null || printf '%s\\n' " + quoted(dirNonEmpty)
	default:
		return "rm -rf " + quoted(r.path)
	}
}

func (b Bootstrap) removals(ctx context.Context, tier environment.Tier) ([]removal, error) {
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

const (
	KindApps        = "docker:app-containers"
	KindAppNetworks = "docker:app-networks"

	KindResourceVolumes = "docker:resource-volumes"
)

type appsPresent struct{ containers, networks, volumes bool }

func tierSelector(tier environment.Tier) string { return LabelTier + "=" + string(tier) }

func appsProbe(tier environment.Tier) string {
	filter := quoted("label=" + tierSelector(tier))
	return "if command -v " + quoted(dockerEngine) + " >/dev/null 2>&1; then\n" +
		"if [ -n \"$(docker ps --all --quiet --filter " + filter + " 2>/dev/null)\" ]; then echo containers; fi\n" +
		"if [ -n \"$(docker network ls --quiet --filter " + filter + " 2>/dev/null)\" ]; then echo networks; fi\n" +
		"if [ -n \"$(docker volume ls --quiet --filter " + filter + " 2>/dev/null)\" ]; then echo volumes; fi\n" +
		"fi"
}

func (h *Host) presentApps(ctx context.Context, tier environment.Tier) (appsPresent, error) {
	said, err := h.run(ctx, "ask what "+string(tier)+" still runs", appsProbe(tier), nil)
	if err != nil {
		return appsPresent{}, err
	}
	return appsPresent{
		containers: strings.Contains(said, "containers"),
		networks:   strings.Contains(said, "networks"),
		volumes:    strings.Contains(said, "volumes"),
	}, nil
}

func appsRemoving(tier environment.Tier, apps appsPresent) []removal {
	var taken []removal
	if apps.containers {
		taken = append(taken, taking(KindApps, tierSelector(tier),
			""))
	}
	if apps.volumes {
		taken = append(taken, taking(KindResourceVolumes, tierSelector(tier),
			"resource data, not recoverable"))
	}
	if apps.networks {
		taken = append(taken, taking(KindAppNetworks, tierSelector(tier),
			""))
	}
	return taken
}

func removing(read, sibling Reading, apps appsPresent) []removal {
	beside := sibling.Tier
	last := !sibling.observed(KindDir, TierDir(beside)) && !sibling.observed(KindDir, StateDir(beside))

	beneath := append([]removal{taking(KindUnit, EnvSourceSyncService(read.Tier), "")}, appsRemoving(read.Tier, apps)...)
	beneath = append(beneath,
		taking(KindDir, StateDir(read.Tier), "deploy records"),
		taking(KindSealKey, SealKeyPath(read.Tier), "sealed values become unreadable"),
		taking(KindFile, sudoersSeal(read.Tier), ""),
	)
	stamp := []removal{taking(KindDir, TierDir(read.Tier),
		"")}
	var above []removal
	if last {
		beneath = append(beneath, read.Front.placedRemovals()...)
		beneath = append(beneath, proxyRemovals()...)
		beneath = append(beneath, liveRemovals()...)
		beneath = append(beneath, envSourceSyncRemovals()...)
		beneath = append(beneath, backupRemovals()...)
		beneath = append(beneath,
			taking(KindDir, sshDir, ""),
			taking(KindDir, releasesRoot, "images stay"),
			taking(KindFile, imagesLock, ""),
			sharing(stateRoot, ""),
			taking(KindUser, deployUser, ""),
			taking(KindFile, boxstore.KeyValuesHelper, ""),
			taking(KindFile, releasesHelper, ""),
			taking(KindFile, boxstore.SealHelper, ""),
			taking(KindFile, SwitchboardBinary, ""),
			taking(KindDir, SwitchboardDir, ""),
			sharing(boxstore.Dir, ""),
		)
		above = []removal{sharing(tierRoot, "")}
	}
	ordered := slices.Concat(beneath, stamp, above)

	present := make([]removal, 0, len(ordered))
	for _, candidate := range ordered {
		if candidate.kind == KindApps || candidate.kind == KindResourceVolumes || candidate.kind == KindAppNetworks || candidate.kind == KindPlaced ||
			read.observed(candidate.kind, candidate.path) || sibling.observed(candidate.kind, candidate.path) {
			present = append(present, candidate)
		}
	}
	if len(present) == 0 {
		return nil
	}
	if read.observed(KindEngine, dockerEngine) || sibling.observed(KindEngine, dockerEngine) {
		return append([]removal{keptEngine()}, present...)
	}
	return present
}
