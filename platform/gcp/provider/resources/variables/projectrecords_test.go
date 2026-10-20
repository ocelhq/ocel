package variables

import (
	"context"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/credential"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/googletest"
)

func refusingDatabases(t *testing.T) *gcloud.Clients {
	t.Helper()
	tags := googletest.NewTieredTags(t)
	databases := googletest.NewDatabases(t, tags)
	databases.CreateDenied = true
	return tags.Tagged(googletest.Clients(databases.URL))
}

func TestAProjectDatabaseTheDeployCredentialMayNotCreateIsRefusedNamingTheTieredDatabasesRole(t *testing.T) {
	t.Parallel()
	c := refusingDatabases(t)

	err := makeProjectDatabase(context.Background(), c, environment.TierPreview, c.ProjectDatabase(environment.TierPreview, "shop"))

	want := "the deploy credential lacks " + c.DeployCredential(environment.TierPreview).Tiered(credential.DatabasesRole).String()
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("makeProjectDatabase() = %v, want it to say %q: Firestore checks creating a database against the untagged project, which the tiered databases role reaches", err, want)
	}
}
