package gcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"cloud.google.com/go/storage"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/bootstrapplan"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/googleretry"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/run"
)

const (
	reasonCurrent = "already current"
	reasonShared  = "the %s bootstrap is still installed and shares it"
	reasonAbsent  = "nothing is provisioned here"
)

type bootstrapGate struct{ p *Provider }

func (g bootstrapGate) Parts() []provider.Part { return bootstrap{}.Parts() }

func (g bootstrapGate) CheckHost(context.Context, provider.HostCheckRequest) ([]provider.HostCheck, error) {
	return nil, nil
}

func (g bootstrapGate) RemovePartUser(ctx context.Context, tier environment.Tier, project string, progress progress.Log) error {
	return g.p.removePartUser(ctx, tier, project, progress)
}

func (g bootstrapGate) openBootstrap(ctx context.Context) (bootstrap, error) {
	opened, err := g.p.openClients(ctx)
	if err != nil {
		return bootstrap{}, err
	}
	return bootstrap{
		clients:        opened,
		fronts:         g.p.Edges(),
		stores:         g.p.memorystore(),
		records:        g.p.KeyValues(),
		writtenBy:      g.p.writtenBy,
		surveyed:       &g.p.surveyed,
		pushBinary:     g.p.pushBinary,
		deployAndRoute: g.p.services().DeployAndRoute,
		tearDown:       g.p.services().TearDown,
	}, nil
}

func (g bootstrapGate) Describe(ctx context.Context, tier environment.Tier) (provider.BootstrapDescription, error) {
	b, err := g.openBootstrap(ctx)
	if err != nil {
		return provider.BootstrapDescription{}, err
	}
	return b.Describe(ctx, tier)
}

func (g bootstrapGate) Plan(ctx context.Context, req provider.BootstrapRequest) (provider.Plan, error) {
	b, err := g.openBootstrap(ctx)
	if err != nil {
		return provider.Plan{}, err
	}
	return b.Plan(ctx, req)
}

func (g bootstrapGate) Apply(ctx context.Context, req provider.BootstrapRequest, progress progress.Log) error {
	b, err := g.openBootstrap(ctx)
	if err != nil {
		return err
	}
	return b.Apply(ctx, req, progress)
}

func (g bootstrapGate) PlanRemove(ctx context.Context, tier environment.Tier) (provider.Plan, error) {
	b, err := g.openBootstrap(ctx)
	if err != nil {
		return provider.Plan{}, err
	}
	return b.PlanRemove(ctx, tier)
}

func (g bootstrapGate) Remove(ctx context.Context, tier environment.Tier, progress progress.Log) error {
	b, err := g.openBootstrap(ctx)
	if err != nil {
		return err
	}
	return b.Remove(ctx, tier, progress)
}

type bootstrap struct {
	clients   *gcloud.Clients
	fronts    provider.Edges
	stores    memorystore
	records   records.Store
	writtenBy provider.WrittenBy
	surveyed  *surveys

	pushBinary     func(ctx context.Context, tier environment.Tier, name, ref string, binary []byte, path string) error
	deployAndRoute func(ctx context.Context, s run.Serving, progress progress.Log) (run.Release, error)
	tearDown       func(ctx context.Context, service string, progress progress.Log) error
}

func (b bootstrap) Describe(ctx context.Context, tier environment.Tier) (provider.BootstrapDescription, error) {
	read, err := b.survey(ctx, tier)
	if err != nil {
		return provider.BootstrapDescription{}, err
	}
	description, err := b.described(ctx, read)
	if err != nil {
		return provider.BootstrapDescription{}, err
	}
	edgeStacks, err := b.edgeBootstrapStacks(ctx, read)
	if err != nil {
		return provider.BootstrapDescription{}, err
	}
	description.Stacks = append(description.Stacks, edgeStacks...)
	return description, nil
}

