package preflight

import (
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/projectconfig"
)

func TestHostnames(t *testing.T) {
	t.Parallel()

	cfg := &projectconfig.Config{
		Domains: map[string][]string{
			"production": {"acme.com", "www.acme.com"},
			"preview":    {"*.preview.acme.com"},
		},
		Apps: []projectconfig.App{
			{Name: "web", Domains: map[string][]string{"production": {"app.acme.com", "acme.com"}}},
			{Name: "api", Domains: map[string][]string{"production": {"api.acme.com"}}},
			{Name: "admin"},
		},
	}

	t.Run("the project's and the apps' hostnames come back in declared order, deduped", func(t *testing.T) {
		t.Parallel()

		got := Hostnames(cfg, "production")
		want := []string{"acme.com", "www.acme.com", "app.acme.com", "api.acme.com"}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("Hostnames(production) = %v, want %v (declared order, deduped)", got, want)
		}
	})

	t.Run("an environment sees only its own hostnames", func(t *testing.T) {
		t.Parallel()

		if got := Hostnames(cfg, "preview"); len(got) != 1 || got[0] != "*.preview.acme.com" {
			t.Errorf("Hostnames(preview) = %v, want [*.preview.acme.com]", got)
		}
	})

	t.Run("a domain-less project declares nothing", func(t *testing.T) {
		t.Parallel()

		if got := Hostnames(&projectconfig.Config{}, "production"); len(got) != 0 {
			t.Errorf("Hostnames of a domain-less project = %v, want none", got)
		}
	})
}
