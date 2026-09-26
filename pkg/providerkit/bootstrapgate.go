package providerkit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/pkg/providerkit/records"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	"github.com/ocelhq/ocel/pkg/providerkit/stackrecords"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type Gate struct {
	Bootstrap provider.Bootstrap
	Records   records.Store
	WrittenBy provider.WrittenBy
	Edge      edge.Kind
}

type BootstrapState struct {
	Class      edge.Class
	Present    bool
	Stacks     []provider.BootstrapStack
	Features   []string
	Schema     int
	WrittenBy  provider.WrittenBy
	AutoHeal   bool
	Unfinished bool
	Reading    any
}

func (g Gate) State(ctx context.Context, class edge.Class) (BootstrapState, error) {
	described, err := g.Bootstrap.Describe(ctx, class)
	if err != nil {
		return BootstrapState{}, err
	}
	standing := BootstrapState{Class: class, Present: described.Present, Stacks: described.Stacks,
		Unfinished: described.Unfinished, Reading: described.Reading}
	var carried []string
	for _, stack := range described.Stacks {
		if stack.Feature == "" {
			standing.Schema = int(stack.Schema)
			standing.WrittenBy = provider.WrittenBy(stack.WrittenBy)
			continue
		}
		if stack.Present {
			carried = append(carried, stack.Feature)
		}
	}
	standing.Features = inCatalogueOrder(g.Bootstrap.Catalogue(), carried)
	if standing.AutoHeal, err = g.autoHeal(ctx, class); err != nil {
		return BootstrapState{}, err
	}
	return standing, nil
}

