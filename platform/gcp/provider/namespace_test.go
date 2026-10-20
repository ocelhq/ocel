package gcp_test

import (
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	gcp "github.com/ocelhq/ocel/platform/gcp/provider"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
)

func TestEveryNameThisProviderDerivesContainsTheNamespace(t *testing.T) {
	for _, tc := range []struct {
		name      string
		namespace string
		stem      string
	}{
		{name: "the namespace a run names", namespace: "j-shop7", stem: "j-shop7"},
		{name: "the default where a run names none", namespace: "", stem: "ocel"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(provider.NamespaceEnvVar, tc.namespace)

			names := namesOf(t, newProvider(t, gcp.Options{Project: "acme-prod", Region: "europe-west1"}))
			for what, got := range map[string]string{
				"edge bucket":       names.TierBucket(environment.TierProduction, naming.StorageEdge, "k3x9q2"),
				"state bucket":      names.TierBucket(environment.TierPreview, naming.StorageState, "k3x9q2"),
				"record database":   names.RecordsDatabase(environment.TierProduction),
				"key ring":          names.KeyRing(),
				"passphrase secret": names.PassphraseSecret(environment.TierProduction),
				"image repository":  names.Repository(environment.TierProduction),
				"runs database":     names.ProjectWritableDatabase(environment.TierProduction, "shop"),
				"delay queue":       names.DelayQueue(environment.TierProduction),
				"push account":      names.PushAccount(environment.TierProduction),
			} {
				if !strings.HasPrefix(got, tc.stem) {
					t.Errorf("the %s is %q, want it derived from namespace %q", what, got, tc.stem)
				}
			}
			if got, want := names.TierBucket(environment.TierProduction, naming.StorageEdge, "k3x9q2"), tc.stem+"-prod-edge-k3x9q2"; got != want {
				t.Errorf("TierBucket(production, edge) = %q, want %q", got, want)
			}
			if got, want := names.TierBucket(environment.TierPreview, naming.StorageState, "k3x9q2"), tc.stem+"-preview-state-k3x9q2"; got != want {
				t.Errorf("TierBucket(preview, state) = %q, want %q", got, want)
			}
			if got, want := names.PassphraseSecret(environment.TierProduction), tc.stem+"-prod-pulumi-passphrase"; got != want {
				t.Errorf("PassphraseSecret() = %q, want %q", got, want)
			}
			for tier, want := range map[environment.Tier]string{
				environment.TierProduction: tc.stem + "-prod-cloudflare-edge-credentials",
				environment.TierPreview:    tc.stem + "-preview-cloudflare-edge-credentials",
			} {
				if got := names.EdgeCredentialsSecret(tier, edge.Kind("cloudflare")); got != want {
					t.Errorf("EdgeCredentialsSecret(%s) = %q, want %q", tier, got, want)
				}
			}
			for tier, want := range map[environment.Tier]string{
				environment.TierProduction: tc.stem + "-prod-cloudflare-purge-seed",
				environment.TierPreview:    tc.stem + "-preview-cloudflare-purge-seed",
			} {
				if got := names.PurgeSeedSecret(tier, edge.Kind("cloudflare")); got != want {
					t.Errorf("PurgeSeedSecret(%s) = %q, want %q", tier, got, want)
				}
			}
			if got, want := names.Repository(environment.TierPreview), tc.stem+"-preview-containers"; got != want {
				t.Errorf("Repository() = %q, want %q", got, want)
			}
			if got, want := names.RepositoryPath("europe-west1", environment.TierPreview), "europe-west1-docker.pkg.dev/acme-prod/"+tc.stem+"-preview-containers"; got != want {
				t.Errorf("RepositoryPath() = %q, want %q: the deploy pushes images to that host", got, want)
			}
			if got, want := names.ProjectWritableDatabasePrefix(environment.TierPreview), tc.stem+"-app-preview-w-"; got != want || !strings.HasPrefix(names.ProjectWritableDatabase(environment.TierPreview, "shop"), want) {
				t.Errorf("ProjectWritableDatabasePrefix() = %q, want %q naming every preview project's database of runs and tag records", got, want)
			}
			if got, want := names.DelayQueue(environment.TierProduction), tc.stem+"-prod-delays"; got != want {
				t.Errorf("DelayQueue() = %q, want %q", got, want)
			}
			if got := names.PushAccountEmail(environment.TierProduction); !strings.HasPrefix(got, names.PushAccount(environment.TierProduction)+"@acme-prod.iam.gserviceaccount.com") ||
				names.PushAccount(environment.TierProduction) == names.PushAccount(environment.TierPreview) || len(names.PushAccount(environment.TierProduction)) > 30 {
				t.Errorf("PushAccount() = %q (%q), want an account of the tier's own, in 30 characters", names.PushAccount(environment.TierProduction), got)
			}
			if got, want := names.RecordsDatabase(environment.TierPreview), tc.stem+"-preview-records"; got != want {
				t.Errorf("RecordsDatabase(preview) = %q, want %q", got, want)
			}
			if names.KeyRing() != tc.stem {
				t.Errorf("KeyRing() = %q, want %q", names.KeyRing(), tc.stem)
			}
		})
	}
}

