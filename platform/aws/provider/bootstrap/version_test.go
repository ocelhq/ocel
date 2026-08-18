package bootstrap

import (
	"strings"
	"testing"
)

func TestCheckCompat(t *testing.T) {
	t.Parallel()

	t.Run("matrix", func(t *testing.T) {
		t.Parallel()

		const required = 3
		cases := []struct {
			name     string
			deployed int
			present  bool
			want     Compatibility
		}{
			{"missing stack needs init", 0, false, NeedsBootstrapInit},
			{"older deployed needs upgrade", 2, true, NeedsBootstrapUpgrade},
			{"equal is compatible", 3, true, Compatible},
			{"newer deployed needs cli upgrade", 4, true, NeedsCLIUpgrade},
			{"present zero needs upgrade", 0, true, NeedsBootstrapUpgrade},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				if got := CheckCompat(tc.deployed, tc.present, required); got != tc.want {
					t.Fatalf("CheckCompat(%d, %v, %d) = %v, want %v", tc.deployed, tc.present, required, got, tc.want)
				}
			})
		}
	})

	t.Run("stale bootstrap trips", func(t *testing.T) {
		t.Parallel()

		if got := CheckCompat(1, true, RequiredBootstrapVersion); got != NeedsBootstrapUpgrade {
			t.Fatalf("CheckCompat(1, true, %d) = %v, want NeedsBootstrapUpgrade", RequiredBootstrapVersion, got)
		}
	})
}

func TestRequiredBootstrapVersion(t *testing.T) {
	t.Parallel()

	if RequiredBootstrapVersion != 11 {
		t.Fatalf("RequiredBootstrapVersion = %d, want 11", RequiredBootstrapVersion)
	}
}

func TestCompatibility(t *testing.T) {
	t.Parallel()

	t.Run("explain compatible", func(t *testing.T) {
		t.Parallel()

		if err := Compatible.Explain(6, 6, "ocel bootstrap"); err != nil {
			t.Errorf("Compatible.Explain() = %v, want nil", err)
		}
	})

	t.Run("explain names the command it was given", func(t *testing.T) {
		t.Parallel()

		const previewCmd = "ocel bootstrap --preview"
		for _, c := range []Compatibility{NeedsBootstrapInit, NeedsBootstrapUpgrade} {
			msg := c.Explain(4, 6, previewCmd).Error()
			if !strings.Contains(msg, "`"+previewCmd+"`") {
				t.Errorf("%v.Explain() = %q, want it to suggest %q", c, msg, previewCmd)
			}
			if strings.Contains(msg, "`ocel bootstrap`") {
				t.Errorf("%v.Explain() = %q, must not suggest the bare production command", c, msg)
			}
		}
	})

	t.Run("explain reports both versions", func(t *testing.T) {
		t.Parallel()

		cases := []struct {
			name     string
			compat   Compatibility
			deployed int
			want     []string
		}{
			{"outdated names deployed and required", NeedsBootstrapUpgrade, 4, []string{"version 4", "version 6"}},
			{"newer names deployed and required", NeedsCLIUpgrade, 7, []string{"version 7", "version 6"}},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				msg := tc.compat.Explain(tc.deployed, 6, "ocel bootstrap").Error()
				for _, want := range tc.want {
					if !strings.Contains(msg, want) {
						t.Errorf("Explain() = %q, want it to contain %q", msg, want)
					}
				}
			})
		}
	})

	t.Run("explain unversioned bootstrap", func(t *testing.T) {
		t.Parallel()

		msg := NeedsBootstrapUpgrade.Explain(0, 6, "ocel bootstrap").Error()
		if strings.Contains(msg, "version 0") {
			t.Errorf("Explain() = %q, must not report a fabricated version 0", msg)
		}
		if !strings.Contains(msg, "version 6") {
			t.Errorf("Explain() = %q, want it to name the required version", msg)
		}
	})

	t.Run("explain separates diagnosis from action", func(t *testing.T) {
		t.Parallel()

		for _, c := range []Compatibility{NeedsBootstrapInit, NeedsBootstrapUpgrade, NeedsCLIUpgrade} {
			msg := c.Explain(4, 6, "ocel bootstrap").Error()
			if got := strings.Count(msg, "\n"); got != 1 {
				t.Errorf("%v.Explain() = %q, want exactly two lines", c, msg)
			}
		}
	})
}