func (s BootstrapState) Stale(required []string) []string {
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

func (s BootstrapState) Downgrade(writing provider.WrittenBy) bool {
	return s.WrittenBy.Newer(writing)
}

func (s BootstrapState) healable(required []string) []string {
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

	AutoHeal *bool

	AcceptReplacements bool
}

type intent struct {
	standing  BootstrapState
	requested []string
	removing  []string
	ordered   []string
}

func (g Gate) intended(standing BootstrapState, req ApplyRequest) (intent, error) {
	catalogue := g.Bootstrap.Catalogue()
	class := standing.Class
	if err := RefuseSchemaAhead(standing.Schema, standing.Present, class); err != nil {
		return intent{}, err
	}
	asked, err := featureClosure(catalogue, req.Features)
	if err != nil {
		return intent{}, err
	}
	removing, err := featureRemoval(catalogue, standing.Features, req.Remove)
	if err != nil {
		return intent{}, err
	}
	if err := refuseBothWays(asked, removing); err != nil {
		return intent{}, err
	}
	if err := g.refuseUnfronting(catalogue, req.Remove, removing); err != nil {
		return intent{}, err
	}
	requested, err := featureClosure(catalogue, g.ensuringEdge(catalogue, req.Features))
	if err != nil {
		return intent{}, err
	}
	ordered, err := featureDeleteOrder(catalogue, removing)
	if err != nil {
		return intent{}, err
	}
	return intent{standing: standing, requested: requested, removing: removing, ordered: ordered}, nil
}

func (g Gate) ensuringEdge(catalogue []provider.Feature, requested []string) []string {
	fronting := FeatureNeedingEdge(catalogue, g.Edge)
	if fronting == "" || slices.Contains(requested, fronting) {
		return requested
	}
	return append(slices.Clone(requested), fronting)
}

func (g Gate) refuseUnfronting(catalogue []provider.Feature, named, removing []string) error {
	fronting := FeatureNeedingEdge(catalogue, g.Edge)
	if fronting == "" || !slices.Contains(removing, fronting) {
		return nil
	}
	if slices.Contains(named, fronting) {
		return refusal.Refuse(refusal.CodeInvalid,
			"%s fronts this project's deploys, so this run will not remove it: point the project at another edge first",
			fronting)
	}
	return refusal.Refuse(refusal.CodeInvalid,
		"removing %s takes %s down with it, and %s fronts this project's deploys: point the project at another edge first",
		strings.Join(named, ", "), fronting, fronting)
}

func (i intent) request(class edge.Class, req ApplyRequest, writer provider.WrittenBy) provider.BootstrapRequest {
	return provider.BootstrapRequest{
		Class:      class,
		Features:   i.requested,
		Remove:     i.ordered,
		Unattended: !req.AcceptReplacements,
		WrittenBy:  writer,
		Reading:    i.standing.Reading,
	}
}

func (g Gate) Plan(ctx context.Context, class edge.Class, req ApplyRequest) (provider.Plan, error) {
	standing, err := g.State(ctx, class)
	if err != nil {
		return provider.Plan{}, err
	}
	return g.PlanFrom(ctx, standing, req)
}

func (g Gate) PlanFrom(ctx context.Context, standing BootstrapState, req ApplyRequest) (provider.Plan, error) {
	intended, err := g.intended(standing, req)
	if err != nil {
		return provider.Plan{}, err
	}
	class := standing.Class
	plan, err := g.Bootstrap.Plan(ctx, intended.request(class, req, g.WrittenBy))
	if err != nil {
		return provider.Plan{}, err
	}
	return plan, g.noteDependents(ctx, class, plan.Groups)
}

func (g Gate) noteDependents(ctx context.Context, class edge.Class, groups []provider.ChangeGroup) error {
	var dropped []string
	for _, group := range groups {
		if group.Action == provider.ActionDelete && group.Feature != "" {
			dropped = append(dropped, group.Feature)
		}
	}
	if len(dropped) == 0 {
		return nil
	}
	recorded, err := g.RecordedFeatures(ctx, class)
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

func (g Gate) Apply(ctx context.Context, shown provider.Plan, class edge.Class, req ApplyRequest, progress edge.Progress) error {
	standing, err := g.State(ctx, class)
	if err != nil {
		return err
	}
	intended, err := g.intended(standing, req)
	if err != nil {
		return err
	}
	if err := g.admitRemovals(ctx, class, intended.removing, req.Force); err != nil {
		return err
	}
	drawn, err := g.Bootstrap.Plan(ctx, intended.request(class, req, g.WrittenBy))
	if err != nil {
		return err
	}
	if err := RefuseGrowth(shown, drawn); err != nil {
		return err
	}

	autoHeal := intended.standing.AutoHeal
	if req.AutoHeal != nil {
		autoHeal = *req.AutoHeal
	}
	if err := g.Bootstrap.Apply(ctx, intended.request(class, req, g.WrittenBy), progress); err != nil {
		return err
	}
	if err := g.RecordBootstrap(ctx, class, stackrecords.BootstrapSettings{AutoHeal: autoHeal}); err != nil {
		return err
	}
	return stackrecords.EnsureSchema(ctx, g.Records, class)
}

func (g Gate) Remove(ctx context.Context, shown provider.Plan, class edge.Class, progress edge.Progress) error {
	if err := g.Vacant(ctx, class); err != nil {
		return err
	}
	if len(shown.Groups) > 0 {
		standing, err := g.Bootstrap.PlanRemove(ctx, class)
		if err != nil {
			return err
		}
		if err := RefuseGrowth(shown, standing); err != nil {
			return err
		}
	}
	if err := g.Bootstrap.Remove(ctx, class, progress); err != nil {
		return err
	}
	return records.Forget(ctx, g.Records, stackrecords.BootstrapRecord(class))
}

func (g Gate) Vacant(ctx context.Context, class edge.Class) error {
	occupancy, err := g.Occupancy(ctx, class)
	if err != nil {
		return err
	}
	return occupancy.Refuse(class)
}

func (g Gate) admitRemovals(ctx context.Context, class edge.Class, removing []string, force bool) error {
	if len(removing) == 0 || force {
		return nil
	}
	recorded, err := g.RecordedFeatures(ctx, class)
	if err != nil {
		return err
	}
	dependents := ProjectsDependingOn(recorded, removing)
	if len(dependents) == 0 {
		return nil
	}
	return refusal.Refuse(refusal.CodeNotReady,
		"removing %s would break %d project(s) already deployed here: %s — re-run with --force to remove it anyway, or leave it standing",
		strings.Join(removing, ", "), len(dependents), strings.Join(dependents, ", "))
}

func (g Gate) RecordedFeatures(ctx context.Context, class edge.Class) (map[string][]string, error) {
	held, err := g.Records.List(ctx, stackrecords.ProjectsRecord(class))
	if err != nil {
		return nil, fmt.Errorf("read the projects deployed here: %w", err)
	}
	recorded := map[string][]string{}
	for _, record := range held {
		rest, under := record.Name.Under(stackrecords.ProjectsRecord(class))
		if !under || len(rest) != 1 || len(record.Bytes) == 0 {
			continue
		}
		var project stackrecords.Project
		if err := json.Unmarshal(record.Bytes, &project); err != nil {
			return nil, fmt.Errorf("read %s's record: %w", record.Name, err)
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

func (g Gate) Admit(ctx context.Context, class edge.Class, required []string, heal bool, progress edge.Progress) (BootstrapState, error) {
	standing, err := g.State(ctx, class)
	if err != nil {
		return BootstrapState{}, err
	}
	command := provider.BootstrapCommand(class)
	if err := CheckSchema(standing.Schema, standing.Present, class); err != nil {
		return standing, err
	}
	if err := standing.lacking(required, command); err != nil {
		return standing, err
	}
	if heal && g.heal(ctx, standing, required, progress) {
		if standing, err = g.State(ctx, class); err != nil {
			return BootstrapState{}, err
		}
	}
	if stale := standing.Stale(required); len(stale) > 0 {
		detail(progress, fmt.Sprintf(
			"this account's Ocel bootstrap is the shape this build needs but its content is behind: %s. Re-run `%s` to refresh it",
			strings.Join(stale, ", "), command))
	}
	return standing, nil
}

func (s BootstrapState) lacking(required []string, command string) error {
	missing := missingFeatures(s.Features, required)
	if len(missing) == 0 {
		return nil
	}
	return refusal.Refuse(refusal.CodeNotReady,
		"this account's Ocel bootstrap lacks the features this project needs: %s.\nRun `%s --features %s` and try again",
		strings.Join(missing, ", "), command, strings.Join(missing, ","))
}

func (g Gate) heal(ctx context.Context, standing BootstrapState, required []string, progress edge.Progress) bool {
	if !standing.AutoHeal || len(standing.healable(required)) == 0 {
		return false
	}
	if !g.WrittenBy.Release() {
		detail(progress, fmt.Sprintf(
			"this provider is a development build (%s), so it leaves the account's stale bootstrap stacks as they are", g.WrittenBy))
		return false
	}
	if !standing.WrittenBy.Release() {
		detail(progress, fmt.Sprintf(
			"this account's bootstrap was written by a development build (%s), so it is refreshed only by the run that writes it next", standing.WrittenBy))
		return false
	}
	err := g.Bootstrap.Apply(ctx, provider.BootstrapRequest{
		Class:      standing.Class,
		Features:   standing.Features,
		Unattended: true,
		Heal:       true,
		WrittenBy:  g.WrittenBy,
	}, progress)
	var refused refusal.Refusal
	if errors.As(err, &refused) && refused.Code == refusal.CodeDenied {
		detail(progress, denied(refused))
		return false
	}
	if err != nil {
		detail(progress, "could not refresh this account's bootstrap, and this run continues against it as it stands: "+err.Error())
		return false
	}
	return true
}

func denied(refusal refusal.Refusal) string {
	said := "this run may not refresh what this bootstrap has fallen behind on, so it is left as it stands"
	if refusal.Message == "" {
		return said
	}
	return said + ": " + refusal.Message
}

func (g Gate) autoHeal(ctx context.Context, class edge.Class) (bool, error) {
	held, err := records.ReadOrEmpty(ctx, g.Records, stackrecords.BootstrapRecord(class))
	if err != nil {
		return false, fmt.Errorf("read the %s bootstrap record: %w", class, err)
	}
	if len(held.Bytes) == 0 {
		return false, nil
	}
	var state stackrecords.BootstrapSettings
	if err := json.Unmarshal(held.Bytes, &state); err != nil {
		return false, fmt.Errorf("read the %s bootstrap record: %w", class, err)
	}
	return state.AutoHeal, nil
}

func (g Gate) RecordBootstrap(ctx context.Context, class edge.Class, state stackrecords.BootstrapSettings) error {
	held, err := records.ReadOrEmpty(ctx, g.Records, stackrecords.BootstrapRecord(class))
	if err != nil {
		return fmt.Errorf("read the %s bootstrap record: %w", class, err)
	}
	held.Bytes, err = json.Marshal(state)
	if err != nil {
		return fmt.Errorf("record the %s bootstrap: %w", class, err)
	}
	if _, err := g.Records.Write(ctx, held); err != nil {
		return fmt.Errorf("record the %s bootstrap: %w", class, err)
	}
	return nil
}

type Occupancy struct {
	Projects []string
	Wildcard string
}

func (g Gate) Occupancy(ctx context.Context, class edge.Class) (Occupancy, error) {
	held, err := g.Records.List(ctx, stackrecords.ProjectsRecord(class))
	if err != nil {
		return Occupancy{}, fmt.Errorf("read the projects deployed here: %w", err)
	}
	var projects []string
	for _, record := range held {
		rest, under := record.Name.Under(stackrecords.ProjectsRecord(class))
		if !under || rest[0] == "" {
			continue
		}
		projects = append(projects, rest[0])
	}
	slices.Sort(projects)
	occupancy := Occupancy{Projects: slices.Compact(projects)}

	wildcard, err := records.ReadOrEmpty(ctx, g.Records, stackrecords.WildcardRecord(class))
	if err != nil {
		return Occupancy{}, fmt.Errorf("read the %s preview wildcard: %w", class, err)
	}
	if len(wildcard.Bytes) > 0 {
		var recorded stackrecords.Wildcard
		if err := json.Unmarshal(wildcard.Bytes, &recorded); err != nil {
			return Occupancy{}, fmt.Errorf("read the %s preview wildcard: %w", class, err)
		}
		occupancy.Wildcard = recorded.BaseDomain
	}
	return occupancy, nil
}

func (o Occupancy) Refuse(class edge.Class) error {
	if len(o.Projects) == 0 && o.Wildcard == "" {
		return nil
	}
	var reasons []string
	if len(o.Projects) > 0 {
		reasons = append(reasons, fmt.Sprintf(
			"%d project(s) are still deployed into it: %s — run `%s` in each one first",
			len(o.Projects), strings.Join(o.Projects, ", "), destroyCommand(class)))
	}
	if o.Wildcard != "" {
		reasons = append(reasons, fmt.Sprintf(
			"previews are still served on %s — release it with `ocel domain release --preview` first",
			edge.PreviewWildcard(o.Wildcard)))
	}
	return refusal.Refuse(refusal.CodeNotReady, "the %s bootstrap is still in use, so `%s` will not remove it: %s",
		class, bootstrapDestroyCommand(class), strings.Join(reasons, "; "))
}

type compatibility int

const (
	compatible compatibility = iota
	needsBootstrapInit
	needsBootstrapUpgrade
	needsCLIUpgrade
)

func checkCompat(deployed int, present bool, required int) compatibility {
	switch {
	case !present:
		return needsBootstrapInit
	case deployed < required:
		return needsBootstrapUpgrade
	case deployed > required:
		return needsCLIUpgrade
	default:
		return compatible
	}
}

func (c compatibility) explain(deployed, required int, command string) error {
	switch c {
	case needsBootstrapInit:
		return refusal.Refuse(refusal.CodeNotReady, "this account has no Ocel bootstrap.\nRun `%s` to create it, then try again", command)
	case needsBootstrapUpgrade:
		if deployed == 0 {
			return refusal.Refuse(refusal.CodeNotReady, "this account's Ocel bootstrap predates schema tracking; this provider requires schema %d.\nRun `%s` to upgrade it, then try again", required, command)
		}
		return refusal.Refuse(refusal.CodeNotReady, "this account's Ocel bootstrap is out of date: the account is at schema %d, this provider requires schema %d.\nRun `%s` to upgrade it, then try again", deployed, required, command)
	case needsCLIUpgrade:
		return refusal.Refuse(refusal.CodeNotReady, "this account's Ocel bootstrap is newer than this provider understands: the account is at schema %d, this provider supports up to schema %d.\nUpgrade the Ocel CLI and try again", deployed, required)
	default:
		return nil
	}
}

func CheckSchema(deployed int, present bool, class edge.Class) error {
	return checkCompat(deployed, present, provider.BootstrapSchema).explain(deployed, provider.BootstrapSchema, provider.BootstrapCommand(class))
}

func RefuseSchemaAhead(deployed int, present bool, class edge.Class) error {
	if checkCompat(deployed, present, provider.BootstrapSchema) != needsCLIUpgrade {
		return nil
	}
	return schemaAhead(deployed, class)
}

func schemaAhead(deployed int, class edge.Class) error {
	return refusal.Refuse(refusal.CodeNotReady,
		"this account's Ocel bootstrap is newer than this provider understands: the account is at schema %d, this provider supports up to schema %d.\nUpgrade the Ocel CLI, or run `%s` and bootstrap it afresh — there is no way to write an older shape over a newer one",
		deployed, provider.BootstrapSchema, bootstrapDestroyCommand(class))
}

func bootstrapDestroyCommand(class edge.Class) string {
	if class == edge.ClassPreview {
		return "ocel bootstrap destroy preview"
	}
	return "ocel bootstrap destroy production"
}

func destroyCommand(class edge.Class) string {
	if class == edge.ClassPreview {
		return "ocel destroy preview"
	}
	return "ocel destroy production"
}

func detail(progress edge.Progress, message string) {
	if progress == nil {
		return
	}
	progress.Detail(message)
}
