package names_test

import (
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
)

func TestATiersLogViewIsNamedForItsNamespaceAndTierOnTheDefaultBucket(t *testing.T) {
	named := names.New("ocel", "acme-prod")
	if got := named.LogViewPath(environment.TierPreview); got != "projects/acme-prod/locations/global/buckets/_Default/views/ocel-preview-logs" {
		t.Errorf("LogViewPath(preview) = %q, want the ocel-preview-logs view on the project's _Default bucket", got)
	}
	if named.LogView(environment.TierProduction) == named.LogView(environment.TierPreview) {
		t.Errorf("both tiers read their logs through %s, and a view holds one tier's entries", named.LogView(environment.TierPreview))
	}
}