func TestTheCachePurgeRoleIDReplacesEveryDashOfTheNamespaceWithAnUnderscore(t *testing.T) {
	t.Setenv(provider.NamespaceEnvVar, "ocel-dev")

	names := namesOf(t, newProvider(t, gcp.Options{Project: "acme-prod", Region: "europe-west1"}))

	if got, want := names.CDNPurgeRole(), "ocel_dev_cdn_purge"; got != want {
		t.Errorf("CDNPurgeRole() = %q, want %q: a custom role id allows no dash", got, want)
	}
}

func TestANamespaceNoNameCanBeDerivedFromIsRefusedAtConstruction(t *testing.T) {
	for _, tc := range []struct {
		name      string
		namespace string
		project   string
		names     string
	}{
		{
			name:      "a namespace no cloud takes",
			namespace: "Not A Namespace",
			project:   "acme-prod",
			names:     provider.NamespaceEnvVar,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(provider.NamespaceEnvVar, tc.namespace)

			var refused refusal.Refusal
			p, err := gcp.NewProvider(gcp.Options{Project: tc.project, Region: "europe-west1"})
			if !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid {
				t.Fatalf("NewProvider() under namespace %q = %v, %v, want an %s refusal", tc.namespace, p, err, refusal.CodeInvalid)
			}
			if !strings.Contains(refused.Message, tc.names) {
				t.Errorf("NewProvider() refused with %q, want it to name %q", refused.Message, tc.names)
			}
		})
	}
}

func TestANamespaceLongerThanAServiceAccountIsTakenByItsCompactedField(t *testing.T) {
	t.Setenv(provider.NamespaceEnvVar, strings.Repeat("a", 30))

	if _, err := gcp.NewProvider(gcp.Options{Project: "acme-prod", Region: "europe-west1"}); err != nil {
		t.Fatalf("NewProvider() under a 30 character namespace = %v, want it taken: every service account is named for the namespace's 12-character field", err)
	}
}

