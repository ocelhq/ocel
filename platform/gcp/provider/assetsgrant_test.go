package gcp

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/googletest"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/grants"
)

func assetBindingsOf(server *googletest.IAM) []string {
	var bound []string
	for _, binding := range projectBindingsOf(server) {
		if strings.HasPrefix(binding, grants.AssetsRole+" ") {
			bound = append(bound, binding)
		}
	}
	return bound
}

func TestAnAppsAccountMayReadItsOwnStaticFilesAndNoOtherApps(t *testing.T) {
	server := &googletest.Run{}
	p := providerServing(server, t)
	if _, err := p.ProvisionFunctions(context.Background(), staticSpec(), nil); err != nil {
		t.Fatalf("ProvisionFunctions() = %v", err)
	}

	granted := assetBindingsOf(server.Identities())
	want := grants.AssetsRole + " serviceAccount:" + namesOf(t, p).AppAccountEmail(environment.TierProduction, "shop", stackrecords.ProductionEnv, "web") +
		` resource.name.startsWith("projects/_/buckets/` + namesOf(t, p).Bucket(environment.TierProduction) + `/objects/assets/prod/shop/web/")`
	if !slices.Equal(granted, []string{want}) {
		t.Errorf("the app's asset bindings = %q, want exactly %q", granted, want)
	}
}

func TestAnAppsAssetsGrantIsTitledForTheApp(t *testing.T) {
	t.Parallel()
	server := appAccountsOnly()
	c := server.Open(t)

	if err := applyAppGrants(t, c, staticSpec()); err != nil {
		t.Fatalf("applyAppGrants() = %v", err)
	}

	for _, binding := range server.Project.Bindings {
		if binding.Role == grants.AssetsRole && (binding.Condition == nil || binding.Condition.Title != "ocel assets of prod/shop/web") {
			t.Errorf("the assets binding's condition = %+v, want the title %q", binding.Condition, "ocel assets of prod/shop/web")
		}
	}
}

func TestARedeployOfAnAppWritesNoAssetsPolicy(t *testing.T) {
	t.Parallel()
	server := appAccountsOnly()
	c := server.Open(t)
	if err := applyAppGrants(t, c, staticSpec()); err != nil {
		t.Fatal(err)
	}
	before := server.ProjectWrites

	if err := applyAppGrants(t, c, staticSpec()); err != nil {
		t.Fatalf("second applyAppGrants() = %v", err)
	}
	if got := server.ProjectWrites - before; got != 0 {
		t.Errorf("a redeploy wrote the project policy %d times, want 0", got)
	}
}

func TestAnAppThatRoutesNothingIsGrantedNoAssets(t *testing.T) {
	t.Parallel()
	server := appAccountsOnly()
	c := server.Open(t)

	if err := applyAppGrants(t, c, nextSpec()); err != nil {
		t.Fatalf("applyAppGrants() = %v", err)
	}
	if got := assetBindingsOf(server); len(got) != 0 {
		t.Errorf("an app that routes nothing holds the asset bindings %q, want none", got)
	}
}
