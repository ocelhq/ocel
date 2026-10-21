package bootstrap

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
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/bootstrapplan"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/googleretry"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/run"
)

const (
	reasonCurrent = "already current"
	reasonShared  = "the %s bootstrap is still installed and shares it"
	ReasonAbsent  = "nothing is provisioned here"
)

type Engine struct {
	Clients   *gcloud.Clients
	Edges     provider.Edges
	Stores    gcloud.Memorystore
	Records   records.Store
	WrittenBy provider.WrittenBy
	Surveyed  *Surveys

	PushBinary     func(ctx context.Context, tier environment.Tier, name, ref string, binary []byte, path string) error
	DeployAndRoute func(ctx context.Context, s run.Serving, progress progress.Log) (run.Release, error)
	TearDown       func(ctx context.Context, service string, progress progress.Log) error

	Removable             []RemovablePart
	ReplacementsConsented bool
	Accounts              []Account
	FrontParts            FrontParts
	TakeRecords           func(ctx context.Context, b Engine, tier environment.Tier) error
	SweepAccounts         func(ctx context.Context, b Engine, req provider.BootstrapRequest, progress progress.Log) error
}

func (b Engine) Describe(ctx context.Context, tier environment.Tier) (provider.BootstrapDescription, error) {
	read, err := b.survey(ctx, tier)
	if err != nil {
		return provider.BootstrapDescription{}, err
	}
	description, err := b.Described(ctx, read)
	if err != nil {
		return provider.BootstrapDescription{}, err
	}
	edgeStacks, err := b.EdgeBootstrapStacks(ctx, read)
	if err != nil {
		return provider.BootstrapDescription{}, err
	}
	description.Stacks = append(description.Stacks, edgeStacks...)
	return description, nil
}

