package providerkit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const BootstrapSchema = 1

type Gate struct {
	Bootstrapper Bootstrapper
	Records      RecordStore
	Writer       Writer
	Edge         edge.Kind
}

type Standing struct {
	Class      Class
	Present    bool
	Stacks     []BootstrapStack
	Features   []string
	Schema     int
	Writer     Writer
	AutoHeal   bool
	Unfinished bool
	Held       any
}

func (g Gate) Standing(ctx context.Context, class Class) (Standing, error) {
	described, err := g.Bootstrapper.Describe(ctx, class)
	if err != nil {
		return Standing{}, err
	}
	standing := Standing{Class: class, Present: described.Present, Stacks: described.Stacks,
		Unfinished: described.Unfinished, Held: described.Held}
	var carried []string
	for _, stack := range described.Stacks {
		if stack.Feature == "" {
			standing.Schema = int(stack.Schema)
			standing.Writer = Writer(stack.Writer)
			continue
		}
		if stack.Present {
			carried = append(carried, stack.Feature)
		}
	}
	standing.Features = inCatalogueOrder(g.Bootstrapper.Catalogue(), carried)
	if standing.AutoHeal, err = g.autoHeal(ctx, class); err != nil {
		return Standing{}, err
	}
	return standing, nil
}

func (s Standing) Stale(required []string) []string {
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

func (s Standing) Downgrade(writing Writer) bool {
	return s.Writer.Newer(writing)
}

func (s Standing) healable(required []string) []string {
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
	standing  Standing
	requested []string
	removing  []string
	ordered   []string
}

func (g Gate) intended(standing Standing, req ApplyRequest) (intent, error) {
	catalogue := g.Bootstrapper.Catalogue()
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

func (g Gate) ensuringEdge(catalogue []Feature, requested []string) []string {
	fronting := FeatureNeedingEdge(catalogue, g.Edge)
	if fronting == "" || slices.Contains(requested, fronting) {
		return requested
	}
	return append(slices.Clone(requested), fronting)
}

func (g Gate) refuseUnfronting(catalogue []Feature, named, removing []string) error {
	fronting := FeatureNeedingEdge(catalogue, g.Edge)
	if fronting == "" || !slices.Contains(removing, fronting) {
		return nil
	}
	if slices.Contains(named, fronting) {
		return Refuse(CodeInvalid,
			"%s fronts this project's deploys, so this run will not remove it: point the project at another edge first",
			fronting)
	}
	return Refuse(CodeInvalid,
		"removing %s takes %s down with it, and %s fronts this project's deploys: point the project at another edge first",
		strings.Join(named, ", "), fronting, fronting)
}

func (i intent) request(class Class, req ApplyRequest, writer Writer) BootstrapRequest {
	return BootstrapRequest{
		Class:      class,
		Features:   i.requested,
		Remove:     i.ordered,
		Unattended: !req.AcceptReplacements,
		Writer:     writer,
		Held:       i.standing.Held,
	}
}

func (g Gate) Plan(ctx context.Context, class Class, req ApplyRequest) (BootstrapPlan, error) {
	standing, err := g.Standing(ctx, class)
	if err != nil {
		return BootstrapPlan{}, err
	}
	return g.PlanFrom(ctx, standing, req)
}

func (g Gate) PlanFrom(ctx context.Context, standing Standing, req ApplyRequest) (BootstrapPlan, error) {
	intended, err := g.intended(standing, req)
	if err != nil {
		return BootstrapPlan{}, err
	}
	class := standing.Class
	plan, err := g.Bootstrapper.Plan(ctx, intended.request(class, req, g.Writer))
	if err != nil {
		return BootstrapPlan{}, err
	}
	return plan, g.noteDependents(ctx, class, plan.Groups)
}

func (g Gate) noteDependents(ctx context.Context, class Class, groups []ChangeGroup) error {
	var dropped []string
	for _, group := range groups {
		if group.Action == ActionDelete && group.Feature != "" {
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
		if group.Action != ActionDelete || group.Feature == "" {
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

func (g Gate) Apply(ctx context.Context, class Class, req ApplyRequest, report Reporter) error {
	standing, err := g.Standing(ctx, class)
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

	autoHeal := intended.standing.AutoHeal
	if req.AutoHeal != nil {
		autoHeal = *req.AutoHeal
	}
	if err := g.Bootstrapper.Apply(ctx, intended.request(class, req, g.Writer), report); err != nil {
		return err
	}
	if err := g.RecordBootstrap(ctx, class, BootstrapState{AutoHeal: autoHeal}); err != nil {
		return err
	}
	return EnsureRecordSchema(ctx, g.Records, class)
}

func (g Gate) Remove(ctx context.Context, class Class, report Reporter) error {
	if err := g.Vacant(ctx, class); err != nil {
		return err
	}
	if err := g.Bootstrapper.Remove(ctx, class, report); err != nil {
		return err
	}
	return Forget(ctx, g.Records, BootstrapRecord(class))
}

func (g Gate) Vacant(ctx context.Context, class Class) error {
	occupancy, err := g.Occupancy(ctx, class)
	if err != nil {
		return err
	}
	return occupancy.Refuse(class)
}

func (g Gate) admitRemovals(ctx context.Context, class Class, removing []string, force bool) error {
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
	return Refuse(CodeNotReady,
		"removing %s would break %d project(s) already deployed here: %s — re-run with --force to remove it anyway, or leave it standing",
		strings.Join(removing, ", "), len(dependents), strings.Join(dependents, ", "))
}

func (g Gate) RecordedFeatures(ctx context.Context, class Class) (map[string][]string, error) {
	held, err := g.Records.List(ctx, ProjectsRecord(class))
	if err != nil {
		return nil, fmt.Errorf("read the projects deployed here: %w", err)
	}
	recorded := map[string][]string{}
	for _, record := range held {
		rest, under := record.Name.Under(ProjectsRecord(class))
		if !under || len(rest) != 1 || len(record.Bytes) == 0 {
			continue
		}
		var project Project
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

func (g Gate) Admit(ctx context.Context, class Class, required []string, report Reporter) (Standing, error) {
	standing, err := g.Standing(ctx, class)
	if err != nil {
		return Standing{}, err
	}
	command := BootstrapCommand(class)
	if err := CheckSchema(standing.Schema, standing.Present, class); err != nil {
		return standing, err
	}
	if err := standing.lacking(required, command); err != nil {
		return standing, err
	}
	if g.heal(ctx, standing, required, report) {
		if standing, err = g.Standing(ctx, class); err != nil {
			return Standing{}, err
		}
	}
	if stale := standing.Stale(required); len(stale) > 0 {
		detail(report, fmt.Sprintf(
			"this account's Ocel bootstrap is the shape this build needs but its content is behind: %s. Re-run `%s` to refresh it",
			strings.Join(stale, ", "), command))
	}
	return standing, nil
}

func (s Standing) lacking(required []string, command string) error {
	missing := missingFeatures(s.Features, required)
	if len(missing) == 0 {
		return nil
	}
	return Refuse(CodeNotReady,
		"this account's Ocel bootstrap lacks the features this project needs: %s.\nRun `%s --features %s` and try again",
		strings.Join(missing, ", "), command, strings.Join(missing, ","))
}

func (g Gate) heal(ctx context.Context, standing Standing, required []string, report Reporter) bool {
	if !standing.AutoHeal || len(standing.healable(required)) == 0 {
		return false
	}
	if !g.Writer.Release() {
		detail(report, fmt.Sprintf(
			"this provider is a development build (%s), so it leaves the account's stale bootstrap stacks as they are", g.Writer))
		return false
	}
	if !standing.Writer.Release() {
		detail(report, fmt.Sprintf(
			"this account's bootstrap was written by a development build (%s), so it is refreshed only by the run that writes it next", standing.Writer))
		return false
	}
	err := g.Bootstrapper.Apply(ctx, BootstrapRequest{
		Class:      standing.Class,
		Features:   standing.Features,
		Unattended: true,
		Heal:       true,
		Writer:     g.Writer,
	}, report)
	var refusal Refusal
	if errors.As(err, &refusal) && refusal.Code == CodeDenied {
		detail(report, "this run may not refresh the account's stale bootstrap stacks, so they are left as they stand: "+refusal.Message)
		return false
	}
	if err != nil {
		detail(report, "could not refresh this account's bootstrap, and this run continues against it as it stands: "+err.Error())
		return false
	}
	return true
}

func (g Gate) autoHeal(ctx context.Context, class Class) (bool, error) {
	held, err := Held(ctx, g.Records, BootstrapRecord(class))
	if err != nil {
		return false, fmt.Errorf("read the %s bootstrap record: %w", class, err)
	}
	if len(held.Bytes) == 0 {
		return false, nil
	}
	var state BootstrapState
	if err := json.Unmarshal(held.Bytes, &state); err != nil {
		return false, fmt.Errorf("read the %s bootstrap record: %w", class, err)
	}
	return state.AutoHeal, nil
}

func (g Gate) RecordBootstrap(ctx context.Context, class Class, state BootstrapState) error {
	held, err := Held(ctx, g.Records, BootstrapRecord(class))
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

func (g Gate) Occupancy(ctx context.Context, class Class) (Occupancy, error) {
	held, err := g.Records.List(ctx, ProjectsRecord(class))
	if err != nil {
		return Occupancy{}, fmt.Errorf("read the projects deployed here: %w", err)
	}
	var projects []string
	for _, record := range held {
		rest, under := record.Name.Under(ProjectsRecord(class))
		if !under || rest[0] == "" {
			continue
		}
		projects = append(projects, rest[0])
	}
	slices.Sort(projects)
	occupancy := Occupancy{Projects: slices.Compact(projects)}

	wildcard, err := Held(ctx, g.Records, WildcardRecord(class))
	if err != nil {
		return Occupancy{}, fmt.Errorf("read the %s preview wildcard: %w", class, err)
	}
	if len(wildcard.Bytes) > 0 {
		var recorded Wildcard
		if err := json.Unmarshal(wildcard.Bytes, &recorded); err != nil {
			return Occupancy{}, fmt.Errorf("read the %s preview wildcard: %w", class, err)
		}
		occupancy.Wildcard = recorded.BaseDomain
	}
	return occupancy, nil
}

func (o Occupancy) Refuse(class Class) error {
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
	return Refuse(CodeNotReady, "the %s bootstrap is still in use, so `%s` will not remove it: %s",
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
		return Refuse(CodeNotReady, "this account has no Ocel bootstrap.\nRun `%s` to create it, then try again", command)
	case needsBootstrapUpgrade:
		if deployed == 0 {
			return Refuse(CodeNotReady, "this account's Ocel bootstrap predates schema tracking; this provider requires schema %d.\nRun `%s` to upgrade it, then try again", required, command)
		}
		return Refuse(CodeNotReady, "this account's Ocel bootstrap is out of date: the account is at schema %d, this provider requires schema %d.\nRun `%s` to upgrade it, then try again", deployed, required, command)
	case needsCLIUpgrade:
		return Refuse(CodeNotReady, "this account's Ocel bootstrap is newer than this provider understands: the account is at schema %d, this provider supports up to schema %d.\nUpgrade the Ocel CLI and try again", deployed, required)
	default:
		return nil
	}
}

func CheckSchema(deployed int, present bool, class Class) error {
	return checkCompat(deployed, present, BootstrapSchema).explain(deployed, BootstrapSchema, BootstrapCommand(class))
}

func RefuseSchemaAhead(deployed int, present bool, class Class) error {
	if checkCompat(deployed, present, BootstrapSchema) != needsCLIUpgrade {
		return nil
	}
	return schemaAhead(deployed, class)
}

func schemaAhead(deployed int, class Class) error {
	return Refuse(CodeNotReady,
		"this account's Ocel bootstrap is newer than this provider understands: the account is at schema %d, this provider supports up to schema %d.\nUpgrade the Ocel CLI, or run `%s` and bootstrap it afresh — there is no way to write an older shape over a newer one",
		deployed, BootstrapSchema, bootstrapDestroyCommand(class))
}

func BootstrapCommand(class Class) string {
	if class == ClassPreview {
		return "ocel bootstrap preview"
	}
	return "ocel bootstrap production"
}

func BootstrapFeaturesCommand(class Class) string {
	return BootstrapCommand(class) + " --features"
}

func bootstrapDestroyCommand(class Class) string {
	if class == ClassPreview {
		return "ocel bootstrap destroy preview"
	}
	return "ocel bootstrap destroy production"
}

func destroyCommand(class Class) string {
	if class == ClassPreview {
		return "ocel destroy preview"
	}
	return "ocel destroy production"
}

func detail(report Reporter, message string) {
	if report == nil {
		return
	}
	report.Detail(message)
}
