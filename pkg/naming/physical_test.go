package naming_test

import (
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/refusal"
)

func mustRelease(t *testing.T, value string) naming.ReleaseToken {
	t.Helper()
	release, err := naming.ParseReleaseToken(value)
	if err != nil {
		t.Fatal(err)
	}
	return release
}

func TestAppResourceNameFitsTheNamesEveryVendorGivesAnAppResource(t *testing.T) {
	release := mustRelease(t, "r1a2b3c4d")
	app := func(env, kind, name string) naming.Coordinate {
		return naming.Coordinate{Project: "acme", Env: env, App: "web", Kind: naming.Kind(kind), Name: name, Release: release}
	}
	envLevel := func(env, kind, name string) naming.Coordinate {
		c := app(env, kind, name)
		c.App = naming.InfraApp
		return c
	}
	for _, tc := range []struct {
		resource   string
		coordinate naming.Coordinate
		tag        string
		max        int
		want       string
	}{
		{"Lambda function", app("prod", "fn", "api"), release.String(), 64, "ocel-app-acme-prod-web-api-r1a2b3c4d"},
		{"Lambda function in a preview env", app("pr-42", "fn", "api"), release.String(), 64, "ocel-app-acme-pr-42-web-api-r1a2b3c4d"},
		{"S3 bucket", envLevel("prod", "bucket", "uploads"), "k3x9q2", 63, "ocel-app-acme-prod-uploads-k3x9q2"},
		{"RDS cluster", envLevel("prod", "db", "main"), "a65fde", 63, "ocel-app-acme-prod-main-a65fde"},
		{"RDS cluster in a preview env", envLevel("pr-42", "db", "main"), "abfa7e", 63, "ocel-app-acme-pr-42-main-abfa7e"},
		{"ElastiCache cluster", envLevel("prod", "kv", "cache"), "d5ed52", 40, "ocel-app-acme-prod-cache-d5ed52"},
		{"SQS queue", envLevel("prod", "topic", "orders-ship"), "d013ca", 80, "ocel-app-acme-prod-orders-ship-d013ca"},
		{"Cloud Run service", app("prod", "fn", "api"), "e0a849", 37, "ocel-app-acme-prod-web-api-e0a849"},
	} {
		got, err := tc.coordinate.AppResourceName("ocel", tc.tag).Fit(tc.max)
		if err != nil {
			t.Errorf("%s: %v", tc.resource, err)
			continue
		}
		if got != tc.want {
			t.Errorf("%s = %q, want %q", tc.resource, got, tc.want)
		}
	}
}

func TestPhysicalNameOmitsAnEmptyApp(t *testing.T) {
	got, err := naming.PhysicalName{Fence: naming.AppFence("ocel"), Project: "acme", Env: "prod", Declared: "main", Tag: "a65fde"}.Fit(63)
	if err != nil {
		t.Fatal(err)
	}
	if want := "ocel-app-acme-prod-main-a65fde"; got != want {
		t.Errorf("Fit = %q, want %q", got, want)
	}
}

func TestPhysicalNameOmitsTheInfraApp(t *testing.T) {
	got, err := naming.PhysicalName{Fence: naming.AppFence("ocel"), Project: "acme", Env: "prod", App: naming.InfraApp, Declared: "main", Tag: "a65fde"}.Fit(63)
	if err != nil {
		t.Fatal(err)
	}
	if want := "ocel-app-acme-prod-main-a65fde"; got != want {
		t.Errorf("Fit = %q, want %q", got, want)
	}
}

func TestPhysicalNameLowercasesEveryFieldIntoKebabCase(t *testing.T) {
	got, err := naming.PhysicalName{Fence: naming.AppFence("ocel"), Project: "Acme", Env: "prod", App: "Web_Shop", Declared: "orders/ship", Tag: "d013ca"}.Fit(80)
	if err != nil {
		t.Fatal(err)
	}
	if want := "ocel-app-acme-prod-web-shop-orders-ship-d013ca"; got != want {
		t.Errorf("Fit = %q, want %q", got, want)
	}
}