func (b Engine) Described(ctx context.Context, read Survey) (provider.BootstrapDescription, error) {
	items := Resources(read.Names, read.Tier, read.Emulated)
	stacks := []provider.BootstrapStack{{
		Name:          read.Project + "/" + string(read.Tier),
		Present:       read.Present,
		DigestCurrent: read.Stamp.Digest == DigestOf(read.Names.Namespace(), items) && read.current(items),
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
	b.Surveyed.Remember(read)
	return provider.BootstrapDescription{
		Tier:       read.Tier,
		Present:    read.Present,
		Unfinished: read.Present && read.Stamp.State != StateComplete,
		Stacks:     stacks,
	}, nil
}

func (b Engine) surveyFor(ctx context.Context, tier environment.Tier) (Survey, error) {
	if remembered, ok := b.Surveyed.recall(tier); ok {
		return remembered, nil
	}
	return b.survey(ctx, tier)
}

type Surveys struct {
	mu   sync.Mutex
	read map[environment.Tier]Survey
}

func (s *Surveys) Remember(read Survey) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.read == nil {
		s.read = map[environment.Tier]Survey{}
	}
	s.read[read.Tier] = read
}

func (s *Surveys) recall(tier environment.Tier) (Survey, bool) {
	if s == nil {
		return Survey{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	read, ok := s.read[tier]
	return read, ok
}

func (s *Surveys) forget(tier environment.Tier) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.read, tier)
}

func (b Engine) Plan(ctx context.Context, req provider.BootstrapRequest) (provider.Plan, error) {
	named, req := b.SplitRemovals(req)
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
	current, err := b.Described(ctx, read)
	if err != nil {
		return provider.Plan{}, err
	}
	groups := bootstrapplan.ChangeGroups(current, CredentialParts(), req)
	groups[0].Changes = Planned(read, StackResources(read.Names, read.Tier, read.Emulated))

	params := provider.ChangeGroup{
		Kind:    provider.ParameterGroupKind,
		Name:    provider.ParameterGroupKind,
		Changes: Planned(read, ParameterResources(read.Names, read.Tier)),
	}
	params.Action, params.Reason = provider.RollUp(params.Changes)
	fronts, err := b.PlannedFronts(ctx, req.Tier, req.Parts)
	if err != nil {
		return provider.Plan{}, err
	}
	added, err := b.PlanFrontedParts(ctx, req)
	if err != nil {
		return provider.Plan{}, err
	}
	removed, err := b.PlanRemovedParts(ctx, req.Tier, named, req.Force)
	if err != nil {
		return provider.Plan{}, err
	}
	return provider.Plan{Groups: slices.Concat(bootstrapplan.PrefixWithVendor(gcloud.Vendor, append(append(groups, fronts...), params)), added, removed)}, nil
}

func Planned(read Survey, items []Resource) []provider.Change {
	changes := make([]provider.Change, 0, len(items))
	for _, item := range items {
		change := provider.Change{
			Kind:   string(item.Kind),
			Name:   read.named(item),
			Action: provider.ActionCreate,
			Reason: item.Note,
			Slow:   item.Slow,
		}
		switch {
		case read.replaces(item) != "":
			change.Action, change.Reason = provider.ActionReplace, read.replaces(item)
		case read.mends(item) != "":
			change.Action, change.Reason = provider.ActionUpdate, read.mends(item)
		case item.Shared && read.Sibling && read.has(item):
			change.Action, change.Reason, change.Slow = provider.ActionKeep, sharedWith(read.Tier), false
		case read.Emulated && item.KindOps().emulatorKeeps != "":
			change.Action, change.Reason, change.Slow = provider.ActionKeep, item.KindOps().emulatorKeeps, false
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

func (b Engine) Apply(ctx context.Context, req provider.BootstrapRequest, progress progress.Log) error {
	named, req := b.SplitRemovals(req)
	read, err := b.surveyFor(ctx, req.Tier)
	if err != nil {
		return err
	}
	defer b.Surveyed.forget(req.Tier)
	items := Resources(read.Names, req.Tier, read.Emulated)
	b.ReplacementsConsented = !req.RefuseReplacements
	if req.RefuseReplacements {
		if err := RefuseReplacements(read, items, "re-run with --yes"); err != nil {
			return err
		}
	}
	if err := b.preflight(ctx, read, req.Parts); err != nil {
		return err
	}

	foremost := foremostItems(read, items)
	for _, item := range foremost {
		if err := b.Provision(ctx, read, item, progress); err != nil {
			return err
		}
	}
	if read.Buckets, err = b.Clients.TierBuckets(ctx, req.Tier, naming.StorageState); err != nil {
		return err
	}
	if err := b.dropParts(ctx, read, req, progress); err != nil {
		return err
	}
	if err := b.RemoveNamedParts(ctx, req.Tier, named, req.Force, progress); err != nil {
		return err
	}

	written := Stamp{
		State:  stateApplying,
		Writer: req.WrittenBy.String(),
		Digest: DigestOf(read.Names.Namespace(), items),
		Parts:  InstalledParts(CredentialParts(), read.Stamp.Parts, req),
	}
	generation, err := b.stampWith(ctx, read, written, read.Generation)
	if err != nil {
		return err
	}
	for _, item := range items {
		if slices.ContainsFunc(foremost, func(first Resource) bool { return first.ID() == item.ID() }) {
			continue
		}
		if err := b.Provision(ctx, read, item, progress); err != nil {
			return err
		}
	}
	if err := b.raiseParts(ctx, req, progress); err != nil {
		return err
	}
	written.State = StateComplete
	if _, err := b.stampWith(ctx, read, written, generation); err != nil {
		return err
	}
	if err := b.SweepAccounts(ctx, b, req, progress); err != nil {
		return err
	}
	repaired, err := b.RepairParts(ctx, req.Tier)
	for _, part := range repaired {
		gcloud.EnsureProgress(progress).Say("Checked the " + part + " part against this build, mending any drift")
	}
	return err
}

func RefuseReplacements(read Survey, items []Resource, consent string) error {
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
		"bootstrapping the %s tier would replace %s\nRestore what it would replace, or %s to replace it",
		read.Tier, strings.Join(replaced, "; "), consent)
}

func stateBucketOf(read Survey) Resource {
	return Resource{Kind: KindBucket, Name: read.Names.TierBucketStem(read.Tier, naming.StorageState), Class: naming.StorageState}
}

func databaseOf(read Survey) Resource {
	return Resource{Kind: KindDatabase, Name: read.Names.RecordsDatabase(read.Tier)}
}

func tierTagOf() Resource {
	return Resource{Kind: KindTierTag, Name: names.TierTagKey, Shared: true}
}

func foremostItems(read Survey, items []Resource) []Resource {
	foremost := make([]Resource, 0, 3)
	for _, first := range []Resource{tierTagOf(), databaseOf(read), stateBucketOf(read)} {
		if at := slices.IndexFunc(items, func(each Resource) bool { return each.ID() == first.ID() }); at >= 0 {
			first = items[at]
		}
		foremost = append(foremost, first)
	}
	return foremost
}

func (b Engine) Provision(ctx context.Context, read Survey, target Resource, progress progress.Log) error {
	if mends := read.mends(target); mends != "" {
		if err := target.KindOps().mendOrMake(ctx, b, read, target); err != nil {
			return err
		}
		if err := b.ensureTierTag(ctx, read, target); err != nil {
			return err
		}
		gcloud.EnsureProgress(progress).Say("Mended " + target.phrase() + ": " + mends)
		return nil
	}
	if read.has(target) {
		gcloud.EnsureProgress(progress).Debug("The " + target.phrase() + " is " + reasonCurrent)
		return nil
	}
	if err := target.KindOps().Make(ctx, b, read, target); err != nil {
		return err
	}
	if err := b.ensureTierTag(ctx, read, target); err != nil {
		return err
	}
	gcloud.EnsureProgress(progress).Say("Created " + target.phrase())
	return nil
}

func (b Engine) stampWith(ctx context.Context, read Survey, written Stamp, generation int64) (int64, error) {
	client, err := b.Clients.Storage()
	if err != nil {
		return 0, err
	}
	body, err := json.MarshalIndent(written, "", "  ")
	if err != nil {
		return 0, fmt.Errorf("write %s: %w", StampObject, err)
	}
	object := client.Bucket(read.Buckets[naming.StorageState]).Object(StampObject).If(onlyWriter(generation))
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

func (b Engine) stampRefusal(ctx context.Context, read Survey, err error) error {
	if googleretry.AnsweredCode(err) != http.StatusPreconditionFailed {
		return fmt.Errorf("write %s: %w", StampObject, err)
	}
	read.Stamp.Writer = b.writerNow(ctx, read)
	return refusal.Refuse(refusal.CodeBusy,
		"%s wrote %s in %s while this run was writing it, and two bootstraps of the same tier interleaving leave a stack neither of them describes.\n"+
			"Wait for that run to finish, then try again",
		writerNamed(read.Stamp.Writer), StampObject, read.Buckets[naming.StorageState])
}

func (b Engine) writerNow(ctx context.Context, read Survey) string {
	now, err := b.stamped(ctx, read.Buckets[naming.StorageState])
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

type Removal struct {
	Resource     Resource
	Action       provider.ChangeAction
	Reason       string
	DestroyAfter time.Duration
}

func (r Removal) change(name string) provider.Change {
	return provider.Change{Kind: string(r.Resource.Kind), Name: name, Action: r.Action, Reason: r.Reason, Slow: r.Resource.Slow}
}

func (b Engine) PlanRemove(ctx context.Context, tier environment.Tier) (provider.Plan, error) {
	read, err := b.survey(ctx, tier)
	if err != nil {
		return provider.Plan{}, err
	}
	if err := b.partsFree(ctx, tier, read.Stamp.Parts); err != nil {
		return provider.Plan{}, err
	}
	if err := b.RefusePartsInUse(ctx, tier); err != nil {
		return provider.Plan{}, err
	}
	parts, err := b.ListPartRemovals(ctx, tier)
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
	for _, each := range b.Removable {
		if each.Plan == nil {
			continue
		}
		changes, err := each.Plan(ctx, b, tier)
		if err != nil {
			return provider.Plan{}, err
		}
		stack.Changes = append(stack.Changes, changes...)
	}
	for _, taking := range Removals(read) {
		change := taking.change(read.named(taking.Resource))
		if taking.Resource.KindOps().parameter {
			params.Changes = append(params.Changes, change)
			continue
		}
		stack.Changes = append(stack.Changes, change)
	}
	params.Action, params.Reason = provider.RollUp(params.Changes)
	fronts, err := b.RemovedFronts(ctx, tier, read.Stamp.Parts)
	if err != nil {
		return provider.Plan{}, err
	}
	return provider.Plan{Groups: slices.Concat(parts, bootstrapplan.PrefixWithVendor(gcloud.Vendor, append([]provider.ChangeGroup{stack, params}, fronts...)))}, nil
}

var removalOrder = []Kind{KindSecret, KindRole, KindRepository, KindBucket}

func Removals(read Survey) []Removal {
	stampBucket := stateBucketOf(read)
	rank := func(target Resource) int {
		switch {
		case target.ID() == stampBucket.ID():
			return len(removalOrder)
		case target.Kind == KindDatabase:
			return len(removalOrder) + 1
		}
		return slices.Index(removalOrder, target.Kind)
	}
	items := Resources(read.Names, read.Tier, read.Emulated)
	slices.SortStableFunc(items, func(a, b Resource) int { return rank(a) - rank(b) })
	out := make([]Removal, 0, len(items))
	for _, target := range items {
		out = append(out, Removing(read, target))
	}
	return out
}

func Removing(read Survey, target Resource) Removal {
	taking := deleting(read, target)
	if kept := target.KindOps().removal; kept != nil {
		taking = kept(read, target)
	}
	if taking.Action != provider.ActionKeep && !read.has(taking.Resource) {
		taking.Action, taking.Reason = provider.ActionKeep, ReasonAbsent
	}
	return taking
}

func deleting(read Survey, target Resource) Removal {
	if target.Shared && read.Sibling {
		return Removal{Resource: target, Action: provider.ActionKeep, Reason: sharedWith(read.Tier)}
	}
	return Removal{Resource: target, Action: provider.ActionDelete}
}

func (b Engine) Remove(ctx context.Context, tier environment.Tier, progress progress.Log) error {
	defer b.Surveyed.forget(tier)
	defer b.Clients.DropTierBucketsRead(tier)
	read, err := b.survey(ctx, tier)
	if err != nil {
		return err
	}
	if read.stateOf != "" {
		return refusal.Refuse(refusal.CodeNotReady,
			"%s still stores the state of %s, and removing the bucket a stack is recorded in strands what that stack provisioned.\n"+
				"Run `%s` in every project deployed here first",
			read.Buckets[naming.StorageState], read.stateOf, destroyIn(tier))
	}
	if err := b.partsFree(ctx, tier, read.Stamp.Parts); err != nil {
		return err
	}
	if err := b.RefusePartsInUse(ctx, tier); err != nil {
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
	if err := b.RemoveParts(ctx, tier, progress); err != nil {
		return err
	}
	for _, each := range b.Removable {
		if each.Take == nil {
			continue
		}
		if err := each.Take(ctx, b, tier, progress); err != nil {
			return err
		}
	}
	for _, taking := range Removals(read) {
		taken, err := b.RemoveResource(ctx, read, taking)
		if err != nil {
			return err
		}
		taken.Report(progress)
	}
	return nil
}

func (b Engine) RemoveResource(ctx context.Context, read Survey, taking Removal) (Removal, error) {
	if leave := taking.Resource.KindOps().leave; leave != nil {
		if err := leave(ctx, b, read, taking); err != nil {
			return Removal{}, err
		}
	}
	if taking.Action == provider.ActionKeep {
		return taking, nil
	}
	if err := b.removeTierTag(ctx, read, taking.Resource); err != nil {
		return Removal{}, err
	}
	var err error
	if taking.DestroyAfter, err = taking.Resource.KindOps().Take(ctx, b, read, taking.Resource); err != nil {
		return Removal{}, err
	}
	return taking, nil
}

func (r Removal) Report(progress progress.Log) {
	progress = gcloud.EnsureProgress(progress)
	switch {
	case r.Action == provider.ActionKeep && r.Reason == ReasonAbsent:
		progress.Debug("Nothing to remove: " + r.Resource.phrase() + " does not exist")
	case r.Action == provider.ActionKeep:
		progress.Say("Kept " + r.Resource.phrase() + ": " + r.Reason)
	case r.Resource.KindOps().taken != nil:
		progress.Say(r.Resource.KindOps().taken(r))
	default:
		progress.Say("Removed " + r.Resource.phrase())
	}
}

func destroyIn(tier environment.Tier) string {
	if tier == environment.TierPreview {
		return "ocel projects destroy --tier preview"
	}
	return "ocel projects destroy --tier production"
}
