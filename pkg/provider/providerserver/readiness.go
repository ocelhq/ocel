package providerserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/bootstrapplan"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

type Readiness struct {
	Bootstrap provider.Bootstrap
	KeyValues keyvalue.Store
	WrittenBy provider.WrittenBy
	Edge      edge.Kind
	Statuses  *BootstrapStatuses
}

type BootstrapStatus struct {
	Tier        environment.Tier
	Present     bool
	Stacks      []provider.BootstrapStack
	Parts       []string
	WrittenBy   provider.WrittenBy
	Unfinished  bool
	VendorState any
}

func (r Readiness) Status(ctx context.Context, tier environment.Tier) (BootstrapStatus, error) {
	described, err := r.Bootstrap.Describe(ctx, tier)
	if err != nil {
		return BootstrapStatus{}, err
	}
	status := BootstrapStatus{Tier: tier, Present: described.Present, Stacks: described.Stacks,
		Unfinished: described.Unfinished, VendorState: described.VendorState}
	var present []string
	for _, stack := range described.Stacks {
		if stack.Part == "" {
			status.WrittenBy = provider.WrittenBy(stack.WrittenBy)
			continue
		}
		if stack.Present {
			present = append(present, stack.Part)
		}
	}
	status.Parts = bootstrapplan.InOfferedOrder(r.Bootstrap.Parts(), present)
	return status, nil
}

func (s BootstrapStatus) Stale(required []string) []string {
	var out []string
	for _, stack := range s.Stacks {
		if !stack.Present || stack.DigestCurrent || stack.ReadError != "" {
			continue
		}
		if stack.Part != "" && !slices.Contains(required, stack.Part) {
			continue
		}
		out = append(out, stack.Name)
	}
	return out
}

func (s BootstrapStatus) Unreadable(required []string) []string {
	var out []string
	for _, stack := range s.Stacks {
		if stack.ReadError == "" {
			continue
		}
		if stack.Part != "" && !slices.Contains(required, stack.Part) {
			continue
		}
		out = append(out, stack.Name+" ("+stack.ReadError+")")
	}
	return out
}

func (s BootstrapStatus) Downgrade(writing provider.WrittenBy) bool {
	return s.WrittenBy.Newer(writing)
}

type ApplyRequest struct {
	Remove []string

	Force bool

	AcceptReplacements bool
}

type partChange struct {
	status    BootstrapStatus
	requested []string
	removing  []string
	wants     map[string][]string
}

func (r Readiness) resolvePartChange(ctx context.Context, status BootstrapStatus, req ApplyRequest) (partChange, error) {
	offered := r.Bootstrap.Parts()
	removing, err := bootstrapplan.PartsToRemove(offered, status.Parts, req.Remove)
	if err != nil {
		return partChange{}, err
	}
	if err := r.refuseRemovingEdgePart(offered, removing); err != nil {
		return partChange{}, err
	}
	wants, err := r.wantedParts(ctx, status.Tier, offered, removing)
	if err != nil {
		return partChange{}, err
	}
	var requested []string
	if edgePart := bootstrapplan.PartFrontingEdge(offered, r.Edge); edgePart != "" {
		requested = append(requested, edgePart)
	}
	for _, part := range slices.Sorted(maps.Keys(wants)) {
		if !slices.Contains(requested, part) {
			requested = append(requested, part)
		}
	}
	requested = bootstrapplan.InOfferedOrder(offered, requested)
	return partChange{status: status, requested: requested, removing: removing, wants: wants}, nil
}

