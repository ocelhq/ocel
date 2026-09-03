package providerkit_test

import (
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

func TestLoaderID(t *testing.T) {
	t.Run("covers the runtime not just the bundle", func(t *testing.T) {
		t.Parallel()
		bundle := []byte(`{"version":1}`)
		base := providerkit.LoaderID(bundle, "2026-07-13", []string{"nodejs_compat"})

		if again := providerkit.LoaderID(bundle, "2026-07-13", []string{"nodejs_compat"}); again != base {
			t.Error("id changed with nothing changed; a redeploy must reuse the loaded code")
		}
		for _, tc := range []struct {
			name string
			got  string
		}{
			{"changed bundle", providerkit.LoaderID([]byte(`{"version":2}`), "2026-07-13", []string{"nodejs_compat"})},
			{"changed compat date", providerkit.LoaderID(bundle, "2026-09-01", []string{"nodejs_compat"})},
			{"changed compat flag", providerkit.LoaderID(bundle, "2026-07-13", []string{"nodejs_compat", "no_nodejs_compat_v2"})},
			{"dropped compat flag", providerkit.LoaderID(bundle, "2026-07-13", nil)},
		} {
			if tc.got == base {
				t.Errorf("%s: id unchanged, want a new id for code the loader evaluates differently", tc.name)
			}
		}
	})
}
