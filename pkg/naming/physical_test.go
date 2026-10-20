package naming_test

import (
	"strconv"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
)

func TestTierWordIsProdForProductionAndPreviewForPreview(t *testing.T) {
	for tier, want := range map[environment.Tier]string{
		environment.TierProduction: "prod",
		environment.TierPreview:    "preview",
	} {
		if got := naming.TierWord(tier); got != want {
			t.Errorf("TierWord(%q) = %q, want %q", tier, got, want)
		}
	}
}

func TestAppFenceIsTheNamespaceFieldThenApp(t *testing.T) {
	for namespace, want := range map[string]string{
		"ocel":                           "ocel-app-",
		"acme-staging-eu-west-blue-team": "acme-20ddf6-app-",
	} {
		if got := naming.AppFence(namespace); got != want {
			t.Errorf("AppFence(%q) = %q, want %q", namespace, got, want)
		}
	}
}

func TestOcelNameJoinsTheNamespaceFieldAndOcelsWords(t *testing.T) {
	for _, tc := range []struct {
		namespace string
		words     []string
		want      string
	}{
		{"ocel", []string{naming.TierWord(environment.TierProduction), "edge", "k3x9q2"}, "ocel-prod-edge-k3x9q2"},
		{"ocel", []string{naming.TierWord(environment.TierProduction), "env-sync"}, "ocel-prod-env-sync"},
		{"ocel", []string{naming.TierWord(environment.TierPreview), "containers"}, "ocel-preview-containers"},
		{"acme-staging-eu-west-blue-team", []string{"prod", "state", "a1b2c3"}, "acme-20ddf6-prod-state-a1b2c3"},
	} {
		got, err := naming.OcelName(tc.namespace, tc.words...)
		if err != nil {
			t.Errorf("OcelName(%q, %q): %v", tc.namespace, tc.words, err)
			continue
		}
		if got != tc.want {
			t.Errorf("OcelName(%q, %q) = %q, want %q", tc.namespace, tc.words, got, tc.want)
		}
	}
}

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
	got, err := naming.PhysicalName{Namespace: "ocel", Project: "acme", Env: "prod", Declared: "main", Tag: "a65fde"}.Fit(63)
	if err != nil {
		t.Fatal(err)
	}
	if want := "ocel-app-acme-prod-main-a65fde"; got != want {
		t.Errorf("Fit = %q, want %q", got, want)
	}
}

func TestPhysicalNameLowercasesEveryFieldIntoKebabCase(t *testing.T) {
	got, err := naming.PhysicalName{Namespace: "ocel", Project: "Acme", Env: "prod", App: "Web_Shop", Declared: "orders/ship", Tag: "d013ca"}.Fit(80)
	if err != nil {
		t.Fatal(err)
	}
	if want := "ocel-app-acme-prod-web-shop-orders-ship-d013ca"; got != want {
		t.Errorf("Fit = %q, want %q", got, want)
	}
}

func TestPhysicalNameRefusesAMissingField(t *testing.T) {
	full := naming.PhysicalName{Namespace: "ocel", Project: "acme", Env: "prod", App: "web", Declared: "api", Tag: "e0a849"}
	for field, blank := range map[string]func(*naming.PhysicalName){
		"namespace": func(p *naming.PhysicalName) { p.Namespace = "" },
		"project":   func(p *naming.PhysicalName) { p.Project = "" },
		"env":       func(p *naming.PhysicalName) { p.Env = "" },
		"declared":  func(p *naming.PhysicalName) { p.Declared = "" },
		"tag":       func(p *naming.PhysicalName) { p.Tag = "" },
	} {
		p := full
		blank(&p)
		if got, err := p.Fit(64); err == nil {
			t.Errorf("Fit without a %s = %q, want a refusal", field, got)
		}
	}
}

func TestPhysicalNameCutsEnvThenProjectThenApp(t *testing.T) {
	long := naming.PhysicalName{Namespace: "ocel", Project: "northwind-traders", Env: "feature-checkout-redesign", App: "storefront"}
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

func TestPhysicalNameKeepsTheDeclaredNameAndTagWithinEveryLimit(t *testing.T) {
	long := naming.PhysicalName{Namespace: "ocel", Project: "northwind-traders", Env: "feature-checkout-redesign", App: "storefront"}
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

func TestPhysicalNameRefusesANameThatCannotFitNamingItAndTheLimit(t *testing.T) {
	long := naming.PhysicalName{Namespace: "ocel", Project: "northwind-traders", Env: "feature-checkout-redesign", App: "storefront", Declared: "checkout", Tag: "7c2a9b"}
	for resource, max := range map[string]int{"target group": 32, "GCP service account": 30} {
		got, err := long.Fit(max)
		if err == nil {
			t.Errorf("%s: Fit(%d) = %q, want a refusal", resource, max, got)
			continue
		}
		for _, want := range []string{"ocel-app-northwind-traders-feature-checkout-redesign-storefront-checkout-7c2a9b", strconv.Itoa(max)} {
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

func TestFitWithChildrenLeavesRoomForTheLongestChildSuffix(t *testing.T) {
	cluster := naming.PhysicalName{Namespace: "ocel", Project: "northwind-traders", Env: "feature-checkout-redesign", App: "storefront", Declared: "orders", Tag: "3f9a1c"}
	suffixes := []string{"1", "subnets", "final-20261019120000"}
	parent, err := cluster.FitWithChildren(63, suffixes...)
	if err != nil {
		t.Fatal(err)
	}
	if want := "ocel-app-nort-fea-storefront-orders-3f9a1c"; parent != want {
		t.Errorf("FitWithChildren = %q, want %q", parent, want)
	}
	for _, suffix := range suffixes {
		if child := parent + "-" + suffix; len(child) > 63 {
			t.Errorf("child %q is %d characters, over the limit of 63", child, len(child))
		}
	}
}

func TestOcelNameRefusesANameInsideTheAppFence(t *testing.T) {
	for _, words := range [][]string{{"app", "env-sync"}, {"App", "invalidator"}, {"app-env-sync"}, {}} {
		if got, err := naming.OcelName("ocel", words...); err == nil {
			t.Errorf("OcelName(%q, %q) = %q, want a refusal: its name would sit inside the fence %q", "ocel", words, got, naming.AppFence("ocel"))
		}
	}
}
