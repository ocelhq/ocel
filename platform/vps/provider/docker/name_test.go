package docker_test

import (
	"strings"
	"testing"

	"github.com/ocelhq/ocel/platform/vps/provider/docker"
)

func TestANameOnTheBoxJoinsProjectEnvAppDeclaredNameKindAndTag(t *testing.T) {
	for _, tc := range []struct {
		name docker.Name
		want string
	}{
		{docker.Name{Project: "acme", Env: "prod", Declared: "main", Kind: "pg", Tag: "a1b2c3"}, "acme-prod-main-pg-a1b2c3"},
		{docker.Name{Project: "acme", Env: "prod", App: "web", Declared: "main", Kind: "pg", Tag: "a1b2c3"}, "acme-prod-web-main-pg-a1b2c3"},
		{docker.Name{Project: "acme", Env: "prod", App: "web", Tag: "r1a2b3c4d"}, "acme-prod-web-r1a2b3c4d"},
		{docker.Name{Project: "acme", Env: "prod", App: "web", Declared: "mailer", Tag: "r1a2b3c4d"}, "acme-prod-web-mailer-r1a2b3c4d"},
	} {
		got, err := tc.name.Fit()
		if err != nil {
			t.Errorf("%+v: %v", tc.name, err)
			continue
		}
		if got != tc.want {
			t.Errorf("%+v.Fit() = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestANameOnTheBoxCutsEnvThenProjectThenAppToFit(t *testing.T) {
	long := docker.Name{Project: "northwind-traders", Env: "feature-checkout-redesign", App: "storefront", Kind: "pg", Tag: "a1b2c3"}
	for declared, want := range map[string]string{
		"customer-uploads":        "northwind-traders-feature-storefront-customer-uploads-pg-a1b2c3",
		"orders-ledger-snapshots": "northwind-trad-fea-storefront-orders-ledger-snapshots-pg-a1b2c3",
	} {
		name := long
		name.Declared = declared
		got, err := name.Fit()
		if err != nil {
			t.Errorf("%s: %v", declared, err)
			continue
		}
		if got != want {
			t.Errorf("%s: Fit() = %q, want %q", declared, got, want)
		}
		if len(got) > docker.NameMax {
			t.Errorf("%s: Fit() = %q, %d characters over the %d docker resolves", declared, got, len(got), docker.NameMax)
		}
	}
}

func TestANameOnTheBoxThatCannotFitIsRefusedNamingTheDeclaredName(t *testing.T) {
	declared := strings.Repeat("ledger-", 7) + "x"
	got, err := docker.Name{Project: "acme", Env: "prod", Declared: declared, Kind: "pg", Tag: "a1b2c3"}.Fit()
	if err == nil {
		t.Fatalf("Fit() = %q, want a refusal", got)
	}
	if !strings.Contains(err.Error(), declared) {
		t.Errorf("the refusal reads %q and never names %s", err, declared)
	}
}

func TestANameOnTheBoxWithoutATagIsRefused(t *testing.T) {
	if got, err := (docker.Name{Project: "acme", Env: "prod", Declared: "main", Kind: "pg"}).Fit(); err == nil {
		t.Errorf("Fit() = %q, want a refusal", got)
	}
}
