package gcp

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/googletest"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
	"github.com/ocelhq/ocel/platform/gcp/provider/resources/variables"
)

func TestTheTagRecordsOfAnISRPrefixAreReadThroughOneIndexOnPrefixThenWriteTime(t *testing.T) {
	t.Parallel()

	indexes := tagIndexes()
	if len(indexes) != 1 {
		t.Fatalf("tagIndexes() = %d indexes, want the one a prefix's records are read through", len(indexes))
	}
	if indexes[0].QueryScope != "COLLECTION" {
		t.Errorf("the index's query scope = %q, want COLLECTION", indexes[0].QueryScope)
	}
	var got []string
	for _, field := range indexes[0].Fields {
		got = append(got, field.FieldPath+" "+field.Order)
	}
	if want := []string{"prefix ASCENDING", "writtenAt ASCENDING"}; !slices.Equal(got, want) {
		t.Errorf("the index's fields = %q, want %q", got, want)
	}
}

func TestNoTagRecordFieldAQueryNeverFiltersOnIsIndexed(t *testing.T) {
	t.Parallel()

	want := []string{"writtenAt", "tag", "stale", "expired"}
	if got := unindexedTagFields(); !slices.Equal(got, want) {
		t.Errorf("unindexedTagFields() = %q, want %q", got, want)
	}
	if slices.Contains(unindexedTagFields(), "prefix") {
		t.Error("the prefix is exempted from indexing, and a prune ranges over it")
	}
}

func TestNoTierKeepsADatabaseOfRunsOrTagRecordsAndADeployMayMakeEachProjectsOwn(t *testing.T) {
	t.Parallel()

	names := names.New("ocel", "acme-prod")
	for _, tier := range googletest.BothTiers {
		for _, item := range bootstrap.Resources(names, tier, false) {
			if item.Kind == bootstrap.KindDatabase && item.Name != names.Database() {
				t.Errorf("a %s bootstrap provisions database %s, and every database but the namespace's is a project's own", tier, item.Name)
			}
		}
		prefix := `resource.name.startsWith("projects/acme-prod/databases/` + names.ProjectWritableDatabasePrefix(tier) + `")`
		if grant := variables.ProjectRecordsGrant(names, "europe-west1"); !strings.Contains(grant, prefix) {
			t.Errorf("a deploy is granted %s, want it to name the %s databases of projects' runs and tag records it makes", grant, tier)
		}
	}
	for _, offered := range composed(bootstrap.Engine{}).Parts() {
		if offered.Name == "tasks" || offered.Name == "tag-database" {
			t.Errorf("the bootstrap offers the %s part, and runs and tag records are kept in each project's own database", offered.Name)
		}
	}
}

func revalidating(revalidates bool) provider.DeployPreflight {
	return provider.DeployPreflight{
		Deploy: provider.DeploySpec{Tier: environment.TierProduction, Slug: "shop"},
		Uses:   []buildoutput.Uses{{ImageOptimization: true}, {Revalidation: revalidates}},
	}
}

func TestADryDeployWhoseBuildRevalidatesPlansItsProjectsWritableDatabaseAndOneThatDoesNotPlansNothing(t *testing.T) {
	database := ports.ProjectWritableDatabase("ocel", environment.TierProduction, "shop")
	if planned := plannedBy(t, failingOnAnyCall(t), revalidating(true)); !plansWritableDatabase(planned, database) {
		t.Errorf("a dry deploy whose build revalidates planned %+v, want %s created", planned, database)
	}
	p := failingOnAnyCall(t)
	if planned := plannedBy(t, p, revalidating(false)); len(planned) != 0 {
		t.Errorf("a dry deploy of a build that revalidates nothing planned %+v, want nothing", planned)
	}
}

func TestTheFirstDeployThatRevalidatesMakesItsProjectsWritableDatabaseAndTheNextPlansNothing(t *testing.T) {
	p, _ := deployingNetworks(t)
	ctx := context.Background()

	if err := p.ApplyPreflight(ctx, revalidating(true)); err != nil {
		t.Fatalf("ApplyPreflight() = %v", err)
	}
	if planned := plannedBy(t, p, revalidating(true)); len(planned) != 0 {
		t.Errorf("a dry deploy after the first planned %+v, want nothing: the first recorded the database it made", planned)
	}
}

func TestRemovingAProjectFromATierForgetsTheDatabaseOfItsRunsAndTagRecords(t *testing.T) {
	p, _ := deployingNetworks(t)
	ctx := context.Background()
	if err := p.ApplyPreflight(ctx, revalidating(true)); err != nil {
		t.Fatalf("ApplyPreflight() = %v", err)
	}

	if err := p.RemoveProject(ctx, environment.TierProduction, "shop", progress.Discard()); err != nil {
		t.Fatalf("RemoveProject() = %v", err)
	}
	if planned := plannedBy(t, p, revalidating(true)); !plansWritableDatabase(planned, ports.ProjectWritableDatabase("ocel", environment.TierProduction, "shop")) {
		t.Errorf("a dry deploy after shop left the tier planned %+v, want its database made again", planned)
	}
}
