package providerkit

import (
	"errors"
	"strings"
	"testing"
)

func TestWriterFor(t *testing.T) {
	t.Parallel()

	t.Run("a release version parses", func(t *testing.T) {
		t.Parallel()

		for _, raw := range []string{"1.2.3", "v1.2.3", "0.0.1", "1.2.3-rc.1", "1.2.3+meta"} {
			if w := WriterFor(raw); !w.Release() {
				t.Errorf("WriterFor(%q).Release() = false, want true", raw)
			}
		}
	})

	t.Run("a dev build never parses as a version", func(t *testing.T) {
		t.Parallel()

		for _, raw := range []string{"", "dev", "(devel)", "dev+cafe", "1.2", "1.2.3.4", "nightly"} {
			if w := Writer(raw); w.Release() {
				t.Errorf("Writer(%q).Release() = true, want false", raw)
			}
		}
	})

	t.Run("a dev build stamps its revision", func(t *testing.T) {
		t.Parallel()

		w := writerFor("dev", "cafebabe")
		if string(w) != "dev+cafebabe" {
			t.Fatalf("writerFor(dev, cafebabe) = %q, want dev+cafebabe", w)
		}
		if w.Release() {
			t.Fatalf("%q must never read as a release", w)
		}
	})

	t.Run("a dev build without a revision is still dev", func(t *testing.T) {
		t.Parallel()

		if w := writerFor("dev", ""); string(w) != "dev" {
			t.Fatalf("writerFor(dev, \"\") = %q, want dev", w)
		}
	})

	t.Run("an unset writer reads as unknown", func(t *testing.T) {
		t.Parallel()

		if got := Writer("").String(); got != "unknown" {
			t.Fatalf("Writer(\"\").String() = %q, want unknown", got)
		}
	})

	t.Run("the live writer is never empty", func(t *testing.T) {
		t.Parallel()

		if got := WriterFor("dev"); !strings.HasPrefix(string(got), "dev") {
			t.Fatalf("WriterFor(dev) = %q, want it to start with dev", got)
		}
	})
}

func TestWriterNewer(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		mine, theirs Writer
		want         bool
	}{
		{"1.3.0", "1.2.9", true},
		{"1.2.3", "1.2.3", false},
		{"1.2.3", "1.3.0", false},
		{"1.2.3", "1.2.3-rc.1", true},
		{"1.2.3-rc.1", "1.2.3", false},
		{"dev+cafe", "1.2.3", false},
		{"1.2.3", "dev+cafe", false},
	} {
		if got := tc.mine.Newer(tc.theirs); got != tc.want {
			t.Errorf("Writer(%q).Newer(%q) = %v, want %v", tc.mine, tc.theirs, got, tc.want)
		}
	}
}

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

		if got := checkCompat(BootstrapSchema-1, true, BootstrapSchema); got != needsBootstrapUpgrade {
			t.Fatalf("checkCompat(%d, true, %d) = %v, want needsBootstrapUpgrade", BootstrapSchema-1, BootstrapSchema, got)
		}
		if got := checkCompat(BootstrapSchema+1, true, BootstrapSchema); got != needsCLIUpgrade {
			t.Fatalf("checkCompat(%d, true, %d) = %v, want needsCLIUpgrade", BootstrapSchema+1, BootstrapSchema, got)
		}
	})

	t.Run("the numbering starts at one", func(t *testing.T) {
		t.Parallel()

		if BootstrapSchema < 1 {
			t.Fatalf("BootstrapSchema = %d, want the numbering to start at 1", BootstrapSchema)
		}
	})
}

func TestCompatibilityExplain(t *testing.T) {
	t.Parallel()

	t.Run("a compatible bootstrap explains nothing", func(t *testing.T) {
		t.Parallel()

		if err := compatible.explain(6, 6, "ocel bootstrap"); err != nil {
			t.Errorf("compatible.explain() = %v, want nil", err)
		}
	})

	t.Run("it names the command it was given", func(t *testing.T) {
		t.Parallel()

		const previewCommand = "ocel bootstrap --preview"
		for _, c := range []compatibility{needsBootstrapInit, needsBootstrapUpgrade} {
			message := c.explain(4, 6, previewCommand).Error()
			if !strings.Contains(message, "`"+previewCommand+"`") {
				t.Errorf("%v.explain() = %q, want it to suggest %q", c, message, previewCommand)
			}
			if strings.Contains(message, "`ocel bootstrap`") {
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
				message := tc.compat.explain(tc.deployed, 6, "ocel bootstrap").Error()
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

		message := needsBootstrapUpgrade.explain(0, 6, "ocel bootstrap").Error()
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
			message := c.explain(4, 6, "ocel bootstrap").Error()
			if got := strings.Count(message, "\n"); got != 1 {
				t.Errorf("%v.explain() = %q, want exactly two lines", c, message)
			}
		}
	})

	t.Run("every explanation is a refusal the wire can carry", func(t *testing.T) {
		t.Parallel()

		for _, c := range []compatibility{needsBootstrapInit, needsBootstrapUpgrade, needsCLIUpgrade} {
			var refusal Refusal
			if err := c.explain(4, 6, "ocel bootstrap"); !errors.As(err, &refusal) || refusal.Code != CodeNotReady {
				t.Errorf("%v.explain() = %v, want a %s refusal", c, err, CodeNotReady)
			}
		}
	})
}
