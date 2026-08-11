package deploy

import (
	"strings"
	"testing"
)

func TestSafeName(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   string
		want string
	}{
		{"underscore separator becomes hyphen", "bucket_uploads", "bucket-uploads"},
		{"postgres logical name", "postgres_main", "postgres-main"},
		{"uppercase is lowercased", "Bucket_Uploads", "bucket-uploads"},
		{"already safe is unchanged", "api-service", "api-service"},
		{"leading digit gets letter prefix", "3things_here", "a3things-here"},
		{"other symbols become hyphen", "my.bucket@v2", "my-bucket-v2"},
		{"consecutive separators collapse", "a__b--c", "a-b-c"},
		{"leading and trailing separators trimmed", "_uploads_", "uploads"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := safeName(tc.in); got != tc.want {
				t.Errorf("safeName(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}

	t.Run("starts with a letter", func(t *testing.T) {
		t.Parallel()

		for _, in := range []string{"123", "_9", "-4x", ""} {
			got := safeName(in)
			if got == "" {
				t.Errorf("safeName(%q) is empty", in)
				continue
			}
			if c := got[0]; c < 'a' || c > 'z' {
				t.Errorf("safeName(%q) = %q, must start with a letter", in, got)
			}
		}
	})

	t.Run("capped and no invalid chars", func(t *testing.T) {
		t.Parallel()

		long := strings.Repeat("long_name_", 20)
		got := safeName(long)
		if len(got) > maxSafeNamePrefixLen {
			t.Errorf("safeName(long) length = %d, want <= %d", len(got), maxSafeNamePrefixLen)
		}
		if strings.HasSuffix(got, "-") || strings.HasPrefix(got, "-") {
			t.Errorf("safeName(long) = %q, must not start or end with a hyphen", got)
		}
		for _, r := range got {
			safe := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-'
			if !safe {
				t.Errorf("safeName(long) = %q contains invalid char %q", got, r)
			}
		}
	})
}

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
			{"s3 bucket", "bucket_uploads", "", "bucket-uploads-"},
			{"rds cluster", "postgres_main", "", "postgres-main-"},
			{"rds cluster instance", "postgres_main", "instance", "postgres-main-instance-"},
			{"rds subnet group", "postgres_main", "subnets", "postgres-main-subnets-"},
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
