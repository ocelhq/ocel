package transform_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/provider/transform"
	"github.com/ocelhq/ocel/pkg/refusal"
)

func TestAPatchSettingAFieldOcelOwnsIsRefusedNamingIt(t *testing.T) {
	t.Parallel()

	owned := []string{"name", "cdnPolicy.cacheMode"}
	placeholder := map[string]any{"$ocelOutput": map[string]any{"type": "custom", "name": "edge", "property": "policy"}}
	for _, patch := range []map[string]any{
		{"cdnPolicy": map[string]any{"cacheMode": "CACHE_ALL_STATIC"}},
		{"cdnPolicy": nil},
		{"cdnPolicy": placeholder},
		{"cdnPolicy": []any{}},
		{"cdnPolicy": "USE_ORIGIN_HEADERS"},
	} {
		err := transform.RefuseOwned("edge web's backendService", patch, owned)
		var refused refusal.Refusal
		if !errors.As(err, &refused) || !strings.Contains(refused.Message, "edge web's backendService.cdnPolicy.cacheMode") {
			t.Errorf("RefuseOwned(%v) = %v, want the owned field refused by name: a value that is not an object replaces every field under it", patch, err)
		}
	}
}

func TestAPatchLeavingEveryFieldOcelOwnsAloneIsNotRefused(t *testing.T) {
	t.Parallel()

	owned := []string{"name", "cdnPolicy.cacheMode"}
	for _, patch := range []map[string]any{
		{"cdnPolicy": map[string]any{"negativeCaching": true}},
		{"description": "web"},
		{},
	} {
		if err := transform.RefuseOwned("edge web's backendService", patch, owned); err != nil {
			t.Errorf("RefuseOwned(%v) = %v, want nil: the patch sets no field ocel owns", patch, err)
		}
	}
}
