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
		Class:   read.Class,
		Present: read.Present,
		Held:    read,
		Stacks: []providerkit.BootstrapStack{{
			Name:          principal,
			Present:       read.Present,
			Schema:        uint32(read.Stamp.Schema),
			DigestCurrent: read.settled(),
			Writer:        read.Stamp.Writer,
		}},
	}, nil
}

func (b Bootstrapper) Plan(ctx context.Context, req providerkit.BootstrapRequest) (providerkit.BootstrapPlan, error) {
	read, err := b.reading(ctx, req)
	if err != nil {
		return providerkit.BootstrapPlan{}, err
	}
	described, err := b.described(ctx, read)
	if err != nil {
		return providerkit.BootstrapPlan{}, err
	}
	groups := providerkit.DeriveGroups(described, b.Catalogue(), req)
	groups[0].Changes = planned(read)
	return providerkit.BootstrapPlan{Groups: providerkit.Vendored(groupVendor, groups)}, nil
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
	for _, item := range items {
		if shown.current(item) && !standing.current(item) {
			return providerkit.Refuse(providerkit.CodeInvalid,
				"%s stood as the plan was drawn and no longer does, so this apply would do work nobody consented to.\nRun bootstrap again to see the plan as the host now stands",
				item.ID())
		}
	}

	if req.Unattended {
		if err := admitReplacements(standing, items); err != nil {
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
	stamp.State, stamp.Seal = StateComplete, minted.Seal
	return b.host.Stamp(ctx, req.Class, stamp)
}

func admitReplacements(read Reading, items []Item) error {
	var over []string
	for _, item := range items {
		if !read.current(item) && read.standing(item.Kind, item.Name) {
			over = append(over, item.ID())
		}
	}
	if len(over) == 0 {
		return nil
	}
	return providerkit.Refuse(providerkit.CodeNotReady,
		"%s already stands as something other than what ocel writes, so this apply would write over it rather than install it, and nothing here is present to accept that.\n"+
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
	for _, item := range items {
		if standing.current(item) {
			say(report, item.ID()+": "+reasonStanding)
			continue
		}
		if err := b.host.Install(ctx, item); err != nil {
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

func (b Bootstrapper) PlanRemoval(ctx context.Context, class providerkit.Class) (providerkit.BootstrapPlan, error) {
	removals, err := b.removals(ctx, class)
	if err != nil || len(removals) == 0 {
		return providerkit.BootstrapPlan{}, err
	}
	principal, err := b.host.Principal(ctx)
	if err != nil {
		return providerkit.BootstrapPlan{}, err
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
	return providerkit.BootstrapPlan{Groups: providerkit.Vendored(groupVendor, []providerkit.ChangeGroup{group})}, nil
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
	if r.shared {
		return "rmdir --ignore-fail-on-non-empty " + quoted(r.path)
	}
	return "rm -rf " + quoted(r.path)
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
		beneath = append(beneath,
			taking(KindDir, sshDir, "the deploy login's own key store, which nothing but ocel ever wrote"),
			sharing(stateRoot),
			taking(KindUser, deployUser, "the login every deploy onto this host runs as"),
			taking(KindFile, sudoersSeal, ""),
			taking(KindFile, recordsHelper, ""),
			taking(KindFile, SealHelper, ""),
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
