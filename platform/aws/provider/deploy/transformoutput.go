package deploy

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
	"github.com/ocelhq/ocel/platform/aws/provider/transform"
)

const outputPlaceholderKey = "$ocelOutput"

type outputRef struct {
	Link     string
	Property string
	List     bool
}

type outputSite struct {
	Resource string
	Surface  string
	Field    string
}

func (s outputSite) String() string {
	return fmt.Sprintf("%s's %s.%s", s.Resource, s.Surface, s.Field)
}

type placedOutput struct {
	Ref outputRef
	At  outputSite
}

type OutputPlaceholderError struct {
	At     outputSite
	Reason string
}

func (e *OutputPlaceholderError) Error() string {
	return fmt.Sprintf(
		"a transform fills %s with a link output that %s; author one with `output(link, property)` or `outputList(link, property)`",
		e.At, e.Reason)
}

type UnpublishedOutputError struct {
	Ref         outputRef
	At          outputSite
	Class       string
	Environment string
	Published   []string
	FoundIn     []string
}

func (e *UnpublishedOutputError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b,
		"a transform fills %s from link %q's %s, and nothing has published a record under that name to %s. "+
			"Ocel never runs your infrastructure tool for you: run it, then deploy again",
		e.At, e.Ref.Link, e.Ref.Property, describeCoordinate(e.Class, e.Environment))
	if len(e.FoundIn) > 0 {
		fmt.Fprintf(&b, "\n\n%q is published to %s instead. A publisher writes to one coordinate: point one at %s as well",
			e.Ref.Link, strings.Join(e.FoundIn, " and "), e.Class)
	}
	if len(e.Published) == 0 {
		fmt.Fprintf(&b, "\n\nNothing at all is published to %s.", describeCoordinate(e.Class, e.Environment))
		return b.String()
	}
	fmt.Fprintf(&b, "\n\nPublished to %s: %s.", describeCoordinate(e.Class, e.Environment), strings.Join(e.Published, ", "))
	return b.String()
}

type OutputPropertyError struct {
	Ref     outputRef
	At      outputSite
	Carries []string
}

func (e *OutputPropertyError) Error() string {
	return fmt.Sprintf(
		"a transform fills %s from link %q's %s, and the published record carries no such property. "+
			"The record carries %s — republish it with %s",
		e.At, e.Ref.Link, e.Ref.Property, carried(e.Carries), e.Ref.Property)
}

type ProvisionedOutputError struct {
	Ref outputRef
	At  outputSite
}

func (e *ProvisionedOutputError) Error() string {
	return fmt.Sprintf(
		"a transform fills %s from link %q's %s, and this deploy provisions %q itself. "+
			"Ocel publishes what it provisions long after the transforms have run, so its outputs are not there to read: "+
			"drop %q from the resource declarations and bind it through `links` if your own infrastructure owns it, or name a link that does",
		e.At, e.Ref.Link, e.Ref.Property, e.Ref.Link, e.Ref.Link)
}

type EmptyOutputError struct {
	Ref outputRef
	At  outputSite
}

func (e *EmptyOutputError) Error() string {
	return fmt.Sprintf(
		"a transform fills %s from link %q's %s, and the published record carries nothing under it. "+
			"A field an operator filled is never rendered as one they left alone, so this deploy stops here: republish %q with a value under %s",
		e.At, e.Ref.Link, e.Ref.Property, e.Ref.Link, e.Ref.Property)
}

func resolveOutputs(ctx context.Context, cfg Config, manifest *deploymentsv1.Manifest, candidates []transformCandidate, results []transform.Result) error {
	var placed []placedOutput
	if err := walkOutputs(candidates, results, func(ref outputRef, at outputSite, authored any) (any, error) {
		placed = append(placed, placedOutput{Ref: ref, At: at})
		return authored, nil
	}); err != nil {
		return err
	}
	if len(placed) == 0 {
		return nil
	}
	if err := refuseProvisionedOutputs(manifest, placed); err != nil {
		return err
	}

	values, err := readOutputs(ctx, cfg, manifest.GetSlug(), placed)
	if err != nil {
		return err
	}
	return walkOutputs(candidates, results, func(ref outputRef, _ outputSite, _ any) (any, error) {
		return values[ref], nil
	})
}

func refuseProvisionedOutputs(manifest *deploymentsv1.Manifest, placed []placedOutput) error {
	provisioned := map[string]bool{}
	for _, r := range manifest.GetResources() {
		if !r.GetLinked() {
			provisioned[r.GetLogicalName()] = true
		}
	}
	for _, p := range placed {
		if provisioned[p.Ref.Link] {
			return &ProvisionedOutputError{Ref: p.Ref, At: p.At}
		}
	}
	return nil
}

