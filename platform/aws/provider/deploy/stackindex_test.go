package deploy

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/naming"
)

type fakeStackIndex struct {
	projects      []string
	stacks        map[string][]naming.StackName
	stacksQueried []string
	added         []naming.StackName
	removed       []naming.StackName
	projectsGone  []string
	err           error
}

func (f *fakeStackIndex) AddProject(_ context.Context, project string, features []string) error {
	if f.err != nil {
		return f.err
	}
	if !slices.Contains(f.projects, project) {
		f.projects = append(f.projects, project)
	}
	return nil
}

func (f *fakeStackIndex) AddStack(_ context.Context, _ string, stack naming.StackName) error {
	if f.err != nil {
		return f.err
	}
	f.added = append(f.added, stack)
	return nil
}

func (f *fakeStackIndex) RemoveStack(_ context.Context, _ string, stack naming.StackName) error {
	if f.err != nil {
		return f.err
	}
	f.removed = append(f.removed, stack)
	for project, names := range f.stacks {
		f.stacks[project] = slices.DeleteFunc(names, func(n naming.StackName) bool { return n == stack })
	}
	return nil
}

func (f *fakeStackIndex) RemoveProject(_ context.Context, project string) error {
	if f.err != nil {
		return f.err
	}
	f.projectsGone = append(f.projectsGone, project)
	f.projects = slices.DeleteFunc(f.projects, func(s string) bool { return s == project })
	return nil
}

func (f *fakeStackIndex) Stacks(_ context.Context, project string) ([]naming.StackName, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.stacksQueried = append(f.stacksQueried, project)
	return f.stacks[project], nil
}

func (f *fakeStackIndex) Projects(_ context.Context) ([]string, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.projects, nil
}

func testRelease(t *testing.T, buildID string) naming.Release {
	t.Helper()
	return naming.NewRelease(buildID, "")
}

func TestPlanProjectTeardown(t *testing.T) {
	t.Parallel()

	t.Run("covers only this project's stacks", func(t *testing.T) {
		t.Parallel()

		web := naming.AppStack("prod", "web", testRelease(t, "b1"))
		index := &fakeStackIndex{stacks: map[string][]naming.StackName{
			"shop": {naming.InfraStack("prod"), web},
			"othr": {naming.InfraStack("prod")},
		}}

		plan, err := PlanProjectTeardown(context.Background(), Config{Stacks: index}, "shop")
		if err != nil {
			t.Fatalf("PlanProjectTeardown: %v", err)
		}
		want := ProjectTeardownPlan{
			InfraStack: naming.InfraStack("prod"),
			AppStacks:  []naming.StackName{web},
		}
		if !reflect.DeepEqual(plan, want) {
			t.Fatalf("PlanProjectTeardown = %+v, want %+v", plan, want)
		}
		if !reflect.DeepEqual(index.stacksQueried, []string{"shop"}) {
			t.Errorf("queried %v, want the project's own partition only", index.stacksQueried)
		}
	})

	t.Run("without an index the plan is refused", func(t *testing.T) {
		t.Parallel()

		if _, err := PlanProjectTeardown(context.Background(), Config{}, "shop"); !errors.Is(err, errNoStackIndex) {
			t.Fatalf("PlanProjectTeardown err = %v, want %v", err, errNoStackIndex)
		}
	})
}

func TestPlanPreviewProjectTeardown(t *testing.T) {
	t.Parallel()

	web := naming.AppStack("staging", "web", testRelease(t, "b1"))
	index := &fakeStackIndex{stacks: map[string][]naming.StackName{
		"shop": {
			naming.InfraStack("staging"),
			web,
			naming.InfraStack("prod"),
		},
	}}

	plan, err := PlanPreviewProjectTeardown(context.Background(), Config{Stacks: index}, "shop")
	if err != nil {
		t.Fatalf("PlanPreviewProjectTeardown: %v", err)
	}
	want := PreviewProjectTeardownPlan{
		InfraStacks: []naming.StackName{naming.InfraStack("staging")},
		AppStacks:   []naming.StackName{web},
		Pointers:    []string{"staging"},
	}
	if !reflect.DeepEqual(plan, want) {
		t.Fatalf("PlanPreviewProjectTeardown = %+v, want %+v — production stacks are not preview ones", plan, want)
	}
}

func TestListPreviewStacksFromIndex(t *testing.T) {
	t.Parallel()

	index := &fakeStackIndex{stacks: map[string][]naming.StackName{
		"shop": {
			naming.InfraStack("staging"),
			naming.AppStack("staging", "web", testRelease(t, "b1")),
			naming.AppStack("pr-1", "web", testRelease(t, "b2")),
		},
	}}

	got, err := ListPreviewStacks(context.Background(), index, "shop")
	if err != nil {
		t.Fatalf("ListPreviewStacks: %v", err)
	}
	if len(got) != 2 || got[0].Identity != "pr-1" || got[1].Identity != "staging" {
		t.Fatalf("ListPreviewStacks = %+v, want one entry per pointer", got)
	}
}

func TestForgetProjectIfEmpty(t *testing.T) {
	t.Parallel()

	t.Run("the last stack gone drops the project", func(t *testing.T) {
		t.Parallel()

		index := &fakeStackIndex{projects: []string{"shop"}}
		if err := forgetProjectIfEmpty(context.Background(), index, "shop"); err != nil {
			t.Fatalf("forgetProjectIfEmpty: %v", err)
		}
		if !reflect.DeepEqual(index.projectsGone, []string{"shop"}) {
			t.Fatalf("projects dropped = %v, want [shop]", index.projectsGone)
		}
	})

	t.Run("a surviving stack keeps the project", func(t *testing.T) {
		t.Parallel()

		index := &fakeStackIndex{
			projects: []string{"shop"},
			stacks:   map[string][]naming.StackName{"shop": {naming.InfraStack("prod")}},
		}
		if err := forgetProjectIfEmpty(context.Background(), index, "shop"); err != nil {
			t.Fatalf("forgetProjectIfEmpty: %v", err)
		}
		if index.projectsGone != nil {
			t.Fatalf("dropped %v while a stack is still standing", index.projectsGone)
		}
	})
}
