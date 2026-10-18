package stackrecords_test

import (
	"context"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

func recordWants(t *testing.T, store *fake.KeyValues, tier environment.Tier, wants map[string][]string) {
	t.Helper()
	for part, projects := range wants {
		for _, project := range projects {
			if err := stackrecords.RecordBootstrapWant(context.Background(), store, tier, part, project); err != nil {
				t.Fatalf("RecordBootstrapWant(%s, %s) error = %v", part, project, err)
			}
		}
	}
}

func listWants(t *testing.T, store *fake.KeyValues, tier environment.Tier) map[string][]string {
	t.Helper()
	wants, err := stackrecords.ListBootstrapWants(context.Background(), store, tier)
	if err != nil {
		t.Fatalf("ListBootstrapWants() error = %v", err)
	}
	return wants
}

func TestATierListsEveryProjectThatWantsAPartOnceHoweverOftenEachRecordedIt(t *testing.T) {
	t.Parallel()
	store := fake.NewKeyValues()
	recordWants(t, store, environment.TierProduction, map[string][]string{
		"variables-key":   {"web", "api", "web"},
		"cloudflare-edge": {"docs"},
	})

	got := listWants(t, store, environment.TierProduction)
	for part := range got {
		slices.Sort(got[part])
	}

	want := map[string][]string{"variables-key": {"api", "web"}, "cloudflare-edge": {"docs"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ListBootstrapWants() = %v, want %v", got, want)
	}
	if other := listWants(t, store, environment.TierPreview); len(other) != 0 {
		t.Errorf("ListBootstrapWants(preview) = %v, want nothing: the wants were production's", other)
	}
}

func TestAWantIsNeverListedAsAPartAndAPartNeverAsAWant(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewKeyValues()
	recordWants(t, store, environment.TierProduction, map[string][]string{"variables-key": {"web"}})
	if _, _, err := stackrecords.ClaimBootstrapPart(ctx, store, environment.TierProduction, "containers", claimOf("api")); err != nil {
		t.Fatal(err)
	}

	parts, err := stackrecords.ListBootstrapParts(ctx, store, environment.TierProduction)
	if err != nil {
		t.Fatal(err)
	}
	if _, listed := parts["variables-key"]; listed || len(parts) != 1 {
		t.Errorf("ListBootstrapParts() = %v, want containers alone", parts)
	}
	if wants := listWants(t, store, environment.TierProduction); !reflect.DeepEqual(wants, map[string][]string{"variables-key": {"web"}}) {
		t.Errorf("ListBootstrapWants() = %v, want web's want of variables-key alone", wants)
	}
}

func TestForgettingAPartsWantsForgetsEveryProjectsWantOfItAndNoOther(t *testing.T) {
	t.Parallel()
	store := fake.NewKeyValues()
	recordWants(t, store, environment.TierProduction, map[string][]string{
		"variables-key":   {"web", "api"},
		"cloudflare-edge": {"web"},
	})

	if err := stackrecords.ForgetBootstrapWants(context.Background(), store, environment.TierProduction, "variables-key"); err != nil {
		t.Fatal(err)
	}

	want := map[string][]string{"cloudflare-edge": {"web"}}
	if got := listWants(t, store, environment.TierProduction); !reflect.DeepEqual(got, want) {
		t.Errorf("ListBootstrapWants() = %v, want %v", got, want)
	}
}

func TestForgettingAProjectsWantsLeavesEveryOtherProjectsWants(t *testing.T) {
	t.Parallel()
	store := fake.NewKeyValues()
	recordWants(t, store, environment.TierProduction, map[string][]string{
		"variables-key":   {"web", "api"},
		"cloudflare-edge": {"web"},
	})

	if err := stackrecords.ForgetProjectWants(context.Background(), store, environment.TierProduction, "web"); err != nil {
		t.Fatal(err)
	}

	want := map[string][]string{"variables-key": {"api"}}
	if got := listWants(t, store, environment.TierProduction); !reflect.DeepEqual(got, want) {
		t.Errorf("ListBootstrapWants() = %v, want %v", got, want)
	}
}

func TestATierListsEachPartsProjectsInTheOrderEachFirstWantedIt(t *testing.T) {
	t.Parallel()
	store := fake.NewKeyValues()
	for _, project := range []string{"web", "api", "web"} {
		recordWants(t, store, environment.TierProduction, map[string][]string{"variables-key": {project}})
		time.Sleep(time.Millisecond)
	}

	got := listWants(t, store, environment.TierProduction)

	if want := []string{"web", "api"}; !slices.Equal(got["variables-key"], want) {
		t.Errorf("ListBootstrapWants() = %v, want %v: web wanted it first, and wanting it again changes nothing", got, want)
	}
}

type listingStore struct {
	*fake.KeyValues
	listed [][]string
}

func (s *listingStore) List(ctx context.Context, in records.Partition, under ...string) ([]records.Entry, error) {
	s.listed = append(s.listed, slices.Clone(under))
	return s.KeyValues.List(ctx, in, under...)
}

func TestForgettingAProjectsWantsReadsThatProjectsWantsAlone(t *testing.T) {
	t.Parallel()
	store := &listingStore{KeyValues: fake.NewKeyValues()}
	recordWants(t, store.KeyValues, environment.TierProduction, map[string][]string{"variables-key": {"web", "api"}})

	if err := stackrecords.ForgetProjectWants(context.Background(), store, environment.TierProduction, "web"); err != nil {
		t.Fatal(err)
	}

	for _, under := range store.listed {
		if !slices.Equal(under, []string{"wants", "web"}) {
			t.Errorf("ForgetProjectWants(web) listed under %v, want web's wants alone, however many projects the tier has", under)
		}
	}
}
