package deploy

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	"google.golang.org/protobuf/proto"

	linksv1 "github.com/ocelhq/ocel/pkg/proto/common/links/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

type Consumed struct {
	Resource string
	Record   PublishedRecord
}

type UnpublishedLinkError struct {
	Links       []string
	Class       string
	Environment string
	Siblings    []string
	FoundIn     map[string][]string
}

func (e *UnpublishedLinkError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b,
		"`links` binds %s, and nothing has published a record under %s to %s. "+
			"Ocel never runs your infrastructure tool for you: run it, then deploy again",
		quoteAll(e.Links), thatName(len(e.Links)), describeCoordinate(e.Class, e.Environment),
	)
	for _, link := range e.Links {
		if classes := e.FoundIn[link]; len(classes) > 0 {
			fmt.Fprintf(&b, "\n\n%q is published to %s instead. A publisher writes to one coordinate: point one at %s as well",
				link, strings.Join(classes, " and "), e.Class)
		}
	}
	if len(e.Siblings) == 0 {
		fmt.Fprintf(&b, "\n\nNothing at all is published to %s.", describeCoordinate(e.Class, e.Environment))
		return b.String()
	}
	fmt.Fprintf(&b, "\n\nPublished to %s: %s.", describeCoordinate(e.Class, e.Environment), strings.Join(e.Siblings, ", "))
	return b.String()
}

type HandoverError struct {
	Links []string
	Stack string
}

func (e *HandoverError) Error() string {
	return fmt.Sprintf(
		"`links` binds %s, which ocel provisions in this environment today — stack %s still holds what it provisioned under %s. "+
			"Binding it hands ownership to your own infrastructure, and this deploy would delete ocel's copy: a database is torn down with no final snapshot, and its data goes with it. "+
			"Ocel hands no live resource over on its own. Back the data up, drop %s from the resource declarations and from `links`, deploy once to let ocel release it, then declare it again with the link in place",
		quoteAll(e.Links), e.Stack, thatName(len(e.Links)), quoteAll(e.Links),
	)
}

func handedOver(manifest *contractv1.Manifest, provisioned map[string]bool, stack string) error {
	var handed []string
	for _, r := range linkedResources(manifest) {
		if provisioned[r.GetLogicalName()] {
			handed = append(handed, r.GetResource().GetName())
		}
	}
	if len(handed) == 0 {
		return nil
	}
	return &HandoverError{Links: handed, Stack: stack}
}

type LinkShapeError struct {
	Link      string
	Declared  linksv1.LinkType
	Published linksv1.LinkType
}

func (e *LinkShapeError) Error() string {
	return fmt.Sprintf(
		"`links` binds %q as a %s, and the record published under that name is a %s. "+
			"Every app that uses %q would fail at its first cold start, so this deploy stops here. "+
			"Declare it as what was published, or republish it as a %s",
		e.Link, e.Declared, e.Published, e.Link, e.Declared,
	)
}

type LinkSourceError struct {
	Link   string
	Type   linksv1.LinkType
	Source string
}

func (e *LinkSourceError) Error() string {
	return fmt.Sprintf(
		"`links` binds %q to a %s record published by %s, and ocel's bucket client cannot serve a bucket it did not provision. "+
			"Hand the app its bucket name as an env var (`ocel env set`) instead",
		e.Link, e.Type, e.Source,
	)
}

var foreignSourceAdmitted = map[linksv1.LinkType]bool{
	linksv1.LinkType_LINK_TYPE_POSTGRES: true,
	linksv1.LinkType_LINK_TYPE_BUCKET:   false,
}

type CustomLinkBoundError struct {
	Link string
}

func (e *CustomLinkBoundError) Error() string {
	return fmt.Sprintf(
		"`links` binds %q, and the record published under that name is a custom one: "+
			"a custom link is read by transforms; it is external by definition and never provisioned, so it is not bound here. "+
			"Drop %q from `links` and read it from a transform as `links.%s.<property>`",
		e.Link, e.Link, e.Link)
}

func readableAs(record PublishedRecord, declared linksv1.LinkType) error {
	if record.Type() == linksv1.LinkType_LINK_TYPE_CUSTOM {
		return &CustomLinkBoundError{Link: record.Name()}
	}
	if published := record.Type(); published != declared {
		return &LinkShapeError{Link: record.Name(), Declared: declared, Published: published}
	}
	if source := record.Link.GetSource(); source != "" && !foreignSourceAdmitted[declared] {
		return &LinkSourceError{Link: record.Name(), Type: declared, Source: source}
	}
	return nil
}