func (r Readiness) wantedParts(ctx context.Context, tier environment.Tier, offered []provider.Part, removing []string) (map[string][]string, error) {
	recorded, err := stackrecords.ListBootstrapWants(ctx, r.KeyValues, tier)
	if storeAbsent(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	wants := map[string][]string{}
	for _, part := range bootstrapplan.InOfferedOrder(offered, slices.Collect(maps.Keys(recorded))) {
		if !slices.Contains(removing, part) {
			wants[part] = recorded[part]
		}
	}
	return wants, nil
}

func (r Readiness) refuseRemovingEdgePart(offered []provider.Part, removing []string) error {
	edgePart := bootstrapplan.PartFrontingEdge(offered, r.Edge)
	if edgePart == "" || !slices.Contains(removing, edgePart) {
		return nil
	}
	return refusal.Refuse(refusal.CodeInvalid,
		"%s fronts this project's deploys, so this run will not remove it: point the project at another edge first",
		edgePart)
}

func (c partChange) request(tier environment.Tier, req ApplyRequest, writer provider.WrittenBy) provider.BootstrapRequest {
	return provider.BootstrapRequest{
		Tier:               tier,
		Parts:              c.requested,
		Remove:             c.removing,
		RefuseReplacements: !req.AcceptReplacements,
		WrittenBy:          writer,
		VendorState:        c.status.VendorState,
	}
}

func (r Readiness) Plan(ctx context.Context, tier environment.Tier, req ApplyRequest) (provider.Plan, error) {
	status, err := r.Status(ctx, tier)
	if err != nil {
		return provider.Plan{}, err
	}
	return r.PlanFrom(ctx, status, req)
}

func (r Readiness) PlanFrom(ctx context.Context, status BootstrapStatus, req ApplyRequest) (provider.Plan, error) {
	change, err := r.resolvePartChange(ctx, status, req)
	if err != nil {
		return provider.Plan{}, err
	}
	tier := status.Tier
	plan, err := r.Bootstrap.Plan(ctx, change.request(tier, req, r.WrittenBy))
	if err != nil {
		return provider.Plan{}, err
	}
	return plan, r.noteDependents(ctx, tier, plan.Groups)
}

func (r Readiness) noteDependents(ctx context.Context, tier environment.Tier, groups []provider.ChangeGroup) error {
	var dropped []string
	for _, group := range groups {
		if group.Action == provider.ActionDelete && group.Part != "" {
			dropped = append(dropped, group.Part)
		}
	}
	if len(dropped) == 0 {
		return nil
	}
	recorded, err := r.RecordedParts(ctx, tier)
	if err != nil {
		return err
	}
	for i, group := range groups {
		if group.Action != provider.ActionDelete || group.Part == "" {
			continue
		}
		dependents := ProjectsDependingOn(recorded, []string{group.Part})
		if len(dependents) == 0 {
			continue
		}
		groups[i].Reason = strings.Join(dependents, ", ") + " were deployed against it"
	}
	return nil
}

func (r Readiness) Apply(ctx context.Context, shown provider.Plan, tier environment.Tier, req ApplyRequest, progress progress.Log) error {
	status, err := r.Status(ctx, tier)
	if err != nil {
		return err
	}
	change, err := r.resolvePartChange(ctx, status, req)
	if err != nil {
		return err
	}
	if err := r.admitRemovals(ctx, tier, change.removing, req.Force); err != nil {
		return err
	}
	drawn, err := r.Bootstrap.Plan(ctx, change.request(tier, req, r.WrittenBy))
	if err != nil {
		return err
	}
	if err := bootstrapplan.RefuseUnconsentedChanges(shown, drawn); err != nil {
		return err
	}

	claimed, err := r.claimParts(ctx, change.status, change.requested, change.wants)
	if err != nil {
		return errors.Join(err, r.releaseParts(ctx, tier, claimed))
	}
	err = r.Bootstrap.Apply(ctx, change.request(tier, req, r.WrittenBy), progress)
	r.Statuses.forget()
	if err != nil {
		return errors.Join(err, r.releaseParts(ctx, tier, claimed))
	}
	if err := r.recordParts(ctx, tier, change.requested, change.removing); err != nil {
		return err
	}
	return r.forgetWants(ctx, tier, change.wants)
}

func (r Readiness) Remove(ctx context.Context, shown provider.Plan, tier environment.Tier, progress progress.Log) error {
	if err := r.RefuseIfInUse(ctx, tier); err != nil {
		return err
	}
	if len(shown.Groups) > 0 {
		current, err := r.Bootstrap.PlanRemove(ctx, tier)
		if err != nil {
			return err
		}
		if err := bootstrapplan.RefuseUnconsentedChanges(shown, current); err != nil {
			return err
		}
	}
	err := r.Bootstrap.Remove(ctx, tier, progress)
	r.Statuses.forget()
	if err != nil {
		return err
	}
	if err := r.forgetParts(ctx, tier); err != nil {
		return err
	}
	if tier == environment.TierPreview {
		return keyvalue.Forget(ctx, r.KeyValues, stackrecords.NewPreviewKeyRecordKey())
	}
	return nil
}

func (r Readiness) RefuseIfInUse(ctx context.Context, tier environment.Tier) error {
	users, err := r.BootstrapUsers(ctx, tier)
	if err != nil {
		return err
	}
	return users.Refuse(tier)
}

func (r Readiness) admitRemovals(ctx context.Context, tier environment.Tier, removing []string, force bool) error {
	if len(removing) == 0 || force {
		return nil
	}
	recorded, err := r.RecordedParts(ctx, tier)
	if err != nil {
		return err
	}
	dependents := ProjectsDependingOn(recorded, removing)
	if len(dependents) == 0 {
		return nil
	}
	return refusal.Refuse(refusal.CodeNotReady,
		"removing %s would break %d project(s) already deployed here: %s — re-run with --force to remove it anyway, or leave it installed",
		strings.Join(removing, ", "), len(dependents), strings.Join(dependents, ", "))
}

func (r Readiness) RecordedParts(ctx context.Context, tier environment.Tier) (map[string][]string, error) {
	projects, err := r.KeyValues.List(ctx, stackrecords.ProjectsPartition(tier))
	if err != nil && !storeAbsent(err) {
		return nil, fmt.Errorf("read the projects deployed here: %w", err)
	}
	recorded := map[string][]string{}
	for _, entry := range projects {
		rest := entry.Key.Path
		if len(rest) != 1 || len(entry.Value) == 0 {
			continue
		}
		var project stackrecords.Project
		if err := json.Unmarshal(entry.Value, &project); err != nil {
			return nil, fmt.Errorf("read %s's record: %w", entry.Key, err)
		}
		recorded[rest[0]] = project.Parts
	}
	return recorded, nil
}

func ProjectsDependingOn(recorded map[string][]string, dropped []string) []string {
	var out []string
	for project, parts := range recorded {
		for _, name := range parts {
			if slices.Contains(dropped, name) {
				out = append(out, project)
				break
			}
		}
	}
	slices.Sort(out)
	return out
}

func (r Readiness) EnsureReady(ctx context.Context, tier environment.Tier, wantedBy string, required []string, progress progress.Log) (BootstrapStatus, error) {
	status, err := r.SessionStatus(ctx, tier)
	if err != nil {
		return BootstrapStatus{}, err
	}
	command := provider.BootstrapCommand(tier)
	if !status.Present {
		return status, refusal.Refuse(refusal.CodeNotReady, "this account has no Ocel bootstrap.\nRun `%s` to create it, then try again", command)
	}
	if missing := bootstrapplan.MissingParts(status.Parts, required); len(missing) > 0 {
		return status, errors.Join(refuseLacking(missing, command, wantedBy != ""), r.RecordWants(ctx, tier, wantedBy, missing))
	}
	if stale := status.Stale(required); len(stale) > 0 {
		warn(progress, fmt.Sprintf(
			"This account's Ocel bootstrap is the shape this build needs but its content is behind: %s. Re-run `%s` to refresh it",
			strings.Join(stale, ", "), command))
	}
	if unreadable := status.Unreadable(required); len(unreadable) > 0 {
		warn(progress, fmt.Sprintf(
			"Could not read part of this account's Ocel bootstrap, so it is neither refreshed nor reported as behind: %s",
			strings.Join(unreadable, ", ")))
	}
	return status, nil
}

func refuseLacking(missing []string, command string, recorded bool) error {
	if !recorded {
		return refusal.Refuse(refusal.CodeNotReady,
			"this account's Ocel bootstrap lacks a part this project wants: %s, and only the bootstrap credential may add it.\n"+
				"A dry run records nothing: deploy without --dry, which records what this project wants, then run `%s` and try again",
			strings.Join(missing, ", "), command)
	}
	return refusal.Refuse(refusal.CodeNotReady,
		"this account's Ocel bootstrap lacks a part this project wants: %s, and only the bootstrap credential may add it.\n"+
			"Run `%s`, which adds every part a project here was refused for, and try again",
		strings.Join(missing, ", "), command)
}

func (r Readiness) RecordWants(ctx context.Context, tier environment.Tier, project string, parts []string) error {
	if project == "" {
		return nil
	}
	for _, part := range parts {
		if err := stackrecords.RecordBootstrapWant(ctx, r.KeyValues, tier, part, project); err != nil {
			return err
		}
	}
	return nil
}

func storeAbsent(err error) bool {
	var refused refusal.Refusal
	return errors.As(err, &refused) && refused.Code == refusal.CodeNotReady
}

type BootstrapUsers struct {
	Projects []string
	Wildcard string
}

func (r Readiness) BootstrapUsers(ctx context.Context, tier environment.Tier) (BootstrapUsers, error) {
	recorded, err := r.KeyValues.List(ctx, stackrecords.ProjectsPartition(tier))
	if storeAbsent(err) {
		return BootstrapUsers{}, nil
	}
	if err != nil {
		return BootstrapUsers{}, fmt.Errorf("read the projects deployed here: %w", err)
	}
	var projects []string
	for _, entry := range recorded {
		rest := entry.Key.Path
		if len(rest) != 1 {
			continue
		}
		projects = append(projects, rest[0])
	}
	slices.Sort(projects)
	users := BootstrapUsers{Projects: slices.Compact(projects)}

	wildcard, err := keyvalue.ReadOrEmpty(ctx, r.KeyValues, stackrecords.WildcardKey(tier))
	if err != nil {
		return BootstrapUsers{}, fmt.Errorf("read the %s preview wildcard: %w", tier, err)
	}
	if len(wildcard.Value) > 0 {
		var recorded stackrecords.Wildcard
		if err := json.Unmarshal(wildcard.Value, &recorded); err != nil {
			return BootstrapUsers{}, fmt.Errorf("read the %s preview wildcard: %w", tier, err)
		}
		users.Wildcard = recorded.BaseDomain
	}
	return users, nil
}

func (u BootstrapUsers) Refuse(tier environment.Tier) error {
	if len(u.Projects) == 0 && u.Wildcard == "" {
		return nil
	}
	var reasons []string
	if len(u.Projects) > 0 {
		reasons = append(reasons, fmt.Sprintf(
			"%d project(s) are still deployed into it: %s — run `%s` in each one first",
			len(u.Projects), strings.Join(u.Projects, ", "), destroyCommand(tier)))
	}
	if u.Wildcard != "" {
		reasons = append(reasons, fmt.Sprintf(
			"previews are still served on %s — release it with `ocel domain release --preview` first",
			edge.PreviewWildcard(u.Wildcard)))
	}
	return refusal.Refuse(refusal.CodeNotReady, "the %s bootstrap is still in use, so `%s` will not remove it: %s",
		tier, bootstrapDestroyCommand(tier), strings.Join(reasons, "; "))
}

func bootstrapDestroyCommand(tier environment.Tier) string {
	if tier == environment.TierPreview {
		return "ocel bootstrap destroy preview"
	}
	return "ocel bootstrap destroy production"
}

func destroyCommand(tier environment.Tier) string {
	if tier == environment.TierPreview {
		return "ocel destroy preview"
	}
	return "ocel destroy production"
}

func warn(progress progress.Log, message string) {
	if progress != nil {
		progress.Warn(message)
	}
}