func (b bootstrap) described(ctx context.Context, read survey) (provider.BootstrapDescription, error) {
	items := bootstrapItems(read.Names, read.Tier, read.Emulated)
	stacks := []provider.BootstrapStack{{
		Name:          read.Project + "/" + string(read.Tier),
		Present:       read.Present,
		DigestCurrent: read.Stamp.Digest == digestOf(read.Names.Namespace(), items) && read.current(items),
		WrittenBy:     read.Stamp.Writer,
	}}
	for _, part := range read.Stamp.Parts {
		installed, err := b.partInstalled(ctx, read.Tier, part)
		if err != nil {
			return provider.BootstrapDescription{}, err
		}
		stacks = append(stacks, provider.BootstrapStack{
			Name:          read.Project + "/" + string(read.Tier) + "/" + part,
			Part:          part,
			Present:       installed,
			DigestCurrent: installed,
			WrittenBy:     read.Stamp.Writer,
		})
	}
	b.surveyed.remember(read)
	return provider.BootstrapDescription{
		Tier:       read.Tier,
		Present:    read.Present,
		Unfinished: read.Present && read.Stamp.State != stateComplete,
		Stacks:     stacks,
	}, nil
}

func (b bootstrap) surveyFor(ctx context.Context, tier environment.Tier) (survey, error) {
	if remembered, ok := b.surveyed.recall(tier); ok {
		return remembered, nil
	}
	return b.survey(ctx, tier)
}

type surveys struct {
	mu   sync.Mutex
	read map[environment.Tier]survey
}

func (s *surveys) remember(read survey) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.read == nil {
		s.read = map[environment.Tier]survey{}
	}
	s.read[read.Tier] = read
}

func (s *surveys) recall(tier environment.Tier) (survey, bool) {
	if s == nil {
		return survey{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	read, ok := s.read[tier]
	return read, ok
}

func (s *surveys) forget(tier environment.Tier) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.read, tier)
}

func (b bootstrap) Plan(ctx context.Context, req provider.BootstrapRequest) (provider.Plan, error) {
	named, req := splitRemovals(req)
	read, err := b.surveyFor(ctx, req.Tier)
	if err != nil {
		return provider.Plan{}, err
	}
	if err := b.preflight(ctx, read, req.Parts); err != nil {
		return provider.Plan{}, err
	}
	if err := b.partsFree(ctx, req.Tier, droppedParts(read.Stamp.Parts, req)); err != nil {
		return provider.Plan{}, err
	}
	current, err := b.described(ctx, read)
	if err != nil {
		return provider.Plan{}, err
	}
	groups := bootstrapplan.ChangeGroups(current, credentialParts(), req)
	groups[0].Changes = planned(read, stackItems(read.Names, read.Tier, read.Emulated))

	params := provider.ChangeGroup{
		Kind:    provider.ParameterGroupKind,
		Name:    provider.ParameterGroupKind,
		Changes: planned(read, parameterItems(read.Names, read.Tier)),
	}
	params.Action, params.Reason = provider.RollUp(params.Changes)
	fronts, err := b.plannedFronts(ctx, req.Tier, req.Parts)
	if err != nil {
		return provider.Plan{}, err
	}
	removed, err := b.planRemovedParts(ctx, req.Tier, named, req.Force)
	if err != nil {
		return provider.Plan{}, err
	}
	return provider.Plan{Groups: slices.Concat(bootstrapplan.PrefixWithVendor(Vendor, append(append(groups, fronts...), params)), removed)}, nil
}

func planned(read survey, items []item) []provider.Change {
	changes := make([]provider.Change, 0, len(items))
	for _, item := range items {
		change := provider.Change{
			Kind:   string(item.Kind),
			Name:   item.Name,
			Action: provider.ActionCreate,
			Reason: item.Note,
			Slow:   item.Slow,
		}
		switch {
		case read.replaces(item) != "":
			change.Action, change.Reason = provider.ActionReplace, read.replaces(item)
		case read.mends(item) != "":
			change.Action, change.Reason = provider.ActionUpdate, read.mends(item)
		case item.Shared && read.sibling && read.has(item):
			change.Action, change.Reason, change.Slow = provider.ActionKeep, sharedWith(read.Tier), false
		case read.Emulated && item.kind().emulatorKeeps != "":
			change.Action, change.Reason, change.Slow = provider.ActionKeep, item.kind().emulatorKeeps, false
		case read.has(item):
			change.Action, change.Reason, change.Slow = provider.ActionKeep, reasonCurrent, false
		}
		changes = append(changes, change)
	}
	return changes
}