func thatName(n int) string {
	if n == 1 {
		return "that name"
	}
	return "those names"
}

func quoteAll(names []string) string {
	quoted := make([]string, 0, len(names))
	for _, n := range names {
		quoted = append(quoted, fmt.Sprintf("%q", n))
	}
	return strings.Join(quoted, ", ")
}

func describeCoordinate(class, environment string) string {
	if environment == "" {
		return class
	}
	return class + "/" + environment
}

func linkedResources(manifest *contractv1.Manifest) []*contractv1.ManifestResource {
	var out []*contractv1.ManifestResource
	for _, r := range manifest.GetResources() {
		if r.GetLinked() {
			out = append(out, r)
		}
	}
	return out
}

func consumeLinks(ctx context.Context, cfg Config, manifest *contractv1.Manifest, warn func(string)) (map[string]Consumed, error) {
	linked := linkedResources(manifest)
	if cfg.Links == nil {
		if len(linked) == 0 {
			return nil, nil
		}
		return nil, fmt.Errorf("this deploy binds %d linked resources but reached no variable store to read their records from", len(linked))
	}

	environment := overrideEnvironment(cfg)
	published, err := cfg.Links.PublishedNames(ctx, manifest.GetSlug(), string(cfg.Class), environment)
	if err != nil {
		return nil, err
	}
	warnShadowedProvisioning(cfg, manifest, published, warn)
	if len(linked) == 0 {
		return nil, nil
	}

	names := make([]string, 0, len(linked))
	var missing []string
	for _, r := range linked {
		name := r.GetResource().GetName()
		names = append(names, name)
		if !slices.Contains(published, name) {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return nil, cfg.refuseUnpublished(ctx, manifest.GetSlug(), environment, missing, published)
	}

	records, err := cfg.Links.ResolveRecords(ctx, manifest.GetSlug(), environment, names)
	if err != nil {
		return nil, err
	}
	for i, r := range linked {
		if err := readableAs(records[i], r.GetResource().GetType()); err != nil {
			return nil, err
		}
	}

	out := make(map[string]Consumed, len(linked))
	for i, r := range linked {
		out[r.GetLogicalName()] = Consumed{Resource: r.GetLogicalName(), Record: records[i]}
	}
	return out, nil
}

func (cfg Config) refuseUnpublished(ctx context.Context, slug, environment string, missing, published []string) error {
	found := map[string][]string{}
	for _, name := range missing {
		if classes := cfg.classesPublishing(ctx, slug, environment, name); len(classes) > 0 {
			found[name] = classes
		}
	}
	return &UnpublishedLinkError{
		Links:       missing,
		Class:       string(cfg.Class),
		Environment: environment,
		Siblings:    published,
		FoundIn:     found,
	}
}

func (cfg Config) classesPublishing(ctx context.Context, slug, environment, name string) []string {
	var found []string
	for _, class := range cfg.VarsSiblingClasses {
		if class == string(cfg.Class) {
			continue
		}
		if slices.Contains(cfg.publishedOrNothing(ctx, slug, class, environment), name) {
			found = append(found, class)
		}
	}
	return found
}

func (cfg Config) publishedOrNothing(ctx context.Context, slug, class, environment string) []string {
	names, err := cfg.Links.PublishedNames(ctx, slug, class, environment)
	if err != nil {
		return nil
	}
	return names
}

func warnShadowedProvisioning(cfg Config, manifest *contractv1.Manifest, published []string, warn func(string)) {
	for _, r := range manifest.GetResources() {
		name := r.GetResource().GetName()
		if r.GetLinked() || !slices.Contains(published, name) {
			continue
		}
		warn(fmt.Sprintf(
			"a link named %q is already published to %s, and this deploy provisions %s beside it. "+
				"Ocel binds neither to the other on its own: add %q to `links` to consume the published record instead",
			name, describeCoordinate(string(cfg.Class), overrideEnvironment(cfg)), r.GetLogicalName(), name,
		))
	}
}

func consumedLinks(consumed map[string]Consumed) []*linksv1.Link {
	out := make([]*linksv1.Link, 0, len(consumed))
	for _, name := range slices.Sorted(maps.Keys(consumed)) {
		c := consumed[name]
		link := proto.Clone(c.Record.Link).(*linksv1.Link)
		link.Name = c.Resource
		out = append(out, link)
	}
	return out
}