func TestPhysicalNameWithoutAFenceStartsAtTheProjectAndEndsWithItsKind(t *testing.T) {
	for _, tc := range []struct {
		name naming.PhysicalName
		want string
	}{
		{naming.PhysicalName{Project: "acme", Env: "prod", Declared: "main", Kind: "pg", Tag: "a1b2c3"}, "acme-prod-main-pg-a1b2c3"},
		{naming.PhysicalName{Project: "acme", Env: "prod", App: "web", Declared: "main", Kind: "pg", Tag: "a1b2c3"}, "acme-prod-web-main-pg-a1b2c3"},
		{naming.PhysicalName{Project: "acme", Env: "prod", App: "web", Declared: "mailer", Tag: "r1a2b3c4d"}, "acme-prod-web-mailer-r1a2b3c4d"},
	} {
		got, err := tc.name.Fit(63)
		if err != nil {
			t.Errorf("%+v: %v", tc.name, err)
			continue
		}
		if got != tc.want {
			t.Errorf("%+v.Fit = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestPhysicalNameOfTheAppItselfEndsWithTheAppAndCutsItLikeAnyApp(t *testing.T) {
	short, err := naming.PhysicalName{Fence: naming.AppFence("ocel"), Project: "acme", Env: "prod", App: "web", Tag: "r1a2b3c4d"}.Fit(64)
	if err != nil {
		t.Fatal(err)
	}
	if want := "ocel-app-acme-prod-web-r1a2b3c4d"; short != want {
		t.Errorf("Fit = %q, want %q", short, want)
	}
	long, err := naming.PhysicalName{Fence: naming.AppFence("acme-staging"), Project: "acme", Env: "prod", App: "storefront", Tag: "e0a849"}.Fit(37)
	if err != nil {
		t.Fatalf("an app resource that fits once its app is cut was refused: %v", err)
	}
	if want := "acme-staging-app-acm-pro-store-e0a849"; long != want {
		t.Errorf("Fit = %q, want %q", long, want)
	}
}

func TestPhysicalNameQualifierFollowsTheFenceAndIsNeverCut(t *testing.T) {
	cluster := naming.PhysicalName{Fence: naming.AppFence("ocel"), Qualifier: "prod", Project: "acme", Env: "prod", Declared: "main", Tag: "a65fde"}
	got, err := cluster.Fit(63)
	if err != nil {
		t.Fatal(err)
	}
	if want := "ocel-app-prod-acme-prod-main-a65fde"; got != want {
		t.Errorf("Fit = %q, want %q", got, want)
	}
	long := naming.PhysicalName{Fence: naming.AppFence("ocel"), Qualifier: "preview", Project: "northwind-traders", Env: "feature-checkout-redesign", Declared: "orders", Tag: "3f9a1c"}
	got, err = long.Fit(63, "1", "subnets")
	if err != nil {
		t.Fatal(err)
	}
	if want := "ocel-app-preview-northwind-traders-featur-orders-3f9a1c"; got != want {
		t.Errorf("Fit = %q, want %q", got, want)
	}
}

func TestPhysicalNameRefusesAMissingFieldNamingTheFirstInOrder(t *testing.T) {
	full := naming.PhysicalName{Fence: naming.AppFence("ocel"), Project: "acme", Env: "prod", App: "web", Declared: "api", Tag: "e0a849"}
	for _, tc := range []struct {
		field string
		blank func(*naming.PhysicalName)
	}{
		{"project", func(p *naming.PhysicalName) { p.Project, p.Env, p.Tag = "", "", "" }},
		{"env", func(p *naming.PhysicalName) { p.Env, p.Tag = "", "" }},
		{"tag", func(p *naming.PhysicalName) { p.Tag = "" }},
		{"declared name or an app", func(p *naming.PhysicalName) { p.App, p.Declared = "", "" }},
	} {
		p := full
		tc.blank(&p)
		got, err := p.Fit(64)
		if err == nil {
			t.Errorf("Fit without a %s = %q, want a refusal", tc.field, got)
			continue
		}
		if !strings.Contains(err.Error(), tc.field) {
			t.Errorf("Fit refused with %q, want it to name the missing %s", err, tc.field)
		}
	}
}

func TestPhysicalNameCutsEnvThenProjectThenApp(t *testing.T) {
	long := naming.PhysicalName{Fence: naming.AppFence("ocel"), Project: "northwind-traders", Env: "feature-checkout-redesign", App: "storefront"}
	for _, tc := range []struct {
		resource string
		declared string
		tag      string
		max      int
		want     string
	}{
		{"Lambda function", "checkout", "r1a2b3c4d", 64, "ocel-app-northwind-traders-feature-storefront-checkout-r1a2b3c4d"},
		{"S3 bucket", "customer-uploads", "k3x9q2", 63, "ocel-app-northwind-trade-fea-storefront-customer-uploads-k3x9q2"},
		{"ElastiCache cluster", "cache", "5d1e0f", 40, "ocel-app-nor-fea-storefront-cache-5d1e0f"},
		{"Cloud Run service", "checkout", "7c2a9b", 37, "ocel-app-nor-fea-stor-checkout-7c2a9b"},
	} {
		p := long
		p.Declared, p.Tag = tc.declared, tc.tag
		got, err := p.Fit(tc.max)
		if err != nil {
			t.Errorf("%s: %v", tc.resource, err)
			continue
		}
		if got != tc.want {
			t.Errorf("%s = %q, want %q", tc.resource, got, tc.want)
		}
	}
}

func TestPhysicalNameWithoutAFenceCutsEnvThenProjectThenApp(t *testing.T) {
	long := naming.PhysicalName{Project: "northwind-traders", Env: "feature-checkout-redesign", App: "storefront", Kind: "pg", Tag: "a1b2c3"}
	for declared, want := range map[string]string{
		"customer-uploads":        "northwind-traders-feature-storefront-customer-uploads-pg-a1b2c3",
		"orders-ledger-snapshots": "northwind-trad-fea-storefront-orders-ledger-snapshots-pg-a1b2c3",
	} {
		name := long
		name.Declared = declared
		got, err := name.Fit(63)
		if err != nil {
			t.Errorf("%s: %v", declared, err)
			continue
		}
		if got != want {
			t.Errorf("%s: Fit = %q, want %q", declared, got, want)
		}
	}
}

func TestPhysicalNameKeepsTheDeclaredNameAndTagWithinEveryLimit(t *testing.T) {
	long := naming.PhysicalName{Fence: naming.AppFence("ocel"), Project: "northwind-traders", Env: "feature-checkout-redesign", App: "storefront"}
	for _, tc := range []struct {
		resource string
		declared string
		tag      string
		max      int
	}{
		{"Lambda function", "checkout", "r1a2b3c4d", 64},
		{"IAM role", "checkout", "r1a2b3c4d", 64},
		{"S3 bucket", "customer-uploads", "k3x9q2", 63},
		{"RDS cluster", "orders", "3f9a1c", 63},
		{"ElastiCache cluster", "cache", "5d1e0f", 40},
		{"Cloud Run service", "checkout", "7c2a9b", 37},
	} {
		p := long
		p.Declared, p.Tag = tc.declared, tc.tag
		got, err := p.Fit(tc.max)
		if err != nil {
			t.Errorf("%s: %v", tc.resource, err)
			continue
		}
		if len(got) > tc.max {
			t.Errorf("%s = %q, %d characters over a limit of %d", tc.resource, got, len(got), tc.max)
		}
		if !strings.HasPrefix(got, naming.AppFence("ocel")) {
			t.Errorf("%s = %q, want it inside the fence %q", tc.resource, got, naming.AppFence("ocel"))
		}
		if suffix := "-" + tc.declared + "-" + tc.tag; !strings.HasSuffix(got, suffix) {
			t.Errorf("%s = %q, want it to end with %q", tc.resource, got, suffix)
		}
	}
}

func TestPhysicalNameThatCannotFitIsRefusedAsInvalidNamingTheDeclaredNameAndTheLimit(t *testing.T) {
	long := naming.PhysicalName{Fence: naming.AppFence("ocel"), Project: "northwind-traders", Env: "feature-checkout-redesign", App: "storefront", Declared: "checkout", Tag: "7c2a9b"}
	for resource, max := range map[string]int{"target group": 32, "GCP service account": 30} {
		got, err := long.Fit(max)
		if err == nil {
			t.Errorf("%s: Fit(%d) = %q, want a refusal", resource, max, got)
			continue
		}
		var refused refusal.Refusal
		if !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid {
			t.Errorf("%s: Fit(%d) failed with %#v, want a refusal coded %q", resource, max, err, refusal.CodeInvalid)
		}
		for _, want := range []string{"ocel-app-northwind-traders-feature-checkout-redesign-storefront-checkout-7c2a9b", strconv.Itoa(max), "Shorten checkout"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%s: Fit(%d) refused with %q, want it to name %q", resource, max, err, want)
			}
		}
	}
}

func TestTwoEnvsCutToTheSameHeadStillGetDistinctNames(t *testing.T) {
	one := naming.Coordinate{Project: "northwind-traders", Env: "feature-checkout-redesign", App: naming.InfraApp, Kind: naming.KindKV, Name: "cache"}
	two := one
	two.Env = "feature-checkout-refresh"
	a, err := one.AppResourceName("ocel", one.HashTag("ocel")).Fit(40)
	if err != nil {
		t.Fatal(err)
	}
	b, err := two.AppResourceName("ocel", two.HashTag("ocel")).Fit(40)
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Errorf("envs %q and %q both name their cache %q", one.Env, two.Env, a)
	}
}

func TestPhysicalNameLeavesRoomForTheLongestChildSuffix(t *testing.T) {
	cluster := naming.PhysicalName{Fence: naming.AppFence("ocel"), Project: "northwind-traders", Env: "feature-checkout-redesign", App: "storefront", Declared: "orders", Tag: "3f9a1c"}
	suffixes := []string{"1", "subnets", "final-20261019120000"}
	parent, err := cluster.Fit(63, suffixes...)
	if err != nil {
		t.Fatal(err)
	}
	if want := "ocel-app-nort-fea-storefront-orders-3f9a1c"; parent != want {
		t.Errorf("Fit = %q, want %q", parent, want)
	}
	for _, suffix := range suffixes {
		if child := parent + "-" + suffix; len(child) > 63 {
			t.Errorf("child %q is %d characters, over the limit of 63", child, len(child))
		}
	}
}

func TestFitUntaggedLeavesRoomForARandomTagMintedLater(t *testing.T) {
	bucket := naming.PhysicalName{Fence: naming.AppFence("ocel"), Project: "northwind-traders", Env: "feature-checkout-redesign", App: "storefront", Declared: "customer-uploads"}
	head, err := bucket.FitUntagged(63)
	if err != nil {
		t.Fatal(err)
	}
	if want := "ocel-app-northwind-trade-fea-storefront-customer-uploads"; head != want {
		t.Errorf("FitUntagged = %q, want %q", head, want)
	}
	if name := head + "-" + naming.NewRandomTag(); len(name) > 63 {
		t.Errorf("%q is %d characters once tagged, over the limit of 63", name, len(name))
	}
	cluster := naming.PhysicalName{Fence: naming.AppFence("ocel"), Project: "northwind-traders", Env: "feature-checkout-redesign", Declared: "orders"}
	head, err = cluster.FitUntagged(63, "subnets")
	if err != nil {
		t.Fatal(err)
	}
	if child := head + "-" + naming.NewRandomTag() + "-subnets"; len(child) > 63 {
		t.Errorf("child %q is %d characters, over the limit of 63", child, len(child))
	}
}