func sharedWith(tier environment.Tier) string {
	return fmt.Sprintf(reasonShared, tier.Sibling())
}

func (b bootstrap) Apply(ctx context.Context, req provider.BootstrapRequest, progress progress.Log) error {
	named, req := splitRemovals(req)
	read, err := b.surveyFor(ctx, req.Tier)
	if err != nil {
		return err
	}
	defer b.surveyed.forget(req.Tier)
	items := bootstrapItems(read.Names, req.Tier, read.Emulated)
	if req.RefuseReplacements {
		if err := refuseReplacements(read, items); err != nil {
			return err
		}
	}
	if err := b.preflight(ctx, read, req.Parts); err != nil {
		return err
	}

	stampBucket := item{Kind: KindBucket, Name: read.Names.Bucket(req.Tier)}
	if err := b.provision(ctx, read, stampItem(items, stampBucket), progress); err != nil {
		return err
	}
	if err := b.dropParts(ctx, read, req, progress); err != nil {
		return err
	}
	if err := b.removeNamedParts(ctx, req.Tier, named, req.Force, progress); err != nil {
		return err
	}

	written := stamp{
		State:  stateApplying,
		Writer: req.WrittenBy.String(),
		Digest: digestOf(read.Names.Namespace(), items),
		Parts:  installedParts(credentialParts(), read.Stamp.Parts, req),
	}
	generation, err := b.stampWith(ctx, read, written, read.Generation)
	if err != nil {
		return err
	}
	for _, item := range items {
		if item.ID() == stampBucket.ID() {
			continue
		}
		if err := b.provision(ctx, read, item, progress); err != nil {
			return err
		}
	}
	if err := b.raiseParts(ctx, req, progress); err != nil {
		return err
	}
	written.State = stateComplete
	if _, err := b.stampWith(ctx, read, written, generation); err != nil {
		return err
	}
	if err := b.deleteUnusedAccounts(ctx, req, progress); err != nil {
		return err
	}
	repaired, err := b.repairParts(ctx, req.Tier)
	for _, part := range repaired {
		gcloud.EnsureProgress(progress).Say("Checked the " + part + " part against this build, mending any drift")
	}
	return err
}

func refuseReplacements(read survey, items []item) error {
	var replaced []string
	for _, item := range items {
		if reason := read.replaces(item); reason != "" {
			replaced = append(replaced, item.phrase()+": "+reason)
		}
	}
	if len(replaced) == 0 {
		return nil
	}
	return refusal.Refuse(refusal.CodeNotReady,
		"bootstrapping the %s tier would replace %s\nRestore what it would replace, or re-run with --yes to replace it",
		read.Tier, strings.Join(replaced, "; "))
}

func stampItem(items []item, stampBucket item) item {
	if at := slices.IndexFunc(items, func(each item) bool { return each.ID() == stampBucket.ID() }); at >= 0 {
		return items[at]
	}
	return stampBucket
}

func (b bootstrap) provision(ctx context.Context, read survey, target item, progress progress.Log) error {
	if mends := read.mends(target); mends != "" {
		if err := target.kind().mendOrMake(ctx, b, read, target); err != nil {
			return err
		}
		gcloud.EnsureProgress(progress).Say("Mended " + target.phrase() + ": " + mends)
		return nil
	}
	if read.has(target) {
		gcloud.EnsureProgress(progress).Debug("The " + target.phrase() + " is " + reasonCurrent)
		return nil
	}
	if err := target.kind().make(ctx, b, read, target); err != nil {
		return err
	}
	gcloud.EnsureProgress(progress).Say("Created " + target.phrase())
	return nil
}

