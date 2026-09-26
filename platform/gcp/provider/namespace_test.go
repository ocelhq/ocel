package gcp_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
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
			t.Setenv(provider.NamespaceEnvVar, tc.namespace)

			names := names(t, newProvider(t, gcp.Options{Project: "acme-prod", Region: "europe-west1"}))
			for what, got := range map[string]string{
				"artifact bucket":   names.Bucket(edge.ClassProduction),
				"state bucket":      names.StateBucket(edge.ClassPreview),
				"record database":   names.Database(),
				"key ring":          names.KeyRing(),
				"passphrase secret": names.PassphraseSecret(edge.ClassProduction),
				"image repository":  names.Repository(edge.ClassProduction),
				"runtime account":   names.WorkloadAccount(edge.ClassProduction),
			} {
				if !strings.HasPrefix(got, tc.stem) {
					t.Errorf("the %s is %q, want it derived from namespace %q", what, got, tc.stem)
				}
			}
			if got, want := names.Bucket(edge.ClassProduction), tc.stem+"-acme-prod-production"; got != want {
				t.Errorf("Bucket() = %q, want %q", got, want)
			}
			if got, want := names.StateBucket(edge.ClassPreview), tc.stem+"-acme-prod-preview-state"; got != want {
				t.Errorf("StateBucket() = %q, want %q", got, want)
			}
			if got, want := names.PassphraseSecret(edge.ClassProduction), tc.stem+"-production-pulumi-passphrase"; got != want {
				t.Errorf("PassphraseSecret() = %q, want %q", got, want)
			}
			if got, want := names.Repository(edge.ClassPreview), tc.stem+"-acme-prod-preview"; got != want {
				t.Errorf("Repository() = %q, want %q", got, want)
			}
			if got, want := names.RepositoryPath("europe-west1", edge.ClassPreview), "europe-west1-docker.pkg.dev/acme-prod/"+tc.stem+"-acme-prod-preview"; got != want {
				t.Errorf("RepositoryPath() = %q, want %q: the deploy pushes images to that host", got, want)
			}
			if got, want := names.WorkloadAccount(edge.ClassProduction), tc.stem+"-production"; got != want {
				t.Errorf("WorkloadAccount() = %q, want %q", got, want)
			}
			if got, want := names.WorkloadAccountEmail(edge.ClassProduction), tc.stem+"-production@acme-prod.iam.gserviceaccount.com"; got != want {
				t.Errorf("WorkloadAccountEmail() = %q, want %q: a service runs as the account that address names", got, want)
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
		t.Fatalf("NewProvider() under a 19 character namespace = %v, want it taken: Google gives a service account id 30 characters and the longest class is 10", err)
	}
}
