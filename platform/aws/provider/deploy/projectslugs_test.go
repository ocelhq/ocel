package deploy

import (
	"reflect"
	"testing"
)

func TestProjectSlugsBesides(t *testing.T) {
	cases := []struct {
		name       string
		slug       string
		stackNames []string
		want       []string
	}{
		{
			name:       "empty backend",
			slug:       "my-app",
			stackNames: nil,
			want:       nil,
		},
		{
			name:       "slug already owns an infra stack",
			slug:       "my-app",
			stackNames: []string{"my-app--infra", "other--infra"},
			want:       nil,
		},
		{
			name:       "slug owns only an app-deploy stack",
			slug:       "my-app",
			stackNames: []string{"my-app--web--build123"},
			want:       nil,
		},
		{
			name:       "slug owns only a preview stack",
			slug:       "my-app",
			stackNames: []string{"my-app--preview-pr-7--web--build123"},
			want:       nil,
		},
		{
			name:       "unrecognized slug beside other projects",
			slug:       "my-app",
			stackNames: []string{"my-application--infra", "my-application--web--build123", "billing--infra"},
			want:       []string{"billing", "my-application"},
		},
		{
			name:       "a hyphenated slug is recovered whole",
			slug:       "typo",
			stackNames: []string{"my-long-hyphenated-name--web--build123"},
			want:       []string{"my-long-hyphenated-name"},
		},
		{
			name:       "a project whose scope prefixes another is not conflated",
			slug:       "my",
			stackNames: []string{"my-app--infra"},
			want:       []string{"my-app"},
		},
		{
			name:       "names carrying no delimiter are skipped",
			slug:       "my-app",
			stackNames: []string{"stray", ""},
			want:       nil,
		},
		{
			name:       "the slug's own stacks are matched on their safeName form",
			slug:       "My--App",
			stackNames: []string{"my-app--infra"},
			want:       nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := projectSlugsBesides(tc.slug, tc.stackNames)
			if len(got) == 0 && len(tc.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("projectSlugsBesides(%q, %v) = %v, want %v", tc.slug, tc.stackNames, got, tc.want)
			}
		})
	}
}

func TestProjectSlugsBesides_LongSlugMatchesItsTruncatedScope(t *testing.T) {
	slug := "a-very-long-project-slug-that-runs-past-the-safe-name-cap"
	scope := safeName(slug)
	if scope == slug {
		t.Fatalf("safeName(%q) = %q, want it truncated — the fixture no longer exercises the cap", slug, scope)
	}

	if got := projectSlugsBesides(slug, []string{scope + "--infra"}); got != nil {
		t.Errorf("projectSlugsBesides() = %v, want nil — the truncated scope is the slug's own", got)
	}
}
