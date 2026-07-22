package previewid

import (
	"regexp"
	"testing"
)

// validKey matches a valid DNS label: leading lowercase letter, then letters,
// digits and hyphens, not ending in a hyphen, 1–63 chars. Key is used as a
// preview subdomain label and store pointer.
var validKey = regexp.MustCompile(`^[a-z]([a-z0-9-]{0,61}[a-z0-9])?$`)

func TestResolve_KeyIsStable(t *testing.T) {
	a, err := Resolve("feature/login", "")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	b, err := Resolve("feature/login", "")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if a.Key != b.Key {
		t.Fatalf("Key not stable across calls: %q != %q", a.Key, b.Key)
	}
}

func TestResolve_KeyIsSubstrateSafe(t *testing.T) {
	refs := []string{
		"feature/Login-Page",
		"USER/Fix_Bug#42",
		"release/v1.2.3",
		"a//b\\c  d",
		"482-hotfix",
	}
	for _, ref := range refs {
		id, err := Resolve(ref, "")
		if err != nil {
			t.Fatalf("Resolve(%q) error = %v", ref, err)
		}
		if !validKey.MatchString(id.Key) {
			t.Errorf("Resolve(%q).Key = %q, not a substrate-safe key", ref, id.Key)
		}
	}
}

func TestResolve_HashDisambiguatesCollidingBases(t *testing.T) {
	// Both sanitize to the same base token but are different refs.
	a, err := Resolve("feature/login", "")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	b, err := Resolve("feature-login", "")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if a.Key == b.Key {
		t.Fatalf("distinct refs collided on key %q", a.Key)
	}
}

func TestResolve_PRIsLabelNotKey(t *testing.T) {
	id, err := Resolve("feature/login", "482")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if id.Label != "pr-482" {
		t.Errorf("Label = %q, want %q", id.Label, "pr-482")
	}
	withoutPR, err := Resolve("feature/login", "")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if id.Key != withoutPR.Key {
		t.Errorf("PR number leaked into key: %q vs %q", id.Key, withoutPR.Key)
	}
}

func TestResolve_EmptyPRMeansEmptyLabel(t *testing.T) {
	id, err := Resolve("feature/login", "")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if id.Label != "" {
		t.Errorf("Label = %q, want empty", id.Label)
	}
}

func TestResolve_SourceIsGit(t *testing.T) {
	id, err := Resolve("main", "")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if id.Source != SourceGit {
		t.Errorf("Source = %q, want %q", id.Source, SourceGit)
	}
}

func TestResolve_EmptyRefIsError(t *testing.T) {
	if _, err := Resolve("", "482"); err == nil {
		t.Fatal("Resolve(\"\") = nil error, want error")
	}
}

func TestResolve_LeadingDigitRefStartsWithLetter(t *testing.T) {
	id, err := Resolve("482-hotfix", "")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	first := id.Key[0]
	if !(first >= 'a' && first <= 'z') {
		t.Errorf("Key = %q must start with a lowercase letter", id.Key)
	}
	if !validKey.MatchString(id.Key) {
		t.Errorf("Key = %q, not a valid unquoted identifier", id.Key)
	}
}

func TestResolve_RefThatSanitizesToNothing(t *testing.T) {
	id, err := Resolve("///", "")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if !validKey.MatchString(id.Key) {
		t.Errorf("Key = %q, not a substrate-safe key", id.Key)
	}
}

func TestResolve_KeyHasNoUnderscore(t *testing.T) {
	// The slug is now a DNS label, so an underscore (valid only in the old
	// Postgres-identifier form) must never appear.
	id, err := Resolve("feature/Fix_Bug", "")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	for _, r := range id.Key {
		if r == '_' {
			t.Fatalf("Key = %q contains an underscore, not DNS-label-safe", id.Key)
		}
	}
}

func TestValidLabel(t *testing.T) {
	valid := []string{"staging", "feature-login", "web-1", "a"}
	for _, s := range valid {
		if !ValidLabel(s) {
			t.Errorf("ValidLabel(%q) = false, want true", s)
		}
	}
	invalid := []string{"", "Staging", "1web", "-x", "x-", "foo_bar", "a.b", "*"}
	for _, s := range invalid {
		if ValidLabel(s) {
			t.Errorf("ValidLabel(%q) = true, want false", s)
		}
	}
}

func TestResolve_KeyIsValidLabel(t *testing.T) {
	for _, ref := range []string{"feature/login", "482-hotfix", "///", "USER/Fix_Bug#42"} {
		id, err := Resolve(ref, "")
		if err != nil {
			t.Fatalf("Resolve(%q) error = %v", ref, err)
		}
		if !ValidLabel(id.Key) {
			t.Errorf("Resolve(%q).Key = %q, ValidLabel = false", ref, id.Key)
		}
	}
}
