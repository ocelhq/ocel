package providerserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

type Gate struct {
	Bootstrap provider.Bootstrap
	KeyValues keyvalue.Store
	WrittenBy provider.WrittenBy
	Edge      edge.Kind
}

type BootstrapStatus struct {
	Tier           environment.Tier
	Present        bool
	Stacks         []provider.BootstrapStack
	Features       []string
	WrittenBy      provider.WrittenBy
	RepairOnDeploy bool
	Unfinished     bool
	VendorState    any
}

func (g Gate) Status(ctx context.Context, tier environment.Tier) (BootstrapStatus, error) {
	described, err := g.Bootstrap.Describe(ctx, tier)
	if err != nil {
		return BootstrapStatus{}, err
	}
	status := BootstrapStatus{Tier: tier, Present: described.Present, Stacks: described.Stacks,
		Unfinished: described.Unfinished, VendorState: described.VendorState}
	var present []string
	for _, stack := range described.Stacks {
		if stack.Feature == "" {
			status.WrittenBy = provider.WrittenBy(stack.WrittenBy)
			continue
		}
		if stack.Present {
			present = append(present, stack.Feature)
		}
	}
	status.Features = bootstrapplan.InCatalogueOrder(g.Bootstrap.Catalogue(), present)
	if status.RepairOnDeploy, err = g.repairOnDeploy(ctx, tier); err != nil {
		return BootstrapStatus{}, err
	}
	return status, nil
}

func (s BootstrapStatus) Stale(required []string) []string {
	var out []string
	for _, stack := range s.Stacks {
		if !stack.Present || stack.DigestCurrent {
			continue
		}
		if stack.Feature != "" && !slices.Contains(required, stack.Feature) {
			continue
		}
		out = append(out, stack.Name)
	}
	return out
}

func (s BootstrapStatus) Downgrade(writing provider.WrittenBy) bool {
	return s.WrittenBy.Newer(writing)
}

func (s BootstrapStatus) repairable(required []string) []string {
	var out []string
	for _, stack := range s.Stacks {
		if stack.Feature == "" || stack.DigestCurrent || !stack.Present {
			continue
		}
		if slices.Contains(required, stack.Feature) {
			out = append(out, stack.Name)
		}
	}
	return out
}

type ApplyRequest struct {
	Features []string

	Remove []string

	Force bool

	RepairOnDeploy *bool

	AcceptReplacements bool
}

type featureChange struct {
	status    BootstrapStatus
	requested []string
	removing  []string
	ordered   []string
}

func (g Gate) resolveFeatureChange(status BootstrapStatus, req ApplyRequest) (featureChange, error) {
	catalogue := g.Bootstrap.Catalogue()
	asked, err := bootstrapplan.FeaturesWithDependencies(catalogue, req.Features)
	if err != nil {
		return featureChange{}, err
	}
	removing, err := bootstrapplan.FeaturesToRemove(catalogue, status.Features, req.Remove)
	if err != nil {
		return featureChange{}, err
	}
	if err := bootstrapplan.RefuseEnsuringAndRemoving(asked, removing); err != nil {
		return featureChange{}, err
	}
	if err := g.refuseRemovingEdgeFeature(catalogue, req.Remove, removing); err != nil {
		return featureChange{}, err
	}
	requested, err := bootstrapplan.FeaturesWithDependencies(catalogue, g.withEdgeFeature(catalogue, req.Features))
	if err != nil {
		return featureChange{}, err
	}
	ordered, err := bootstrapplan.DeleteOrder(catalogue, removing)
	if err != nil {
		return featureChange{}, err
	}
	return featureChange{status: status, requested: requested, removing: removing, ordered: ordered}, nil
}

func (g Gate) withEdgeFeature(catalogue []provider.Feature, requested []string) []string {
	edgeFeature := bootstrapplan.FeatureNeedingEdge(catalogue, g.Edge)
	if edgeFeature == "" || slices.Contains(requested, edgeFeature) {
		return requested
	}
	return append(slices.Clone(requested), edgeFeature)
}

func (g Gate) refuseRemovingEdgeFeature(catalogue []provider.Feature, named, removing []string) error {
	edgeFeature := bootstrapplan.FeatureNeedingEdge(catalogue, g.Edge)
	if edgeFeature == "" || !slices.Contains(removing, edgeFeature) {
		return nil
	}
	if slices.Contains(named, edgeFeature) {
		return refusal.Refuse(refusal.CodeInvalid,
			"%s fronts this project's deploys, so this run will not remove it: point the project at another edge first",
			edgeFeature)
	}
	return refusal.Refuse(refusal.CodeInvalid,
		"removing %s takes %s down with it, and %s fronts this project's deploys: point the project at another edge first",
		strings.Join(named, ", "), edgeFeature, edgeFeature)
}