var secretID = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,255}$`)

var accountID = regexp.MustCompile(`^[a-z]([-a-z0-9]{4,28}[a-z0-9])$`)

func longNames(t *testing.T) names.Names {
	t.Helper()
	return namesOf(t, newProvider(t, gcp.Options{Project: "acme-production-workloads", Region: "europe-west1"}))
}

func gatewayOf(t *testing.T, named names.Names, project, env string) string {
	t.Helper()
	gateway, err := named.RealtimeGateway(project, env)
	if err != nil {
		t.Fatalf("RealtimeGateway(%q, %q) = %v", project, env, err)
	}
	return gateway
}

func TestAGatewayServiceFitsCloudRunWhateverItsProjectAndEnvironmentAreCalled(t *testing.T) {
	names := longNames(t)

	for _, scope := range [][2]string{
		{"shop", "prod"},
		{"a-project-slug-that-runs-on-and-on", "feature-branch-with-a-very-long-name"},
	} {
		service := gatewayOf(t, names, scope[0], scope[1])
		if len(service) > 49 || !cloudRunName.MatchString(service) {
			t.Errorf("RealtimeGateway(%q, %q) = %q (%d characters), which Cloud Run will not take as a service name of at most 49", scope[0], scope[1], service, len(service))
		}
		if !strings.HasPrefix(service, names.Namespace().String()+"-") {
			t.Errorf("RealtimeGateway(%q, %q) = %q, want it under the namespace", scope[0], scope[1], service)
		}
	}
}

func TestEachEnvironmentHasAGatewayAndKeysOfItsOwn(t *testing.T) {
	names := longNames(t)

	if gatewayOf(t, names, "shop", "prod") == gatewayOf(t, names, "shop", "pr-7") {
		t.Error("two environments share one gateway, and a preview's keys would be trusted in production")
	}
	if gatewayOf(t, names, "shop-prod", "x") == gatewayOf(t, names, "shop", "prod-x") {
		t.Error("two scopes whose dashes fall differently share one gateway")
	}
	if must(names.RealtimeKeysSecret(environment.TierProduction, "shop", "prod")) == must(names.RealtimeKeysSecret(environment.TierProduction, "shop", "pr-7")) {
		t.Error("two environments share one keys secret")
	}
	app, err := names.Service("shop", "prod", "realtime", "realtime")
	if err != nil {
		t.Fatal(err)
	}
	if gatewayOf(t, names, "shop", "prod") == app {
		t.Error("the gateway is named as an app called realtime would be, and one would replace the other")
	}
}

func TestEveryRealtimeSecretIsOneSecretManagerTakesAndEachResourceHasItsOwn(t *testing.T) {
	names := longNames(t)
	project, env := "a-project-slug-that-runs-on-and-on", "feature-branch-with-a-very-long-name"

	keys := must(names.RealtimeKeysSecret(environment.TierPreview, project, env))
	app, chat := must(names.RealtimeSigningSecret(project, env, "app")), must(names.RealtimeSigningSecret(project, env, "chat"))
	for _, secret := range []string{keys, app, chat} {
		if !secretID.MatchString(secret) {
			t.Errorf("%q is no secret id Secret Manager takes", secret)
		}
	}
	if app == chat || app == keys {
		t.Errorf("signing secrets %q and %q and keys secret %q are not apart", app, chat, keys)
	}
}

func TestEveryRealtimeSecretSharesAPrefixNoOtherSecretOfAnyNamespaceStartsWith(t *testing.T) {
	ours := longNames(t)
	production, preview := environment.TierProduction, environment.TierPreview
	keys := must(ours.RealtimeKeysSecret(production, "shop", "prod"))
	signing := must(ours.RealtimeSigningSecret("shop", "prod", "app"))

	for _, secret := range []string{keys, signing, must(ours.RealtimeKeysSecret(preview, "shop", "pr-7"))} {
		if !strings.HasPrefix(secret, ours.RealtimeSecretPrefix()) {
			t.Errorf("%q is not under %q, so a deploy granted the realtime secrets alone could not keep it", secret, ours.RealtimeSecretPrefix())
		}
	}
	if !strings.HasPrefix(keys, ours.RealtimeKeysSecretPrefix(production)) {
		t.Errorf("%q is not under %q, so the production gateway could not read it", keys, ours.RealtimeKeysSecretPrefix(production))
	}
	for secret, why := range map[string]string{
		signing: "a gateway would read the seeds tokens are signed with",
		must(ours.RealtimeKeysSecret(preview, "shop", "pr-7")): "the production gateway would read the preview tier's keys",
	} {
		if strings.HasPrefix(secret, ours.RealtimeKeysSecretPrefix(production)) {
			t.Errorf("%q is under the production keys prefix %q: %s", secret, ours.RealtimeKeysSecretPrefix(production), why)
		}
	}

	t.Setenv(provider.NamespaceEnvVar, ours.Namespace().String()+"-realtime")
	other := namesOf(t, newProvider(t, gcp.Options{Project: "acme-production-workloads", Region: "europe-west1"}))
	for _, secret := range []string{
		ours.PassphraseSecret(production), ours.ConnectorKeySecret(),
		other.PassphraseSecret(production), other.ConnectorKeySecret(), must(other.RealtimeSigningSecret("shop", "prod", "app")),
	} {
		if strings.HasPrefix(secret, ours.RealtimeSecretPrefix()) {
			t.Errorf("%q is under %q, and a deploy granted the realtime secrets of %s would reach it", secret, ours.RealtimeSecretPrefix(), ours.Namespace())
		}
	}
}

func TestEachTierRunsItsGatewaysAsAnAccountOfItsOwn(t *testing.T) {
	names := longNames(t)

	production, preview := names.RealtimeAccount(environment.TierProduction), names.RealtimeAccount(environment.TierPreview)
	for _, account := range []string{production, preview} {
		if !accountID.MatchString(account) {
			t.Errorf("RealtimeAccount() = %q, which IAM will not take as an account id", account)
		}
	}
	if production == preview || production == names.PushAccount(environment.TierProduction) {
		t.Errorf("the production gateway runs as %q, which is not an account of its own", production)
	}
}

func TestAnAppsAccountIsNamedForItsNamespacesAppFenceAndAHashOfItsProjectEnvironmentAndApp(t *testing.T) {
	if account := namesOf(t, testProvider(t)).AppAccount("shop", stackrecords.ProductionEnv, "web"); account != "ocel-app-550f4d4c26" {
		t.Errorf("AppAccount() = %q, want ocel-app- and the first ten hex digits of the digest over ocel--shop--prod--web", account)
	}

	t.Setenv(provider.NamespaceEnvVar, strings.Repeat("a", 19))
	names := namesOf(t, newProvider(t, gcp.Options{Project: "acme-prod", Region: "europe-west1"}))

	account := names.AppAccount("shop", stackrecords.ProductionEnv, "web")
	if !regexp.MustCompile(`^aaaaa-[0-9a-f]{6}-app-[0-9a-f]{10}$`).MatchString(account) || !accountID.MatchString(account) {
		t.Errorf("AppAccount() = %q, want the namespace's app fence and ten hex characters in the 30 IAM takes", account)
	}
	if email := names.AppAccountEmail("shop", stackrecords.ProductionEnv, "web"); email != account+"@acme-prod.iam.gserviceaccount.com" {
		t.Errorf("AppAccountEmail() = %q, want the account in the project's own domain", email)
	}
	others := map[string]string{
		"another project":     names.AppAccount("blog", stackrecords.ProductionEnv, "web"),
		"another environment": names.AppAccount("shop", "pr-7", "web"),
		"another app":         names.AppAccount("shop", stackrecords.ProductionEnv, "api"),
		"the realtime":        names.RealtimeAccount(environment.TierProduction),
		"the push":            names.PushAccount(environment.TierProduction),
		"the env source sync": names.EnvSourceSyncAccount(environment.TierProduction),
	}
	for what, other := range others {
		if other == account {
			t.Errorf("an app's account is %q, the same as %s", account, what)
		}
	}
}

func TestEachProjectOfEachTierKeepsItsRunsAndTagRecordsInADatabaseOfItsOwn(t *testing.T) {
	names := namesOf(t, newProvider(t, gcp.Options{Project: "acme-prod", Region: "europe-west1"}))

	databases := map[string]string{}
	for _, tier := range []environment.Tier{environment.TierProduction, environment.TierPreview} {
		for _, project := range []string{"shop", "blog"} {
			database := names.ProjectWritableDatabase(tier, project)
			if other, taken := databases[database]; taken {
				t.Errorf("%s of %s and %s share the database %s", project, tier, other, database)
			}
			if database == names.ProjectDatabase(tier, project) {
				t.Errorf("%s of %s keeps its runs in %s, the database of its values", project, tier, database)
			}
			databases[database] = project + " of " + string(tier)
		}
	}
}

func must[T any](value T, err error) T {
	if err != nil {
		panic(err)
	}
	return value
}
