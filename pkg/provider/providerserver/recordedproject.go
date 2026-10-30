package providerserver

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider/ledger"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

var projectTiers = []environment.Tier{environment.TierProduction, environment.TierPreview}

type recordedProject struct {
	slug    string
	records map[environment.Tier]stackrecords.Project
}

func (p recordedProject) tiers() []environment.Tier {
	var tiers []environment.Tier
	for _, tier := range projectTiers {
		if _, recorded := p.records[tier]; recorded {
			tiers = append(tiers, tier)
		}
	}
	return tiers
}

func (p recordedProject) isRecordedIn(tier environment.Tier) bool {
	_, recorded := p.records[tier]
	return recorded
}

func (p recordedProject) edge() *stackrecords.ProjectEdge {
	return p.records[p.tiers()[0]].Edge
}

func (p recordedProject) registry() *stackrecords.ProjectRegistry {
	for _, tier := range p.tiers() {
		if recorded := p.records[tier].Registry; recorded != nil {
			return recorded
		}
	}
	return nil
}

func (p recordedProject) updatedAt() int64 {
	var latest int64
	for _, project := range p.records {
		latest = max(latest, project.UpdatedAt)
	}
	return latest
}

func (p recordedProject) summary(productionPromotionID string, previews int) *contractv1.ProjectSummary {
	summary := &contractv1.ProjectSummary{
		Slug:                  p.slug,
		ProductionPromotionId: productionPromotionID,
		Previews:              int32(previews),
		UpdatedAt:             p.updatedAt(),
		Edge:                  string(p.edge().Kind),
	}
	for _, tier := range p.tiers() {
		summary.Tiers = append(summary.Tiers, encodeTier(tier))
	}
	return summary
}

func isUnbootstrapped(err error) bool {
	var refused refusal.Refusal
	return errors.As(err, &refused) && refused.Code == refusal.CodeNotReady
}

func listRecordedProjects(ctx context.Context, store records.Store) ([]recordedProject, error) {
	bySlug := map[string]recordedProject{}
	for _, tier := range projectTiers {
		listed, err := stackrecords.ListProjects(ctx, store, tier)
		if isUnbootstrapped(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, project := range listed {
			recorded, known := bySlug[project.Slug]
			if !known {
				recorded = recordedProject{slug: project.Slug, records: map[environment.Tier]stackrecords.Project{}}
			}
			recorded.records[tier] = project.Project
			bySlug[project.Slug] = recorded
		}
	}
	projects := make([]recordedProject, 0, len(bySlug))
	for _, project := range bySlug {
		projects = append(projects, project)
	}
	slices.SortFunc(projects, func(a, b recordedProject) int { return strings.Compare(a.slug, b.slug) })
	return projects, nil
}

func readRecordedProject(ctx context.Context, store records.Store, slug string, tiers []environment.Tier) (recordedProject, bool, error) {
	project := recordedProject{slug: slug, records: map[environment.Tier]stackrecords.Project{}}
	for _, tier := range tiers {
		recorded, found, err := stackrecords.ReadProject(ctx, store, tier, slug)
		if isUnbootstrapped(err) {
			continue
		}
		if err != nil {
			return recordedProject{}, false, err
		}
		if found {
			project.records[tier] = recorded
		}
	}
	return project, len(project.records) > 0, nil
}

func readActiveProduction(ctx context.Context, store records.Store, project recordedProject) (router.Promotion, error) {
	if !project.isRecordedIn(environment.TierProduction) {
		return router.Promotion{}, nil
	}
	active, _, err := ledger.New(store, environment.TierProduction, project.slug).ReadActive(ctx, router.DefaultPointer)
	return active, err
}

func readPreviews(ctx context.Context, store records.Store, project recordedProject) ([]stackrecords.Environment, error) {
	if !project.isRecordedIn(environment.TierPreview) {
		return nil, nil
	}
	return stackrecords.PreviewEnvironments(ctx, store, project.slug)
}

func recordedEdge(kind edge.Kind, selection *contractv1.EdgeSelection) (*stackrecords.ProjectEdge, error) {
	recorded := &stackrecords.ProjectEdge{
		Kind:          kind,
		DNSKind:       selection.GetDns().GetKind(),
		DNSZone:       selection.GetDns().GetZone(),
		AllowDegraded: slices.Clone(selection.GetAllowDegraded()),
	}
	if options := selection.GetOptions(); len(options.GetFields()) > 0 {
		encoded, err := protojson.Marshal(options)
		if err != nil {
			return nil, fmt.Errorf("record the options of the %s edge: %w", kind, err)
		}
		recorded.Options = encoded
	}
	return recorded, nil
}

func edgeSelectionOf(recorded *stackrecords.ProjectEdge) (*contractv1.EdgeSelection, error) {
	selection := &contractv1.EdgeSelection{
		Kind:          string(recorded.Kind),
		AllowDegraded: slices.Clone(recorded.AllowDegraded),
	}
	if recorded.DNSKind != "" || recorded.DNSZone != "" {
		selection.Dns = &contractv1.Dns{Kind: recorded.DNSKind, Zone: recorded.DNSZone}
	}
	if len(recorded.Options) > 0 {
		options := &structpb.Struct{}
		if err := protojson.Unmarshal(recorded.Options, options); err != nil {
			return nil, fmt.Errorf("read the recorded options of the %s edge: %w", recorded.Kind, err)
		}
		selection.Options = options
	}
	return selection, nil
}

func recordedRegistry(registry *contractv1.ImageRegistry) *stackrecords.ProjectRegistry {
	if registry.GetServer() == "" {
		return nil
	}
	return &stackrecords.ProjectRegistry{
		Server:    registry.GetServer(),
		Namespace: registry.GetNamespace(),
		Username:  registry.GetUsername(),
	}
}

func imageRegistryOf(recorded *stackrecords.ProjectRegistry) *contractv1.ImageRegistry {
	if recorded == nil {
		return nil
	}
	return &contractv1.ImageRegistry{
		Server:    recorded.Server,
		Namespace: recorded.Namespace,
		Username:  recorded.Username,
	}
}