func (c featureChange) request(tier environment.Tier, req ApplyRequest, writer provider.WrittenBy) provider.BootstrapRequest {
	return provider.BootstrapRequest{
		Tier:               tier,
		Features:           c.requested,
		Remove:             c.ordered,
		RefuseReplacements: !req.AcceptReplacements,
		WrittenBy:          writer,
		VendorState:        c.status.VendorState,
	}
}

func (g Gate) Plan(ctx context.Context, tier environment.Tier, req ApplyRequest) (provider.Plan, error) {
	status, err := g.Status(ctx, tier)
	if err != nil {
		return provider.Plan{}, err
	}
	return g.PlanFrom(ctx, status, req)
}

func (g Gate) PlanFrom(ctx context.Context, status BootstrapStatus, req ApplyRequest) (provider.Plan, error) {
	change, err := g.resolveFeatureChange(status, req)
	if err != nil {
		return provider.Plan{}, err
	}
	tier := status.Tier
	plan, err := g.Bootstrap.Plan(ctx, change.request(tier, req, g.WrittenBy))
	if err != nil {
		return provider.Plan{}, err
	}
	return plan, g.noteDependents(ctx, tier, plan.Groups)
}

func (g Gate) noteDependents(ctx context.Context, tier environment.Tier, groups []provider.ChangeGroup) error {
	var dropped []string
	for _, group := range groups {
		if group.Action == provider.ActionDelete && group.Feature != "" {
			dropped = append(dropped, group.Feature)
		}
	}
	if len(dropped) == 0 {
		return nil
	}
	recorded, err := g.RecordedFeatures(ctx, tier)
	if err != nil {
		return err
	}
	for i, group := range groups {
		if group.Action != provider.ActionDelete || group.Feature == "" {
			continue
		}
		dependents := ProjectsDependingOn(recorded, []string{group.Feature})
		if len(dependents) == 0 {
			continue
		}
		groups[i].Reason = strings.Join(dependents, ", ") + " were deployed against it"
	}
	return nil
}

func (g Gate) Apply(ctx context.Context, shown provider.Plan, tier environment.Tier, req ApplyRequest, progress progress.Log) error {
	status, err := g.Status(ctx, tier)
	if err != nil {
		return err
	}
	change, err := g.resolveFeatureChange(status, req)
	if err != nil {
		return err
	}
	if err := g.admitRemovals(ctx, tier, change.removing, req.Force); err != nil {
		return err
	}
	drawn, err := g.Bootstrap.Plan(ctx, change.request(tier, req, g.WrittenBy))
	if err != nil {
		return err
	}
	if err := bootstrapplan.RefuseUnconsentedChanges(shown, drawn); err != nil {
		return err
	}

	repairOnDeploy := change.status.RepairOnDeploy
	if req.RepairOnDeploy != nil {
		repairOnDeploy = *req.RepairOnDeploy
	}
	if err := g.Bootstrap.Apply(ctx, change.request(tier, req, g.WrittenBy), progress); err != nil {
		return err
	}
	if err := g.RecordBootstrap(ctx, tier, stackrecords.BootstrapSettings{RepairOnDeploy: repairOnDeploy}); err != nil {
		return err
	}
	return stackrecords.EnsureSchema(ctx, g.KeyValues, tier)
}

func (g Gate) Remove(ctx context.Context, shown provider.Plan, tier environment.Tier, progress progress.Log) error {
	if err := g.RefuseIfInUse(ctx, tier); err != nil {
		return err
	}
	if len(shown.Groups) > 0 {
		current, err := g.Bootstrap.PlanRemove(ctx, tier)
		if err != nil {
			return err
		}
		if err := bootstrapplan.RefuseUnconsentedChanges(shown, current); err != nil {
			return err
		}
	}
	if err := g.Bootstrap.Remove(ctx, tier, progress); err != nil {
		return err
	}
	return keyvalue.Forget(ctx, g.KeyValues, stackrecords.BootstrapKey(tier))
}

func (g Gate) RefuseIfInUse(ctx context.Context, tier environment.Tier) error {
	users, err := g.BootstrapUsers(ctx, tier)
	if err != nil {
		return err
	}
	return users.Refuse(tier)
}

