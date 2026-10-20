package variables

import (
	"context"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/credential"
)

func TestARunsDatabaseTheDeployCredentialMayNotCreateIsRefusedNamingTheTieredDatabasesRole(t *testing.T) {
	t.Parallel()
	b := bootstrap.Engine{Clients: refusingDatabases(t)}

	err := makeWritableDatabase(context.Background(), b, environment.TierProduction, b.Clients.ProjectWritableDatabase(environment.TierProduction, "shop"))

	want := "the deploy credential lacks " + b.Clients.DeployCredential(environment.TierProduction).Tiered(credential.DatabasesRole).String()
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("makeWritableDatabase() = %v, want it to say %q: Firestore checks creating a database against the untagged project, which the tiered databases role reaches", err, want)
	}
}
