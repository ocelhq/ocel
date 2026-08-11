package deploy

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestProjectSlugsBesides(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		slug   string
		scopes []string
		want   []string
	}{
		{
			name:   "empty index",
			slug:   "my-app",
			scopes: nil,
			want:   nil,
		},
		{
			name:   "slug is already indexed",
			slug:   "my-app",
			scopes: []string{"my-app", "other"},
			want:   nil,
		},
		{
			name:   "unrecognized slug beside other projects",
			slug:   "my-app",
			scopes: []string{"my-application", "billing"},
			want:   []string{"billing", "my-application"},
		},
		{
			name:   "a hyphenated slug is recovered whole",
			slug:   "typo",
			scopes: []string{"my-long-hyphenated-name"},
			want:   []string{"my-long-hyphenated-name"},
		},
		{
			name:   "a project whose scope prefixes another is not conflated",
			slug:   "my",
			scopes: []string{"my-app"},
			want:   []string{"my-app"},
		},
		{
			name:   "empty scopes are skipped",
			slug:   "my-app",
			scopes: []string{""},
			want:   nil,
		},
		{
			name:   "the slug's own scope is matched on its safeName form",
			slug:   "My--App",
			scopes: []string{"my-app"},
			want:   nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := projectSlugsBesides(tc.slug, tc.scopes)
			if len(got) == 0 && len(tc.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("projectSlugsBesides(%q, %v) = %v, want %v", tc.slug, tc.scopes, got, tc.want)
			}
		})
	}

	t.Run("a long slug matches its truncated scope", func(t *testing.T) {
		t.Parallel()

		slug := "a-very-long-project-slug-that-runs-past-the-safe-name-cap"
		scope := safeName(slug)
		if scope == slug {
			t.Fatalf("safeName(%q) = %q, want it truncated — the fixture no longer exercises the cap", slug, scope)
		}

		if got := projectSlugsBesides(slug, []string{scope}); got != nil {
			t.Errorf("projectSlugsBesides() = %v, want nil — the truncated scope is the slug's own", got)
		}
	})

	t.Run("asks the index for the account's projects", func(t *testing.T) {
		t.Parallel()

		index := &fakeStackIndex{projects: []string{"billing", "my-app"}}
		got, err := ProjectSlugsBesides(context.Background(), index, "shop")
		if err != nil {
			t.Fatalf("ProjectSlugsBesides: %v", err)
		}
		if want := []string{"billing", "my-app"}; !reflect.DeepEqual(got, want) {
			t.Errorf("ProjectSlugsBesides = %v, want %v", got, want)
		}
		if index.stacksQueried != nil {
			t.Errorf("queried %v: which projects exist is one query, not one per project", index.stacksQueried)
		}
	})

	t.Run("without an index the caller is told", func(t *testing.T) {
		t.Parallel()

		if _, err := ProjectSlugsBesides(context.Background(), nil, "shop"); !errors.Is(err, errNoStackIndex) {
			t.Fatalf("ProjectSlugsBesides err = %v, want %v", err, errNoStackIndex)
		}
	})
}
