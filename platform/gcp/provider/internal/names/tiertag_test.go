package names_test

import (
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
)

func TestEveryServiceAccountAnyNamespaceMakesIsKnownAsOcels(t *testing.T) {
	for _, namespace := range []provider.Namespace{"ocel", "shop", "northwind-traders", "a1-b2"} {
		n := names.New(namespace, "acme-prod")
		made := []string{n.AppAccount("shop", "prod", "web")}
		for _, tier := range []environment.Tier{environment.TierProduction, environment.TierPreview} {
			made = append(made, n.RealtimeAccount(tier), n.AssetReaderAccount(tier), n.EnvSourceSyncAccount(tier), n.BastionAccount(tier), n.PushAccount(tier))
		}
		for _, id := range made {
			if !names.IsOcelAccount(id) {
				t.Errorf("IsOcelAccount(%q) = false for an account namespace %s makes, so another namespace's bootstrap would bind it to outside", id, namespace)
			}
		}
	}
}

func TestAServiceAccountNoNamespaceMakesIsNotKnownAsOcels(t *testing.T) {
	for _, id := range []string{"deployer", "billing-prod-runner", "ocel-connector", "ocel-prod-probe", "my-app-runner", "ocel-app-0123", "514957526729-compute"} {
		if names.IsOcelAccount(id) {
			t.Errorf("IsOcelAccount(%q) = true, and no namespace makes it, so no bootstrap binds it to outside", id)
		}
	}
}
