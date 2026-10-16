package gcp

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/stackrecords"
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

func TestTheTagDatabaseIsAPartADeployAddsAndNoLongerInTheBase(t *testing.T) {
	t.Parallel()

	names := Names{namespace: "ocel", project: "acme-prod"}
	b := bootstrap{clients: &clients{Names: names, region: "europe-west1"}}
	for _, tier := range bothTiers {
		database := item{Kind: KindDatabase, Name: names.TagDatabase(tier)}
		if slices.Contains(idsOf(bootstrapItems(names, tier, false)), database.ID()) {
			t.Errorf("a %s bootstrap provisions %s, which the first deploy that revalidates adds", tier, database.ID())
		}
		tags := b.tagDatabasePart(tier)
		if tags.name != tagDatabasePart || len(tags.resources) != 1 || tags.resources[0].ID != names.TagDatabase(tier) {
			t.Errorf("the %s tag database part is %+v, want it holding %s", tier, tags, names.TagDatabase(tier))
		}
		if condition := partDatabasesCondition(names); !strings.Contains(condition, `"projects/acme-prod/databases/`+names.TagDatabase(tier)+`"`) {
			t.Errorf("a deploy is granted the part databases role on %s, want it to name the %s tag database it makes", condition, tier)
		}
	}
}

func revalidating(revalidates bool) provider.DeployPreflight {
	return provider.DeployPreflight{
		Deploy: provider.DeploySpec{Tier: environment.TierProduction, Slug: "shop"},
		Uses:   []buildoutput.Uses{{ImageOptimization: true}, {Revalidation: revalidates}},
	}
}

func TestADryDeployWhoseBuildRevalidatesPlansTheTagDatabaseAndOneThatDoesNotReadsNothing(t *testing.T) {
	var planned []provider.ChangeGroup
	pre := revalidating(true)
	pre.Dry = true
	pre.AddToPlan = func(groups ...provider.ChangeGroup) { planned = append(planned, groups...) }

	if err := failingOnAnyCall(t).PreflightDeploy(context.Background(), pre); err != nil {
		t.Fatalf("PreflightDeploy() = %v", err)
	}
	if len(planned) != 1 || planned[0].Part != tagDatabasePart || planned[0].Action != provider.ActionCreate {
		t.Errorf("a dry deploy whose build revalidates planned %+v, want the %s part created", planned, tagDatabasePart)
	}
	p := failingOnAnyCall(t)
	if err := p.PreflightDeploy(context.Background(), revalidating(false)); err != nil {
		t.Errorf("PreflightDeploy() of a build that revalidates nothing = %v", err)
	}
	if entries, err := stackrecords.ListBootstrapParts(context.Background(), p.KeyValues(), environment.TierProduction); err != nil || len(entries) != 0 {
		t.Errorf("a deploy that revalidates nothing recorded %v, %v, want no part", entries, err)
	}
}

func TestTheFirstDeployThatRevalidatesRecordsTheTagDatabasePart(t *testing.T) {
	p, _ := deployingNetworks(t)
	ctx := context.Background()

	if err := p.PreflightDeploy(ctx, revalidating(true)); err != nil {
		t.Fatalf("PreflightDeploy() = %v", err)
	}
	recorded, present, err := stackrecords.ReadBootstrapPart(ctx, p.KeyValues(), environment.TierProduction, tagDatabasePart)
	if err != nil || !present || recorded.State != stackrecords.PartReady || recorded.AddedBy != "shop" {
		t.Errorf("the part record = %+v, %t, %v, want it ready and added by shop", recorded, present, err)
	}
}
