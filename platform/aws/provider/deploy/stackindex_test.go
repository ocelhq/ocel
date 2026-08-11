package deploy

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"
)

type fakeStackIndex struct {
	projects      []string
	stacks        map[string][]string
	stacksQueried []string
	added         []string
	removed       []string
	projectsGone  []string
	err           error
}

func (f *fakeStackIndex) AddProject(_ context.Context, scope string) error {
	if f.err != nil {
		return f.err
	}
	if !slices.Contains(f.projects, scope) {
		f.projects = append(f.projects, scope)
	}
	return nil
}

func (f *fakeStackIndex) AddStack(_ context.Context, stackName string) error {
	if f.err != nil {
		return f.err
	}
	f.added = append(f.added, stackName)
	return nil
}

func (f *fakeStackIndex) RemoveStack(_ context.Context, stackName string) error {
	if f.err != nil {
		return f.err
	}
	f.removed = append(f.removed, stackName)
	for scope, names := range f.stacks {
		f.stacks[scope] = slices.DeleteFunc(names, func(n string) bool { return n == stackName })
	}
	return nil
}

func (f *fakeStackIndex) RemoveProject(_ context.Context, scope string) error {
	if f.err != nil {
		return f.err
	}
	f.projectsGone = append(f.projectsGone, scope)
	f.projects = slices.DeleteFunc(f.projects, func(s string) bool { return s == scope })
	return nil
}

func (f *fakeStackIndex) Stacks(_ context.Context, scope string) ([]string, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.stacksQueried = append(f.stacksQueried, scope)
	return f.stacks[scope], nil
}

func (f *fakeStackIndex) Projects(_ context.Context) ([]string, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.projects, nil
}

func TestPlanProjectTeardown(t *testing.T) {
	t.Parallel()

	t.Run("covers only this project's stacks", func(t *testing.T) {
		t.Parallel()

		index := &fakeStackIndex{stacks: map[string][]string{
			"shop": {InfraStackName("shop"), AppDeployStackName("shop", "web", buildOnly("b1"))},
			"othr": {InfraStackName("othr")},
		}}

		plan, err := PlanProjectTeardown(context.Background(), Config{Stacks: index}, "shop")
		if err != nil {
			t.Fatalf("PlanProjectTeardown: %v", err)
		}
		want := ProjectTeardownPlan{
			InfraStack: InfraStackName("shop"),
			AppStacks:  []string{AppDeployStackName("shop", "web", buildOnly("b1"))},
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

	index := &fakeStackIndex{stacks: map[string][]string{
		"shop": {
			PreviewInfraStackName("shop", "staging"),
			PreviewAppDeployStackName("shop", "staging", "web", buildOnly("b1")),
			InfraStackName("shop"),
		},
	}}

	plan, err := planPreviewProjectTeardown(context.Background(), Config{Stacks: index}, "shop")
	if err != nil {
		t.Fatalf("planPreviewProjectTeardown: %v", err)
	}
	want := PreviewProjectTeardownPlan{
		InfraStacks: []string{PreviewInfraStackName("shop", "staging")},
		AppStacks:   []string{PreviewAppDeployStackName("shop", "staging", "web", buildOnly("b1"))},
		Pointers:    []string{"staging"},
	}
	if !reflect.DeepEqual(plan, want) {
		t.Fatalf("planPreviewProjectTeardown = %+v, want %+v — production stacks are not preview ones", plan, want)
	}
}

func TestListPreviewStacksFromIndex(t *testing.T) {
	t.Parallel()

	index := &fakeStackIndex{stacks: map[string][]string{
		"shop": {
			PreviewInfraStackName("shop", "staging"),
			PreviewAppDeployStackName("shop", "staging", "web", buildOnly("b1")),
			PreviewAppDeployStackName("shop", "pr-1", "web", buildOnly("b2")),
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
			stacks:   map[string][]string{"shop": {InfraStackName("shop")}},
		}
		if err := forgetProjectIfEmpty(context.Background(), index, "shop"); err != nil {
			t.Fatalf("forgetProjectIfEmpty: %v", err)
		}
		if index.projectsGone != nil {
			t.Fatalf("dropped %v while a stack is still standing", index.projectsGone)
		}
	})
}
