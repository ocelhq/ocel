package cloudflare

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/router"
)

func TestALoaderIDChangesWithTheBundleAndTheRuntimeItLoadsUnder(t *testing.T) {
	t.Parallel()
	base := loaderID("digest-1", "2026-07-13", []string{"nodejs_compat"})

	if again := loaderID("digest-1", "2026-07-13", []string{"nodejs_compat"}); again != base {
		t.Error("the id changed with nothing changed; a redeploy must reuse the loaded code")
	}
	for name, got := range map[string]string{
		"changed bundle":      loaderID("digest-2", "2026-07-13", []string{"nodejs_compat"}),
		"changed compat date": loaderID("digest-1", "2026-09-01", []string{"nodejs_compat"}),
		"changed compat flag": loaderID("digest-1", "2026-07-13", []string{"nodejs_compat", "no_nodejs_compat_v2"}),
		"dropped compat flag": loaderID("digest-1", "2026-07-13", nil),
	} {
		if got == base {
			t.Errorf("%s: the id is unchanged, want a new id for code the loader evaluates differently", name)
		}
	}
}

func TestARecordServedWithAnEdgeBundleRunsUnderThisEdgesCompatibility(t *testing.T) {
	t.Parallel()
	served := newServedRecord(router.ReleaseRecord{App: "web", EdgeBundle: &router.EdgeBundle{Key: "prod/shop/web/r1/edge/bundle.json", Digest: "d1"}}, "")

	code := served.Edge.Code
	if code == nil {
		t.Fatal("the served record names no code, though its release ships an edge bundle")
	}
	if code.BundleKey != "prod/shop/web/r1/edge/bundle.json" || code.LoaderID != loaderID("d1", compatDate, compatFlags) ||
		code.CompatDate != compatDate || !slices.Equal(code.CompatFlags, compatFlags) {
		t.Errorf("code = %+v, want the bundle under this edge's compatibility %s %v", code, compatDate, compatFlags)
	}
	if plain := newServedRecord(router.ReleaseRecord{App: "web"}, ""); plain.Edge.Code != nil {
		t.Errorf("code = %+v, want none for a release with no edge bundle", plain.Edge.Code)
	}
}

func TestARecordServedToTheEdgeCarriesItsSecretsUnderEdgeAndNotTheLedgersSealedCopy(t *testing.T) {
	t.Parallel()
	record := router.ReleaseRecord{
		App:     "web",
		Secrets: router.ReleaseSecrets{PurgeSecret: "p", Env: map[string]string{"MODE": "loud"}, Envelope: "bare"},
		Sealed:  []byte("the tier cipher's ciphertext"),
	}

	served := newServedRecord(record, "wrapped")
	encoded, err := json.Marshal(served)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "sealed") || strings.Contains(string(encoded), "bare") {
		t.Errorf("served record = %s, want neither the ledger's sealed copy nor the unwrapped envelope", encoded)
	}
	if served.Edge.PurgeSecret != "p" || served.Edge.Env["MODE"] != "loud" || served.Edge.Envelope != "wrapped" {
		t.Errorf("served edge = %+v, want the purge secret, env and wrapped envelope the entry worker reads", served.Edge)
	}
}
