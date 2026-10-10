package gcp

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
)

func assetBindingsOf(server *iamServer) []string {
	var bound []string
	for _, binding := range projectBindingsOf(server) {
		if strings.HasPrefix(binding, appAssetsRole+" ") {
			bound = append(bound, binding)
		}
	}
	return bound
}

func TestAnAppsAccountMayReadItsOwnStaticFilesAndNoOtherApps(t *testing.T) {
	server := &runServer{}
	p := server.open(t)
	if _, err := p.ProvisionFunctions(context.Background(), staticSpec(), nil); err != nil {
		t.Fatalf("ProvisionFunctions() = %v", err)
	}

	granted := assetBindingsOf(server.identities())
	want := appAssetsRole + " serviceAccount:" + names(t, p).AppAccountEmail(environment.TierProduction, "shop", "web") +
		` resource.name.startsWith("projects/_/buckets/` + names(t, p).Bucket(environment.TierProduction) + `/objects/assets/prod/shop/web/")`
	if !slices.Equal(granted, []string{want}) {
		t.Errorf("the app's asset bindings = %q, want exactly %q", granted, want)
	}
}

func TestAnAppsAssetsGrantIsTitledForTheApp(t *testing.T) {
	t.Parallel()
	server := appAccountsOnly()
	c := server.open(t)
	account := c.AppAccountEmail(environment.TierProduction, "shop", "web")

	if err := grantAssets(context.Background(), c, staticSpec(), account); err != nil {
		t.Fatalf("grantAssets() = %v", err)
	}

	for _, binding := range server.project.Bindings {
		if binding.Role == appAssetsRole && (binding.Condition == nil || binding.Condition.Title != "ocel assets of prod/shop/web") {
			t.Errorf("the assets binding's condition = %+v, want the title %q", binding.Condition, "ocel assets of prod/shop/web")
		}
	}
}

func TestARedeployOfAnAppWritesNoAssetsPolicy(t *testing.T) {
	t.Parallel()
	server := appAccountsOnly()
	c := server.open(t)
	account := c.AppAccountEmail(environment.TierProduction, "shop", "web")
	if err := grantAssets(context.Background(), c, staticSpec(), account); err != nil {
		t.Fatal(err)
	}
	before := server.projectWrites

	if err := grantAssets(context.Background(), c, staticSpec(), account); err != nil {
		t.Fatalf("second grantAssets() = %v", err)
	}
	if got := server.projectWrites - before; got != 0 {
		t.Errorf("a redeploy wrote the project policy %d times, want 0", got)
	}
}

func TestAnAppThatRoutesNothingIsGrantedNoAssets(t *testing.T) {
	t.Parallel()
	server := appAccountsOnly()
	c := server.open(t)

	if err := grantAssets(context.Background(), c, nextSpec(), c.AppAccountEmail(environment.TierProduction, "shop", "web")); err != nil {
		t.Fatalf("grantAssets() = %v", err)
	}
	if got := assetBindingsOf(server); len(got) != 0 {
		t.Errorf("an app that routes nothing holds the asset bindings %q, want none", got)
	}
}
