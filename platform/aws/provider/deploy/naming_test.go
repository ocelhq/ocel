package deploy

import (
	"strings"
	"testing"
)

func TestPhysicalNamePrefix(t *testing.T) {
	t.Parallel()

	t.Run("safe for constrained resources", func(t *testing.T) {
		t.Parallel()

		cases := []struct {
			name        string
			logicalName string
			infix       string
			want        string
		}{
			{"s3 bucket", "bucket--uploads", "", "bucket-uploads-"},
			{"rds cluster", "db--main", "", "db-main-"},
			{"rds cluster instance", "db--main", "instance", "db-main-instance-"},
			{"rds subnet group", "db--main", "subnets", "db-main-subnets-"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()

				got := physicalNamePrefix(tc.logicalName, tc.infix)
				if got != tc.want {
					t.Errorf("physicalNamePrefix(%q, %q) = %q, want %q", tc.logicalName, tc.infix, got, tc.want)
				}
				if strings.Contains(got, "_") {
					t.Errorf("physicalNamePrefix(%q, %q) = %q, must not contain underscores", tc.logicalName, tc.infix, got)
				}
			})
		}
	})

	t.Run("a long name is capped and stays distinct", func(t *testing.T) {
		t.Parallel()

		shared := strings.Repeat("long-name-", 5)
		a := physicalNamePrefix(shared+"alpha", "")
		b := physicalNamePrefix(shared+"beta", "")
		if a == b {
			t.Fatalf("two logical names collapsed to one prefix %q", a)
		}
		for _, got := range []string{a, b} {
			if len(got) > maxPhysicalNamePrefixLen+1 {
				t.Errorf("prefix %q is %d chars, over the %d-char cap", got, len(got), maxPhysicalNamePrefixLen+1)
			}
			if c := got[0]; c < 'a' || c > 'z' {
				t.Errorf("prefix %q must start with a letter", got)
			}
		}
	})
}

func TestLambdaResourceName(t *testing.T) {
	t.Parallel()

	t.Run("a short name is unchanged", func(t *testing.T) {
		t.Parallel()

		short := "e2e_local_9385dcd9_api_revalidate"
		if got := lambdaResourceName(short); got != short {
			t.Errorf("lambdaResourceName(%q) = %q, want it unchanged", short, got)
		}
	})

	t.Run("a long name leaves room for the autoname suffix and stays deterministic", func(t *testing.T) {
		t.Parallel()

		long := "e2e_local_9385dcd9_variable_revalidate_authorization_route_cookies"
		got := lambdaResourceName(long)
		if len(got)+lambdaAutonameSuffixLen > maxLambdaNameLen {
			t.Errorf("lambdaResourceName(%q) = %q (%d chars); autonaming would exceed %d", long, got, len(got), maxLambdaNameLen)
		}
		if got != lambdaResourceName(long) {
			t.Error("lambdaResourceName is not deterministic")
		}

		sibling := long + "_more"
		if other := lambdaResourceName(sibling); other == got {
			t.Errorf("two logical names sharing a prefix collided on %q", got)
		}
	})
}
