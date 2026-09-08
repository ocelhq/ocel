package gcp_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
	gcp "github.com/ocelhq/ocel/platform/gcp/provider"
)

func TestEveryNameThisProviderDerivesCarriesTheNamespace(t *testing.T) {
	for _, tc := range []struct {
		name      string
		namespace string
		stem      string
	}{
		{name: "the namespace a run names", namespace: "j-a1b2c3", stem: "j-a1b2c3"},
		{name: "the default where a run names none", namespace: "", stem: "ocel"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(providerkit.NamespaceEnvVar, tc.namespace)

			names := newProvider(t, gcp.Options{Project: "acme-prod", Region: "europe-west1"}).Names()
			for what, got := range map[string]string{
				"artifact bucket":   names.Bucket(providerkit.ClassProduction),
				"state bucket":      names.StateBucket(providerkit.ClassPreview),
				"record database":   names.Database(),
				"key ring":          names.KeyRing(),
				"passphrase secret": names.PassphraseSecret(providerkit.ClassProduction),
				"image repository":  names.Repository(providerkit.ClassProduction),
				"runtime account":   names.RuntimeAccount(providerkit.ClassProduction),
			} {
				if !strings.HasPrefix(got, tc.stem) {
					t.Errorf("the %s is %q, want it derived from namespace %q", what, got, tc.stem)
				}
			}
			if got, want := names.Bucket(providerkit.ClassProduction), tc.stem+"-acme-prod-production"; got != want {
				t.Errorf("Bucket() = %q, want %q", got, want)
			}
			if got, want := names.StateBucket(providerkit.ClassPreview), tc.stem+"-acme-prod-preview-state"; got != want {
				t.Errorf("StateBucket() = %q, want %q", got, want)
			}
			if got, want := names.PassphraseSecret(providerkit.ClassProduction), tc.stem+"-production-pulumi-passphrase"; got != want {
				t.Errorf("PassphraseSecret() = %q, want %q", got, want)
			}
			if got, want := names.Repository(providerkit.ClassPreview), tc.stem+"-acme-prod-preview"; got != want {
				t.Errorf("Repository() = %q, want %q", got, want)
			}
			if got, want := names.RepositoryPath("europe-west1", providerkit.ClassPreview), "europe-west1-docker.pkg.dev/acme-prod/"+tc.stem+"-acme-prod-preview"; got != want {
				t.Errorf("RepositoryPath() = %q, want %q: the deploy pushes images to that host", got, want)
			}
			if got, want := names.RuntimeAccount(providerkit.ClassProduction), tc.stem+"-production"; got != want {
				t.Errorf("RuntimeAccount() = %q, want %q", got, want)
			}
			if got, want := names.RuntimeAccountEmail(providerkit.ClassProduction), tc.stem+"-production@acme-prod.iam.gserviceaccount.com"; got != want {
				t.Errorf("RuntimeAccountEmail() = %q, want %q: a service runs as the account that address names", got, want)
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
			names:     providerkit.NamespaceEnvVar,
		},
		{
			name:      "a namespace and project too long for a bucket",
			namespace: strings.Repeat("a", providerkit.MaxNamespaceLength),
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
			name:      "a namespace a Firestore database may not end on",
			namespace: "ocel-",
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
			t.Setenv(providerkit.NamespaceEnvVar, tc.namespace)

			var refusal providerkit.Refusal
			p, err := gcp.NewProvider(context.Background(), gcp.Options{Project: tc.project, Region: "europe-west1"})
			if !errors.As(err, &refusal) || refusal.Code != providerkit.CodeInvalid {
				t.Fatalf("NewProvider() under namespace %q = %v, %v, want an %s refusal", tc.namespace, p, err, providerkit.CodeInvalid)
			}
			if !strings.Contains(refusal.Message, tc.names) {
				t.Errorf("NewProvider() refused with %q, want it to name %q", refusal.Message, tc.names)
			}
		})
	}
}

func TestTheLongestNamespaceARuntimeAccountLeavesRoomForIsTaken(t *testing.T) {
	t.Setenv(providerkit.NamespaceEnvVar, strings.Repeat("a", 19))

	if _, err := gcp.NewProvider(context.Background(), gcp.Options{Project: "acme-prod", Region: "europe-west1"}); err != nil {
		t.Fatalf("NewProvider() under a 19 character namespace = %v, want it taken: Google gives a service account id 30 characters and the longest class is 10", err)
	}
}
