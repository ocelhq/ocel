package variables

import (
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
)

func TestTheRecordsOfOneTierAndOfBothTiersAreNamedByOneCondition(t *testing.T) {
	t.Parallel()
	c := &gcloud.Clients{Names: names.New(provider.Namespace("ocel"), "acme-prod"), Region: "europe-west1"}
	production := `resource.name == "projects/acme-prod/databases/ocel-prod-records" || resource.name == "projects/acme-prod/databases/ocel-app-prod-shared" || resource.name.startsWith("projects/acme-prod/databases/ocel-app-prod-p-")`
	preview := `resource.name == "projects/acme-prod/databases/ocel-preview-records" || resource.name == "projects/acme-prod/databases/ocel-app-preview-shared" || resource.name.startsWith("projects/acme-prod/databases/ocel-app-preview-p-")`

	tier := RecordsCondition(c, environment.TierProduction)
	if tier.Title != "ocel ocel production records" || tier.Expression != production {
		t.Errorf("the production records condition is %+v, want production's records database, its shared database and the databases its projects own", tier)
	}
	both := RecordsCondition(c, environment.TierProduction, environment.TierPreview)
	if both.Title != "ocel ocel records" || both.Expression != production+" || "+preview {
		t.Errorf("the records condition of both tiers is %+v, want each tier's records database, shared database and the databases its projects own", both)
	}
}
