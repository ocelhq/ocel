package previewid

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
)

var validKey = regexp.MustCompile(`^[a-z]([a-z0-9-]{0,` + strconv.Itoa(maxKeyLen-2) + `}[a-z0-9])?$`)

func mustResolve(t *testing.T, ref, prNumber string) Identity {
	t.Helper()
	id, err := Resolve(ref, prNumber)
	if err != nil {
		t.Fatalf("Resolve(%q, %q) error = %v", ref, prNumber, err)
	}
	return id
}

func TestResolve(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		ref       string
		prNumber  string
		wantLabel string
	}{
		{name: "a slash-separated branch ref", ref: "feature/login"},
		{name: "a PR number becomes the label, not part of the key", ref: "feature/login", prNumber: "482", wantLabel: "pr-482"},
		{name: "an empty PR number means an empty label", ref: "feature/login"},
		{name: "mixed case and a hyphen in the ref", ref: "feature/Login-Page"},
		{name: "an uppercase segment with an underscore and a hash", ref: "USER/Fix_Bug#42"},
		{name: "an underscore in the ref never reaches the key", ref: "feature/Fix_Bug"},
		{name: "a release tag with dots", ref: "release/v1.2.3"},
		{name: "repeated slashes, a backslash and spaces", ref: `a//b\c  d`},
		{name: "a ref beginning with a digit still starts the key with a letter", ref: "482-hotfix"},
		{name: "the default branch", ref: "main"},
		{name: "a ref that sanitizes to nothing", ref: "///"},
		{name: "a long digit-leading ref", ref: "4" + strings.Repeat("2", 200)},
		{name: "a long ref sanitizing to empty", ref: strings.Repeat("/", 200)},
		{name: "a long letter-leading ref", ref: "feature/" + strings.Repeat("x", 200)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			id := mustResolve(t, tc.ref, tc.prNumber)

			if !validKey.MatchString(id.Key) {
				t.Errorf("Resolve(%q).Key = %q, not a substrate-safe key", tc.ref, id.Key)
			}
			if err := ValidateLabel(id.Key); err != nil {
				t.Errorf("Resolve(%q).Key = %q, ValidateLabel = %v, want nil", tc.ref, id.Key, err)
			}
			if len(id.Key) > maxKeyLen {
				t.Errorf("len(Key) = %d (%q), want <= %d", len(id.Key), id.Key, maxKeyLen)
			}
			if first := id.Key[0]; first < 'a' || first > 'z' {
				t.Errorf("Key = %q must start with a lowercase letter", id.Key)
			}
			if strings.ContainsRune(id.Key, '_') {
				t.Errorf("Key = %q contains an underscore, not DNS-label-safe", id.Key)
			}
			if id.Label != tc.wantLabel {
				t.Errorf("Label = %q, want %q", id.Label, tc.wantLabel)
			}
			if id.Source != SourceGit {
				t.Errorf("Source = %q, want %q", id.Source, SourceGit)
			}
		})
	}

	t.Run("the key is stable across repeated calls", func(t *testing.T) {
		t.Parallel()

		a := mustResolve(t, "feature/login", "")
		b := mustResolve(t, "feature/login", "")
		if a.Key != b.Key {
			t.Fatalf("Key not stable across calls: %q != %q", a.Key, b.Key)
		}
	})

	t.Run("the hash disambiguates refs whose bases collide", func(t *testing.T) {
		t.Parallel()

		a := mustResolve(t, "feature/login", "")
		b := mustResolve(t, "feature-login", "")
		if a.Key == b.Key {
			t.Fatalf("distinct refs collided on key %q", a.Key)
		}
	})

	t.Run("the PR number does not leak into the key", func(t *testing.T) {
		t.Parallel()

		withPR := mustResolve(t, "feature/login", "482")
		withoutPR := mustResolve(t, "feature/login", "")
		if withPR.Key != withoutPR.Key {
			t.Errorf("PR number leaked into key: %q vs %q", withPR.Key, withoutPR.Key)
		}
	})

	t.Run("an empty ref is an error", func(t *testing.T) {
		t.Parallel()

		if _, err := Resolve("", "482"); err == nil {
			t.Fatal("Resolve(\"\") = nil error, want error")
		}
	})

	t.Run("a max-length ref fills the key budget exactly", func(t *testing.T) {
		t.Parallel()

		id := mustResolve(t, "feature/"+strings.Repeat("x", 200), "")
		if len(id.Key) != maxKeyLen {
			t.Errorf("len(Key) = %d (%q), want exactly %d", len(id.Key), id.Key, maxKeyLen)
		}
	})
}

func TestValidateLabel(t *testing.T) {
	t.Parallel()

	t.Run("accepts DNS-label-safe names", func(t *testing.T) {
		t.Parallel()

		valid := []string{"staging", "feature-login", "web-1", "a", "a" + strings.Repeat("b", maxKeyLen-1)}
		for _, s := range valid {
			if err := ValidateLabel(s); err != nil {
				t.Errorf("ValidateLabel(%q) = %v, want nil", s, err)
			}
		}
	})

	t.Run("refuses names that are not DNS-label-safe", func(t *testing.T) {
		t.Parallel()

		invalid := []string{"", "Staging", "1web", "-x", "x-", "foo_bar", "a.b", "*", "a--b", "staging--web"}
		for _, s := range invalid {
			if err := ValidateLabel(s); err == nil {
				t.Errorf("ValidateLabel(%q) = nil, want an error", s)
			}
		}
	})

	t.Run("refuses a too-long name actionably", func(t *testing.T) {
		t.Parallel()

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
	})
}

func TestKeyBudget(t *testing.T) {
	t.Parallel()

	t.Run("the key budget is the whole DNS label", func(t *testing.T) {
		t.Parallel()

		if maxKeyLen != maxLabelLen {
			t.Errorf("maxKeyLen = %d, want the full DNS label %d", maxKeyLen, maxLabelLen)
		}
	})
}