func (b bootstrap) stampWith(ctx context.Context, read survey, written stamp, generation int64) (int64, error) {
	client, err := b.clients.Storage()
	if err != nil {
		return 0, err
	}
	body, err := json.MarshalIndent(written, "", "  ")
	if err != nil {
		return 0, fmt.Errorf("write %s: %w", StampObject, err)
	}
	object := client.Bucket(read.Names.Bucket(read.Tier)).Object(StampObject).If(onlyWriter(generation))
	writer := object.NewWriter(ctx)
	if _, err := writer.Write(append(body, '\n')); err != nil {
		writer.Close()
		return 0, b.stampRefusal(ctx, read, err)
	}
	if err := writer.Close(); err != nil {
		return 0, b.stampRefusal(ctx, read, err)
	}
	return writer.Attrs().Generation, nil
}

func onlyWriter(generation int64) storage.Conditions {
	if generation == 0 {
		return storage.Conditions{DoesNotExist: true}
	}
	return storage.Conditions{GenerationMatch: generation}
}

func (b bootstrap) stampRefusal(ctx context.Context, read survey, err error) error {
	if googleretry.AnsweredCode(err) != http.StatusPreconditionFailed {
		return fmt.Errorf("write %s: %w", StampObject, err)
	}
	read.Stamp.Writer = b.writerNow(ctx, read)
	return refusal.Refuse(refusal.CodeBusy,
		"%s wrote %s in %s while this run was writing it, and two bootstraps of the same tier interleaving leave a stack neither of them describes.\n"+
			"Wait for that run to finish, then try again",
		writerNamed(read.Stamp.Writer), StampObject, read.Names.Bucket(read.Tier))
}

func (b bootstrap) writerNow(ctx context.Context, read survey) string {
	now, err := b.stamped(ctx, read.Names.Bucket(read.Tier))
	if err != nil || !now.present {
		return read.Stamp.Writer
	}
	return now.stamp.Writer
}

func writerNamed(writer string) string {
	if writer == "" {
		return "another run"
	}
	return writer
}

type removal struct {
	item         item
	action       provider.ChangeAction
	reason       string
	destroyAfter time.Duration
}

func (b bootstrap) PlanRemove(ctx context.Context, tier environment.Tier) (provider.Plan, error) {
	read, err := b.survey(ctx, tier)
	if err != nil {
		return provider.Plan{}, err
	}
	if err := b.partsFree(ctx, tier, read.Stamp.Parts); err != nil {
		return provider.Plan{}, err
	}
	if err := b.refusePartsInUse(ctx, tier); err != nil {
		return provider.Plan{}, err
	}
	parts, err := b.listPartRemovals(ctx, tier)
	if err != nil {
		return provider.Plan{}, err
	}
	stack, params := provider.ChangeGroup{
		Kind:   provider.StackGroupKind,
		Name:   read.Project + "/" + string(tier),
		Action: provider.ActionDelete,
	}, provider.ChangeGroup{
		Kind:   provider.ParameterGroupKind,
		Name:   provider.ParameterGroupKind,
		Action: provider.ActionDelete,
	}
	bastions, err := b.bastionRemovals(ctx, tier)
	if err != nil {
		return provider.Plan{}, err
	}
	syncs, err := b.envSourceSyncRemovals(ctx, tier)
	if err != nil {
		return provider.Plan{}, err
	}
	stack.Changes = slices.Concat(syncs, bastions)
	for _, taking := range removals(read) {
		change := provider.Change{
			Kind:   string(taking.item.Kind),
			Name:   taking.item.Name,
			Action: taking.action,
			Reason: taking.reason,
			Slow:   taking.item.Slow,
		}
		if taking.item.kind().parameter {
			params.Changes = append(params.Changes, change)
			continue
		}
		stack.Changes = append(stack.Changes, change)
	}
	params.Action, params.Reason = provider.RollUp(params.Changes)
	fronts, err := b.removedFronts(ctx, tier, read.Stamp.Parts)
	if err != nil {
		return provider.Plan{}, err
	}
	return provider.Plan{Groups: slices.Concat(parts, bootstrapplan.PrefixWithVendor(Vendor, append([]provider.ChangeGroup{stack, params}, fronts...)))}, nil
}

