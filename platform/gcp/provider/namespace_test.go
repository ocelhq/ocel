package gcp_test

import (
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	gcp "github.com/ocelhq/ocel/platform/gcp/provider"
)

func TestEveryNameThisProviderDerivesContainsTheNamespace(t *testing.T) {
	for _, tc := range []struct {
		name      string
		namespace string
		stem      string
	}{
		{name: "the namespace a run names", namespace: "j-a1b2c3", stem: "j-a1b2c3"},
		{name: "the default where a run names none", namespace: "", stem: "ocel"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(provider.NamespaceEnvVar, tc.namespace)

			names := names(t, newProvider(t, gcp.Options{Project: "acme-prod", Region: "europe-west1"}))
			for what, got := range map[string]string{
				"artifact bucket":   names.Bucket(environment.TierProduction),
				"state bucket":      names.StateBucket(environment.TierPreview),
				"record database":   names.Database(),
				"key ring":          names.KeyRing(),
				"passphrase secret": names.PassphraseSecret(environment.TierProduction),
				"image repository":  names.Repository(environment.TierProduction),
				"delay account":     names.DelayAccount(environment.TierProduction),
				"task database":     names.TaskDatabase(environment.TierProduction),
				"delay queue":       names.DelayQueue(environment.TierProduction),
				"push account":      names.PushAccount(environment.TierProduction),
			} {
				if !strings.HasPrefix(got, tc.stem) {
					t.Errorf("the %s is %q, want it derived from namespace %q", what, got, tc.stem)
				}
			}
			if got, want := names.Bucket(environment.TierProduction), tc.stem+"-acme-prod-production"; got != want {
				t.Errorf("Bucket() = %q, want %q", got, want)
			}
			if got, want := names.StateBucket(environment.TierPreview), tc.stem+"-acme-prod-preview-state"; got != want {
				t.Errorf("StateBucket() = %q, want %q", got, want)
			}
			if got, want := names.PassphraseSecret(environment.TierProduction), tc.stem+"-production-pulumi-passphrase"; got != want {
				t.Errorf("PassphraseSecret() = %q, want %q", got, want)
			}
			if got, want := names.Repository(environment.TierPreview), tc.stem+"-acme-prod-preview"; got != want {
				t.Errorf("Repository() = %q, want %q", got, want)
			}
			if got, want := names.RepositoryPath("europe-west1", environment.TierPreview), "europe-west1-docker.pkg.dev/acme-prod/"+tc.stem+"-acme-prod-preview"; got != want {
				t.Errorf("RepositoryPath() = %q, want %q: the deploy pushes images to that host", got, want)
			}
			if got, want := names.DelayAccount(environment.TierProduction), tc.stem+"-production"; got != want {
				t.Errorf("DelayAccount() = %q, want %q", got, want)
			}
			if got, want := names.DelayAccountEmail(environment.TierProduction), tc.stem+"-production@acme-prod.iam.gserviceaccount.com"; got != want {
				t.Errorf("DelayAccountEmail() = %q, want %q: a delayed message is published as the account that address names", got, want)
			}
			if got, want := names.TaskDatabase(environment.TierPreview), tc.stem+"-preview-tasks"; got != want {
				t.Errorf("TaskDatabase() = %q, want %q", got, want)
			}
			if got, want := names.DelayQueue(environment.TierProduction), tc.stem+"-production-delays"; got != want {
				t.Errorf("DelayQueue() = %q, want %q", got, want)
			}
			if got := names.PushAccountEmail(environment.TierProduction); !strings.HasPrefix(got, names.PushAccount(environment.TierProduction)+"@acme-prod.iam.gserviceaccount.com") ||
				names.PushAccount(environment.TierProduction) == names.PushAccount(environment.TierPreview) || len(names.PushAccount(environment.TierProduction)) > 30 {
				t.Errorf("PushAccount() = %q (%q), want an account of the tier's own, in 30 characters", names.PushAccount(environment.TierProduction), got)
			}
			if names.Database() != tc.stem || names.KeyRing() != tc.stem {
				t.Errorf("Database() = %q and KeyRing() = %q, want both %q", names.Database(), names.KeyRing(), tc.stem)
			}
		})
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
		{
			name:      "a namespace and project too long for a bucket",
			namespace: strings.Repeat("a", provider.MaxNamespaceLength),
			project:   "acme-prod-" + strings.Repeat("b", 20),
			names:     "-production-state",
		},
		{
			name:      "a namespace too long for a runtime service account",
			namespace: strings.Repeat("a", 20),
			project:   "acme-prod",
			names:     "-production",
		},
		{
			name:      "a namespace too short for a Firestore database",
			namespace: "abc",
			project:   "acme-prod",
			names:     "Firestore",
		},
		{
			name:      "a namespace that reads as a UUID",
			namespace: "ab2c3d45-6789-4abc-8def-0123456789ab",
			project:   "acme-prod",
			names:     "Firestore",
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

func TestTheLongestNamespaceARuntimeAccountLeavesRoomForIsTaken(t *testing.T) {
	t.Setenv(provider.NamespaceEnvVar, strings.Repeat("a", 19))

	if _, err := gcp.NewProvider(gcp.Options{Project: "acme-prod", Region: "europe-west1"}); err != nil {
		t.Fatalf("NewProvider() under a 19 character namespace = %v, want it taken: Google gives a service account id 30 characters and the longest tier is 10", err)
	}
}

var secretID = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,255}$`)

var accountID = regexp.MustCompile(`^[a-z]([-a-z0-9]{4,28}[a-z0-9])$`)

func longNames(t *testing.T) gcp.Names {
	t.Helper()
	return names(t, newProvider(t, gcp.Options{Project: "acme-production-workloads", Region: "europe-west1"}))
}

func TestAGatewayServiceFitsCloudRunWhateverItsProjectAndEnvironmentAreCalled(t *testing.T) {
	names := longNames(t)

	for _, scope := range [][2]string{
		{"shop", "prod"},
		{"a-project-slug-that-runs-on-and-on", "feature-branch-with-a-very-long-name"},
	} {
		service := names.RealtimeGateway(scope[0], scope[1])
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

	if names.RealtimeGateway("shop", "prod") == names.RealtimeGateway("shop", "pr-7") {
		t.Error("two environments share one gateway, and a preview's keys would be trusted in production")
	}
	if names.RealtimeGateway("shop-prod", "x") == names.RealtimeGateway("shop", "prod-x") {
		t.Error("two scopes whose dashes fall differently share one gateway")
	}
	if names.RealtimeKeysSecret(environment.TierProduction, "shop", "prod") == names.RealtimeKeysSecret(environment.TierProduction, "shop", "pr-7") {
		t.Error("two environments share one keys secret")
	}
	app, err := names.Service("shop", "prod", "realtime", "realtime")
	if err != nil {
		t.Fatal(err)
	}
	if names.RealtimeGateway("shop", "prod") == app {
		t.Error("the gateway is named as an app called realtime would be, and one would replace the other")
	}
}

func TestEveryRealtimeSecretIsOneSecretManagerTakesAndEachResourceHasItsOwn(t *testing.T) {
	names := longNames(t)
	project, env := "a-project-slug-that-runs-on-and-on", "feature-branch-with-a-very-long-name"

	keys := names.RealtimeKeysSecret(environment.TierPreview, project, env)
	app, chat := names.RealtimeSigningSecret(project, env, "app"), names.RealtimeSigningSecret(project, env, "chat")
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
	keys := ours.RealtimeKeysSecret(production, "shop", "prod")
	signing := ours.RealtimeSigningSecret("shop", "prod", "app")

	for _, secret := range []string{keys, signing, ours.RealtimeKeysSecret(preview, "shop", "pr-7")} {
		if !strings.HasPrefix(secret, ours.RealtimeSecretPrefix()) {
			t.Errorf("%q is not under %q, so a deploy granted the realtime secrets alone could not keep it", secret, ours.RealtimeSecretPrefix())
		}
	}
	if !strings.HasPrefix(keys, ours.RealtimeKeysSecretPrefix(production)) {
		t.Errorf("%q is not under %q, so the production gateway could not read it", keys, ours.RealtimeKeysSecretPrefix(production))
	}
	for secret, why := range map[string]string{
		signing: "a gateway would read the seeds tokens are signed with",
		ours.RealtimeKeysSecret(preview, "shop", "pr-7"): "the production gateway would read the preview tier's keys",
	} {
		if strings.HasPrefix(secret, ours.RealtimeKeysSecretPrefix(production)) {
			t.Errorf("%q is under the production keys prefix %q: %s", secret, ours.RealtimeKeysSecretPrefix(production), why)
		}
	}

	t.Setenv(provider.NamespaceEnvVar, ours.Namespace().String()+"-realtime")
	other := names(t, newProvider(t, gcp.Options{Project: "acme-production-workloads", Region: "europe-west1"}))
	for _, secret := range []string{
		ours.PassphraseSecret(production), ours.ConnectorKeySecret(),
		other.PassphraseSecret(production), other.ConnectorKeySecret(), other.RealtimeSigningSecret("shop", "prod", "app"),
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
	if production == preview || production == names.DelayAccount(environment.TierProduction) {
		t.Errorf("the production gateway runs as %q, which is not an account of its own", production)
	}
}

func TestAnAppsAccountIsNamedForItsNamespaceAndAHashOfItsTierProjectAndApp(t *testing.T) {
	t.Setenv(provider.NamespaceEnvVar, strings.Repeat("a", 19))
	names := names(t, newProvider(t, gcp.Options{Project: "acme-prod", Region: "europe-west1"}))

	account := names.AppAccount(environment.TierProduction, "shop", "web")
	if !regexp.MustCompile(`^a{19}-[0-9a-f]{10}$`).MatchString(account) || !accountID.MatchString(account) {
		t.Errorf("AppAccount() = %q, want the namespace and ten hex characters in the 30 IAM takes", account)
	}
	if email := names.AppAccountEmail(environment.TierProduction, "shop", "web"); email != account+"@acme-prod.iam.gserviceaccount.com" {
		t.Errorf("AppAccountEmail() = %q, want the account in the project's own domain", email)
	}
	others := map[string]string{
		"another tier":        names.AppAccount(environment.TierPreview, "shop", "web"),
		"another project":     names.AppAccount(environment.TierProduction, "blog", "web"),
		"another app":         names.AppAccount(environment.TierProduction, "shop", "api"),
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
