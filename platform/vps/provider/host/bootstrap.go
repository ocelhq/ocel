package host

import (
	"context"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

const groupVendor providerkit.Vendor = "vps"

const reasonStanding = "already current"

type Bootstrapper struct{ host *Host }

func Bootstrap(h *Host) Bootstrapper { return Bootstrapper{host: h} }

func (b Bootstrapper) Catalogue() []providerkit.Feature { return nil }

func (b Bootstrapper) Describe(ctx context.Context, class providerkit.Class) (providerkit.Bootstrap, error) {
	read, err := b.host.Read(ctx, class)
	if err != nil {
		return providerkit.Bootstrap{}, err
	}
	return b.described(ctx, read)
}

func (b Bootstrapper) described(ctx context.Context, read Reading) (providerkit.Bootstrap, error) {
	principal, err := b.host.Principal(ctx)
	if err != nil {
		return providerkit.Bootstrap{}, err
	}
	return providerkit.Bootstrap{
		Class:      read.Class,
		Present:    read.Present,
		Unfinished: read.unfinished(),
		Held:       read,
		Stacks: []providerkit.BootstrapStack{{
			Name:          principal,
			Present:       read.Present,
			Schema:        uint32(read.Stamp.Schema),
			DigestCurrent: read.settled(),
			Writer:        read.Stamp.Writer,
		}},
	}, nil
}

func (b Bootstrapper) Plan(ctx context.Context, req providerkit.BootstrapRequest) (providerkit.Plan, error) {
	read, err := b.reading(ctx, req)
	if err != nil {
		return providerkit.Plan{}, err
	}
	described, err := b.described(ctx, read)
	if err != nil {
		return providerkit.Plan{}, err
	}
	groups := providerkit.DeriveGroups(described, b.Catalogue(), req)
	groups[0].Changes = planned(read)
	return providerkit.Plan{Groups: providerkit.Vendored(groupVendor, groups)}, nil
}

func planned(read Reading) []providerkit.Change {
	items := Items(read.Class, read.Keys)
	changes := make([]providerkit.Change, 0, len(items))
	for _, item := range items {
		change := providerkit.Change{
			Kind:   item.Kind,
			Name:   item.Name,
			Action: providerkit.ActionCreate,
			Reason: item.Note,
			Slow:   item.Slow,
		}
		switch {
		case read.current(item):
			change.Action, change.Reason = providerkit.ActionKeep, reasonStanding
		case read.standing(item.Kind, item.Name):
			change.Action = providerkit.ActionUpdate
			if change.Reason == "" {
				change.Reason = "not as this ocel writes it"
			}
		}
		changes = append(changes, change)
	}
	return slowLast(changes)
}

func itemPlan(read Reading) providerkit.Plan {
	return providerkit.Plan{Groups: []providerkit.ChangeGroup{{
		Kind:    providerkit.StackGroupKind,
		Name:    string(read.Class),
		Changes: planned(read),
	}}}
}

func slowLast(changes []providerkit.Change) []providerkit.Change {
	slices.SortStableFunc(changes, func(a, b providerkit.Change) int {
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

func (b Bootstrapper) reading(ctx context.Context, req providerkit.BootstrapRequest) (Reading, error) {
	if held, carried := req.Held.(Reading); carried && held.Class == req.Class {
		return held, nil
	}
	return b.host.Read(ctx, req.Class)
}

func (b Bootstrapper) Apply(ctx context.Context, req providerkit.BootstrapRequest, report providerkit.Reporter) error {
	if req.Heal {
		return b.heal(ctx, req, report)
	}
	shown, err := b.reading(ctx, req)
	if err != nil {
		return err
	}
	standing, err := b.host.Read(ctx, req.Class)
	if err != nil {
		return err
	}
	if err := standing.adopting(); err != nil {
		return err
	}
	items := Items(req.Class, standing.Keys)
	if err := providerkit.RefuseGrowth(itemPlan(shown), itemPlan(standing)); err != nil {
		return err
	}

	if req.Unattended {
		if err := refuseReplacements(standing, items); err != nil {
			return err
		}
	}

	stamp := Stamp{
		Schema:  providerkit.BootstrapSchema,
		State:   StateApplying,
		Writer:  req.Writer.String(),
		Digests: digests(items),
	}
	if err := b.write(ctx, standing, ClassItems(req.Class), report); err != nil {
		return err
	}
	if err := b.host.Stamp(ctx, req.Class, stamp); err != nil {
		return err
	}
	if err := b.write(ctx, standing, StorageItems(req.Class, standing.Keys), report); err != nil {
		return err
	}

	minted, err := b.host.Read(ctx, req.Class)
	if err != nil {
		return err
	}
	if minted.Seal.Fingerprint == "" {
		return providerkit.Refuse(providerkit.CodeDenied,
			"%s stands with no seal key, and a class that seals nothing is a class no deploy can hold a value for",
			req.Class)
	}
	held := Reading{Class: req.Class, Present: true, Seal: minted.Seal, Stamp: standing.Stamp}
	if err := held.adopting(); err != nil {
		return err
	}
	if err := b.write(ctx, standing, EngineItems(), report); err != nil {
		return err
	}
	if err := b.write(ctx, standing, ProxyItems(), report); err != nil {
		return err
	}
	stamp.State, stamp.Seal = StateComplete, minted.Seal
	return b.host.Stamp(ctx, req.Class, stamp)
}

func (b Bootstrapper) heal(ctx context.Context, req providerkit.BootstrapRequest, report providerkit.Reporter) error {
	read, err := b.host.Own(ctx, req.Class)
	if err != nil {
		return err
	}
	work, err := healing(read, req.Unattended)
	if err != nil {
		return err
	}
	return b.writing(ctx, read, work, report, b.host.Reassert)
}

func healing(read Reading, unattended bool) ([]Item, error) {
	work, err := healable(read)
	if err != nil {
		return nil, err
	}
	if unattended {
		if err := refuseReplacements(read, work); err != nil {
			return nil, err
		}
	}
	return work, nil
}

func healable(read Reading) ([]Item, error) {
	command := providerkit.BootstrapCommand(read.Class)
	if !read.Present {
		return nil, providerkit.Refuse(providerkit.CodeDenied,
			"nothing has bootstrapped the %s class on this host, and heal reasserts what a bootstrap wrote rather than writing one.\nRun `%s`",
			read.Class, command)
	}
	if read.unfinished() {
		return nil, providerkit.Refuse(providerkit.CodeDenied,
			"%s records an apply that never finished, and heal finishes nothing it did not start.\nRun `%s` to plan the work that is left and finish it",
			StampPath(read.Class), command)
	}
	if err := read.adopting(); err != nil {
		return nil, err
	}
	var work []Item
	var denied []string
	for _, item := range Items(read.Class, read.Keys) {
		if read.current(item) {
			continue
		}
		if deployOwned(item) {
			work = append(work, item)
			continue
		}
		if !read.standing(item.Kind, item.Name) {
			continue
		}
		denied = append(denied, item.ID())
	}
	if len(denied) > 0 {
		return nil, providerkit.Refuse(providerkit.CodeDenied,
			"heal writes what %s owns under %s and nothing else, and this one would write %s.\nRun `%s` as the login that bootstrapped this host",
			deployUser, stateRoot, strings.Join(denied, ", "), command)
	}
	return work, nil
}

func deployOwned(item Item) bool {
	if item.Kind != KindDir && item.Kind != KindFile {
		return false
	}
	if beneath(sshDir, item.Name) || beneath(proxyRoot, item.Name) {
		return false
	}
	return item.Owner == stateOwner && beneath(stateRoot, item.Name)
}

func beneath(root, name string) bool {
	return name == root || strings.HasPrefix(name, root+"/")
}

func replacing(item Item) bool {
	switch item.Kind {
	case KindDir, KindUnit, KindNetwork, KindVolume, KindContainer:
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
	return providerkit.Refuse(providerkit.CodeNotReady,
		"%s already stands as something other than what ocel writes, so this apply would write over it rather than install it, and what is there now does not survive that.\n"+
			"Re-run with --yes to write it anyway",
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
	return providerkit.Refuse(providerkit.CodeInvalid,
		"%s records the seal key %s and %s now holds %s.\n"+
			"Every value this class holds was sealed to the key the stamp records, and nothing opens them under another: "+
			"this apply will not adopt the key that replaced it.\n"+
			"Put the recorded key back, or `ocel destroy` the class and seal its values again",
		StampPath(r.Class), recorded, SealKeyPath(r.Class), standing)
}

func (b Bootstrapper) write(ctx context.Context, standing Reading, items []Item, report providerkit.Reporter) error {
	return b.writing(ctx, standing, items, report, b.host.Install)
}

func (b Bootstrapper) writing(ctx context.Context, standing Reading, items []Item, report providerkit.Reporter,
	install func(context.Context, Item) error) error {
	for _, item := range items {
		if standing.current(item) {
			say(report, item.ID()+": "+reasonStanding)
			continue
		}
		if err := install(ctx, item); err != nil {
			return err
		}
		say(report, "wrote "+item.ID())
	}
	return nil
}

func say(report providerkit.Reporter, message string) {
	if report != nil {
		report.Say(message)
	}
}

func (b Bootstrapper) PlanRemoval(ctx context.Context, class providerkit.Class) (providerkit.Plan, error) {
	removals, err := b.removals(ctx, class)
	if err != nil || len(removals) == 0 {
		return providerkit.Plan{}, err
	}
	principal, err := b.host.Principal(ctx)
	if err != nil {
		return providerkit.Plan{}, err
	}
	changes := make([]providerkit.Change, 0, len(removals))
	for _, removal := range removals {
		changes = append(changes, providerkit.Change{
			Kind:   removal.kind,
			Name:   removal.path,
			Action: removal.action,
			Reason: removal.reason,
		})
	}
	group := providerkit.ChangeGroup{
		Kind:    providerkit.StackGroupKind,
		Name:    principal,
		Action:  providerkit.ActionDelete,
		Changes: changes,
	}
	return providerkit.Plan{Groups: providerkit.Vendored(groupVendor, []providerkit.ChangeGroup{group})}, nil
}

func (b Bootstrapper) Remove(ctx context.Context, class providerkit.Class, report providerkit.Reporter) error {
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
		if removal.action != providerkit.ActionDelete {
			say(report, "kept "+removal.kind+" "+removal.path)
			continue
		}
		if err := b.host.remove(ctx, removal); err != nil {
			return err
		}
		say(report, "removed "+removal.kind+" "+removal.path)
	}
	say(report, leavingKnownHosts(forget))
	return nil
}

func leavingKnownHosts(forget string) string {
	return "your known_hosts is as ocel found it; to drop this host's key from it yourself, run: " + forget
}

type removal struct {
	kind   string
	path   string
	reason string
	action providerkit.ChangeAction
	shared bool
}

func taking(kind, path, reason string) removal {
	return removal{kind: kind, path: path, reason: reason, action: providerkit.ActionDelete}
}

func sharing(path string) removal {
	return removal{kind: KindDir, path: path, action: providerkit.ActionDelete, shared: true}
}

func (r removal) command() string {
	switch {
	case r.kind == KindContainer:
		return "docker rm --force " + quoted(r.path)
	case r.kind == KindVolume:
		return "docker volume rm " + quoted(r.path)
	case r.kind == KindNetwork:
		return "docker network rm " + quoted(r.path) + " || true"
	case r.shared:
		return "rmdir --ignore-fail-on-non-empty " + quoted(r.path)
	default:
		return "rm -rf " + quoted(r.path)
	}
}

func (b Bootstrapper) removals(ctx context.Context, class providerkit.Class) ([]removal, error) {
	read, err := b.host.Survey(ctx, class)
	if err != nil {
		return nil, err
	}
	sibling, err := b.host.Survey(ctx, other(class))
	if err != nil {
		return nil, err
	}
	return removing(read, sibling), nil
}

func removing(read, sibling Reading) []removal {
	beside := sibling.Class
	last := !sibling.standing(KindDir, ClassDir(beside)) && !sibling.standing(KindDir, StateDir(beside))

	beneath := []removal{
		taking(KindDir, StateDir(read.Class), "every record ocel holds for this class on this host, and nothing writes them again"),
		taking(KindSealKey, SealKeyPath(read.Class), "the key every value this class holds was sealed to, and no other machine ever held it: what it sealed, nothing opens again"),
	}
	stamp := []removal{taking(KindDir, ClassDir(read.Class),
		"the stamp that says what this host carries, taken last so an interrupted destroy leaves a host that still says what it is")}
	var above []removal
	if last {
		beneath = append(beneath, proxyRemovals()...)
		beneath = append(beneath,
			taking(KindDir, sshDir, "the deploy login's own key store, which nothing but ocel ever wrote"),
			sharing(stateRoot),
			taking(KindUser, deployUser, "the login every deploy onto this host runs as"),
			taking(KindFile, sudoersSeal, ""),
			taking(KindFile, recordsHelper, ""),
			taking(KindFile, SealHelper, ""),
			taking(KindFile, ProxyHelper, ""),
			sharing(helperRoot),
		)
		above = []removal{sharing(classRoot)}
	}
	ordered := slices.Concat(beneath, stamp, above)

	standing := make([]removal, 0, len(ordered))
	for _, candidate := range ordered {
		if read.standing(candidate.kind, candidate.path) || sibling.standing(candidate.kind, candidate.path) {
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

func other(class providerkit.Class) providerkit.Class {
	if class == providerkit.ClassProduction {
		return providerkit.ClassPreview
	}
	return providerkit.ClassProduction
}
