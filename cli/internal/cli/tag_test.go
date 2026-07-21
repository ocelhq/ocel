package cli

import (
	"strings"
	"testing"
)

func TestValidateTag_AcceptsSlugs(t *testing.T) {
	for _, tag := range []string{"v1.2.3", "release-2026-07", "hotfix_3", "abc", "V2"} {
		if err := validateTag(tag); err != nil {
			t.Errorf("validateTag(%q) = %v, want nil", tag, err)
		}
	}
}

func TestValidateTag_AcceptsEmptyAsUntagged(t *testing.T) {
	if err := validateTag(""); err != nil {
		t.Errorf("validateTag(\"\") = %v, want nil (empty is the untagged default)", err)
	}
}

func TestValidateTag_RejectsBadCharacters(t *testing.T) {
	for _, tag := range []string{"v1 2", "feature/x", "tag!", "über", "a b"} {
		if err := validateTag(tag); err == nil {
			t.Errorf("validateTag(%q) = nil, want an error", tag)
		}
	}
}

func TestValidateTag_RejectsOverlong(t *testing.T) {
	if err := validateTag(strings.Repeat("a", maxTagLen+1)); err == nil {
		t.Error("validateTag(65 chars) = nil, want an error")
	}
	if err := validateTag(strings.Repeat("a", maxTagLen)); err != nil {
		t.Errorf("validateTag(64 chars) = %v, want nil", err)
	}
}
