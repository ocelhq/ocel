package previewid

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
)

var validKey = regexp.MustCompile(`^[a-z]([a-z0-9-]{0,` + strconv.Itoa(maxKeyLen-2) + `}[a-z0-9])?$`)

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

func TestValidateLabel(t *testing.T) {
	valid := []string{"staging", "feature-login", "web-1", "a", "a" + strings.Repeat("b", maxKeyLen-1)}
	for _, s := range valid {
		if err := ValidateLabel(s); err != nil {
			t.Errorf("ValidateLabel(%q) = %v, want nil", s, err)
		}
	}
	invalid := []string{"", "Staging", "1web", "-x", "x-", "foo_bar", "a.b", "*", "a--b", "staging--web"}
	for _, s := range invalid {
		if err := ValidateLabel(s); err == nil {
			t.Errorf("ValidateLabel(%q) = nil, want an error", s)
		}
	}
}

func TestValidateLabel_TooLongNameIsRefusedActionably(t *testing.T) {
	name := "a" + strings.Repeat("b", maxKeyLen)
	err := ValidateLabel(name)
	if err == nil {
		t.Fatalf("ValidateLabel(%d-char name) = nil, want an error", len(name))
	}
	for _, want := range []string{"too long", strconv.Itoa(maxKeyLen)} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("ValidateLabel error = %q, want it to mention %q", err, want)
		}
	}
}

func TestKeyBudgetIsTheWholeLabel(t *testing.T) {
	if maxKeyLen != maxLabelLen {
		t.Errorf("maxKeyLen = %d, want the full DNS label %d", maxKeyLen, maxLabelLen)
	}
}

func TestResolve_KeyFitsTheDNSLabel(t *testing.T) {
	cases := []struct {
		name string
		ref  string
	}{
		{"long digit-leading ref", "4" + strings.Repeat("2", 200)},
		{"long ref sanitizing to empty", strings.Repeat("/", 200)},
		{"long letter-leading ref", "feature/" + strings.Repeat("x", 200)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id, err := Resolve(tc.ref, "")
			if err != nil {
				t.Fatalf("Resolve(%q) error = %v", tc.ref, err)
			}
			if len(id.Key) > maxKeyLen {
				t.Errorf("len(Key) = %d (%q), want <= %d", len(id.Key), id.Key, maxKeyLen)
			}
			if err := ValidateLabel(id.Key); err != nil {
				t.Errorf("ValidateLabel(%q) = %v, want nil", id.Key, err)
			}
		})
	}
}

func TestResolve_MaxLengthRefFillsTheKeyBudgetExactly(t *testing.T) {
	id, err := Resolve("feature/"+strings.Repeat("x", 200), "")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if len(id.Key) != maxKeyLen {
		t.Errorf("len(Key) = %d (%q), want exactly %d", len(id.Key), id.Key, maxKeyLen)
	}
}

func TestResolve_KeyIsValidLabel(t *testing.T) {
	for _, ref := range []string{"feature/login", "482-hotfix", "///", "USER/Fix_Bug#42"} {
		id, err := Resolve(ref, "")
		if err != nil {
			t.Fatalf("Resolve(%q) error = %v", ref, err)
		}
		if err := ValidateLabel(id.Key); err != nil {
			t.Errorf("Resolve(%q).Key = %q, ValidateLabel = %v", ref, id.Key, err)
		}
	}
}
