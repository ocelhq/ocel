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
}

type Standing struct {
	Class    Class
	Present  bool
	Stacks   []BootstrapStack
	Features []string
	Schema   int
	Writer   Writer
	AutoHeal bool
}

func (g Gate) Standing(ctx context.Context, class Class) (Standing, error) {
	described, err := g.Bootstrapper.Describe(ctx, class)
	if err != nil {
		return Standing{}, err
	}
	standing := Standing{Class: class, Present: described.Present, Stacks: described.Stacks}
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

	Force bool

	AutoHeal *bool

	AcceptReplacements bool
}

func (g Gate) Apply(ctx context.Context, class Class, req ApplyRequest, report Reporter) error {
	catalogue := g.Bootstrapper.Catalogue()
	standing, err := g.Standing(ctx, class)
	if err != nil {
		return err
	}
	if err := RefuseSchemaAhead(standing.Schema, standing.Present, class); err != nil {
		return err
	}

	requested, err := featureClosure(catalogue, req.Features)
	if err != nil {
		return err
	}
	drop := featureDrop(catalogue, standing.Features, requested)
	if err := g.admitDrops(ctx, class, drop, req.Force); err != nil {
		return err
	}
	ordered, err := featureDeleteOrder(catalogue, drop)
	if err != nil {
		return err
	}

	autoHeal := standing.AutoHeal
	if req.AutoHeal != nil {
		autoHeal = *req.AutoHeal
	}
	if err := g.Bootstrapper.Apply(ctx, BootstrapRequest{
		Class:      class,
		Features:   requested,
		Drop:       ordered,
		Unattended: !req.AcceptReplacements,
		Writer:     g.Writer,
	}, report); err != nil {
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

func (g Gate) admitDrops(ctx context.Context, class Class, drop []string, force bool) error {
	if len(drop) == 0 || force {
		return nil
	}
	recorded, err := g.RecordedFeatures(ctx, class)
	if err != nil {
		return err
	}
	dependents := ProjectsDependingOn(recorded, drop)
	if len(dependents) == 0 {
		return nil
	}
	return Refuse(CodeNotReady,
		"dropping %s would break %d project(s) already deployed here: %s — re-run with --force to drop it anyway, or leave the feature in the set",
		strings.Join(drop, ", "), len(dependents), strings.Join(dependents, ", "))
}

func (g Gate) RecordedFeatures(ctx context.Context, class Class) (map[string][]string, error) {
	held, err := g.Records.List(ctx, ProjectsRecord(class))
	if err != nil {
		return nil, fmt.Errorf("read the projects deployed here: %w", err)
	}
	recorded := map[string][]string{}
	for _, record := range held {
		if len(record.Name) != 3 || len(record.Bytes) == 0 {
			continue
		}
		var project Project
		if err := json.Unmarshal(record.Bytes, &project); err != nil {
			return nil, fmt.Errorf("read %s's record: %w", record.Name, err)
		}
		recorded[record.Name[2]] = project.Features
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
	command := bootstrapCommand(class)
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
	full := append(slices.Clone(s.Features), missing...)
	slices.Sort(full)
	return Refuse(CodeNotReady,
		"this account's Ocel bootstrap lacks the features this project needs: %s.\nRun `%s --features %s` and try again",
		strings.Join(missing, ", "), command, strings.Join(slices.Compact(full), ","))
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
		detail(report, "refreshing this account's stale bootstrap stacks needs bootstrap-tier credentials and this run holds deploy-tier ones, so the bootstrap is left as it stands")
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
		if len(record.Name) < 3 || record.Name[2] == "" {
			continue
		}
		projects = append(projects, record.Name[2])
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
	return checkCompat(deployed, present, BootstrapSchema).explain(deployed, BootstrapSchema, bootstrapCommand(class))
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

func bootstrapCommand(class Class) string {
	if class == ClassPreview {
		return "ocel bootstrap --preview"
	}
	return "ocel bootstrap"
}

func bootstrapDestroyCommand(class Class) string {
	if class == ClassPreview {
		return "ocel bootstrap --destroy --preview"
	}
	return "ocel bootstrap --destroy"
}

func destroyCommand(class Class) string {
	if class == ClassPreview {
		return "ocel destroy --preview"
	}
	return "ocel destroy"
}

func detail(report Reporter, message string) {
	if report == nil {
		return
	}
	report.Detail(message)
}
