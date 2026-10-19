package conformance

import (
	"context"
	"errors"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
)

func plansNothing(context.Context, environment.Tier, string) ([]provider.ChangeGroup, error) {
	return nil, nil
}

func removesNothing(context.Context, environment.Tier, string, progress.Log) error { return nil }

func TestARemovalOfAProjectNothingDeployedThatPlansNothingAndRemovesTwicePasses(t *testing.T) {
	hooks := &provider.ProjectRemovalHooks{Plan: plansNothing, Remove: removesNothing}
	if found := projectRemovalFaults(context.Background(), hooks); len(found) != 0 {
		t.Errorf("projectRemovalFaults() = %v, want none", found)
	}
}

func TestAProjectRemovalMissingPlanOrRemoveFails(t *testing.T) {
	for name, hooks := range map[string]*provider.ProjectRemovalHooks{
		"no plan":   {Remove: removesNothing},
		"no remove": {Plan: plansNothing},
	} {
		t.Run(name, func(t *testing.T) {
			if found := projectRemovalFaults(context.Background(), hooks); len(found) == 0 {
				t.Error("projectRemovalFaults() = none, want a removal that cannot both plan and remove flagged")
			}
		})
	}
}

func TestARemovalPlanForAProjectNothingDeployedThatShowsAGroupFails(t *testing.T) {
	hooks := &provider.ProjectRemovalHooks{
		Plan: func(context.Context, environment.Tier, string) ([]provider.ChangeGroup, error) {
			return []provider.ChangeGroup{{Name: "images", Action: provider.ActionDelete}}, nil
		},
		Remove: removesNothing,
	}
	if found := projectRemovalFaults(context.Background(), hooks); len(found) == 0 {
		t.Error("projectRemovalFaults() = none, want a group planned for a project nothing deployed flagged")
	}
}

func TestARemovalPlanThatErrorsFails(t *testing.T) {
	hooks := &provider.ProjectRemovalHooks{
		Plan: func(context.Context, environment.Tier, string) ([]provider.ChangeGroup, error) {
			return nil, errors.New("no such project")
		},
		Remove: removesNothing,
	}
	if found := projectRemovalFaults(context.Background(), hooks); len(found) == 0 {
		t.Error("projectRemovalFaults() = none, want the error planning a project nothing deployed flagged")
	}
}

func TestARemovalThatFailsTheSecondTimeFails(t *testing.T) {
	removed := 0
	hooks := &provider.ProjectRemovalHooks{
		Plan: plansNothing,
		Remove: func(context.Context, environment.Tier, string, progress.Log) error {
			removed++
			if removed > 1 {
				return errors.New("already removed")
			}
			return nil
		},
	}
	if found := projectRemovalFaults(context.Background(), hooks); len(found) == 0 {
		t.Error("projectRemovalFaults() = none, want a rerun removal that fails on what is already gone flagged")
	}
}

func TestARemovalAsksForTheProjectNothingDeployed(t *testing.T) {
	var planned, removed []string
	hooks := &provider.ProjectRemovalHooks{
		Plan: func(_ context.Context, tier environment.Tier, project string) ([]provider.ChangeGroup, error) {
			planned = append(planned, string(tier)+"/"+project)
			return nil, nil
		},
		Remove: func(_ context.Context, tier environment.Tier, project string, _ progress.Log) error {
			removed = append(removed, string(tier)+"/"+project)
			return nil
		},
	}
	projectRemovalFaults(context.Background(), hooks)
	want := string(environment.TierPreview) + "/" + untouchedProject
	if len(planned) != 1 || planned[0] != want || len(removed) != 2 || removed[0] != want || removed[1] != want {
		t.Errorf("projectRemovalFaults() planned %v and removed %v, want %q planned once and removed twice", planned, removed, want)
	}
}
