package providerserver

import (
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/router"
)

const AppConcurrency = appConcurrency

const StackDestroyConcurrency = stackDestroyConcurrency

var RevisionsOf = revisionsOf

var EnvName = envName

var ServeOn = serve

func OpenSecrets(t *testing.T, p provider.Provider, tier environment.Tier, slug string, record router.ReleaseRecord) router.ReleaseSecrets {
	t.Helper()
	secrets, err := openProjectLedger(p, tier, slug).openSecrets(t.Context(), record)
	if err != nil {
		t.Fatalf("open the secrets the ledger keeps for %s/%s: %v", record.App, record.Release, err)
	}
	return secrets
}
