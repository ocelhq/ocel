package cli

import (
	"strings"
	"testing"
)

func TestValidateTag(t *testing.T) {
	t.Parallel()

	t.Run("it accepts slugs", func(t *testing.T) {
		t.Parallel()

		for _, tag := range []string{"v1.2.3", "release-2026-07", "hotfix_3", "abc", "V2"} {
			if err := validateTag(tag); err != nil {
				t.Errorf("validateTag(%q) = %v, want nil", tag, err)
			}
		}
	})

	t.Run("it accepts empty as untagged", func(t *testing.T) {
		t.Parallel()

		if err := validateTag(""); err != nil {
			t.Errorf("validateTag(\"\") = %v, want nil (empty is the untagged default)", err)
		}
	})

	t.Run("it rejects bad characters", func(t *testing.T) {
		t.Parallel()

		for _, tag := range []string{"v1 2", "feature/x", "tag!", "über", "a b"} {
			if err := validateTag(tag); err == nil {
				t.Errorf("validateTag(%q) = nil, want an error", tag)
			}
		}
	})

	t.Run("it rejects an overlong tag", func(t *testing.T) {
		t.Parallel()

		if err := validateTag(strings.Repeat("a", maxTagLen+1)); err == nil {
			t.Error("validateTag(65 chars) = nil, want an error")
		}
		if err := validateTag(strings.Repeat("a", maxTagLen)); err != nil {
			t.Errorf("validateTag(64 chars) = %v, want nil", err)
		}
	})
}
