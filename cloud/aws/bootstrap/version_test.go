package bootstrap

import "testing"

func TestCheckCompat_Matrix(t *testing.T) {
	const required = 3
	cases := []struct {
		name     string
		deployed int
		present  bool
		want     Compatibility
	}{
		{"missing stack needs bootstrap", 0, false, NeedsBootstrap},
		{"older deployed needs bootstrap", 2, true, NeedsBootstrap},
		{"equal is compatible", 3, true, Compatible},
		{"newer deployed needs cli upgrade", 4, true, NeedsCLIUpgrade},
		// A present-but-somehow-zero version is still older than required.
		{"present zero needs bootstrap", 0, true, NeedsBootstrap},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := CheckCompat(tc.deployed, tc.present, required); got != tc.want {
				t.Fatalf("CheckCompat(%d, %v, %d) = %v, want %v", tc.deployed, tc.present, required, got, tc.want)
			}
		})
	}
}

// TestRequiredBootstrapVersion pins the current required version. Bumping it is
// a deliberate act (it forces every older account to re-run `ocel bootstrap`),
// so a change must be matched here.
func TestRequiredBootstrapVersion(t *testing.T) {
	if RequiredBootstrapVersion != 3 {
		t.Fatalf("RequiredBootstrapVersion = %d, want 3", RequiredBootstrapVersion)
	}
}

// TestCheckCompat_StaleBootstrapTrips proves the gate trips for an account
// bootstrapped before the sessions table (deployed v1) against the current
// required version: it must report NeedsBootstrap so the user re-runs bootstrap.
func TestCheckCompat_StaleBootstrapTrips(t *testing.T) {
	if got := CheckCompat(1, true, RequiredBootstrapVersion); got != NeedsBootstrap {
		t.Fatalf("CheckCompat(1, true, %d) = %v, want NeedsBootstrap", RequiredBootstrapVersion, got)
	}
}

func TestCompatibility_Explain(t *testing.T) {
	if err := Compatible.Explain(); err != nil {
		t.Errorf("Compatible.Explain() = %v, want nil", err)
	}
	if err := NeedsBootstrap.Explain(); err == nil {
		t.Error("NeedsBootstrap.Explain() = nil, want an actionable error")
	}
	if err := NeedsCLIUpgrade.Explain(); err == nil {
		t.Error("NeedsCLIUpgrade.Explain() = nil, want an actionable error")
	}
}