func (g Gate) admitRemovals(ctx context.Context, tier environment.Tier, removing []string, force bool) error {
	if len(removing) == 0 || force {
		return nil
	}
	recorded, err := g.RecordedFeatures(ctx, tier)
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

func (g Gate) RecordedFeatures(ctx context.Context, tier environment.Tier) (map[string][]string, error) {
	projects, err := g.KeyValues.List(ctx, stackrecords.ProjectsPartition(tier))
	if err != nil {
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
		recorded[rest[0]] = project.Features
	}
	return recorded, nil
}

func ProjectsDependingOn(recorded map[string][]string, dropped []string) []string {
	var out []string
	for project, features := range recorded {
		for _, name := range features {
			if slices.Contains(dropped, name) {
				out = append(out, project)
				break
			}
		}
	}
	slices.Sort(out)
	return out
}

func (g Gate) EnsureReady(ctx context.Context, tier environment.Tier, required []string, repair bool, progress progress.Log) (BootstrapStatus, error) {
	status, err := g.Status(ctx, tier)
	if err != nil {
		return BootstrapStatus{}, err
	}
	command := provider.BootstrapCommand(tier)
	if !status.Present {
		return status, refusal.Refuse(refusal.CodeNotReady, "this account has no Ocel bootstrap.\nRun `%s` to create it, then try again", command)
	}
	if err := status.lacking(required, command); err != nil {
		return status, err
	}
	if repair && g.repair(ctx, status, required, progress) {
		if status, err = g.Status(ctx, tier); err != nil {
			return BootstrapStatus{}, err
		}
	}
	if stale := status.Stale(required); len(stale) > 0 {
		warn(progress, fmt.Sprintf(
			"This account's Ocel bootstrap is the shape this build needs but its content is behind: %s. Re-run `%s` to refresh it",
			strings.Join(stale, ", "), command))
	}
	return status, nil
}

func (s BootstrapStatus) lacking(required []string, command string) error {
	missing := bootstrapplan.MissingFeatures(s.Features, required)
	if len(missing) == 0 {
		return nil
	}
	return refusal.Refuse(refusal.CodeNotReady,
		"this account's Ocel bootstrap lacks the features this project needs: %s.\nRun `%s --features %s` and try again",
		strings.Join(missing, ", "), command, strings.Join(missing, ","))
}

func (g Gate) repair(ctx context.Context, status BootstrapStatus, required []string, progress progress.Log) bool {
	if !status.RepairOnDeploy || len(status.repairable(required)) == 0 {
		return false
	}
	if !g.WrittenBy.Release() {
		say(progress, fmt.Sprintf(
			"This provider is a development build (%s), so it leaves the account's stale bootstrap stacks as they are", g.WrittenBy))
		return false
	}
	if !status.WrittenBy.Release() {
		say(progress, fmt.Sprintf(
			"This account's bootstrap was written by a development build (%s), so it is refreshed only by the run that writes it next", status.WrittenBy))
		return false
	}
	err := g.Bootstrap.Apply(ctx, provider.BootstrapRequest{
		Tier:               status.Tier,
		Features:           status.Features,
		RefuseReplacements: true,
		Repair:             true,
		WrittenBy:          g.WrittenBy,
	}, progress)
	var refused refusal.Refusal
	if errors.As(err, &refused) && refused.Code == refusal.CodeDenied {
		warn(progress, denied(refused))
		return false
	}
	if err != nil {
		warn(progress, fmt.Sprintf("Could not refresh the %s bootstrap, so this run continues against the one in place: %s", status.Tier, err))
		return false
	}
	return true
}

func denied(refused refusal.Refusal) string {
	said := "This run may not refresh what this bootstrap has fallen behind on, so it is left in place"
	if refused.Message == "" {
		return said
	}
	return said + ": " + refused.Message
}

func (g Gate) repairOnDeploy(ctx context.Context, tier environment.Tier) (bool, error) {
	recorded, err := keyvalue.ReadOrEmpty(ctx, g.KeyValues, stackrecords.BootstrapKey(tier))
	if err != nil {
		return false, fmt.Errorf("read the %s bootstrap record: %w", tier, err)
	}
	if len(recorded.Value) == 0 {
		return false, nil
	}
	var state stackrecords.BootstrapSettings
	if err := json.Unmarshal(recorded.Value, &state); err != nil {
		return false, fmt.Errorf("read the %s bootstrap record: %w", tier, err)
	}
	return state.RepairOnDeploy, nil
}

func (g Gate) RecordBootstrap(ctx context.Context, tier environment.Tier, state stackrecords.BootstrapSettings) error {
	recorded, err := keyvalue.ReadOrEmpty(ctx, g.KeyValues, stackrecords.BootstrapKey(tier))
	if err != nil {
		return fmt.Errorf("read the %s bootstrap record: %w", tier, err)
	}
	recorded.Value, err = json.Marshal(state)
	if err != nil {
		return fmt.Errorf("record the %s bootstrap: %w", tier, err)
	}
	if _, err := g.KeyValues.Write(ctx, recorded); err != nil {
		return fmt.Errorf("record the %s bootstrap: %w", tier, err)
	}
	return nil
}

type BootstrapUsers struct {
	Projects []string
	Wildcard string
}

func (g Gate) BootstrapUsers(ctx context.Context, tier environment.Tier) (BootstrapUsers, error) {
	recorded, err := g.KeyValues.List(ctx, stackrecords.ProjectsPartition(tier))
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

	wildcard, err := keyvalue.ReadOrEmpty(ctx, g.KeyValues, stackrecords.WildcardKey(tier))
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

func say(progress progress.Log, message string) {
	if progress != nil {
		progress.Say(message)
	}
}

func warn(progress progress.Log, message string) {
	if progress != nil {
		progress.Warn(message)
	}
}
