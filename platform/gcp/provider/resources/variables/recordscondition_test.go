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
	ns := `resource.name == "projects/acme-prod/databases/ocel"`
	production := `resource.name.startsWith("projects/acme-prod/databases/` + c.ProjectDatabasePrefix(environment.TierProduction) + `")`
	preview := `resource.name.startsWith("projects/acme-prod/databases/` + c.ProjectDatabasePrefix(environment.TierPreview) + `")`

	tier := RecordsCondition(c, environment.TierProduction)
	if tier.Title != "ocel ocel production records" || tier.Expression != ns+" || "+production {
		t.Errorf("the production records condition is %+v, want the namespace database and production's project databases", tier)
	}
	both := RecordsCondition(c, environment.TierProduction, environment.TierPreview)
	if both.Title != "ocel ocel records" || both.Expression != ns+" || "+production+" || "+preview {
		t.Errorf("the records condition of both tiers is %+v, want the namespace database and both tiers' project databases", both)
	}
}