func walkOutputs(candidates []transformCandidate, results []transform.Result, resolve func(outputRef, outputSite, any) (any, error)) error {
	for i, result := range results {
		for _, key := range slices.Sorted(maps.Keys(result.Surfaces)) {
			args := result.Surfaces[key]
			for _, field := range slices.Sorted(maps.Keys(args)) {
				at := outputSite{Resource: candidates[i].name, Surface: key, Field: field}
				resolved, err := mapOutputs(args[field], at, resolve)
				if err != nil {
					return err
				}
				args[field] = resolved
			}
		}
	}
	return nil
}

func mapOutputs(value any, at outputSite, resolve func(outputRef, outputSite, any) (any, error)) (any, error) {
	switch t := value.(type) {
	case map[string]any:
		ref, named, err := readOutputRef(t, at)
		if err != nil {
			return nil, err
		}
		if named {
			return resolve(ref, at, value)
		}
		out := make(map[string]any, len(t))
		for key, item := range t {
			resolved, err := mapOutputs(item, at, resolve)
			if err != nil {
				return nil, err
			}
			out[key] = resolved
		}
		return out, nil
	case []any:
		out := make([]any, len(t))
		for i, item := range t {
			resolved, err := mapOutputs(item, at, resolve)
			if err != nil {
				return nil, err
			}
			out[i] = resolved
		}
		return out, nil
	}
	return value, nil
}

func readOutputRef(m map[string]any, at outputSite) (outputRef, bool, error) {
	raw, named := m[outputPlaceholderKey]
	if !named {
		return outputRef{}, false, nil
	}
	if len(m) != 1 {
		return outputRef{}, false, &OutputPlaceholderError{At: at, Reason: "carries keys beside the one it names"}
	}
	fields, ok := raw.(map[string]any)
	if !ok {
		return outputRef{}, false, &OutputPlaceholderError{At: at, Reason: "names neither a link nor a property"}
	}
	link, _ := fields["link"].(string)
	property, _ := fields["property"].(string)
	if link == "" {
		return outputRef{}, false, &OutputPlaceholderError{At: at, Reason: "names no link"}
	}
	if property == "" {
		return outputRef{}, false, &OutputPlaceholderError{At: at, Reason: fmt.Sprintf("names link %q and no property on it", link)}
	}
	list, _ := fields["list"].(bool)
	return outputRef{Link: link, Property: property, List: list}, true, nil
}

func readOutputs(ctx context.Context, cfg Config, slug string, placed []placedOutput) (map[outputRef]any, error) {
	if cfg.Links == nil {
		return nil, fmt.Errorf(
			"a transform fills %s from link %q, and this deploy reached no variable store to read published records from",
			placed[0].At, placed[0].Ref.Link)
	}

	environment := overrideEnvironment(cfg)
	published, err := cfg.Links.PublishedNames(ctx, slug, cfg.VarsClass, environment)
	if err != nil {
		return nil, fmt.Errorf("a transform fills %s from link %q: %w", placed[0].At, placed[0].Ref.Link, err)
	}
	for _, p := range placed {
		if !slices.Contains(published, p.Ref.Link) {
			return nil, &UnpublishedOutputError{
				Ref: p.Ref, At: p.At, Class: cfg.VarsClass, Environment: environment, Published: published,
				FoundIn: cfg.classesPublishing(ctx, slug, environment, p.Ref.Link),
			}
		}
	}

	names := make([]string, 0, len(placed))
	for _, p := range placed {
		if !slices.Contains(names, p.Ref.Link) {
			names = append(names, p.Ref.Link)
		}
	}
	slices.Sort(names)

	records, err := cfg.Links.ResolveRecords(ctx, slug, environment, names)
	if err != nil {
		return nil, fmt.Errorf("a transform fills %s from link %q: %w", placed[0].At, placed[0].Ref.Link, err)
	}
	if len(records) != len(names) {
		return nil, fmt.Errorf("the variable store resolved %d records for %d links a transform reads", len(records), len(names))
	}
	properties := make(map[string]map[string]string, len(records))
	for i, name := range names {
		properties[name] = records[i].Properties
	}

	values := make(map[outputRef]any, len(placed))
	for _, p := range placed {
		if _, done := values[p.Ref]; done {
			continue
		}
		raw, carries := properties[p.Ref.Link][p.Ref.Property]
		if !carries {
			return nil, &OutputPropertyError{
				Ref: p.Ref, At: p.At, Carries: slices.Sorted(maps.Keys(properties[p.Ref.Link])),
			}
		}
		value := outputValue(p.Ref, raw)
		if emptyOutput(value) {
			return nil, &EmptyOutputError{Ref: p.Ref, At: p.At}
		}
		values[p.Ref] = value
	}
	return values, nil
}

func outputValue(ref outputRef, raw string) any {
	if !ref.List {
		return raw
	}
	out := []any{}
	for _, item := range strings.Split(raw, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}

func emptyOutput(value any) bool {
	switch t := value.(type) {
	case string:
		return strings.TrimSpace(t) == ""
	case []any:
		return len(t) == 0
	}
	return false
}
