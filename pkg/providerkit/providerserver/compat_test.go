package providerserver

import (
	"errors"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
)

func TestCheckCompat(t *testing.T) {
	t.Parallel()

	t.Run("matrix", func(t *testing.T) {
		t.Parallel()

		const required = 3
		for _, tc := range []struct {
			name     string
			deployed int
			present  bool
			want     compatibility
		}{
			{"a bootstrap that is not there needs init", 0, false, needsBootstrapInit},
			{"older deployed needs upgrade", 2, true, needsBootstrapUpgrade},
			{"equal is compatible", 3, true, compatible},
			{"newer deployed needs cli upgrade", 4, true, needsCLIUpgrade},
			{"present zero needs upgrade", 0, true, needsBootstrapUpgrade},
		} {
			t.Run(tc.name, func(t *testing.T) {
				if got := checkCompat(tc.deployed, tc.present, required); got != tc.want {
					t.Fatalf("checkCompat(%d, %v, %d) = %v, want %v", tc.deployed, tc.present, required, got, tc.want)
				}
			})
		}
	})

	t.Run("the schema gate swings both ways", func(t *testing.T) {
		t.Parallel()

		if got := checkCompat(provider.BootstrapSchema-1, true, provider.BootstrapSchema); got != needsBootstrapUpgrade {
			t.Fatalf("checkCompat(%d, true, %d) = %v, want needsBootstrapUpgrade", provider.BootstrapSchema-1, provider.BootstrapSchema, got)
		}
		if got := checkCompat(provider.BootstrapSchema+1, true, provider.BootstrapSchema); got != needsCLIUpgrade {
			t.Fatalf("checkCompat(%d, true, %d) = %v, want needsCLIUpgrade", provider.BootstrapSchema+1, provider.BootstrapSchema, got)
		}
	})

	t.Run("the numbering starts at one", func(t *testing.T) {
		t.Parallel()

		if provider.BootstrapSchema < 1 {
			t.Fatalf("BootstrapSchema = %d, want the numbering to start at 1", provider.BootstrapSchema)
		}
	})
}

func TestCompatibilityExplain(t *testing.T) {
	t.Parallel()

	t.Run("a compatible bootstrap explains nothing", func(t *testing.T) {
		t.Parallel()

		if err := compatible.explain(6, 6, "ocel bootstrap production"); err != nil {
			t.Errorf("compatible.explain() = %v, want nil", err)
		}
	})

	t.Run("it names the command it was given", func(t *testing.T) {
		t.Parallel()

		const previewCommand = "ocel bootstrap preview"
		for _, c := range []compatibility{needsBootstrapInit, needsBootstrapUpgrade} {
			message := c.explain(4, 6, previewCommand).Error()
			if !strings.Contains(message, "`"+previewCommand+"`") {
				t.Errorf("%v.explain() = %q, want it to suggest %q", c, message, previewCommand)
			}
			if strings.Contains(message, "`ocel bootstrap production`") {
				t.Errorf("%v.explain() = %q, must not suggest the bare production command", c, message)
			}
		}
	})

	t.Run("it reports both versions", func(t *testing.T) {
		t.Parallel()

		for _, tc := range []struct {
			name     string
			compat   compatibility
			deployed int
			want     []string
		}{
			{"outdated names deployed and required", needsBootstrapUpgrade, 4, []string{"schema 4", "schema 6"}},
			{"newer names deployed and required", needsCLIUpgrade, 7, []string{"schema 7", "schema 6"}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				message := tc.compat.explain(tc.deployed, 6, "ocel bootstrap production").Error()
				for _, want := range tc.want {
					if !strings.Contains(message, want) {
						t.Errorf("explain() = %q, want it to contain %q", message, want)
					}
				}
			})
		}
	})

	t.Run("a bootstrap predating schema tracking reports no fabricated zero", func(t *testing.T) {
		t.Parallel()

		message := needsBootstrapUpgrade.explain(0, 6, "ocel bootstrap production").Error()
		if strings.Contains(message, "schema 0") {
			t.Errorf("explain() = %q, must not report a fabricated schema 0", message)
		}
		if !strings.Contains(message, "schema 6") {
			t.Errorf("explain() = %q, want it to name the required schema", message)
		}
	})

	t.Run("it separates diagnosis from action", func(t *testing.T) {
		t.Parallel()

		for _, c := range []compatibility{needsBootstrapInit, needsBootstrapUpgrade, needsCLIUpgrade} {
			message := c.explain(4, 6, "ocel bootstrap production").Error()
			if got := strings.Count(message, "\n"); got != 1 {
				t.Errorf("%v.explain() = %q, want exactly two lines", c, message)
			}
		}
	})

	t.Run("every explanation is a refusal the wire can send", func(t *testing.T) {
		t.Parallel()

		for _, c := range []compatibility{needsBootstrapInit, needsBootstrapUpgrade, needsCLIUpgrade} {
			var refused refusal.Refusal
			if err := c.explain(4, 6, "ocel bootstrap production"); !errors.As(err, &refused) || refused.Code != refusal.CodeNotReady {
				t.Errorf("%v.explain() = %v, want a %s refusal", c, err, refusal.CodeNotReady)
			}
		}
	})
}
