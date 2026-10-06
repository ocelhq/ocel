package gcp

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
)

const invalidatedURLMap = "ocel-alb-production-routes"

func TestInvalidatingTagsPostsThemToTheURLMapsInvalidateCache(t *testing.T) {
	t.Parallel()

	p, server := invalidating(t, 0)
	if err := p.InvalidateTags(context.Background(), invalidatedURLMap, []string{"r00000001", "r00000002"}); err != nil {
		t.Fatalf("InvalidateTags = %v", err)
	}

	wantPath := "/projects/acme-prod/global/urlMaps/" + invalidatedURLMap + "/invalidateCache"
	if len(server.paths) != 1 || server.paths[0] != wantPath {
		t.Fatalf("the invalidation went to %v, want one POST to %s", server.paths, wantPath)
	}
	rule := server.rules[0]
	if !slices.Equal(rule.CacheTags, []string{"r00000001", "r00000002"}) || rule.Host != "" || rule.Path != "" {
		t.Errorf("the rule is %+v, want only the tags: the url map is shared by the tier, and a host or a path would change what the tags clear", rule)
	}
}

func TestElevenTagsAreSentAsTwoInvalidations(t *testing.T) {
	t.Parallel()

	var tags []string
	for i := range 11 {
		tags = append(tags, fmt.Sprintf("r%08d", i))
	}
	p, server := invalidating(t, 0)
	if err := p.InvalidateTags(context.Background(), invalidatedURLMap, tags); err != nil {
		t.Fatalf("InvalidateTags = %v", err)
	}

	if len(server.rules) != 2 || len(server.rules[0].CacheTags) != 10 || len(server.rules[1].CacheTags) != 1 {
		t.Errorf("11 tags went out as %+v, want one request of 10 then one of 1: Cloud CDN takes at most 10 tags a request", server.rules)
	}
}

func TestNoTagsSendsNothing(t *testing.T) {
	t.Parallel()

	p, server := invalidating(t, 0)
	if err := p.InvalidateTags(context.Background(), invalidatedURLMap, nil); err != nil {
		t.Fatalf("InvalidateTags = %v", err)
	}
	if len(server.paths) != 0 {
		t.Errorf("no tags sent %v, want no request", server.paths)
	}
}

func TestATagInvalidationTheAPIRefusesNamesTheTagsAndTheURLMap(t *testing.T) {
	t.Parallel()

	p, _ := invalidating(t, http.StatusForbidden)
	err := p.InvalidateTags(context.Background(), invalidatedURLMap, []string{"r00000001"})
	if err == nil {
		t.Fatal("InvalidateTags = nil, want the refusal")
	}
	for _, want := range []string{"r00000001", invalidatedURLMap} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error reads %q, want it to name %q", err, want)
		}
	}
}