var removalOrder = []Kind{
	KindSecret, KindGrant, KindKey, KindKeyRing, KindRole, KindQueue, KindDatabase, KindServiceAccount, KindRepository, KindBucket,
}

func removals(read survey) []removal {
	stampBucket := item{Kind: KindBucket, Name: read.Names.Bucket(read.Tier)}
	rank := func(target item) int {
		if target.ID() == stampBucket.ID() {
			return len(removalOrder)
		}
		return slices.Index(removalOrder, target.Kind)
	}
	items := bootstrapItems(read.Names, read.Tier, read.Emulated)
	slices.SortStableFunc(items, func(a, b item) int { return rank(a) - rank(b) })
	out := make([]removal, 0, len(items))
	for _, target := range items {
		out = append(out, removing(read, target))
	}
	return out
}

func removing(read survey, target item) removal {
	taking := deleting(read, target)
	if kept := target.kind().removal; kept != nil {
		taking = kept(read, target)
	}
	if taking.action != provider.ActionKeep && !read.has(taking.item) {
		taking.action, taking.reason = provider.ActionKeep, reasonAbsent
	}
	return taking
}

func deleting(read survey, target item) removal {
	if target.Shared && read.sibling {
		return removal{item: target, action: provider.ActionKeep, reason: sharedWith(read.Tier)}
	}
	return removal{item: target, action: provider.ActionDelete}
}

func (b bootstrap) Remove(ctx context.Context, tier environment.Tier, progress progress.Log) error {
	defer b.surveyed.forget(tier)
	read, err := b.survey(ctx, tier)
	if err != nil {
		return err
	}
	if read.stateOf != "" {
		return refusal.Refuse(refusal.CodeNotReady,
			"%s still stores the state of %s, and removing the bucket a stack is recorded in strands what that stack provisioned.\n"+
				"Run `%s` in every project deployed here first",
			read.Names.StateBucket(tier), read.stateOf, destroyIn(tier))
	}
	if err := b.partsFree(ctx, tier, read.Stamp.Parts); err != nil {
		return err
	}
	if err := b.refusePartsInUse(ctx, tier); err != nil {
		return err
	}
	if read.Present {
		leaving := read.Stamp
		leaving.State = stateRemoving
		if _, err := b.stampWith(ctx, read, leaving, read.Generation); err != nil {
			return err
		}
	}
	if err := b.tearParts(ctx, tier, read.Stamp.Parts); err != nil {
		return err
	}
	if err := b.removeParts(ctx, tier, progress); err != nil {
		return err
	}
	if err := b.removeEnvSourceSync(ctx, tier, progress); err != nil {
		return err
	}
	if err := b.removeBastion(ctx, tier, progress); err != nil {
		return err
	}
	for _, taking := range removals(read) {
		if leave := taking.item.kind().leave; leave != nil {
			if err := leave(ctx, b, read, taking); err != nil {
				return err
			}
		}
		if taking.action != provider.ActionKeep {
			var err error
			if taking.destroyAfter, err = taking.item.kind().take(ctx, b, read, taking.item); err != nil {
				return err
			}
		}
		taking.report(progress)
	}
	return nil
}

func (r removal) report(progress progress.Log) {
	progress = gcloud.EnsureProgress(progress)
	switch {
	case r.action == provider.ActionKeep && r.reason == reasonAbsent:
		progress.Debug("Nothing to remove: " + r.item.phrase() + " does not exist")
	case r.action == provider.ActionKeep:
		progress.Say("Kept " + r.item.phrase() + ": " + r.reason)
	case r.item.kind().taken != nil:
		progress.Say(r.item.kind().taken(r))
	default:
		progress.Say("Removed " + r.item.phrase())
	}
}

func destroyIn(tier environment.Tier) string {
	if tier == environment.TierPreview {
		return "ocel destroy preview"
	}
	return "ocel destroy production"
}
